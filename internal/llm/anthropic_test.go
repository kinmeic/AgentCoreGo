package llm

import (
	"agent-core/internal/config"
	"agent-core/internal/model"
	"agent-core/internal/telemetry"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func anthropicTestConfig(base string) config.LLMConfig {
	return config.LLMConfig{APIFormat: config.APIFormatAnthropic, BaseURL: base, APIKey: "test-anthropic-secret", Model: "claude-test", MaxOutputTokens: 512, ContextWindow: 100000}
}

func anthropicTestResponse() string {
	return `{"type":"message","role":"assistant","content":[{"type":"text","text":"hello "},{"type":"text","text":"world"},{"type":"tool_use","id":"next","name":"lookup","input":{"id":9007199254740993}}],"stop_reason":"tool_use","usage":{"input_tokens":100,"cache_creation_input_tokens":20,"cache_read_input_tokens":30,"output_tokens":10}}`
}

func anthropicFrame(event string, value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return "event: " + event + "\ndata: " + string(data) + "\n\n"
}

func anthropicEvent(event string, fields map[string]any) string {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["type"] = event
	return anthropicFrame(event, fields)
}

func anthropicTestStream() string {
	return anthropicEvent("message_start", map[string]any{"message": map[string]any{"type": "message", "role": "assistant", "usage": map[string]any{"input_tokens": 100, "cache_creation_input_tokens": 20, "cache_read_input_tokens": 30, "output_tokens": 1}}}) +
		": keep-alive\n\n" + anthropicEvent("ping", nil) + anthropicEvent("future_event", nil) +
		anthropicEvent("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": "hello "}}) +
		anthropicEvent("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": "world"}}) +
		anthropicEvent("content_block_stop", map[string]any{"index": 0}) +
		anthropicEvent("content_block_start", map[string]any{"index": 1, "content_block": map[string]any{"type": "tool_use", "id": "lookup-call", "name": "lookup", "input": map[string]any{}}}) +
		anthropicEvent("content_block_delta", map[string]any{"index": 1, "delta": map[string]any{"type": "input_json_delta", "partial_json": `{"city":"北`}}) +
		anthropicEvent("content_block_delta", map[string]any{"index": 1, "delta": map[string]any{"type": "input_json_delta", "partial_json": `京"}`}}) +
		anthropicEvent("content_block_stop", map[string]any{"index": 1}) +
		anthropicEvent("content_block_start", map[string]any{"index": 3, "content_block": map[string]any{"type": "tool_use", "id": "clock-call", "name": "clock", "input": map[string]any{}}}) +
		anthropicEvent("content_block_stop", map[string]any{"index": 3}) +
		anthropicEvent("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "tool_use"}, "usage": map[string]any{"output_tokens": 10}}) +
		anthropicEvent("message_stop", nil)
}

func TestAnthropicCompleteConvertsHistoryAndTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Method != http.MethodPost || r.Header.Get("x-api-key") != "test-anthropic-secret" || r.Header.Get("anthropic-version") != anthropicVersion || r.Header.Get("Authorization") != "" {
			t.Errorf("wrong Anthropic endpoint or headers: %s %v", r.URL, r.Header)
		}
		var raw map[string]json.RawMessage
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Error(err)
			return
		}
		for _, field := range []string{"stream_options", "reasoning_effort", "temperature"} {
			if _, exists := raw[field]; exists {
				t.Errorf("OpenAI parameter leaked into Messages: %s", field)
			}
		}
		var request anthropicRequest
		if err := json.Unmarshal(data, &request); err != nil {
			t.Error(err)
			return
		}
		if request.System != "first\n\nsecond" || request.MaxTokens != 512 || request.Model != "claude-test" || request.Stream || request.Thinking["type"] != "disabled" || request.OutputConfig["effort"] != "high" {
			t.Errorf("bad request options: %#v", request)
		}
		if len(request.Messages) != 3 || request.Messages[2].Role != "user" || len(request.Messages[2].Content) != 3 {
			t.Errorf("tool results were not grouped with subsequent user content: %#v", request.Messages)
		} else {
			results := request.Messages[2].Content
			if results[0].ToolUseID != "one" || !results[0].IsError || results[1].ToolUseID != "two" || results[2].Text != "continue" {
				t.Errorf("bad tool results: %#v", results)
			}
		}
		if len(request.Tools) != 1 || request.Tools[0].Name != "lookup" || request.Tools[0].InputSchema["type"] != "object" {
			t.Errorf("bad tool schema: %#v", request.Tools)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, anthropicTestResponse())
	}))
	defer server.Close()
	cfg := anthropicTestConfig(server.URL)
	cfg.ReasoningEffort = "high"
	client := NewClient(cfg)
	messages := []model.Message{
		{Role: model.RoleSystem, Content: "first"}, {Role: model.RoleSystem, Content: "second"}, {Role: model.RoleUser, Content: "hello"},
		{Role: model.RoleAssistant, Content: "checking", ToolCalls: []model.ToolCall{
			{ID: "one", Function: model.ToolFunction{Name: "lookup", Arguments: `{"id":1}`}}, {ID: "two", Function: model.ToolFunction{Name: "lookup", Arguments: `{}`}},
		}},
		{Role: model.RoleTool, ToolCallID: "one", Content: "Error: unavailable", IsError: true}, {Role: model.RoleTool, ToolCallID: "two", Content: "ok"}, {Role: model.RoleUser, Content: "continue"},
	}
	tools := []model.ToolDefinition{{Type: "function", Function: model.FuncDef{Name: "lookup", Description: "look up", Parameters: map[string]any{"type": "object"}}}}
	message, usage, err := client.Complete(context.Background(), messages, tools)
	if err != nil {
		t.Fatal(err)
	}
	if message.Content != "hello world" || len(message.ToolCalls) != 1 || message.ToolCalls[0].Function.Arguments != `{"id":9007199254740993}` {
		t.Fatalf("bad response: %#v", message)
	}
	if usage.PromptTokens != 150 || usage.CompletionTokens != 10 || usage.TotalTokens != 160 || client.Name() != "anthropic" {
		t.Fatalf("bad usage or provider: %#v %s", usage, client.Name())
	}
}

