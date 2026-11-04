package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// StreamChunk represents a parsed chunk from the SSE stream.
type StreamChunk struct {
	// Content delta
	Content string

	// Provider-neutral reasoning delta, kept separate from the visible answer.
	Reasoning string

	// Provider-specific response state used only for protocol continuity.
	ProviderState json.RawMessage

	// Tool call deltas (may be partial)
	ToolCalls []ToolCallDelta

	// Finish reason (set on the final chunk)
	FinishReason string

	// Usage (set on the final chunk if include_usage was requested)
	Usage *StreamUsage

	// Error if parsing failed
	Err error
}

// ToolCallDelta represents a partial tool call update from the stream.
type ToolCallDelta struct {
	Index     int
	ID        string
	Type      string
	Name      string
	Arguments string
}

// StreamUsage represents usage information from the stream.
type StreamUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// streamResponse represents the SSE response format.
type streamResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Index        int     `json:"index"`
		Delta        delta   `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *StreamUsage `json:"usage,omitempty"`
}

type delta struct {
	Role             string          `json:"role,omitempty"`
	Content          string          `json:"content,omitempty"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	ToolCalls        []toolCallDelta `json:"tool_calls,omitempty"`
}

type toolCallDelta struct {
	Index    int        `json:"index"`
	ID       string     `json:"id,omitempty"`
	Type     string     `json:"type,omitempty"`
	Function *funcDelta `json:"function,omitempty"`
}

type funcDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// parseSSEStream reads an SSE stream and sends parsed chunks to the channel.
// The context is checked on every send to prevent goroutine leaks if the caller stops reading.
func parseSSEStream(ctx context.Context, reader io.Reader, ch chan<- StreamChunk) {
	scanner := bufio.NewScanner(reader)
	// Increase buffer size for large chunks
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	// sendChunk sends a chunk to the channel, respecting context cancellation.
	// Returns false if the context was cancelled (caller should stop).
	sendChunk := func(chunk StreamChunk) bool {
		select {
		case ch <- chunk:
			return true
		case <-ctx.Done():
			return false
		}
	}

	completed := false
	for scanner.Scan() {
		line := scanner.Text()

		// SSE allows an optional single space after the field colon.
		if !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
		if data == "[DONE]" {
			completed = true
			return
		}

		var resp streamResponse
		if err := json.Unmarshal([]byte(data), &resp); err != nil {
			if !sendChunk(StreamChunk{Err: fmt.Errorf("parse stream chunk: %w", err)}) {
				return
			}
			continue
		}

		// Handle usage-only chunks (final chunk with include_usage)
		if resp.Usage != nil && len(resp.Choices) == 0 {
			if !sendChunk(StreamChunk{
				Usage: &StreamUsage{
					PromptTokens:     resp.Usage.PromptTokens,
					CompletionTokens: resp.Usage.CompletionTokens,
					TotalTokens:      resp.Usage.TotalTokens,
				},
			}) {
				return
			}
			continue
		}

		for _, choice := range resp.Choices {
			chunk := StreamChunk{}

			// Content delta
			if choice.Delta.Content != "" {
				chunk.Content = choice.Delta.Content
			}
			if choice.Delta.ReasoningContent != "" {
				chunk.Reasoning = choice.Delta.ReasoningContent
			}

			// Tool call deltas
			if len(choice.Delta.ToolCalls) > 0 {
				chunk.ToolCalls = make([]ToolCallDelta, 0, len(choice.Delta.ToolCalls))
				for _, tc := range choice.Delta.ToolCalls {
					tcd := ToolCallDelta{
						Index: tc.Index,
						ID:    tc.ID,
						Type:  tc.Type,
					}
					if tc.Function != nil {
						tcd.Name = tc.Function.Name
						tcd.Arguments = tc.Function.Arguments
					}
					chunk.ToolCalls = append(chunk.ToolCalls, tcd)
				}
			}

			// Finish reason
			if choice.FinishReason != nil {
				chunk.FinishReason = *choice.FinishReason
			}

			// Usage on same chunk
			if resp.Usage != nil {
				chunk.Usage = &StreamUsage{
					PromptTokens:     resp.Usage.PromptTokens,
					CompletionTokens: resp.Usage.CompletionTokens,
					TotalTokens:      resp.Usage.TotalTokens,
				}
			}

			if !sendChunk(chunk) {
				return
			}
		}
	}

	if err := scanner.Err(); err != nil {
		sendChunk(StreamChunk{Err: fmt.Errorf("read stream: %w", err)})
		return
	}
	if ctx.Err() == nil && !completed {
		sendChunk(StreamChunk{Err: fmt.Errorf("read stream: %w", io.ErrUnexpectedEOF)})
	}
}
