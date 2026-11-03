package compressor

import (
	"agent-core/internal/config"
	"agent-core/internal/model"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

// CompressLevel determines how aggressively to compress context.
type CompressLevel int

const (
	// CompressNone means no compression needed.
	CompressNone CompressLevel = iota
	// CompressLight removes only low-value messages (repeated tool outputs).
	CompressLight
	// CompressMedium uses LLM summary for the middle segment.
	CompressMedium
	// CompressHeavy aggressively compresses, keeping only critical context.
	CompressHeavy
)

// SummaryGenerator abstracts HTTP and failover-provider summarizers.
type SummaryGenerator interface {
	SummarizeMessages(ctx context.Context, messages []model.Message) SummaryResult
}

// Compressor manages context window compression with importance tracking and LLM summaries.
type Compressor struct {
	cfg        config.CompressionConfig
	summarizer SummaryGenerator
}

// NewCompressor creates a new context compressor.
func NewCompressor(cfg config.CompressionConfig, summarizer SummaryGenerator) *Compressor {
	return &Compressor{
		cfg:        cfg,
		summarizer: summarizer,
	}
}

// CompressResult contains the compressed message list ready for the LLM.
type CompressResult struct {
	Messages            []model.Message
	ShouldCompress      bool
	Level               CompressLevel
	TokenEstimate       int
	BeforeTokenEstimate int
	SummaryUsed         bool
	TargetReached       bool
}

// ShouldCompress returns true if the estimated tokens exceed the compression threshold.
func (c *Compressor) ShouldCompress(currentTokens, contextWindow int) bool {
	if contextWindow <= 0 {
		return false
	}
	ratio := float64(currentTokens) / float64(contextWindow)
	return ratio >= c.cfg.ThresholdPercent
}

// DetermineLevel picks the compression level based on token ratio.
func (c *Compressor) DetermineLevel(currentTokens, contextWindow int) CompressLevel {
	if contextWindow <= 0 {
		return CompressNone
	}
	ratio := float64(currentTokens) / float64(contextWindow)
	switch {
	case ratio < c.cfg.ThresholdPercent:
		return CompressNone
	case ratio < 0.70:
		return CompressLight
	case ratio < 0.85:
		return CompressMedium
	default:
		return CompressHeavy
	}
}

// Compress processes messages within a window that also includes tool schemas.
func (c *Compressor) Compress(ctx context.Context, messages []model.Message, contextWindow int, tools ...[]model.ToolDefinition) CompressResult {
	totalTokens := EstimateMessageTokens(messages)
	availableWindow := contextWindow
	for _, definitions := range tools {
		availableWindow -= EstimateToolDefinitionsTokens(definitions)
	}
	availableWindow -= 1024 // Reserve protocol and output headroom.
	if availableWindow < 1024 {
		availableWindow = contextWindow
	}
	level := c.DetermineLevel(totalTokens, availableWindow)
	if level == CompressNone {
		return CompressResult{Messages: messages, Level: CompressNone, TokenEstimate: totalTokens,
			BeforeTokenEstimate: totalTokens, TargetReached: true}
	}
	importance := MarkImportance(messages)
	compressed := c.compressToolOutputs(messages, importance)
	keepFirst, keepLast := c.keepBounds(len(compressed), level)
	result, summaryUsed := c.buildCompressedMessages(ctx, compressed, importance, keepFirst, keepLast, level)
	newTokens := EstimateMessageTokens(result)
	target := int(float64(availableWindow) * c.cfg.TargetRatio)
	if level == CompressHeavy && target > 0 && newTokens > target {
		first, last := keepFirst, keepLast
		if first > 1 {
			first = 1
		}
		if last > 5 {
			last = 5
		}
		result, summaryUsed = c.buildCompressedMessages(ctx, compressed, importance, first, last, level)
		newTokens = EstimateMessageTokens(result)
	}
	return CompressResult{Messages: result, ShouldCompress: true, Level: level,
		TokenEstimate: newTokens, BeforeTokenEstimate: totalTokens, SummaryUsed: summaryUsed,
		TargetReached: target <= 0 || newTokens <= target}
}

// keepBounds returns how many messages to keep from start and end.
func (c *Compressor) keepBounds(total int, level CompressLevel) (first, last int) {
	if total == 0 {
		return 0, 0
	}
	if total <= 2 {
		return 0, total
	}
	first = c.cfg.ProtectFirstN
	last = c.cfg.ProtectLastN

	switch level {
	case CompressHeavy:
		// Aggressive: keep fewer messages
		if first > 2 {
			first = 2
		}
		if last > 10 {
			last = 10
		}
	case CompressLight:
		// Light: keep more messages
		if last < 30 {
			last = 30
		}
	}

	// Ensure we don't keep more than we have
	if first+last >= total {
		first = total / 3
		last = total - first - 1
		if last < 1 {
			last = 1
		}
	}

	return first, last
}

// buildCompressedMessages constructs the final message list with summary injection.
func (c *Compressor) buildCompressedMessages(
	ctx context.Context,
	messages []model.Message,
	importance []bool,
	keepFirst, keepLast int,
	level CompressLevel,
) ([]model.Message, bool) {
	// Extract the middle segment (to be compressed)
	middleStart := keepFirst
	middleEnd := len(messages) - keepLast

	// Collect important messages from the middle that should be preserved
	var preservedMiddle []model.Message
	for i := middleStart; i < middleEnd; i++ {
		if importance[i] {
			preservedMiddle = append(preservedMiddle, messages[i])
		}
	}

	// Generate or use fallback summary for the middle segment
	var summary string
	summaryUsed := false
	if level >= CompressMedium && c.summarizer != nil {
		middleMessages := messages[middleStart:middleEnd]
		result := c.summarizer.SummarizeMessages(ctx, middleMessages)
		summary = result.Summary
		summaryUsed = result.Generated
	} else {
		summary = c.buildFallbackSummary(messages[middleStart:middleEnd])
	}

	// Assemble final messages
	result := make([]model.Message, 0, keepFirst+1+len(preservedMiddle)+keepLast)

	// Keep first N
	result = append(result, messages[:keepFirst]...)

	// Inject summary
	if summary != "" {
		result = append(result, model.Message{
			Role:    model.RoleSystem,
			Content: summary,
		})
	}

	// Preserved important middle messages
	result = append(result, preservedMiddle...)

	// Keep last N
	result = append(result, messages[middleEnd:]...)

	return result, summaryUsed
}

// buildFallbackSummary preserves concrete user goals, tools and failures without an LLM.
func (c *Compressor) buildFallbackSummary(messages []model.Message) string {
	userCount, assistantCount, toolCount := 0, 0, 0
	toolNames := make(map[string]bool)
	var goals, failures []string
	for _, message := range messages {
		switch message.Role {
		case model.RoleSystem:
			if len(goals) < 3 && (strings.HasPrefix(message.Content, "## Conversation Summary") || strings.HasPrefix(message.Content, "[Compressed conversation")) {
				goals = append(goals, shortSummaryText(message.Content, 300))
			}
		case model.RoleUser:
			userCount++
			if len(goals) < 3 && strings.TrimSpace(message.Content) != "" {
				goals = append(goals, shortSummaryText(message.Content, 200))
			}
		case model.RoleAssistant:
			assistantCount++
			for _, call := range message.ToolCalls {
				toolNames[call.Function.Name] = true
			}
		case model.RoleTool:
			toolCount++
			lower := strings.ToLower(message.Content)
			if len(failures) < 2 && (strings.Contains(lower, "error") || strings.Contains(lower, "失败")) {
				failures = append(failures, shortSummaryText(message.Content, 200))
			}
		}
	}
	parts := []string{fmt.Sprintf("%d user messages", userCount), fmt.Sprintf("%d assistant messages", assistantCount)}
	if toolCount > 0 {
		parts = append(parts, fmt.Sprintf("%d tool results", toolCount))
	}
	if len(toolNames) > 0 {
		names := make([]string, 0, len(toolNames))
		for name := range toolNames {
			names = append(names, name)
		}
		parts = append(parts, "tools="+strings.Join(names, ","))
	}
	if len(goals) > 0 {
		parts = append(parts, "user goals="+strings.Join(goals, " | "))
	}
	if len(failures) > 0 {
		parts = append(parts, "failures="+strings.Join(failures, " | "))
	}
	return "[Compressed conversation: " + strings.Join(parts, "; ") + "]"
}

func shortSummaryText(value string, maximum int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > maximum {
		return string(runes[:maximum]) + "..."
	}
	return value
}

// compressToolOutputs truncates large tool result messages in-place.
func (c *Compressor) compressToolOutputs(messages []model.Message, importance []bool) []model.Message {
	result := make([]model.Message, len(messages))
	copy(result, messages)

	for i := range result {
		if result[i].Role != model.RoleTool {
			continue
		}
		if importance[i] {
			continue // Don't compress important tool results
		}
		if len(result[i].Content) > ToolOutputThreshold {
			result[i].Content = CompressToolOutput(result[i].Content)
		}
	}
	return result
}

// EstimateTokens is a conservative, Unicode-aware estimate without provider-specific tokenizer dependencies.
func EstimateTokens(text string) int {
	ascii, other := 0, 0.0
	for _, r := range text {
		if r <= 127 {
			ascii++
		} else if unicode.Is(unicode.Han, r) {
			other += 1.5
		} else {
			other++
		}
	}
	tokens := int(float64(ascii)/4 + other)
	if tokens == 0 && len(text) > 0 {
		return 1
	}
	return tokens
}

// EstimateMessageTokens includes roles, tool arguments and tool-call IDs.
func EstimateMessageTokens(messages []model.Message) int {
	total := 0
	for _, message := range messages {
		total += EstimateTokens(message.Content) + EstimateTokens(message.Role) + EstimateTokens(message.ToolCallID) + 4
		if len(message.ToolCalls) > 0 {
			if encoded, err := json.Marshal(message.ToolCalls); err == nil {
				total += EstimateTokens(string(encoded)) + 4
			}
		}
	}
	return total
}

// EstimateToolDefinitionsTokens accounts for schemas sent alongside messages.
func EstimateToolDefinitionsTokens(definitions []model.ToolDefinition) int {
	encoded, err := json.Marshal(definitions)
	if err != nil {
		return 0
	}
	return EstimateTokens(string(encoded))
}

// Summarize creates a durable summary for an exact historical boundary.
func (c *Compressor) Summarize(ctx context.Context, messages []model.Message) (string, bool) {
	if len(messages) == 0 {
		return "", false
	}
	if c.summarizer != nil {
		result := c.summarizer.SummarizeMessages(ctx, messages)
		return result.Summary, result.Generated
	}
	return c.buildFallbackSummary(messages), false
}

// TokenCount represents token count info for a message.
type TokenCount struct {
	ContentTokens int
	Overhead      int // role, formatting overhead
	ToolTokens    int
}

// Total returns total tokens for this message.
func (t TokenCount) Total() int {
	return t.ContentTokens + t.Overhead + t.ToolTokens
}

// SumTokens sums up total tokens from a slice of TokenCount.
func SumTokens(counts []TokenCount) int {
	total := 0
	for _, c := range counts {
		total += c.Total()
	}
	return total
}
