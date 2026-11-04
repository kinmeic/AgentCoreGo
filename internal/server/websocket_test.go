package server

import (
	"agent-core/internal/llm"
	"agent-core/internal/model"
	"agent-core/internal/tool"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func dialSocket(t *testing.T, endpoint string, header http.Header) (*websocket.Conn, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(endpoint, "http")+"/ws", &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn, ctx
}
func callSocket(t *testing.T, ctx context.Context, conn *websocket.Conn, id, method string, params any) wsReply {
	t.Helper()
	raw, _ := json.Marshal(params)
	if err := wsjson.Write(ctx, conn, wsRequest{ID: id, Method: method, Params: raw}); err != nil {
		t.Fatal(err)
	}
	var reply wsReply
	if err := wsjson.Read(ctx, conn, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.ID != id {
		t.Fatalf("wrong response: %+v", reply)
	}
	return reply
}
func TestWebSocketAuthAdmissionReconnectAndHTTPReplay(t *testing.T) {
	service, _ := newServerHarness(t)
	httpServer := httptest.NewServer(service.Handler())
	defer httpServer.Close()
	conn, ctx := dialSocket(t, httpServer.URL, nil)
	reply := callSocket(t, ctx, conn, "auth", "auth", map[string]string{"token": "secret"})
	if reply.Error != nil {
		t.Fatalf("auth failed: %+v", reply)
	}
	reply = callSocket(t, ctx, conn, "send", "session.send", map[string]string{"command_id": "command-1", "message": "hello"})
	if reply.Error != nil {
		t.Fatalf("send: %+v", reply)
	}
	encoded, _ := json.Marshal(reply.Result)
	var admission acceptedRun
	json.Unmarshal(encoded, &admission)
	if admission.RunID == "" || admission.ConversationID == "" {
		t.Fatalf("missing IDs: %s", encoded)
	}
	conn.CloseNow()
	// Retrying on a fresh connection must never re-enter the model/tool execution.
	second, ctx := dialSocket(t, httpServer.URL, http.Header{"Authorization": {"Bearer secret"}})
	retry := callSocket(t, ctx, second, "retry", "session.send", map[string]string{"command_id": "command-1", "message": "hello"})
	encoded, _ = json.Marshal(retry.Result)
	var duplicate acceptedRun
	json.Unmarshal(encoded, &duplicate)
	if admission != duplicate {
		t.Fatalf("retry created another run: %+v vs %+v", admission, duplicate)
	}
	conflict := callSocket(t, ctx, second, "conflict", "session.send", map[string]string{"command_id": "command-1", "message": "changed"})
	if conflict.Error == nil {
		t.Fatal("changed request reused command ID")
	}
	subscribe := callSocket(t, ctx, second, "subscribe", "run.subscribe", map[string]any{"subscription_id": "sub", "run_id": admission.RunID, "after": 0})
	if subscribe.Error != nil {
		t.Fatalf("subscribe: %+v", subscribe)
	}
	var received []model.StreamEvent
	for {
		var frame wsReply
		if err := wsjson.Read(ctx, second, &frame); err != nil {
			t.Fatal(err)
		}
		if frame.SubscriptionID != "sub" || frame.Event == nil {
			t.Fatalf("bad event: %+v", frame)
		}
		received = append(received, *frame.Event)
		if frame.Event.Type == "done" {
			break
		}
	}
	if len(received) < 2 || received[0].EventID == "" {
		t.Fatal("missing durable event envelope")
	}
	httpReplay := request(t, service, "GET", "/v1/runs/"+admission.RunID+"/events?after=0", "", "Bearer secret")
	var stored []model.RunEvent
	if err := json.Unmarshal(httpReplay.Body.Bytes(), &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) != len(received) {
		t.Fatalf("transport events differ: %d/%d", len(stored), len(received))
	}
	for i := range stored {
		if stored[i].EventID != received[i].EventID || stored[i].Sequence != received[i].Sequence {
			t.Fatal("WebSocket and HTTP used different event history")
		}
	}
	logs, err := service.store.GetLogs(admission.ConversationID, 100)
	if err != nil {
		t.Fatal(err)
	}
	users := 0
	for _, entry := range logs {
		if entry.Role == model.RoleUser {
			users++
		}
	}
	if users != 1 {
		t.Fatalf("command executed %d times", users)
	}
	run, _ := service.store.GetRun(admission.RunID)
	if run.Spec.Model == "" || run.Spec.Kind != model.RunKindRoot {
		t.Fatalf("admission lost execution manifest: %+v", run.Spec)
	}
	// Cursor replay returns only later events, with identical IDs.
	third, _ := dialSocket(t, httpServer.URL, http.Header{"Authorization": {"Bearer secret"}})
	callSocket(t, ctx, third, "resume", "run.subscribe", map[string]any{"subscription_id": "resume", "run_id": admission.RunID, "after": received[0].Sequence})
	var frame wsReply
	if err := wsjson.Read(ctx, third, &frame); err != nil {
		t.Fatal(err)
	}
	if frame.Event == nil || frame.Event.EventID != received[1].EventID {
		t.Fatalf("cursor replay: %+v", frame)
	}
}

func TestWebSocketRejectsUnauthenticatedCommandsAndForeignOrigins(t *testing.T) {
	service, _ := newServerHarness(t)
	httpServer := httptest.NewServer(service.Handler())
	defer httpServer.Close()
	conn, ctx := dialSocket(t, httpServer.URL, nil)
	raw, _ := json.Marshal(map[string]string{"command_id": "unauthorized", "message": "execute"})
	if err := wsjson.Write(ctx, conn, wsRequest{ID: "first", Method: "session.send", Params: raw}); err != nil {
		t.Fatal(err)
	}
	var reply wsReply
	if err := wsjson.Read(ctx, conn, &reply); err == nil {
		t.Fatal("unauthenticated command accepted")
	}
	if found, _ := service.store.AcceptedCommand("unauthorized"); found != nil {
		t.Fatal("unauthenticated command persisted")
	}
	_, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpServer.URL, "http")+"/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer secret"}, "Origin": {"https://evil.example"}}})
	if err == nil || response == nil || response.StatusCode != 403 {
		t.Fatalf("foreign origin: %v %+v", err, response)
	}
	_, response, err = websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpServer.URL, "http")+"/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer wrong"}}})
	if err == nil || response == nil || response.StatusCode != 401 {
		t.Fatalf("wrong bearer: %v %+v", err, response)
	}
}

