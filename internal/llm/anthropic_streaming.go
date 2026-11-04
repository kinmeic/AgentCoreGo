package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

func anthropicStreamError(body []byte) *APIError {
	var payload apiErrorResponse
	_ = json.Unmarshal(body, &payload)
	status := http.StatusInternalServerError
	switch payload.Error.Type {
	case "overloaded_error":
		status = 529
	case "rate_limit_error":
		status = http.StatusTooManyRequests
	case "authentication_error":
		status = http.StatusUnauthorized
	case "permission_error":
		status = http.StatusForbidden
	case "not_found_error":
		status = http.StatusNotFound
	case "invalid_request_error":
		status = http.StatusBadRequest
	case "request_too_large":
		status = http.StatusRequestEntityTooLarge
	}
	return classifyError(status, body)
}

func anthropicFinishReason(reason string) string {
	switch reason {
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	case "end_turn", "stop_sequence":
		return "stop"
	case "refusal":
		return "content_filter"
	default:
		return reason
	}
}

type anthropicStreamEvent struct {
	Type         string             `json:"type"`
	Index        *int               `json:"index"`
	Message      *anthropicResponse `json:"message"`
	ContentBlock *anthropicBlock    `json:"content_block"`
	Delta        struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			Thinking    string `json:"thinking"`
			Signature   string `json:"signature"`
			PartialJSON string `json:"partial_json"`
			StopReason  string `json:"stop_reason"`
		} `json:"delta"`
	Usage *anthropicUsage `json:"usage"`
}

type anthropicStreamBlock struct {
	block           anthropicBlock
	input           strings.Builder
	closed          bool
	hasInitialInput bool
}

