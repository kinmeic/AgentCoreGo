package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
)

func TestRedactedRecorderExportAndClose(t *testing.T) {
	recorder, err := NewRecorder(t.TempDir(), CaptureRedacted, 4, 1024)
	if err != nil {
		t.Fatal(err)
	}
	request := []byte(`{"api_key":"secret","messages":[{"role":"user","content":"hello"}]}`)
	response := []byte(`{"choices":[{"message":{"content":"ok"}}]}`)
	meta := ModelCallMeta{RunID: "run", RootID: "root", RequestID: "request", Purpose: PurposeMain,
		Provider: "openai", Model: "model", RequestHeaders: http.Header{"Authorization": []string{"Bearer secret"}}}
	if err := recorder.RecordModelCall(context.Background(), meta, request, response, nil, CallResult{}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordTool(context.Background(), ToolInvocation{RunID: "run", ToolCallID: "call", Name: "write",
		Input: json.RawMessage(`{"password":"secret"}`), Status: ToolSucceeded,
		Output: json.RawMessage(`{"result":"ok"}`)}); err != nil {
		t.Fatal(err)
	}
	added, deleted := 1, 2
	if err := recorder.RecordFileChange(context.Background(), FileChange{RunID: "run", Path: "a.txt", Operation: FileUpdate,
		AddedLines: &added, DeletedLines: &deleted}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	trace, err := recorder.ReadTrace("run")
	if err != nil || len(trace.ModelCalls) != 1 || len(trace.Tools) != 1 || len(trace.Changes) != 1 {
		t.Fatalf("unexpected trace: %#v %v", trace, err)
	}
	if trace.ModelCalls[0].RequestHeaders.Get("Authorization") != RedactionMarker || string(trace.Tools[0].Input) != `{"password":"[REDACTED]"}` {
		t.Fatalf("redaction failed: %#v %s", trace.ModelCalls[0].RequestHeaders, trace.Tools[0].Input)
	}
	manifest, err := recorder.ExportTrace("run", filepath.Join(t.TempDir(), "export"))
	if err != nil || !manifest.CaptureComplete {
		t.Fatalf("export failed: %#v %v", manifest, err)
	}
	if err := recorder.RecordTool(context.Background(), ToolInvocation{RunID: "run", ToolCallID: "late", Name: "late", Status: ToolSucceeded, Input: json.RawMessage(`{}`)}); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected closed error, got %v", err)
	}
}

func TestTrainingRejectsTruncatedCapture(t *testing.T) {
	recorder, err := NewRecorder(t.TempDir(), CaptureTraining, 1, 4)
	if err != nil {
		t.Fatal(err)
	}
	meta := ModelCallMeta{RunID: "run", RequestID: "request", Purpose: PurposeMain}
	if err := recorder.RecordModelCall(context.Background(), meta, []byte(`{"long":"body"}`), nil, nil, CallResult{}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.ExportTrace("run", filepath.Join(t.TempDir(), "export")); !errors.Is(err, ErrIncompleteCapture) {
		t.Fatalf("expected incomplete capture error, got %v", err)
	}
}
