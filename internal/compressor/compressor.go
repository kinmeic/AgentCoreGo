package compressor

import (
	"agent-core/internal/config"
	"fmt"
)

// Compressor manages context window compression.
type Compressor struct {
	cfg config.CompressionConfig
}

// NewCompressor creates a new context compressor.
func NewCompressor(cfg config.CompressionConfig) *Compressor {
	return &Compressor{cfg: cfg}
}

// ShouldCompress returns true if the context is over the compression threshold.
func (c *Compressor) ShouldCompress(currentTokens, contextWindow int) bool {
	if contextWindow <= 0 {
		return false
	}
	ratio := float64(currentTokens) / float64(contextWindow)
	return ratio >= c.cfg.ThresholdPercent
}

// CompressResult contains the instructions for compressing a message list.
type CompressResult struct {
	KeepFirst    int    // Number of messages to keep from the start
	KeepLast     int    // Number of messages to keep from the end
	Summary      string // Summary of the compressed middle section
	ShouldCompress bool // Whether compression is needed
}

// Compress returns compression instructions for the message history.
// It protects the first N and last N messages, summarizing the middle.
func (c *Compressor) Compress(totalMessages, userMsgCount, assistantMsgCount int) CompressResult {
	if totalMessages <= c.cfg.ProtectFirstN+c.cfg.ProtectLastN+1 {
		return CompressResult{ShouldCompress: false}
	}

	summary := fmt.Sprintf(
		"[Conversation history compressed: %d user messages, %d assistant messages were summarized. Key context has been preserved in the messages below.]",
		userMsgCount, assistantMsgCount,
	)

	return CompressResult{
		KeepFirst:      c.cfg.ProtectFirstN,
		KeepLast:       c.cfg.ProtectLastN,
		Summary:        summary,
		ShouldCompress: true,
	}
}

// EstimateTokens provides a rough token estimate based on character count.
// This is a simple heuristic; production use should use tiktoken or similar.
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
