package agent

import (
	"context"
	"testing"
)

func TestHasActiveConversation(t *testing.T) {
	orchestrator, _, _ := newAgentHarness(t, &fakeLLM{})
	if _, err := orchestrator.registerRun("run", "conversation", context.Background()); err != nil {
		t.Fatal(err)
	}
	if !orchestrator.HasActiveConversation("conversation") {
		t.Fatal("active conversation was not reported")
	}
	if orchestrator.HasActiveConversation("other") {
		t.Fatal("unrelated conversation was reported active")
	}
	orchestrator.StopRun("run")
	orchestrator.unregisterRun("run")
	if orchestrator.HasActiveConversation("conversation") {
		t.Fatal("conversation remained active after unregister")
	}
}
