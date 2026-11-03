package vector

import (
	"agent-core/internal/llm"
	"agent-core/internal/telemetry"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmbeddingCapture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1,0.2]}],"usage":{"total_tokens":1}}`))
	}))
	defer server.Close()
	recorder, err := telemetry.NewRecorder(t.TempDir(), telemetry.CaptureRedacted, 4, 1024)
	if err != nil {
		t.Fatal(err)
	}
	client := NewEmbeddingClient(server.URL+"/v1", "secret", "embedding", 2)
	client.SetRecorder(recorder)
	ctx := llm.WithCallMetadata(context.Background(), llm.CallMetadata{RunID: "run", RootID: "root", TraceID: "trace"})
	if _, err := client.EmbedOne(ctx, "hello"); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	trace, err := recorder.ReadTrace("run")
	if err != nil || len(trace.ModelCalls) != 1 || trace.ModelCalls[0].Purpose != telemetry.PurposeEmbedding {
		t.Fatalf("unexpected embedding trace: %#v %v", trace.ModelCalls, err)
	}
	if trace.ModelCalls[0].RawRequest == nil || trace.ModelCalls[0].RawResponse == nil {
		t.Fatal("embedding wire artifacts missing")
	}
}
