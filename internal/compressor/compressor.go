package compressor

import (
	"agent-core/internal/config"
	"agent-core/internal/model"
	"context"
	"fmt"
	"strings"
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

// Compressor manages context window compression with importance tracking and LLM summaries.
type Compressor struct {
	cfg        config.CompressionConfig
	summarizer *Summarizer
}

// NewCompressor creates a new context compressor.
func NewCompressor(cfg config.CompressionConfig, summarizer *Summarizer) *Compressor {
	return &Compressor{
		cfg:        cfg,
		summarizer: summarizer,
	}
}

// CompressResult contains the compressed message list ready for the LLM.
type CompressResult struct {
	Messages       []model.Message
	ShouldCompress bool
	Level          CompressLevel
	TokenEstimate  int
	SummaryUsed    bool
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

// Compress processes the message list and returns a compressed version.
// It protects important messages, compresses tool outputs, and optionally
// generates an LLM summary for the middle segment.
func (c *Compressor) Compress(ctx context.Context, messages []model.Message, contextWindow int) CompressResult {
	// Estimate total tokens
	totalTokens := 0
	for _, m := range messages {
		totalTokens += EstimateTokens(m.Content)
	}

	level := c.DetermineLevel(totalTokens, contextWindow)
	if level == CompressNone {
		return CompressResult{
			Messages:       messages,
			ShouldCompress: false,
			Level:          CompressNone,
			TokenEstimate:  totalTokens,
		}
	}

	// Step 1: Mark important messages
	importance := MarkImportance(messages)

	// Step 2: Compress large tool outputs in-place
	compressed := c.compressToolOutputs(messages, importance)

	// Step 3: Determine keep-first and keep-last based on level
	keepFirst, keepLast := c.keepBounds(len(compressed), level)

	// Step 4: Build result with optional summary
	result := c.buildCompressedMessages(ctx, compressed, importance, keepFirst, keepLast, level)

	// Re-estimate tokens
	newTokens := 0
	for _, m := range result {
		newTokens += EstimateTokens(m.Content)
	}

	return CompressResult{
		Messages:       result,
		ShouldCompress: true,
		Level:          level,
		TokenEstimate:  newTokens,
		SummaryUsed:    level >= CompressMedium,
	}
}

// keepBounds returns how many messages to keep from start and end.
func (c *Compressor) keepBounds(total int, level CompressLevel) (first, last int) {
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
) []model.Message {
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
	if level >= CompressMedium && c.summarizer != nil {
		middleMessages := messages[middleStart:middleEnd]
		result := c.summarizer.SummarizeMessages(ctx, middleMessages)
		summary = result.Summary
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

	return result
}

// buildFallbackSummary creates a text-only summary when LLM is unavailable.
func (c *Compressor) buildFallbackSummary(messages []model.Message) string {
	userCount, assistantCount, toolCount := 0, 0, 0
	var toolNames []string
	for _, m := range messages {
		switch m.Role {
		case model.RoleUser:
			userCount++
		case model.RoleAssistant:
			assistantCount++
		case model.RoleTool:
			toolCount++
			// Try to extract tool name from content
		}
	}

	parts := []string{
		fmt.Sprintf("%d user messages", userCount),
		fmt.Sprintf("%d assistant messages", assistantCount),
	}
	if toolCount > 0 {
		parts = append(parts, fmt.Sprintf("%d tool results", toolCount))
	}
	if len(toolNames) > 0 {
		parts = append(parts, fmt.Sprintf("Tools used: %s", strings.Join(toolNames, ", ")))
	}

	return fmt.Sprintf("[Conversation history compressed: %s. Key context preserved below.]",
		strings.Join(parts, ", "))
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

// EstimateTokens provides a rough token estimate based on character count.
func EstimateTokens(text string) int {
	if len(text) == 0 {
		return 0
	}
	return (len(text) + 3) / 4 // ~3.5 chars per token average
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
