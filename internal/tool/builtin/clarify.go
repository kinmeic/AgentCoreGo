package builtin

import (
	"agent-core/internal/tool"
	"context"
	"encoding/json"
	"fmt"
)

// ClarifyKind defines the type of clarification requested.
type ClarifyKind string

const (
	ClarifyConfirm ClarifyKind = "confirm" // Yes/No confirmation
	ClarifySelect  ClarifyKind = "select"  // Choose from options
	ClarifyInput   ClarifyKind = "input"   // Free text input
)

// ClarifyTool allows the agent to ask the user for clarification
// when it's uncertain about how to proceed. This is a bridge tool
// that triggers a special SSE event for the frontend.
type ClarifyTool struct{}

func NewClarifyTool() *ClarifyTool {
	return &ClarifyTool{}
}

func (t *ClarifyTool) Name() string { return "clarify" }
func (t *ClarifyTool) Description() string {
	return "Ask the user for clarification when you're uncertain about how to proceed. " +
		"Use 'confirm' for yes/no questions, 'select' for choosing options, 'input' for open-ended questions."
}
func (t *ClarifyTool) Schema() tool.ToolSchema {
	return tool.ToolSchema{
		Type: "object",
		Properties: map[string]any{
			"kind": map[string]any{
				"type":        "string",
				"enum":        []string{"confirm", "select", "input"},
				"description": "Type of clarification: 'confirm' (yes/no), 'select' (pick option), 'input' (free text)",
			},
			"message": map[string]any{
				"type":        "string",
				"description": "The question or prompt to show the user",
			},
			"options": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Options for 'select' kind. Each item is a choice the user can pick.",
			},
		},
		Required: []string{"kind", "message"},
	}
}

// ClarifyRequest is the structured output emitted when the tool is called.
type ClarifyRequest struct {
	Kind    ClarifyKind `json:"kind"`
	Message string      `json:"message"`
	Options []string    `json:"options,omitempty"`
}

func (t *ClarifyTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	kind, _ := args["kind"].(string)
	message, _ := args["message"].(string)

	if kind == "" || message == "" {
		return "", fmt.Errorf("kind and message are required")
	}

	var options []string
	if opts, ok := args["options"].([]any); ok {
		for _, o := range opts {
			if s, ok := o.(string); ok {
				options = append(options, s)
			}
		}
	}

	if ClarifyKind(kind) == ClarifySelect && len(options) < 2 {
		return "", fmt.Errorf("select kind requires at least 2 options")
	}

	req := ClarifyRequest{
		Kind:    ClarifyKind(kind),
		Message: message,
		Options: options,
	}

	// Encode the request as JSON — the orchestrator/stream handler
	// intercepts this and sends it as a special SSE event.
	reqJSON, _ := json.Marshal(req)

	// Return the clarification request. The orchestrator should detect this
	// and emit a "clarify" SSE event, then wait for the user response.
	return fmt.Sprintf("CLARIFY_REQUEST:%s", string(reqJSON)), nil
}

// IsClarifyRequest checks if a tool result is a clarification request.
func IsClarifyRequest(result string) bool {
	return len(result) > 16 && result[:16] == "CLARIFY_REQUEST:"
}

// ParseClarifyRequest extracts the ClarifyRequest from a tool result.
func ParseClarifyRequest(result string) (*ClarifyRequest, error) {
	if !IsClarifyRequest(result) {
		return nil, fmt.Errorf("not a clarify request")
	}
	var req ClarifyRequest
	if err := json.Unmarshal([]byte(result[16:]), &req); err != nil {
		return nil, err
	}
	return &req, nil
}
