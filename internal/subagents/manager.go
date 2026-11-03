package subagents

import (
	"agent-core/internal/model"
	"agent-core/internal/store"
	"agent-core/internal/tool"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Request is the public subagent invocation contract.
type Request struct {
	Description     string `json:"description"`
	Prompt          string `json:"prompt"`
	SubagentType    string `json:"subagent_type,omitempty"`
	RunInBackground bool   `json:"run_in_background,omitempty"`
}

// ChildStarter is implemented by agent.Orchestrator.
type ChildStarter interface {
	StartChildRun(ctx context.Context, req model.ChatRequest, spec model.RunSpec, runID string, interactive bool, out chan<- model.StreamEvent) (*model.ChatResponse, error)
	StopRun(runID string) bool
}

// Manager creates bounded, independent child runs.
type Manager struct {
	store   *store.SQLite
	starter ChildStarter
}

func NewManager(store *store.SQLite, starter ChildStarter) *Manager {
	return &Manager{store: store, starter: starter}
}

func (m *Manager) Start(ctx context.Context, parentRunID string, request Request) (*model.ChatResponse, error) {
	spec, runID, err := m.childSpec(parentRunID, request)
	if err != nil {
		return nil, err
	}
	return m.starter.StartChildRun(ctx, model.ChatRequest{Message: request.Prompt, Source: "subagent"}, spec, runID, false, nil)
}

func (m *Manager) StartBackground(ctx context.Context, parentRunID string, request Request) (string, error) {
	spec, runID, err := m.childSpec(parentRunID, request)
	if err != nil {
		return "", err
	}
	go func() {
		_, _ = m.starter.StartChildRun(context.WithoutCancel(ctx), model.ChatRequest{Message: request.Prompt, Source: "subagent"}, spec, runID, false, nil)
	}()
	return runID, nil
}

func (m *Manager) childSpec(parentRunID string, request Request) (model.RunSpec, string, error) {
	if request.Description == "" || request.Prompt == "" {
		return model.RunSpec{}, "", fmt.Errorf("description and prompt are required")
	}
	parent, err := m.store.GetRun(parentRunID)
	if err != nil || parent == nil {
		return model.RunSpec{}, "", fmt.Errorf("parent run not found")
	}
	if parent.Spec.Depth >= 1 {
		return model.RunSpec{}, "", fmt.Errorf("nested subagents are disabled")
	}
	kind := request.SubagentType
	if kind == "" {
		kind = "general-purpose"
	}
	if kind != "general-purpose" && kind != "Explore" {
		return model.RunSpec{}, "", fmt.Errorf("unknown subagent type %q", kind)
	}
	spec := parent.Spec
	if conversation, err := m.store.GetConversation(parent.ConversationID); err == nil && conversation != nil {
		if currentMode := conversation.Metadata["mode"]; currentMode != "" {
			spec.Mode = currentMode
		}
	}
	spec.ParentRunID = parent.ID
	spec.Depth = parent.Spec.Depth + 1
	spec.Kind = model.RunKindSubagent
	spec.ReadOnly = spec.ReadOnly || kind == "Explore"
	return spec, uuid.NewString(), nil
}

func (m *Manager) Stop(runID string) bool { return m.starter.StopRun(runID) }
func (m *Manager) List(parentRunID string, limit int) ([]*model.Run, error) {
	return m.store.ListRuns(parentRunID, limit)
}

// Tool exposes subagents through the ordinary tool permission and trace boundary.
type Tool struct{ manager *Manager }

func NewTool(manager *Manager) *Tool { return &Tool{manager: manager} }
func (t *Tool) Name() string         { return "agent" }
func (t *Tool) Description() string {
	return "Run a fresh general-purpose or read-only Explore subagent; nested subagents are disabled."
}
func (t *Tool) Schema() tool.ToolSchema {
	return tool.ToolSchema{Type: "object", Required: []string{"description", "prompt"}, Properties: map[string]any{
		"description": map[string]any{"type": "string"}, "prompt": map[string]any{"type": "string"},
		"subagent_type":     map[string]any{"type": "string", "enum": []string{"general-purpose", "Explore"}},
		"run_in_background": map[string]any{"type": "boolean"},
	}}
}
func (t *Tool) Capabilities() tool.Capabilities {
	return tool.Capabilities{NeedsApproval: true, SideEffectScope: "agent", RiskLevel: "high", Timeout: 31 * time.Minute}
}
func (t *Tool) Execute(ctx context.Context, args map[string]any) (string, error) {
	info, ok := tool.ExecutionInfoFromContext(ctx)
	if !ok {
		return "", fmt.Errorf("subagent run context is required")
	}
	request := Request{Description: args["description"].(string), Prompt: args["prompt"].(string)}
	request.SubagentType, _ = args["subagent_type"].(string)
	request.RunInBackground, _ = args["run_in_background"].(bool)
	if request.RunInBackground {
		id, err := t.manager.StartBackground(ctx, info.RunID, request)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Subagent launched with run_id %s; query its status before reporting results.", id), nil
	}
	response, err := t.manager.Start(ctx, info.RunID, request)
	if err != nil {
		return "", err
	}
	encoded, _ := json.Marshal(map[string]any{"run_id": response.RunID, "status": response.Status,
		"reply": response.Reply, "usage": response.Usage})
	return string(encoded), nil
}
