package llm

import (
	"agent-core/internal/config"
	"agent-core/internal/model"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIStreamExposesReasoningSeparately(t *testing.T) {
	input := "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"first\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\" second\",\"content\":\"answer\"}}]}\n\n" +
		"data: [DONE]\n\n"
	chunks := collectStream(t, input)
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks: %#v", len(chunks), chunks)
	}
	if chunks[0].Reasoning != "first" || chunks[0].Content != "" ||
		chunks[1].Reasoning != " second" || chunks[1].Content != "answer" {
		t.Fatalf("reasoning and content were mixed: %#v", chunks)
	}
}

func TestAnthropicThinkingRequestModes(t *testing.T) {
	cfg := anthropicTestConfig("https://example.test")
	cfg.MaxOutputTokens = 32000
	cfg.ThinkingType = config.ThinkingTypeEnabled
	cfg.ThinkingBudgetTokens = 16000
	client := NewClient(cfg)
	messages := []model.Message{{Role: model.RoleUser, Content: "hello"}}
	request, err := client.newAnthropicRequest(messages, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if request.Thinking == nil || request.Thinking.Type != config.ThinkingTypeEnabled || request.Thinking.BudgetTokens != 16000 {
		t.Fatalf("manual thinking request mismatch: %#v", request.Thinking)
	}

	cfg.ThinkingType = config.ThinkingTypeAdaptive
	cfg.ReasoningEffort = "high"
	client = NewClient(cfg)
	request, err = client.newAnthropicRequest(messages, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if request.Thinking == nil || request.Thinking.Type != config.ThinkingTypeAdaptive ||
		request.Thinking.BudgetTokens != 0 || request.OutputConfig["effort"] != "high" {
		t.Fatalf("adaptive thinking request mismatch: %#v", request)
	}

	cfg.ThinkingType = config.ThinkingTypeEnabled
	cfg.ThinkingBudgetTokens = 512
	client = NewClient(cfg)
	if _, err := client.newAnthropicRequest(messages, nil, true); err == nil {
		t.Fatal("accepted an undersized manual thinking budget")
	}
}

func TestAnthropicProviderStatePreservesSignedBlocks(t *testing.T) {
	blocks := []anthropicBlock{
		{Type: "thinking", Thinking: "private reasoning", Signature: "signature"},
		{Type: "redacted_thinking", Data: "redacted-data"},
		{Type: "text", Text: "visible"},
		{Type: "tool_use", ID: "call", Name: "lookup", Input: json.RawMessage(`{"id":1}`)},
	}
	state, err := anthropicStateFromBlocks(blocks)
	if err != nil {
		t.Fatal(err)
	}
	cfg := anthropicTestConfig("https://example.test")
	cfg.MaxOutputTokens = 32000
	cfg.ThinkingType = config.ThinkingTypeEnabled
	cfg.ThinkingBudgetTokens = 16000
	client := NewClient(cfg)
	messages := []model.Message{
		{Role: model.RoleUser, Content: "question"},
		{Role: model.RoleAssistant, ProviderState: state},
		{Role: model.RoleTool, ToolCallID: "call", Content: "result"},
		{Role: model.RoleUser, Content: "continue"},
	}
	request, err := client.newAnthropicRequest(messages, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Messages) != 4 {
		t.Fatalf("unexpected message grouping: %#v", request.Messages)
	}
	got := request.Messages[1].Content
	if len(got) != 4 || got[0].Signature != "signature" || got[1].Data != "redacted-data" ||
		got[2].Text != "visible" || got[3].ID != "call" {
		t.Fatalf("signed blocks were not preserved: %#v", got)
	}
}

func TestAnthropicDisabledThinkingStripsThinkingBlocks(t *testing.T) {
	state, err := anthropicStateFromBlocks([]anthropicBlock{
		{Type: "thinking", Thinking: "reasoning", Signature: "signature"},
		{Type: "redacted_thinking", Data: "redacted"},
		{Type: "text", Text: "visible"},
	})
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(anthropicTestConfig("https://example.test"))
	request, err := client.newAnthropicRequest([]model.Message{
		{Role: model.RoleUser, Content: "question"},
		{Role: model.RoleAssistant, ProviderState: state},
	}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	blocks := request.Messages[1].Content
	if len(blocks) != 1 || blocks[0].Type != "text" || blocks[0].Text != "visible" {
		t.Fatalf("disabled thinking retained provider-only blocks: %#v", blocks)
	}
}

func TestAnthropicStreamReasoningAndProviderState(t *testing.T) {
	stream := anthropicEvent("message_start", map[string]any{"message": map[string]any{
		"type": "message", "role": "assistant", "usage": map[string]any{"input_tokens": 1},
	}}) +
		anthropicEvent("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "thinking", "thinking": "first"}}) +
		anthropicEvent("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "thinking_delta", "thinking": " second"}}) +
		anthropicEvent("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "signature_delta", "signature": "signature"}}) +
		anthropicEvent("content_block_stop", map[string]any{"index": 0}) +
		anthropicEvent("content_block_start", map[string]any{"index": 1, "content_block": map[string]any{"type": "redacted_thinking", "data": "redacted-data"}}) +
		anthropicEvent("content_block_stop", map[string]any{"index": 1}) +
		anthropicEvent("content_block_start", map[string]any{"index": 2, "content_block": map[string]any{"type": "text", "text": "answer"}}) +
		anthropicEvent("content_block_delta", map[string]any{"index": 2, "delta": map[string]any{"type": "text_delta", "text": "!"}}) +
		anthropicEvent("content_block_stop", map[string]any{"index": 2}) +
		anthropicEvent("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "end_turn"}}) +
		anthropicEvent("message_stop", nil)
	chunks := collectAnthropicStream(stream)
	var reasoning, content string
	var final StreamChunk
	for _, chunk := range chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		reasoning += chunk.Reasoning
		content += chunk.Content
		if chunk.FinishReason != "" {
			final = chunk
		}
	}
	if reasoning != "first second" || content != "answer!" || len(final.ProviderState) == 0 {
		t.Fatalf("unexpected reasoning/content/final state: %q %q %#v", reasoning, content, final)
	}
	blocks, err := anthropicBlocksFromState(final.ProviderState)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 3 || blocks[0].Thinking != "first second" || blocks[0].Signature != "signature" ||
		blocks[1].Data != "redacted-data" || blocks[2].Text != "answer!" {
		t.Fatalf("stream state mismatch: %#v", blocks)
	}
}

func TestAnthropicNonStreamThinkingStateAndInterleavedHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("anthropic-beta") != anthropicInterleavedThinkingBeta {
			t.Errorf("missing interleaved thinking beta header: %v", r.Header)
		}
		var request anthropicRequest
		data, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(data, &request); err != nil {
			t.Error(err)
		}
		if request.Thinking == nil || request.Thinking.Type != config.ThinkingTypeEnabled {
			t.Errorf("thinking was not enabled: %#v", request.Thinking)
		}
		_, _ = io.WriteString(w, `{"type":"message","role":"assistant","content":[{"type":"thinking","thinking":"plan","signature":"signature"},{"type":"tool_use","id":"call","name":"lookup","input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer server.Close()
	cfg := anthropicTestConfig(server.URL)
	cfg.MaxOutputTokens = 32000
	cfg.ThinkingType = config.ThinkingTypeEnabled
	cfg.ThinkingBudgetTokens = 16000
	client := NewClient(cfg)
	message, _, err := client.Complete(context.Background(), []model.Message{{Role: model.RoleUser, Content: "hello"}},
		[]model.ToolDefinition{{Type: "function", Function: model.FuncDef{Name: "lookup", Parameters: map[string]any{"type": "object"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(message.ToolCalls) != 1 || len(message.ProviderState) == 0 {
		t.Fatalf("missing tool or provider state: %#v", message)
	}
	blocks, err := anthropicBlocksFromState(message.ProviderState)
	if err != nil || len(blocks) != 2 || blocks[0].Signature != "signature" {
		t.Fatalf("non-stream state mismatch: %#v, %v", blocks, err)
	}
}

func TestOpenAIWireMessagesIgnoreAnthropicProviderState(t *testing.T) {
	messages := []model.Message{{Role: model.RoleAssistant, Content: "answer", ProviderState: json.RawMessage(`{"content":[{"type":"thinking","signature":"secret"}]}`)}}
	wire := toAPIMessages(messages)
	if len(wire) != 1 || wire[0].Content != "answer" {
		t.Fatalf("OpenAI wire message mismatch: %#v", wire)
	}
	encoded, err := json.Marshal(wire[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "provider") {
		t.Fatalf("provider state leaked into OpenAI request: %s", encoded)
	}
}
