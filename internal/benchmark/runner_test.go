package benchmark

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRunnerExecutesIsolatedPlanAndGrader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)
		if stream, _ := request["stream"].(bool); stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"benchmark-ok\"}}]}\n\ndata: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"title"}}],"usage":{"total_tokens":1}}`))
	}))
	defer server.Close()
	runner, err := NewRunner(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{SchemaVersion: 1, Name: "smoke", BaseURL: server.URL + "/v1", Model: "mock",
		Concurrency: 2, TimeoutSeconds: 10, Cases: []Case{
			{ID: "one", Prompt: "first", Expect: Expect{Type: "exact", Value: "benchmark-ok"}},
			{ID: "two", Prompt: "second", Expect: Expect{Type: "contains", Value: "benchmark"}},
		}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	plan, err := runner.Run(ctx, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != "completed" || len(plan.Results) != 2 {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	for _, result := range plan.Results {
		if result.Status != "passed" || result.Grader == nil || !result.Grader.Passed || result.RunID == "" {
			t.Fatalf("unexpected result: %#v", result)
		}
	}
	stored, err := runner.Get(plan.ID)
	if err != nil || stored.ManifestDigest != plan.ManifestDigest {
		t.Fatalf("plan not persisted: %#v %v", stored, err)
	}
}

func TestArgvGraderRequiresTrust(t *testing.T) {
	manifest := Manifest{SchemaVersion: 1, Name: "untrusted", BaseURL: "http://localhost", Model: "mock",
		TimeoutSeconds: 1, Cases: []Case{{ID: "one", Prompt: "test", Expect: Expect{Type: "argv", Argv: []string{"/bin/true"}}}}}
	if err := manifest.Validate(); err == nil {
		t.Fatal("untrusted argv grader was accepted")
	}
}
