package store

import (
	"agent-core/internal/model"
	"testing"
	"time"
)

func TestCheckpointDeletionIsRunScoped(t *testing.T) {
	s := newTestStore(t)
	createTestConversation(t, s, "conv")
	for _, id := range []string{"old", "new"} {
		if err := s.SaveRun(&model.Run{ID: id, ConversationID: "conv", Status: model.RunStatusRunning,
			Input: "test", CreatedAt: time.Now(), Spec: model.RunSpec{RootRunID: id, Kind: model.RunKindRoot}}); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveCheckpointData(id, "conv", []byte(`{"run_id":"`+id+`"}`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteCheckpointData("old"); err != nil {
		t.Fatal(err)
	}
	data, err := s.LoadCheckpointData("new")
	if err != nil || data == nil {
		t.Fatalf("new run checkpoint was deleted: %v", err)
	}
}