// Parse complete SSE frames, including CRLF, comments and multiline data. A
// message_stop is required: EOF must never turn a partial tool call into success.
func parseAnthropicStream(ctx context.Context, reader io.Reader, out chan<- StreamChunk) {
	const maximum = 1024 * 1024
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maximum)
	send := func(chunk StreamChunk) bool {
		select {
		case out <- chunk:
			return true
		case <-ctx.Done():
			return false
		}
	}
	fail := func(err error) bool { send(StreamChunk{Err: err}); return false }
	blocks := make(map[int]*anthropicStreamBlock)
	ids := make(map[string]bool)
	var usage anthropicUsage
	var finishReason string
	started, completed, hasUsage, hasContent := false, false, false, false
	process := func(eventName, data string) bool {
		var event anthropicStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return fail(fmt.Errorf("parse Anthropic stream: %w", err))
		}
		if event.Type == "" {
			event.Type = eventName
		}
		if eventName != "" && eventName != event.Type {
			return fail(fmt.Errorf("Anthropic SSE event type mismatch"))
		}
		if event.Type == "error" {
			return fail(anthropicStreamError([]byte(data)))
		}
		switch event.Type {
		case "ping":
			return true
		case "message_start":
			if started || event.Message == nil || event.Message.Type != "message" || event.Message.Role != "assistant" {
				return fail(fmt.Errorf("invalid Anthropic message_start"))
			}
			started = true
			usage.merge(event.Message.Usage)
			hasUsage = event.Message.Usage != nil
		case "content_block_start":
			if !started || finishReason != "" || event.Index == nil || *event.Index < 0 || event.ContentBlock == nil || blocks[*event.Index] != nil {
				return fail(fmt.Errorf("invalid Anthropic content_block_start"))
			}
			block := *event.ContentBlock
			state := &anthropicStreamBlock{block: block}
			blocks[*event.Index] = state
			switch block.Type {
			case "text":
				hasContent = true
				if block.Text != "" && !send(StreamChunk{Content: block.Text}) {
					return false
				}
			case "thinking", "redacted_thinking":
				hasContent = true
				if block.Thinking != "" && !send(StreamChunk{Reasoning: block.Thinking}) {
					return false
				}
			case "tool_use":
				if block.ID == "" || block.Name == "" || ids[block.ID] {
					return fail(fmt.Errorf("invalid Anthropic streamed tool_use"))
				}
				input, err := responseToolInput(block.Input)
				if err != nil {
					return fail(err)
				}
				state.block.Input = input
				var initial map[string]json.RawMessage
				_ = json.Unmarshal(input, &initial)
				state.hasInitialInput = len(initial) > 0
				ids[block.ID], hasContent = true, true
				if !send(StreamChunk{ToolCalls: []ToolCallDelta{{Index: *event.Index, ID: block.ID, Type: "function", Name: block.Name}}}) {
					return false
				}
			}
		case "content_block_delta", "content_block_stop":
			if event.Index == nil || blocks[*event.Index] == nil || blocks[*event.Index].closed || finishReason != "" {
				return fail(fmt.Errorf("Anthropic stream references an unopened content block"))
			}
			state := blocks[*event.Index]
			if event.Type == "content_block_stop" {
				state.closed = true
				if state.block.Type == "tool_use" {
					input := state.input.String()
					if input == "" {
						if !send(StreamChunk{ToolCalls: []ToolCallDelta{{Index: *event.Index, Arguments: string(state.block.Input)}}}) {
							return false
						}
					} else if _, err := toolInput(input); err != nil {
						return fail(fmt.Errorf("incomplete Anthropic tool input: %w", err))
					} else {
						state.block.Input = json.RawMessage(input)
					}
				}
				return true
			}
			switch event.Delta.Type {
			case "text_delta":
				if state.block.Type != "text" {
					return fail(fmt.Errorf("Anthropic text delta has a non-text block"))
				}
				state.block.Text += event.Delta.Text
				if event.Delta.Text != "" && !send(StreamChunk{Content: event.Delta.Text}) {
					return false
				}
			case "thinking_delta":
				if state.block.Type != "thinking" {
					return fail(fmt.Errorf("Anthropic thinking delta has a non-thinking block"))
				}
				state.block.Thinking += event.Delta.Thinking
				if event.Delta.Thinking != "" && !send(StreamChunk{Reasoning: event.Delta.Thinking}) {
					return false
				}
			case "signature_delta":
				if state.block.Type != "thinking" {
					return fail(fmt.Errorf("Anthropic signature delta has a non-thinking block"))
				}
				state.block.Signature += event.Delta.Signature
			case "input_json_delta":
				if state.block.Type != "tool_use" {
					return fail(fmt.Errorf("Anthropic input delta has a non-tool block"))
				}
				if state.hasInitialInput && event.Delta.PartialJSON != "" {
					return fail(fmt.Errorf("Anthropic tool input mixes an initial object with JSON deltas"))
				}
				if state.input.Len()+len(event.Delta.PartialJSON) > maximum {
					return fail(fmt.Errorf("Anthropic tool input exceeds stream budget"))
				}
				state.input.WriteString(event.Delta.PartialJSON)
				if event.Delta.PartialJSON != "" && !send(StreamChunk{ToolCalls: []ToolCallDelta{{Index: *event.Index, Arguments: event.Delta.PartialJSON}}}) {
					return false
				}
			}
		case "message_delta":
			if !started {
				return fail(fmt.Errorf("Anthropic message_delta precedes message_start"))
			}
			for _, state := range blocks {
				if !state.closed {
					return fail(fmt.Errorf("Anthropic message_delta precedes content_block_stop"))
				}
			}
			usage.merge(event.Usage)
			hasUsage = hasUsage || event.Usage != nil
			if event.Delta.StopReason != "" {
				finishReason = anthropicFinishReason(event.Delta.StopReason)
			}
		case "message_stop":
			if !started || finishReason == "" || !hasContent {
				return fail(fmt.Errorf("incomplete Anthropic message_stop"))
			}
			if (finishReason == "tool_calls") != (len(ids) > 0) {
				return fail(fmt.Errorf("Anthropic streamed tools ended with inconsistent stop reason %q", finishReason))
			}
			for _, state := range blocks {
				if !state.closed {
					return fail(fmt.Errorf("Anthropic message_stop has an open content block"))
				}
			}
			indexes := make([]int, 0, len(blocks))
			for index := range blocks {
				indexes = append(indexes, index)
			}
			sort.Ints(indexes)
			ordered := make([]anthropicBlock, 0, len(indexes))
			for _, index := range indexes {
				ordered = append(ordered, blocks[index].block)
			}
			providerState, err := anthropicStateFromBlocks(ordered)
			if err != nil {
				return fail(err)
			}
			chunk := StreamChunk{FinishReason: finishReason, ProviderState: providerState}
			if hasUsage {
				chunk.Usage = usage.tokens()
			}
			completed = true
			send(chunk)
			return false
		}
		return true
	}
	var data strings.Builder
	var eventName string
	dispatch := func() bool {
		if data.Len() == 0 {
			eventName = ""
			return true
		}
		value := strings.TrimSuffix(data.String(), "\n")
		data.Reset()
		name := eventName
		eventName = ""
		return process(name, value)
	}
	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		line := scanner.Text()
		if line == "" {
			if !dispatch() {
				return
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimPrefix(strings.TrimPrefix(line, "event:"), " ")
		}
		if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
			if data.Len()+len(value)+1 > maximum {
				fail(fmt.Errorf("Anthropic SSE frame exceeds stream budget"))
				return
			}
			data.WriteString(value)
			data.WriteByte('\n')
		}
	}
	if ctx.Err() != nil {
		return
	}
	if err := scanner.Err(); err != nil {
		fail(fmt.Errorf("read Anthropic stream: %w", err))
		return
	}
	if !dispatch() {
		return
	}
	if !completed {
		fail(fmt.Errorf("read Anthropic stream: %w", io.ErrUnexpectedEOF))
	}
}