func collectAnthropicStream(input string) []StreamChunk {
	ch := make(chan StreamChunk, 32)
	go func() { defer close(ch); parseAnthropicStream(context.Background(), strings.NewReader(input), ch) }()
	var result []StreamChunk
	for chunk := range ch {
		result = append(result, chunk)
	}
	return result
}

func TestAnthropicStreamCollectsTextParallelToolsAndCumulativeUsage(t *testing.T) {
	chunks := collectAnthropicStream(strings.ReplaceAll(anthropicTestStream(), "\n", "\r\n"))
	var text string
	args := map[int]string{}
	ids := map[int]string{}
	var final StreamChunk
	for _, chunk := range chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		text += chunk.Content
		for _, call := range chunk.ToolCalls {
			args[call.Index] += call.Arguments
			if call.ID != "" {
				ids[call.Index] = call.ID
			}
		}
		if chunk.FinishReason != "" {
			final = chunk
		}
	}
	if text != "hello world" || args[1] != `{"city":"北京"}` || args[3] != "{}" || ids[1] != "lookup-call" || ids[3] != "clock-call" {
		t.Fatalf("wrong stream assembly: %q %v %v", text, args, ids)
	}
	if final.FinishReason != "tool_calls" || final.Usage == nil || final.Usage.PromptTokens != 150 || final.Usage.CompletionTokens != 10 || final.Usage.TotalTokens != 160 {
		t.Fatalf("usage must use final cumulative counts, not sum deltas: %#v", final)
	}
}

func TestAnthropicStreamRejectsBrokenAndTruncatedMessages(t *testing.T) {
	valid := anthropicTestStream()
	for name, input := range map[string]string{
		"truncated":         strings.TrimSuffix(valid, anthropicEvent("message_stop", nil)),
		"malformed":         "event: message_start\ndata: {broken}\n\n",
		"unopened-block":    anthropicEvent("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": "bad"}}),
		"invalid-tool-json": strings.Replace(valid, `京\"}`, `京\"`, 1),
		"event-mismatch":    "event: ping\ndata: {\"type\":\"message_stop\"}\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			chunks := collectAnthropicStream(input)
			if len(chunks) == 0 || chunks[len(chunks)-1].Err == nil {
				t.Fatalf("broken stream completed: %#v", chunks)
			}
			for _, chunk := range chunks {
				if chunk.FinishReason != "" {
					t.Fatal("broken stream emitted success")
				}
			}
		})
	}
	overloaded := collectAnthropicStream(anthropicEvent("error", map[string]any{"error": map[string]any{"type": "overloaded_error", "message": "busy"}}))
	var apiErr *APIError
	if len(overloaded) != 1 || !errors.As(overloaded[0].Err, &apiErr) || apiErr.Kind != ErrorModelOverloaded || !apiErr.Retryable {
		t.Fatalf("stream error classification lost: %#v", overloaded)
	}
}

func TestAnthropicEndpointNormalization(t *testing.T) {
	for base, want := range map[string]string{
		"https://example.test": "https://example.test/v1/messages", "https://example.test/v1/": "https://example.test/v1/messages",
		"https://example.test/gateway/v1": "https://example.test/gateway/v1/messages", "https://example.test/v1/messages": "https://example.test/v1/messages",
		"https://example.test/v1?route=claude": "https://example.test/v1/messages?route=claude",
	} {
		got, err := anthropicEndpoint(base)
		if err != nil || got != want {
			t.Errorf("%s: got %s, %v", base, got, err)
		}
	}
}

