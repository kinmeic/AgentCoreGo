package memory

import (
	"agent-core/internal/model"
	"agent-core/internal/tool"
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// SaveMemoryTool lets the model explicitly save a durable fact.
type SaveMemoryTool struct{ service *Service }

func NewSaveMemoryTool(service *Service) *SaveMemoryTool { return &SaveMemoryTool{service: service} }
func (t *SaveMemoryTool) Name() string                   { return "save_memory" }
func (t *SaveMemoryTool) Description() string {
	return "Save a concise, durable fact or preference; do not store secrets."
}
func (t *SaveMemoryTool) Schema() tool.ToolSchema {
	return tool.ToolSchema{Type: "object", Required: []string{"content"}, Properties: map[string]any{
		"content":    map[string]any{"type": "string", "maxLength": 4000},
		"category":   map[string]any{"type": "string", "default": "fact"},
		"importance": map[string]any{"type": "number", "minimum": 0, "maximum": 1},
	}}
}
func (t *SaveMemoryTool) Capabilities() tool.Capabilities {
	return tool.Capabilities{NeedsApproval: true, SideEffectScope: "memory", RiskLevel: "medium", Timeout: 65 * time.Second}
}
func (t *SaveMemoryTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	category, _ := args["category"].(string)
	if category == "" {
		category = "fact"
	}
	importance := 0.5
	if value, ok := args["importance"].(float64); ok {
		importance = value
	}
	memory := &model.Memory{Content: args["content"].(string), Category: category,
		Source: "agent_tool", Importance: importance, Enabled: true}
	if err := t.service.Add(ctx, memory); err != nil {
		return "", err
	}
	return fmt.Sprintf("Saved memory %d", memory.ID), nil
}

// SearchMemoryTool searches only enabled memories.
type SearchMemoryTool struct{ service *Service }

func NewSearchMemoryTool(service *Service) *SearchMemoryTool {
	return &SearchMemoryTool{service: service}
}
func (t *SearchMemoryTool) Name() string        { return "search_memory" }
func (t *SearchMemoryTool) Description() string { return "Search relevant enabled long-term memories." }
func (t *SearchMemoryTool) Schema() tool.ToolSchema {
	return tool.ToolSchema{Type: "object", Required: []string{"query"}, Properties: map[string]any{
		"query": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 20},
	}}
}
func (t *SearchMemoryTool) Capabilities() tool.Capabilities {
	return tool.Capabilities{ReadOnly: true, ConcurrentSafe: true, SideEffectScope: "memory", RiskLevel: "low", Timeout: 65 * time.Second}
}
func (t *SearchMemoryTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	limit := 5
	if value, ok := args["limit"].(float64); ok {
		limit = int(value)
	}
	results, err := t.service.Search(ctx, args["query"].(string), limit)
	if err != nil {
		return "", err
	}
	encoded, _ := json.Marshal(results)
	return string(encoded), nil
}
