package llm

import (
	"agent-core/internal/model"
	"context"
	"errors"
	"testing"
)

type failoverTestProvider struct {
	name   string
	window int
	fail   bool
}

func (p *failoverTestProvider) Complete(context.Context, []model.Message, []model.ToolDefinition) (*model.Message, *model.UsageInfo, error) {
	if p.fail {
		return nil, nil, errors.New("network unavailable")
	}
	return &model.Message{Role: model.RoleAssistant, Content: "ok"}, &model.UsageInfo{}, nil
}
func (p *failoverTestProvider) CompleteStream(context.Context, []model.Message, []model.ToolDefinition) (<-chan StreamChunk, error) {
	return nil, errors.New("not implemented")
}
func (p *failoverTestProvider) Name() string          { return p.name }
func (p *failoverTestProvider) ModelName() string     { return p.name }
func (p *failoverTestProvider) MaxContextWindow() int { return p.window }

func TestFailoverKeepsActiveContextWindow(t *testing.T) {
	primary := &failoverTestProvider{name: "primary", window: 1000, fail: true}
	fallback := &failoverTestProvider{name: "fallback", window: 2000}
	client := NewFailoverClient([]Provider{primary, fallback})
	if _, _, err := client.Complete(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if client.MaxContextWindow() != 2000 {
		t.Fatalf("context window switched back to primary: %d", client.MaxContextWindow())
	}
}
