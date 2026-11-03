package llm

import (
	"agent-core/internal/config"
	"agent-core/internal/telemetry"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientCapturesSyncAndStreamingWire(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)
		if stream, _ := request["stream"].(bool); stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"total_tokens":1}}`))
	}))
	defer server.Close()
	recorder, err := telemetry.NewRecorder(t.TempDir(), telemetry.CaptureRedacted, 8, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(config.LLMConfig{BaseURL: server.URL + "/v1", Model: "mock", MaxRetries: 0})
	client.SetRecorder(recorder)
	ctx := WithCallMetadata(context.Background(), CallMetadata{RunID: "run", RootID: "root", TraceID: "trace", Purpose: telemetry.PurposeMain})
	if _, _, err := client.Complete(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	chunks, err := client.CompleteStream(ctx, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for range chunks {
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	trace, err := recorder.ReadTrace("run")
	if err != nil || len(trace.ModelCalls) != 2 {
		t.Fatalf("expected two wire records: %#v %v", trace.ModelCalls, err)
	}
	if trace.ModelCalls[0].RawRequest == nil || trace.ModelCalls[0].RawResponse == nil || trace.ModelCalls[1].RawStream == nil {
		t.Fatalf("missing wire artifacts: %#v", trace.ModelCalls)
	}
	if trace.ModelCalls[0].RequestHeaders.Get("Authorization") != telemetry.RedactionMarker {
		t.Fatal("authorization header was not redacted")
	}
}
