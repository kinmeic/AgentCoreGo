package store

import (
	"agent-core/internal/model"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestConversationProviderStateRoundTripAndClone(t *testing.T) {
	s := newTestStore(t)
	createTestConversation(t, s, "conversation")
	state := json.RawMessage(`{"content":[{"type":"thinking","thinking":"reasoning","signature":"signature"},{"type":"redacted_thinking","data":"redacted"}]}`)
	if err := s.AddLog(&model.ConversationLog{ConversationID: "conversation", Role: model.RoleAssistant,
		Content: "visible answer", ProviderState: state, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	check := func(name string, logs []*model.ConversationLog, err error) {
		t.Helper()
		if err != nil || len(logs) != 1 || string(logs[0].ProviderState) != string(state) {
			t.Fatalf("%s provider state mismatch: %#v, %v", name, logs, err)
		}
		encoded, marshalErr := json.Marshal(logs[0])
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if strings.Contains(string(encoded), "provider_state") || strings.Contains(string(encoded), "signature") {
			t.Fatalf("provider state leaked into public log JSON: %s", encoded)
		}
	}
	logs, err := s.GetLogs("conversation", 10)
	check("GetLogs", logs, err)
	logs, err = s.GetLogsBefore("conversation", 0, 10)
	check("GetLogsBefore", logs, err)
	logs, err = s.SearchLogs("conversation", "visible", 10)
	check("SearchLogs", logs, err)

	clone, err := s.CloneConversation("conversation")
	if err != nil {
		t.Fatal(err)
	}
	logs, err = s.GetLogs(clone.ID, 10)
	check("CloneConversation", logs, err)
}
