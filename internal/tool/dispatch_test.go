package tool

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
)

type testProvider struct {
	name     string
	caps     Capabilities
	executed atomic.Int32
}

func (p *testProvider) Name() string        { return p.name }
func (p *testProvider) Description() string { return "test tool" }
func (p *testProvider) Schema() ToolSchema {
	return ToolSchema{Type: "object", Required: []string{"value"}, Properties: map[string]any{
		"value": map[string]any{"type": "string"},
	}}
}
func (p *testProvider) Execute(context.Context, map[string]any) (string, error) {
	p.executed.Add(1)
	return strings.Repeat("x", 20), nil
}
func (p *testProvider) Capabilities() Capabilities { return p.caps }

func TestRegistryRejectsDuplicateNames(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(&testProvider{name: "same"}, SourceMCP, "one"); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(&testProvider{name: "same"}, SourceMCP, "two"); err == nil {
		t.Fatal("expected a name collision error")
	}
}

func TestDispatchValidatesArgumentsBeforeExecution(t *testing.T) {
	registry := NewRegistry()
	provider := &testProvider{name: "validated", caps: Capabilities{ReadOnly: true, MaxOutputBytes: 10}}
	if err := registry.Register(provider, SourceBuiltin, "builtin"); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Dispatch(context.Background(), "validated", map[string]any{}); err == nil {
		t.Fatal("expected schema validation to fail")
	}
	if provider.executed.Load() != 0 {
		t.Fatal("provider executed with invalid arguments")
	}
	result, err := registry.Dispatch(context.Background(), "validated", map[string]any{"value": "ok"})
	if err != nil || len(result) <= 20 || !strings.Contains(result, "truncated") {
		t.Fatalf("expected bounded output, got %q, %v", result, err)
	}
}

func TestBuiltinCannotBeUnregistered(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(&testProvider{name: "builtin"}, SourceBuiltin, "builtin"); err != nil {
		t.Fatal(err)
	}
	if err := registry.Unregister("builtin"); err == nil {
		t.Fatal("expected builtin unregister to fail")
	}
}
