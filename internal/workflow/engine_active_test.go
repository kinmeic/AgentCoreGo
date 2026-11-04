package workflow

import (
	"context"
	"testing"
)

func TestHasActive(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine := &Engine{active: map[string]context.CancelFunc{"run": cancel}}
	if !engine.HasActive("run") {
		t.Fatal("active workflow was not reported")
	}
	if engine.HasActive("other") {
		t.Fatal("unrelated workflow was reported active")
	}
	delete(engine.active, "run")
	if engine.HasActive("run") {
		t.Fatal("workflow remained active after cleanup")
	}
}
