package tool

import (
	"context"
	"fmt"
	"testing"
)

func TestProgressiveDiscoveryUpdatesVisibleDefinitions(t *testing.T) {
	registry := NewRegistry()
	for _, name := range []string{"read_file", "tool_search", "tool_describe", "skill"} {
		if err := registry.Register(&testProvider{name: name, caps: Capabilities{ReadOnly: true}}, SourceBuiltin, "builtin"); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("external_%02d", i)
		if err := registry.Register(&testProvider{name: name}, SourceMCP, "test"); err != nil {
			t.Fatal(err)
		}
	}
	state := NewDiscoveryState(nil, true, 5)
	definitions := registry.ToolDefinitionsForRun(state)
	skillVisible := false
	for _, definition := range definitions {
		if definition.Function.Name == "external_00" {
			t.Fatal("undiscovered external tool was exposed")
		}
		if definition.Function.Name == "skill" {
			skillVisible = true
		}
	}
	if !skillVisible {
		t.Fatal("skill tool was hidden behind progressive discovery")
	}
	search := NewToolSearchTool(registry)
	ctx := WithExecutionInfo(context.Background(), ExecutionInfo{Discovery: state})
	if _, err := search.Execute(ctx, map[string]any{"query": "external_00"}); err != nil {
		t.Fatal(err)
	}
	if after := len(registry.ToolDefinitionsForRun(state)); after <= len(definitions) {
		t.Fatalf("searched tools were not added to the current run: before=%d after=%d", len(definitions), after)
	}
}

func TestDisabledToolsetBlocksExecution(t *testing.T) {
	registry := NewRegistry()
	provider := &testProvider{name: "external", caps: Capabilities{ReadOnly: true}}
	if err := registry.Register(provider, SourceMCP, "test"); err != nil {
		t.Fatal(err)
	}
	if err := registry.ConfigureToolsets(DefaultToolsets(), map[string]bool{"core": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Dispatch(context.Background(), "external", map[string]any{"value": "x"}); err == nil {
		t.Fatal("disabled tool executed")
	}
	if provider.executed.Load() != 0 {
		t.Fatal("provider ran despite disabled toolset")
	}
}