func TestWebSocketFeatureGatesAndCursorValidation(t *testing.T) {
	service, _ := newServerHarness(t)
	service.cfg.Features.MCP = false
	service.cfg.Features.Skill = false
	service.cfg.Memory.Use = false
	for _, path := range []string{"/v1/mcp", "/v1/skills", "/v1/memories"} {
		if response := request(t, service, "GET", path, "", "Bearer secret"); response.Code != 503 {
			t.Fatalf("disabled %s: %d", path, response.Code)
		}
	}
	if response := request(t, service, "GET", "/v1/runs/missing/events/stream", "", "Bearer secret"); response.Code != 404 {
		t.Fatal("missing run started an endless SSE subscription")
	}
	httpServer := httptest.NewServer(service.Handler())
	defer httpServer.Close()
	conn, ctx := dialSocket(t, httpServer.URL, http.Header{"Authorization": {"Bearer secret"}})
	reply := callSocket(t, ctx, conn, "disabled", "mcp.list", map[string]any{})
	if reply.Error == nil || reply.Error.Code != 503 {
		t.Fatalf("WS bypassed feature gate: %+v", reply)
	}
	reply = callSocket(t, ctx, conn, "unknown", "run.subscribe", map[string]any{"run_id": "missing", "subscription_id": "missing"})
	if reply.Error == nil || reply.Error.Code != 404 {
		t.Fatal("missing run subscription accepted")
	}
}

type socketScriptLLM struct {
	serverTestLLM
	script func(context.Context) (<-chan llm.StreamChunk, error)
}

func (p socketScriptLLM) CompleteStream(ctx context.Context, _ []model.Message, _ []model.ToolDefinition) (<-chan llm.StreamChunk, error) {
	return p.script(ctx)
}

type socketApprovalTool struct{ calls atomic.Int32 }

func (p *socketApprovalTool) Name() string        { return "protected_tool" }
func (p *socketApprovalTool) Description() string { return "requires approval" }
func (p *socketApprovalTool) Schema() tool.ToolSchema {
	return tool.ToolSchema{Type: "object", Properties: map[string]any{}}
}
func (p *socketApprovalTool) Execute(context.Context, map[string]any) (string, error) {
	p.calls.Add(1)
	return "approved", nil
}

func TestWebSocketDisconnectKeepsRunAndExplicitStopCancels(t *testing.T) {
	started := make(chan struct{})
	provider := socketScriptLLM{script: func(ctx context.Context) (<-chan llm.StreamChunk, error) {
		chunks := make(chan llm.StreamChunk, 1)
		close(started)
		go func() { defer close(chunks); <-ctx.Done(); chunks <- llm.StreamChunk{Err: ctx.Err()} }()
		return chunks, nil
	}}
	service, _ := newServerHarness(t, provider)
	httpServer := httptest.NewServer(service.Handler())
	defer httpServer.Close()
	conn, ctx := dialSocket(t, httpServer.URL, http.Header{"Authorization": {"Bearer secret"}})
	reply := callSocket(t, ctx, conn, "send", "session.send", map[string]string{"command_id": "blocking", "message": "keep working"})
	if reply.Error != nil {
		t.Fatalf("accept: %+v", reply)
	}
	raw, _ := json.Marshal(reply.Result)
	var admission acceptedRun
	json.Unmarshal(raw, &admission)
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("run never started")
	}
	conn.CloseNow()
	run, _ := service.store.GetRun(admission.RunID)
	if run.FinishedAt != nil {
		t.Fatal("socket disconnect cancelled accepted work")
	}
	next, _ := dialSocket(t, httpServer.URL, http.Header{"Authorization": {"Bearer secret"}})
	reply = callSocket(t, ctx, next, "stop", "run.stop", map[string]string{"id": admission.RunID})
	if reply.Error != nil {
		t.Fatalf("stop: %+v", reply)
	}
	for {
		run, _ = service.store.GetRun(admission.RunID)
		if run.FinishedAt != nil {
			break
		}
		select {
		case <-time.After(5 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("stop never terminated")
		}
	}
	if run.Status != model.RunStatusCancelled {
		t.Fatalf("stop final status: %s", run.Status)
	}
}

