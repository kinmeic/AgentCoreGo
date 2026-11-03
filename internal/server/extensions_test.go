package server

import (
	"agent-core/internal/workflow"
	"agent-core/internal/workspace"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestSlashHelpReturnsLocalCommandResult(t *testing.T) {
	server, _ := newServerHarness(t)
	response := request(t, server, http.MethodPost, "/v1/chat/stream", `{"message":"/help"}`, "Bearer secret")
	if response.Code != 200 || !strings.Contains(response.Body.String(), "command.result") || !strings.Contains(response.Body.String(), "workflow") {
		t.Fatalf("unexpected /help response: %d %s", response.Code, response.Body.String())
	}
}

func TestWorkspaceSnapshotHTTP(t *testing.T) {
	server, _ := newServerHarness(t)
	response := request(t, server, http.MethodPut, "/v1/workspace/files/content", `{"path":"a.txt","content":"first"}`, "Bearer secret")
	if response.Code != 200 {
		t.Fatalf("create file: %d %s", response.Code, response.Body.String())
	}
	var page workspace.FilePage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	response = request(t, server, http.MethodPost, "/v1/snapshots", `{}`, "Bearer secret")
	if response.Code != 201 {
		t.Fatalf("create snapshot: %d %s", response.Code, response.Body.String())
	}
	var snapshot workspace.SnapshotManifest
	if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	response = request(t, server, http.MethodPut, "/v1/workspace/files/content", `{"path":"a.txt","content":"second","expected_revision":"`+page.Revision+`"}`, "Bearer secret")
	if response.Code != 200 {
		t.Fatalf("update file: %d %s", response.Code, response.Body.String())
	}
	response = request(t, server, http.MethodPost, "/v1/snapshots/"+snapshot.ID+"/restore", `{"allow_changes":true}`, "Bearer secret")
	if response.Code != 200 {
		t.Fatalf("restore snapshot: %d %s", response.Code, response.Body.String())
	}
	response = request(t, server, http.MethodGet, "/v1/workspace/files/content?path=a.txt", "", "Bearer secret")
	if !strings.Contains(response.Body.String(), "first") {
		t.Fatalf("restored content missing: %s", response.Body.String())
	}
}

func TestWorkspaceRejectsInvalidWritesBeforeMutation(t *testing.T) {
	server, _ := newServerHarness(t)
	for _, body := range []string{`{"path":"invalid.txt","content":"\u0000"}`, `{"path":"../outside.txt","content":"blocked"}`} {
		response := request(t, server, http.MethodPut, "/v1/workspace/files/content", body, "Bearer secret")
		if response.Code != http.StatusBadRequest && response.Code != http.StatusForbidden {
			t.Fatalf("invalid write returned %d: %s", response.Code, response.Body.String())
		}
	}
	response := request(t, server, http.MethodGet, "/v1/workspace/files/content?path=invalid.txt", "", "Bearer secret")
	if response.Code != http.StatusNotFound {
		t.Fatalf("rejected write created a file: %d %s", response.Code, response.Body.String())
	}
}

func TestWorkflowHTTP(t *testing.T) {
	server, _ := newServerHarness(t)
	body := `{"definition":{"schema_version":1,"name":"http-test","nodes":[{"id":"answer","type":"agent","prompt":"answer"}]}}`
	response := request(t, server, http.MethodPost, "/v1/workflows", body, "Bearer secret")
	if response.Code != 201 {
		t.Fatalf("start workflow: %d %s", response.Code, response.Body.String())
	}
	var view workflow.RunView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Record.Status != "completed" || len(view.Nodes) != 1 {
		t.Fatalf("unexpected workflow: %#v", view)
	}
}
