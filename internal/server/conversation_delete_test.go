package server

import (
	"agent-core/internal/model"
	"net/http"
	"strings"
	"testing"
	"time"
)

func createServerConversation(t *testing.T, server *Server, id string) {
	t.Helper()
	now := time.Now()
	if err := server.store.CreateConversation(&model.Conversation{ID: id, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := server.store.AddLog(&model.ConversationLog{ConversationID: id, Role: model.RoleUser,
		Content: "message", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
}

func TestConversationDeletePreservesArchiveAndSupportsPermanentDeletion(t *testing.T) {
	server, _ := newServerHarness(t)
	createServerConversation(t, server, "conversation")

	response := request(t, server, http.MethodDelete, "/v1/conversations/conversation", "", "Bearer secret")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"archived"`) {
		t.Fatalf("archive response = %d %s", response.Code, response.Body.String())
	}
	conversation, err := server.store.GetConversation("conversation")
	if err != nil || conversation == nil || conversation.ArchivedAt == nil {
		t.Fatalf("conversation was not archived: %#v, %v", conversation, err)
	}
	logs, err := server.store.GetLogs("conversation", 10)
	if err != nil || len(logs) != 1 {
		t.Fatalf("archive removed messages: %#v, %v", logs, err)
	}

	response = request(t, server, http.MethodDelete, "/v1/conversations/conversation?permanent=true", "", "Bearer secret")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"deleted"`) {
		t.Fatalf("permanent response = %d %s", response.Code, response.Body.String())
	}
	conversation, err = server.store.GetConversation("conversation")
	if err != nil || conversation != nil {
		t.Fatalf("deleted conversation remains: %#v, %v", conversation, err)
	}
	logs, err = server.store.GetLogs("conversation", 10)
	if err != nil || len(logs) != 0 {
		t.Fatalf("deleted messages remain: %#v, %v", logs, err)
	}
	response = request(t, server, http.MethodGet, "/v1/conversations/conversation", "", "Bearer secret")
	if response.Code != http.StatusNotFound {
		t.Fatalf("get deleted conversation = %d", response.Code)
	}
}

func TestPermanentDeleteConversationValidationAndConflict(t *testing.T) {
	server, _ := newServerHarness(t)
	createServerConversation(t, server, "conversation")

	response := request(t, server, http.MethodDelete, "/v1/conversations/conversation?permanent=invalid", "", "Bearer secret")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid permanent value = %d", response.Code)
	}
	conversation, err := server.store.GetConversation("conversation")
	if err != nil || conversation == nil || conversation.ArchivedAt != nil {
		t.Fatalf("invalid request changed conversation: %#v, %v", conversation, err)
	}
	response = request(t, server, http.MethodDelete, "/v1/conversations/missing?permanent=true", "", "Bearer secret")
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing conversation = %d", response.Code)
	}

	now := time.Now()
	run := &model.Run{ID: "run", ConversationID: "conversation", Status: model.RunStatusWaitingApproval,
		Input: "input", CreatedAt: now, UpdatedAt: now, Usage: &model.UsageInfo{}}
	if err := server.store.SaveRun(run); err != nil {
		t.Fatal(err)
	}
	response = request(t, server, http.MethodDelete, "/v1/conversations/conversation?permanent=true", "", "Bearer secret")
	if response.Code != http.StatusConflict {
		t.Fatalf("active conversation = %d %s", response.Code, response.Body.String())
	}
	logs, err := server.store.GetLogs("conversation", 10)
	if err != nil || len(logs) != 1 {
		t.Fatalf("conflict removed messages: %#v, %v", logs, err)
	}

	run.Status = model.RunStatusCompleted
	run.FinishedAt = &now
	if err := server.store.SaveRun(run); err != nil {
		t.Fatal(err)
	}
	response = request(t, server, http.MethodDelete, "/v1/conversations/conversation?permanent=true", "", "Bearer secret")
	if response.Code != http.StatusOK {
		t.Fatalf("terminal conversation deletion = %d %s", response.Code, response.Body.String())
	}
}
