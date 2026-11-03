package builtin

import (
	"agent-core/internal/tool"
	"context"
	"fmt"
	"time"
)

// TimeTool returns the current date and time.
type TimeTool struct{}

func NewTimeTool() *TimeTool { return &TimeTool{} }

func (t *TimeTool) Name() string { return "get_current_time" }
func (t *TimeTool) Capabilities() tool.Capabilities {
	return tool.Capabilities{ReadOnly: true, ConcurrentSafe: true, SideEffectScope: "none", RiskLevel: "low", Timeout: 5 * time.Second}
}
func (t *TimeTool) Description() string { return "Get the current date and time in various formats" }
func (t *TimeTool) Schema() tool.ToolSchema {
	return tool.ToolSchema{
		Type: "object",
		Properties: map[string]any{
			"timezone": map[string]any{
				"type":        "string",
				"description": "IANA timezone name (e.g., 'Asia/Shanghai', 'UTC'). Defaults to local timezone.",
			},
		},
	}
}

func (t *TimeTool) Execute(_ context.Context, args map[string]any) (string, error) {
	now := time.Now()

	if tz, ok := args["timezone"].(string); ok && tz != "" {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			return "", fmt.Errorf("invalid timezone %q: %w", tz, err)
		}
		now = now.In(loc)
	}

	result := fmt.Sprintf(
		"Date: %s\nTime: %s\nUnix: %d\nISO8601: %s\nTimezone: %s",
		now.Format("2006-01-02 (Monday)"),
		now.Format("15:04:05 MST"),
		now.Unix(),
		now.Format(time.RFC3339),
		now.Location().String(),
	)
	return result, nil
}
