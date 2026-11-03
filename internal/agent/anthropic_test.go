package agent

import (
	"agent-core/internal/config"
	"agent-core/internal/llm"
	"agent-core/internal/model"
	"agent-core/internal/tool"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type nativeTestTool struct {
	calls atomic.Int32
	fail  bool
}

func (*nativeTestTool) Name() string        { return "native_test" }
func (*nativeTestTool) Description() string { return "native protocol round trip" }
func (*nativeTestTool) Schema() tool.ToolSchema {
	return tool.ToolSchema{Type: "object", Required: []string{"value"}, Properties: map[string]any{"value": map[string]any{"type": "string"}}}
}
func (*nativeTestTool) Capabilities() tool.Capabilities { return tool.Capabilities{ReadOnly: true} }
func (p *nativeTestTool) Execute(_ context.Context, args map[string]any) (string, error) {
	p.calls.Add(1)
	if p.fail {
		return "", fmt.Errorf("native failure")
	}
	return "result: " + args["value"].(string), nil
}

// A real HTTP mock speaks only Messages format; the orchestrator must translate
// both directions and persist the same tool history used by later turns.
func TestAnthropicAgentToolRoundTrip(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/error=%t", stream, fail), func(t *testing.T) {
				var resultRoundTrips atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "native-key" {
						t.Errorf("wrong native request: %s", r.URL)
					}
					var request struct {
						Stream    bool              `json:"stream"`
						MaxTokens int               `json:"max_tokens"`
						Tools     []json.RawMessage `json:"tools"`
						Messages  []struct {
							Role    string           `json:"role"`
							Content []map[string]any `json:"content"`
						} `json:"messages"`
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					if request.MaxTokens != 128 {
						t.Errorf("required output budget lost: %d", request.MaxTokens)
					}
					results := 0
					for _, message := range request.Messages {
						if message.Role == "tool" || message.Role == "system" {
							t.Errorf("OpenAI role leaked into native messages: %s", message.Role)
						}
						for _, block := range message.Content {
							if block["type"] != "tool_result" {
								continue
							}
							results++
							isError, _ := block["is_error"].(bool)
							if message.Role != "user" || isError != fail {
								t.Errorf("tool result role/error lost: %#v", block)
							}
							if fail && block["content"] != "Error: native failure" {
								t.Errorf("tool error lost: %#v", block)
							}
						}
					}
					blocks := []map[string]any{{"type": "text", "text": "native-done"}}
					stop := "end_turn"
					if len(request.Tools) != 0 && results == 0 {
						blocks = []map[string]any{
							{"type": "text", "text": "checking"},
							{"type": "tool_use", "id": "native-one", "name": "native_test", "input": map[string]any{"value": "one"}},
							{"type": "tool_use", "id": "native-two", "name": "native_test", "input": map[string]any{"value": "two"}},
						}
						stop = "tool_use"
					} else if results != 0 {
						if results != 2 {
							t.Errorf("parallel tool results were not grouped: %d", results)
						}
						resultRoundTrips.Add(1)
					}
					if !request.Stream {
						_ = json.NewEncoder(w).Encode(map[string]any{"type": "message", "role": "assistant", "content": blocks, "stop_reason": stop, "usage": map[string]int{"input_tokens": 4, "output_tokens": 5}})
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					emit := func(kind string, data map[string]any) {
						data["type"] = kind
						encoded, _ := json.Marshal(data)
						_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, encoded)
					}
					emit("message_start", map[string]any{"message": map[string]any{"type": "message", "role": "assistant", "usage": map[string]int{"input_tokens": 4, "output_tokens": 0}}})
					for index, block := range blocks {
						if block["type"] == "text" {
							emit("content_block_start", map[string]any{"index": index, "content_block": map[string]any{"type": "text", "text": ""}})
							emit("content_block_delta", map[string]any{"index": index, "delta": map[string]any{"type": "text_delta", "text": block["text"]}})
						} else {
							input := block["input"]
							block["input"] = map[string]any{}
							emit("content_block_start", map[string]any{"index": index, "content_block": block})
							encoded, _ := json.Marshal(input)
							emit("content_block_delta", map[string]any{"index": index, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(encoded)}})
						}
						emit("content_block_stop", map[string]any{"index": index})
					}
					emit("message_delta", map[string]any{"delta": map[string]any{"stop_reason": stop}, "usage": map[string]int{"output_tokens": 5}})
					emit("message_stop", map[string]any{})
				}))
				t.Cleanup(server.Close)
				provider := llm.NewClient(config.LLMConfig{APIFormat: "anthropic", BaseURL: server.URL + "/v1", APIKey: "native-key", Model: "claude-test", MaxOutputTokens: 128, ContextWindow: 100000})
				orchestrator, db, registry := newAgentHarness(t, provider)
				p := &nativeTestTool{fail: fail}
				if err := registry.Register(p, tool.SourceBuiltin, "builtin"); err != nil {
					t.Fatal(err)
				}
				var conversation string
				if stream {
					events := make(chan model.StreamEvent, 128)
					orchestrator.ChatStream(context.Background(), model.ChatRequest{Message: "test native tools"}, events)
					done := false
					for event := range events {
						if event.Type == "error" {
							t.Fatal(event.Message)
						}
						if event.Type == "done" {
							done = true
							conversation = event.ConversationID
							if event.Reply != "native-done" || event.Usage.TotalTokens != 18 {
								t.Fatalf("bad completion: %#v", event)
							}
						}
					}
					if !done {
						t.Fatal("native stream did not finish")
					}
				} else {
					response, err := orchestrator.ChatSync(context.Background(), model.ChatRequest{Message: "test native tools"})
					if err != nil {
						t.Fatal(err)
					}
					conversation = response.ConversationID
					if response.Reply != "native-done" || response.Usage.TotalTokens != 18 {
						t.Fatalf("bad completion: %#v", response)
					}
				}
				if p.calls.Load() != 2 || resultRoundTrips.Load() != 1 {
					t.Fatalf("tools did not round trip: calls=%d results=%d", p.calls.Load(), resultRoundTrips.Load())
				}
				logs, err := db.GetLogs(conversation, 100)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range logs {
					if entry.Role == model.RoleTool && conversationLogMessage(entry).IsError != fail {
						t.Fatalf("persisted tool error flag lost: %#v", entry)
					}
				}
			})
		}
	}
}

func TestConversationToolErrorsAreRestoredWithoutTextHeuristics(t *testing.T) {
	for _, entry := range []*model.ConversationLog{
		{Role: model.RoleTool, Status: "error", Content: "custom failure"},
		{Role: model.RoleTool, Status: "completed", Content: "Error: legitimate file content"},
	} {
		message := conversationLogMessage(entry)
		if message.IsError != (entry.Status == "error") || !strings.Contains(message.Content, entry.Content) {
			t.Fatalf("bad restored result: %#v", message)
		}
	}
}