func TestWebSocketApprovalSharesHTTPState(t *testing.T) {
	var modelCalls atomic.Int32
	provider := socketScriptLLM{script: func(ctx context.Context) (<-chan llm.StreamChunk, error) {
		chunks := make(chan llm.StreamChunk, 1)
		if modelCalls.Add(1) == 1 {
			chunks <- llm.StreamChunk{ToolCalls: []llm.ToolCallDelta{{ID: "approve-call", Name: "protected_tool", Type: "function", Arguments: "{}"}}}
		} else {
			chunks <- llm.StreamChunk{Content: "approved done"}
		}
		close(chunks)
		return chunks, nil
	}}
	service, orchestrator := newServerHarness(t, provider)
	permissions := tool.NewPermissionManager("build")
	orchestrator.SetPermissionManager(permissions)
	protected := &socketApprovalTool{}
	if err := service.toolRegistry.Register(protected, tool.SourceHostCallback, "test"); err != nil {
		t.Fatal(err)
	}
	service.toolRegistry.SetPolicyCheck(func(ctx context.Context, name string, args map[string]any) error {
		registered, _ := service.toolRegistry.Get(name)
		return permissions.Authorize(ctx, registered, args)
	})
	httpServer := httptest.NewServer(service.Handler())
	defer httpServer.Close()
	conn, ctx := dialSocket(t, httpServer.URL, http.Header{"Authorization": {"Bearer secret"}})
	reply := callSocket(t, ctx, conn, "send", "session.send", map[string]string{"command_id": "approval", "message": "use protected tool"})
	if reply.Error != nil {
		t.Fatalf("send: %+v", reply)
	}
	raw, _ := json.Marshal(reply.Result)
	var admission acceptedRun
	json.Unmarshal(raw, &admission)
	callSocket(t, ctx, conn, "subscribe", "run.subscribe", map[string]any{"run_id": admission.RunID, "subscription_id": "approval"})
	var requestID string
	for {
		var frame wsReply
		if err := wsjson.Read(ctx, conn, &frame); err != nil {
			t.Fatal(err)
		}
		if frame.Event != nil && frame.Event.Type == "approval.required" {
			requestID = frame.Event.RequestID
			break
		}
	}
	pending := request(t, service, "GET", "/v1/approvals", "", "Bearer secret")
	if !strings.Contains(pending.Body.String(), requestID) {
		t.Fatal("HTTP could not see WebSocket approval")
	}
	// Use a second socket to reply while the first remains subscribed.
	responder, _ := dialSocket(t, httpServer.URL, http.Header{"Authorization": {"Bearer secret"}})
	reply = callSocket(t, ctx, responder, "approve", "permission.respond", map[string]any{"id": requestID, "body": map[string]string{"decision": "allow", "scope": "once"}})
	if reply.Error != nil {
		t.Fatalf("approval: %+v", reply)
	}
	for {
		var frame wsReply
		if err := wsjson.Read(ctx, conn, &frame); err != nil {
			t.Fatal(err)
		}
		if frame.Event != nil && frame.Event.Type == "done" {
			break
		}
	}
	if protected.calls.Load() != 1 {
		t.Fatalf("protected tool calls: %d", protected.calls.Load())
	}
}

func TestWebSocketFrameLimit(t *testing.T) {
	service, _ := newServerHarness(t)
	httpServer := httptest.NewServer(service.Handler())
	defer httpServer.Close()
	conn, ctx := dialSocket(t, httpServer.URL, http.Header{"Authorization": {"Bearer secret"}})
	_ = wsjson.Write(ctx, conn, map[string]any{"id": "too-big", "method": "session.send", "params": map[string]string{"command_id": "too-big", "message": strings.Repeat("x", 4096)}})
	var reply wsReply
	if err := wsjson.Read(ctx, conn, &reply); err == nil {
		t.Fatal("oversized frame accepted")
	}
	if admission, _ := service.store.AcceptedCommand("too-big"); admission != nil {
		t.Fatal("oversized command persisted")
	}
}
