package llm

import (
	"agent-core/internal/config"
	"agent-core/internal/model"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const anthropicVersion = "2023-06-01"
const anthropicInterleavedThinkingBeta = "interleaved-thinking-2025-05-14"

func (c *Client) requestHeaders() http.Header {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	if c.cfg.APIFormat == config.APIFormatAnthropic {
		headers.Set("x-api-key", c.cfg.APIKey)
		headers.Set("anthropic-version", anthropicVersion)
	} else {
		headers.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	return headers
}

// Accept an API origin, a /v1 base (including gateway prefixes), or a full
// Messages endpoint. Parse the URL so paths never get appended to a query.
func anthropicEndpoint(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return "", fmt.Errorf("invalid Anthropic base URL")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	switch {
	case strings.HasSuffix(u.Path, "/messages"):
	case strings.HasSuffix(u.Path, "/v1"):
		u.Path += "/messages"
	default:
		u.Path += "/v1/messages"
	}
	u.RawPath = ""
	return u.String(), nil
}

type anthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	Data      string          `json:"data,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   any             `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type anthropicProviderState struct {
	Content []anthropicBlock `json:"content"`
}

func anthropicStateFromBlocks(blocks []anthropicBlock) (json.RawMessage, error) {
	state, err := json.Marshal(anthropicProviderState{Content: blocks})
	if err != nil {
		return nil, fmt.Errorf("marshal Anthropic provider state: %w", err)
	}
	return state, nil
}

func anthropicBlocksFromState(raw json.RawMessage) ([]anthropicBlock, error) {
	var state anthropicProviderState
	if len(raw) == 0 {
		return nil, fmt.Errorf("invalid empty Anthropic provider state")
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("decode Anthropic provider state: %w", err)
	}
	if len(state.Content) == 0 {
		return nil, fmt.Errorf("Anthropic provider state has no content blocks")
	}
	return state.Content, nil
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

type anthropicRequest struct {
	Model        string             `json:"model"`
	MaxTokens    int                `json:"max_tokens"`
	System       string             `json:"system,omitempty"`
	Messages     []anthropicMessage `json:"messages"`
	Tools        []anthropicTool    `json:"tools,omitempty"`
	Stream       bool               `json:"stream"`
	Thinking     *anthropicThinking `json:"thinking"`
	OutputConfig map[string]string  `json:"output_config,omitempty"`
}

func toolInput(raw string) (json.RawMessage, error) {
	value := bytes.TrimSpace([]byte(raw))
	if len(value) == 0 {
		value = []byte("{}")
	}
	if !json.Valid(value) || value[0] != '{' {
		return nil, fmt.Errorf("tool input must be a JSON object")
	}
	return json.RawMessage(value), nil
}

func responseToolInput(raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("Anthropic tool_use is missing its input object")
	}
	return toolInput(string(raw))
}

func (c *Client) newAnthropicRequest(messages []model.Message, tools []model.ToolDefinition, stream bool) (anthropicRequest, error) {
	thinkingType := c.cfg.ThinkingType
	if thinkingType == "" {
		thinkingType = config.ThinkingTypeDisabled
	}
	request := anthropicRequest{Model: c.cfg.Model, MaxTokens: c.cfg.MaxOutputTokens, Stream: stream,
		Messages: []anthropicMessage{}}
	if request.MaxTokens <= 0 {
		return request, fmt.Errorf("Anthropic max_output_tokens must be positive")
	}
	if err := config.ValidateThinking(c.cfg); err != nil {
		return request, err
	}
	request.Thinking = &anthropicThinking{Type: thinkingType}
	if thinkingType == config.ThinkingTypeEnabled {
		request.Thinking.BudgetTokens = c.cfg.ThinkingBudgetTokens
	}
	if c.cfg.ReasoningEffort != "" {
		request.OutputConfig = map[string]string{"effort": c.cfg.ReasoningEffort}
	}
	var system []string
	pending := make(map[string]bool)
	appendBlocks := func(role string, blocks []anthropicBlock) {
		if len(blocks) == 0 {
			return
		}
		if n := len(request.Messages); n > 0 && request.Messages[n-1].Role == role {
			request.Messages[n-1].Content = append(request.Messages[n-1].Content, blocks...)
		} else {
			request.Messages = append(request.Messages, anthropicMessage{Role: role, Content: blocks})
		}
	}
	for _, message := range messages {
		if message.Role == model.RoleSystem {
			if message.Content != "" {
				system = append(system, message.Content)
			}
			continue
		}
		if message.Role == model.RoleTool {
			if !pending[message.ToolCallID] {
				return request, fmt.Errorf("tool result %q has no matching tool_use", message.ToolCallID)
			}
			appendBlocks(model.RoleUser, []anthropicBlock{{Type: "tool_result", ToolUseID: message.ToolCallID, Content: message.Content, IsError: message.IsError}})
			delete(pending, message.ToolCallID)
			continue
		}
		if len(pending) != 0 {
			return request, fmt.Errorf("Anthropic tool_use must be followed by all matching tool results")
		}
		if message.Role != model.RoleUser && message.Role != model.RoleAssistant {
			return request, fmt.Errorf("unsupported Anthropic message role %q", message.Role)
		}
		var blocks []anthropicBlock
		if len(message.ProviderState) != 0 {
			if message.Role != model.RoleAssistant {
				return request, fmt.Errorf("Anthropic provider state is only valid for assistant messages")
			}
			stateBlocks, err := anthropicBlocksFromState(message.ProviderState)
			if err != nil {
				return request, err
			}
			for _, block := range stateBlocks {
				if thinkingType == config.ThinkingTypeDisabled && (block.Type == "thinking" || block.Type == "redacted_thinking") {
					continue
				}
				blocks = append(blocks, block)
			}
			for _, block := range blocks {
				if block.Type != "tool_use" {
					continue
				}
				if block.ID == "" || block.Name == "" || pending[block.ID] {
					return request, fmt.Errorf("invalid Anthropic tool_use in provider state")
				}
				if _, err := responseToolInput(block.Input); err != nil {
					return request, fmt.Errorf("tool %s: %w", block.Name, err)
				}
				pending[block.ID] = true
			}
		} else {
			if message.Content != "" {
				blocks = append(blocks, anthropicBlock{Type: "text", Text: message.Content})
			}
			for _, call := range message.ToolCalls {
				if message.Role != model.RoleAssistant || call.ID == "" || call.Function.Name == "" || pending[call.ID] {
					return request, fmt.Errorf("invalid Anthropic tool_use")
				}
				input, err := toolInput(call.Function.Arguments)
				if err != nil {
					return request, fmt.Errorf("tool %s: %w", call.Function.Name, err)
				}
				blocks = append(blocks, anthropicBlock{Type: "tool_use", ID: call.ID, Name: call.Function.Name, Input: input})
				pending[call.ID] = true
			}
		}
		appendBlocks(message.Role, blocks)
	}
	if len(pending) != 0 {
		return request, fmt.Errorf("Anthropic history contains tool_use without results")
	}
	if len(request.Messages) == 0 {
		return request, fmt.Errorf("Anthropic requires a non-empty message history")
	}
	request.System = strings.Join(system, "\n\n")
	for _, definition := range tools {
		if definition.Type != "function" {
			return request, fmt.Errorf("unsupported Anthropic tool type %q", definition.Type)
		}
		request.Tools = append(request.Tools, anthropicTool{Name: definition.Function.Name, Description: definition.Function.Description, InputSchema: definition.Function.Parameters})
	}
	return request, nil
}

type anthropicUsage struct {
	InputTokens              *int `json:"input_tokens"`
	OutputTokens             *int `json:"output_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
}

func (u *anthropicUsage) merge(next *anthropicUsage) {
	if next == nil {
		return
	}
	if next.InputTokens != nil {
		u.InputTokens = next.InputTokens
	}
	if next.OutputTokens != nil {
		u.OutputTokens = next.OutputTokens
	}
	if next.CacheCreationInputTokens != nil {
		u.CacheCreationInputTokens = next.CacheCreationInputTokens
	}
	if next.CacheReadInputTokens != nil {
		u.CacheReadInputTokens = next.CacheReadInputTokens
	}
}

func (u *anthropicUsage) tokens() *StreamUsage {
	if u == nil {
		return nil
	}
	value := func(v *int) int {
		if v == nil {
			return 0
		}
		return *v
	}
	input := value(u.InputTokens) + value(u.CacheCreationInputTokens) + value(u.CacheReadInputTokens)
	output := value(u.OutputTokens)
	return &StreamUsage{PromptTokens: input, CompletionTokens: output, TotalTokens: input + output}
}

type anthropicResponse struct {
	Type       string           `json:"type"`
	Role       string           `json:"role"`
	Content    []anthropicBlock `json:"content"`
	StopReason string           `json:"stop_reason"`
	Usage      *anthropicUsage  `json:"usage"`
}

func (c *Client) completeAnthropic(ctx context.Context, messages []model.Message, tools []model.ToolDefinition) (*model.Message, *model.UsageInfo, error) {
	request, err := c.newAnthropicRequest(messages, tools, false)
	if err != nil {
		return nil, nil, err
	}
	endpoint, err := anthropicEndpoint(c.cfg.BaseURL)
	if err != nil {
		return nil, nil, err
	}
	response, err := c.doJSONRequest(ctx, endpoint, request, false)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024))
	if err != nil {
		return nil, nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, nil, classifyError(response.StatusCode, body)
	}
	var decoded anthropicResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, nil, fmt.Errorf("parse Anthropic response: %w", err)
	}
	if decoded.Type == "error" {
		return nil, nil, anthropicStreamError(body)
	}
	if decoded.Type != "message" || decoded.Role != model.RoleAssistant || decoded.StopReason == "" {
		return nil, nil, fmt.Errorf("invalid Anthropic message response")
	}
	message := &model.Message{Role: model.RoleAssistant}
	var content strings.Builder
	seen := false
	ids := make(map[string]bool)
	for _, block := range decoded.Content {
		switch block.Type {
		case "text":
			seen = true
			content.WriteString(block.Text)
		case "thinking", "redacted_thinking":
			seen = true
		case "tool_use":
			if block.ID == "" || block.Name == "" || ids[block.ID] {
				return nil, nil, fmt.Errorf("invalid Anthropic tool_use response")
			}
			ids[block.ID] = true
			input, err := responseToolInput(block.Input)
			if err != nil {
				return nil, nil, err
			}
			seen = true
			message.ToolCalls = append(message.ToolCalls, model.ToolCall{ID: block.ID, Type: "function", Function: model.ToolFunction{Name: block.Name, Arguments: string(input)}})
		}
	}
	if !seen {
		return nil, nil, fmt.Errorf("Anthropic response contains no supported text or client tool blocks")
	}
	if (decoded.StopReason == "tool_use") != (len(message.ToolCalls) > 0) {
		return nil, nil, fmt.Errorf("Anthropic tool response ended with inconsistent stop_reason %q", decoded.StopReason)
	}
	message.Content = content.String()
	state, err := anthropicStateFromBlocks(decoded.Content)
	if err != nil {
		return nil, nil, err
	}
	message.ProviderState = state
	var usage *model.UsageInfo
	if tokens := decoded.Usage.tokens(); tokens != nil {
		usage = &model.UsageInfo{PromptTokens: tokens.PromptTokens, CompletionTokens: tokens.CompletionTokens, TotalTokens: tokens.TotalTokens}
	}
	return message, usage, nil
}

func (c *Client) streamAnthropic(ctx context.Context, messages []model.Message, tools []model.ToolDefinition) (<-chan StreamChunk, error) {
	request, err := c.newAnthropicRequest(messages, tools, true)
	if err != nil {
		return nil, err
	}
	endpoint, err := anthropicEndpoint(c.cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	response, err := c.doJSONRequest(ctx, endpoint, request, true)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024))
		if err != nil {
			return nil, err
		}
		return nil, classifyError(response.StatusCode, body)
	}
	ch := make(chan StreamChunk, 32)
	go func() {
		defer close(ch)
		defer response.Body.Close()
		parseAnthropicStream(ctx, response.Body, ch)
	}()
	return ch, nil
}