func TestAnthropicRetriesHTTPOverloadAndDoesNotRetryAuthentication(t *testing.T) {
	for _, status := range []int{529, http.StatusUnauthorized} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if calls.Add(1) == 1 {
					w.WriteHeader(status)
					_, _ = io.WriteString(w, `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`)
					return
				}
				_, _ = io.WriteString(w, anthropicTestResponse())
			}))
			defer server.Close()
			cfg := anthropicTestConfig(server.URL)
			cfg.MaxRetries = 1
			_, _, err := NewClient(cfg).Complete(context.Background(), []model.Message{{Role: "user", Content: "hello"}}, nil)
			if status == 529 {
				if err != nil || calls.Load() != 2 {
					t.Fatalf("overload was not retried: %d %v", calls.Load(), err)
				}
			} else {
				var apiErr *APIError
				if !errors.As(err, &apiErr) || apiErr.Kind != ErrorAuthFailed || calls.Load() != 1 {
					t.Fatalf("authentication error was retried or misclassified: %d %v", calls.Load(), err)
				}
			}
		})
	}
}

func TestAnthropicStreamingCaptureRedactsNativeAuthentication(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request anthropicRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if request.Stream {
			_, _ = io.WriteString(w, anthropicTestStream())
		} else {
			_, _ = io.WriteString(w, anthropicTestResponse())
		}
	}))
	defer server.Close()
	recorder, err := telemetry.NewRecorder(t.TempDir(), telemetry.CaptureRedacted, 8, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()
	client := NewClient(anthropicTestConfig(server.URL + "/v1"))
	client.SetRecorder(recorder)
	ctx := WithCallMetadata(context.Background(), CallMetadata{RunID: "native-run", Purpose: telemetry.PurposeMain})
	message := []model.Message{{Role: "user", Content: "hello"}}
	if _, _, err := client.Complete(ctx, message, nil); err != nil {
		t.Fatal(err)
	}
	stream, err := client.CompleteStream(ctx, message, nil)
	if err != nil {
		t.Fatal(err)
	}
	for chunk := range stream {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	trace, err := recorder.ReadTrace("native-run")
	if err != nil || len(trace.ModelCalls) != 2 {
		t.Fatalf("missing capture: %#v %v", trace, err)
	}
	for _, call := range trace.ModelCalls {
		if call.Provider != "anthropic" || call.RequestHeaders.Get("x-api-key") != telemetry.RedactionMarker || call.RequestHeaders.Get("Authorization") != "" || call.RequestHeaders.Get("anthropic-version") != anthropicVersion {
			t.Fatalf("bad native header capture: %#v", call)
		}
	}
	if trace.ModelCalls[0].RawResponse == nil || trace.ModelCalls[1].RawStream == nil {
		t.Fatal("native wire artifacts missing")
	}
}

func TestAnthropicStreamCancellationClosesConnection(t *testing.T) {
	closed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(closed)
		_, _ = io.WriteString(w, anthropicEvent("message_start", map[string]any{"message": map[string]any{"type": "message", "role": "assistant"}}))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := NewClient(anthropicTestConfig(server.URL)).CompleteStream(ctx, []model.Message{{Role: "user", Content: "hello"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	for range stream {
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled stream connection leaked")
	}
}

func TestMixedProtocolFailoverUsesNativeEndpointAndOutputBudget(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer primary-key" {
			t.Errorf("OpenAI request changed: %s %v", r.URL, r.Header)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"message":"unavailable"}}`)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request anthropicRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "test-anthropic-secret" || request.Model != "claude-test" || request.MaxTokens != 512 {
			t.Errorf("fallback lost native format or budget: %s %#v", r.URL, request)
		}
		_, _ = io.WriteString(w, anthropicTestResponse())
	}))
	defer fallback.Close()
	cfg := config.LLMConfig{BaseURL: primary.URL + "/v1", APIKey: "primary-key", Model: "openai-test", MaxOutputTokens: 512}
	fallbackCfg := cfg.FallbackConfig(config.FallbackProvider{APIFormat: config.APIFormatAnthropic, BaseURL: fallback.URL + "/v1", APIKey: "test-anthropic-secret", Model: "claude-test", ContextWindow: 100000})
	client := NewFailoverClient([]Provider{NewClient(cfg), NewClient(fallbackCfg)})
	message, _, err := client.Complete(context.Background(), []model.Message{{Role: "user", Content: "hello"}}, nil)
	if err != nil || message.Content != "hello world" || client.Name() != "anthropic (failover)" || client.MaxContextWindow() != 100000 {
		t.Fatalf("mixed protocol failover failed: %#v %v", message, err)
	}
	overridden := NewClient(fallbackCfg).WithModelOptions("claude-other", "medium")
	if overridden.Name() != "anthropic" || overridden.ModelName() != "claude-other" {
		t.Fatal("model override lost protocol")
	}
}

func TestAnthropicRejectsInvalidHistoryBeforeRequest(t *testing.T) {
	client := NewClient(anthropicTestConfig("https://unused.invalid"))
	for _, messages := range [][]model.Message{
		nil,
		{{Role: "tool", ToolCallID: "missing", Content: "orphaned"}},
		{{Role: "assistant", ToolCalls: []model.ToolCall{{ID: "unfinished", Function: model.ToolFunction{Name: "lookup", Arguments: "{}"}}}}},
		{{Role: "assistant", ToolCalls: []model.ToolCall{{ID: "bad-json", Function: model.ToolFunction{Name: "lookup", Arguments: "[1]"}}}}},
	} {
		if _, _, err := client.Complete(context.Background(), messages, nil); err == nil {
			t.Fatalf("invalid history accepted: %#v", messages)
		}
	}
	client.cfg.MaxOutputTokens = 0
	if _, _, err := client.Complete(context.Background(), []model.Message{{Role: "user", Content: "hello"}}, nil); err == nil {
		t.Fatal("missing required output budget accepted")
	}
}

func TestAnthropicSSEMultilineData(t *testing.T) {
	input := strings.Replace(anthropicTestStream(), `{"message":`, "{\ndata: \"message\":", 1)
	chunks := collectAnthropicStream(input)
	if len(chunks) == 0 || chunks[len(chunks)-1].Err != nil || chunks[len(chunks)-1].FinishReason != "tool_calls" {
		t.Fatalf("multiline SSE failed: %#v", chunks)
	}
}

func TestAnthropicDoesNotForwardAPIKeyAcrossRedirects(t *testing.T) {
	var leaked atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		leaked.Add(1)
		_, _ = io.WriteString(w, anthropicTestResponse())
	}))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/v1/messages", http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	_, _, err := NewClient(anthropicTestConfig(source.URL)).Complete(context.Background(), []model.Message{{Role: "user", Content: "hello"}}, nil)
	if err == nil || leaked.Load() != 0 {
		t.Fatalf("cross-origin redirect sent credentials: requests=%d err=%v", leaked.Load(), err)
	}
}

func TestAnthropicStreamDoesNotOverwriteInitialToolArguments(t *testing.T) {
	old := anthropicEvent("content_block_start", map[string]any{"index": 1, "content_block": map[string]any{"type": "tool_use", "id": "lookup-call", "name": "lookup", "input": map[string]any{}}})
	start := anthropicEvent("content_block_start", map[string]any{"index": 1, "content_block": map[string]any{"type": "tool_use", "id": "lookup-call", "name": "lookup", "input": map[string]any{"id": 42}}})
	mixed := strings.Replace(anthropicTestStream(), old, start, 1)
	chunks := collectAnthropicStream(mixed)
	if chunks[len(chunks)-1].Err == nil {
		t.Fatal("initial arguments were silently overwritten by streamed arguments")
	}
	whole := strings.Replace(mixed, anthropicEvent("content_block_delta", map[string]any{"index": 1, "delta": map[string]any{"type": "input_json_delta", "partial_json": `{"city":"北`}}), "", 1)
	whole = strings.Replace(whole, anthropicEvent("content_block_delta", map[string]any{"index": 1, "delta": map[string]any{"type": "input_json_delta", "partial_json": `京"}`}}), "", 1)
	arguments := ""
	for _, chunk := range collectAnthropicStream(whole) {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		for _, call := range chunk.ToolCalls {
			if call.Index == 1 {
				arguments += call.Arguments
			}
		}
	}
	if arguments != `{"id":42}` {
		t.Fatalf("initial tool arguments lost: %q", arguments)
	}
}

func TestAnthropicDoesNotAcceptToolsTruncatedByTokenLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Replace(anthropicTestResponse(), `"stop_reason":"tool_use"`, `"stop_reason":"max_tokens"`, 1))
	}))
	defer server.Close()
	if _, _, err := NewClient(anthropicTestConfig(server.URL)).Complete(context.Background(), []model.Message{{Role: "user", Content: "hello"}}, nil); err == nil {
		t.Fatal("token-limited response accepted tool calls")
	}
	chunks := collectAnthropicStream(strings.Replace(anthropicTestStream(), `"stop_reason":"tool_use"`, `"stop_reason":"max_tokens"`, 1))
	if chunks[len(chunks)-1].Err == nil {
		t.Fatal("token-limited stream accepted tool calls")
	}
}
