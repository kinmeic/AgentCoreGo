package llm

import (
	"agent-core/internal/config"
	"agent-core/internal/model"
	"agent-core/internal/telemetry"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client provides access to OpenAI Chat Completions and Anthropic Messages APIs.
// It implements the Provider interface.
type Client struct {
	initErr    error
	cfg        config.LLMConfig
	httpClient *http.Client
	recorder   *telemetry.Recorder
}

// SetRecorder enables provider-boundary model-io capture.
func (c *Client) SetRecorder(recorder *telemetry.Recorder) { c.recorder = recorder }

// Verify Client implements Provider at compile time.
var _ Provider = (*Client)(nil)

// NewClient creates a new LLM client.
func NewClient(cfg config.LLMConfig) *Client {
	if cfg.APIFormat == "" {
		cfg.APIFormat = config.APIFormatOpenAI
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	initErr := configureTransport(cfg, transport)
	httpClient := &http.Client{Transport: transport}
	if cfg.APIFormat == config.APIFormatAnthropic {
		// net/http does not treat x-api-key as a sensitive redirect header.
		// Reject cross-origin redirects before a native key can be forwarded.
		httpClient.CheckRedirect = func(request *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many Anthropic redirects")
			}
			if len(via) > 0 && (request.URL.Scheme != via[0].URL.Scheme || request.URL.Host != via[0].URL.Host) {
				return fmt.Errorf("cross-origin Anthropic redirect blocked")
			}
			return nil
		}
	}
	return &Client{cfg: cfg, httpClient: httpClient, initErr: initErr}
}

// Name returns the provider identifier.
func (c *Client) Name() string { return c.cfg.APIFormat }

// ModelName returns the model being used.
func (c *Client) ModelName() string { return c.cfg.Model }

// WithModelOptions creates a shallow client copy for a run-scoped model/effort override.
func (c *Client) WithModelOptions(model, effort string) Provider {
	clone := *c
	if model != "" {
		clone.cfg.Model = model
	}
	if effort != "" {
		clone.cfg.ReasoningEffort = effort
	}
	clone.httpClient = c.httpClient
	return &clone
}

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
	if err := config.ValidateAPIFormat(c.cfg.APIFormat); err != nil {
		return nil, nil, err
	}
	if c.cfg.APIFormat == config.APIFormatAnthropic {
		return c.completeAnthropic(ctx, messages, tools)
	}
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
	if err := config.ValidateAPIFormat(c.cfg.APIFormat); err != nil {
		return nil, err
	}
	if c.cfg.APIFormat == config.APIFormatAnthropic {
		return c.streamAnthropic(ctx, messages, tools)
	}
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

// doRequest performs the HTTP request with retry logic and per-attempt capture.
func (c *Client) doRequest(ctx context.Context, reqBody completionRequest) (*http.Response, error) {
	return c.doJSONRequest(ctx, strings.TrimRight(c.cfg.BaseURL, "/")+"/chat/completions", reqBody, reqBody.Stream)
}

// doJSONRequest shares transport, retries and wire capture across API formats.
func (c *Client) doJSONRequest(ctx context.Context, endpoint string, reqBody any, stream bool) (*http.Response, error) {
	if c.initErr != nil {
		return nil, c.initErr
	}
	if c.cfg.Model == "" {
		return nil, fmt.Errorf("llm.model is not configured")
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	var lastErr error
	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			backoff := c.retryDelay(attempt)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}
		requestCtx, requestCancel := context.WithCancel(ctx)
		req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(jsonData))
		if err != nil {
			requestCancel()
			return nil, fmt.Errorf("create request: %w", err)
		}
		req.Header = c.requestHeaders()
		startedAt := time.Now()
		resp, err := c.httpClient.Do(req)
		if err != nil {
			requestCancel()
			lastErr = fmt.Errorf("http request: %w", err)
			if captureErr := c.capture(ctx, attempt+1, startedAt, jsonData, nil, nil, false, lastErr); captureErr != nil {
				return nil, captureErr
			}
			continue
		}
		idleMs := c.cfg.StreamIdleTimeoutMs
		if !stream {
			idleMs = c.cfg.RequestTimeoutMs
		}
		if idleMs <= 0 {
			idleMs = config.DefaultModelStreamIdleTimeoutMs
		}
		resp.Body = newIdleBody(resp.Body, time.Duration(idleMs)*time.Millisecond, requestCancel)
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
			resp.Body.Close()
			if readErr != nil {
				lastErr = fmt.Errorf("read error response (status %d): %w", resp.StatusCode, readErr)
			} else {
				lastErr = classifyError(resp.StatusCode, body)
			}
			if captureErr := c.capture(ctx, attempt+1, startedAt, jsonData, resp, body, false, lastErr); captureErr != nil {
				return nil, captureErr
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
			resp.Body.Close()
			if readErr != nil {
				return nil, readErr
			}
			resp.Body = io.NopCloser(bytes.NewReader(body))
			lastErr = classifyError(resp.StatusCode, body)
			if captureErr := c.capture(ctx, attempt+1, startedAt, jsonData, resp, body, false, lastErr); captureErr != nil {
				return nil, captureErr
			}
			return resp, nil
		}
		if stream {
			resp.Body = &recordingBody{reader: resp.Body, client: c, ctx: ctx, attempt: attempt + 1, startedAt: startedAt, request: jsonData, response: resp}
			return resp, nil
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
		resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
		if captureErr := c.capture(ctx, attempt+1, startedAt, jsonData, resp, body, false, nil); captureErr != nil {
			return nil, captureErr
		}
		return resp, nil
	}
	return nil, fmt.Errorf("max retries exceeded: %w", lastErr)
}
