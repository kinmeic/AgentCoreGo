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
	"strings"
	"time"
)

// Client provides access to an OpenAI-compatible LLM API.
// It implements the Provider interface.
type Client struct {
	cfg        config.LLMConfig
	httpClient *http.Client
}

// Verify Client implements Provider at compile time.
var _ Provider = (*Client)(nil)

// NewClient creates a new LLM client.
func NewClient(cfg config.LLMConfig) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 2 * time.Minute
	return &Client{cfg: cfg, httpClient: &http.Client{Transport: transport}}
}

// Name returns the provider identifier.
func (c *Client) Name() string { return "openai" }

// ModelName returns the model being used.
func (c *Client) ModelName() string { return c.cfg.Model }

// MaxContextWindow returns the maximum context window in tokens.
func (c *Client) MaxContextWindow() int { return c.cfg.ContextWindow }

// completionRequest is the request body for the chat completions API.
type completionRequest struct {
	Model       string                 `json:"model"`
	Messages    []apiMessage           `json:"messages"`
	Tools       []model.ToolDefinition `json:"tools,omitempty"`
	Stream      bool                   `json:"stream"`
	Temperature float64                `json:"temperature,omitempty"`
	StreamOpts  *streamOptions         `json:"stream_options,omitempty"`
	MaxTokens   int                    `json:"max_tokens,omitempty"`
	Reasoning   string                 `json:"reasoning_effort,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// apiMessage is the wire format for messages sent to the API.
type apiMessage struct {
	Role       string        `json:"role"`
	Content    string        `json:"content,omitempty"`
	ToolCalls  []apiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

type apiToolCall struct {
	ID       string      `json:"id"`
	Type     string      `json:"type"`
	Function apiFunction `json:"function"`
}

type apiFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// completionResponse is the response from the chat completions API (non-streaming).
type completionResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Index        int        `json:"index"`
		Message      apiMessage `json:"message"`
		FinishReason string     `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// toAPIMessages converts model messages to the API wire format.
func toAPIMessages(msgs []model.Message) []apiMessage {
	result := make([]apiMessage, 0, len(msgs))
	for _, m := range msgs {
		am := apiMessage{
			Role:    m.Role,
			Content: m.Content,
		}
		if len(m.ToolCalls) > 0 {
			am.ToolCalls = make([]apiToolCall, len(m.ToolCalls))
			for i, tc := range m.ToolCalls {
				am.ToolCalls[i] = apiToolCall{
					ID:   tc.ID,
					Type: tc.Type,
					Function: apiFunction{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				}
			}
		}
		if m.ToolCallID != "" {
			am.ToolCallID = m.ToolCallID
		}
		result = append(result, am)
	}
	return result
}

// Complete sends a non-streaming completion request.
func (c *Client) Complete(ctx context.Context, messages []model.Message, tools []model.ToolDefinition) (*model.Message, *model.UsageInfo, error) {
	reqBody := completionRequest{
		Model:       c.cfg.Model,
		Messages:    toAPIMessages(messages),
		Tools:       tools,
		Stream:      false,
		Temperature: c.cfg.Temperature,
		MaxTokens:   c.cfg.MaxOutputTokens,
		Reasoning:   c.cfg.ReasoningEffort,
	}

	resp, err := c.doRequest(ctx, reqBody)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, nil, classifyError(resp.StatusCode, body)
	}

	var compResp completionResponse
	if err := json.Unmarshal(body, &compResp); err != nil {
		return nil, nil, fmt.Errorf("parse response: %w", err)
	}

	if len(compResp.Choices) == 0 {
		return nil, nil, fmt.Errorf("no choices in response")
	}

	choice := compResp.Choices[0]
	msg := &model.Message{
		Role:    choice.Message.Role,
		Content: choice.Message.Content,
	}

	// Convert tool calls
	if len(choice.Message.ToolCalls) > 0 {
		msg.ToolCalls = make([]model.ToolCall, len(choice.Message.ToolCalls))
		for i, tc := range choice.Message.ToolCalls {
			msg.ToolCalls[i] = model.ToolCall{
				ID:   tc.ID,
				Type: tc.Type,
				Function: model.ToolFunction{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			}
		}
	}

	usage := &model.UsageInfo{
		PromptTokens:     compResp.Usage.PromptTokens,
		CompletionTokens: compResp.Usage.CompletionTokens,
		TotalTokens:      compResp.Usage.TotalTokens,
	}

	return msg, usage, nil
}

// CompleteStream sends a streaming completion request and returns a channel of stream events.
func (c *Client) CompleteStream(ctx context.Context, messages []model.Message, tools []model.ToolDefinition) (<-chan StreamChunk, error) {
	reqBody := completionRequest{
		Model:       c.cfg.Model,
		Messages:    toAPIMessages(messages),
		Tools:       tools,
		Stream:      true,
		Temperature: c.cfg.Temperature,
		MaxTokens:   c.cfg.MaxOutputTokens,
		Reasoning:   c.cfg.ReasoningEffort,
		StreamOpts:  &streamOptions{IncludeUsage: true},
	}

	resp, err := c.doRequest(ctx, reqBody)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read error response (status %d): %w", resp.StatusCode, readErr)
		}
		return nil, classifyError(resp.StatusCode, body)
	}

	ch := make(chan StreamChunk, 32)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		parseSSEStream(ctx, resp.Body, ch)
	}()

	return ch, nil
}

// doRequest performs the HTTP request with retry logic.
func (c *Client) doRequest(ctx context.Context, reqBody completionRequest) (*http.Response, error) {
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(attempt*attempt) * time.Second
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}

		url := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonData))
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("http request: %w", err)
			continue
		}

		// Retry on rate limit or server errors
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
			resp.Body.Close()
			if readErr != nil {
				lastErr = fmt.Errorf("read error response (status %d): %w", resp.StatusCode, readErr)
			} else {
				lastErr = classifyError(resp.StatusCode, body)
			}
			continue
		}

		return resp, nil
	}

	return nil, fmt.Errorf("max retries exceeded: %w", lastErr)
}
