package skills

import (
	"agent-core/internal/project"
	"agent-core/internal/tool"
	"agent-core/internal/trust"
	"context"
	"fmt"
	"strings"
	"time"
)

// Tool loads a skill body only when the model explicitly requests it.
type Tool struct {
	loader *Loader
	trust  *trust.Manager
}

func NewTool(loader *Loader, manager *trust.Manager) *Tool {
	return &Tool{loader: loader, trust: manager}
}
func (t *Tool) Name() string { return "skill" }
func (t *Tool) Description() string {
	return "Load a discovered SKILL.md by exact name; returns its body and base directory."
}
func (t *Tool) Schema() tool.ToolSchema {
	return tool.ToolSchema{Type: "object", Required: []string{"name"}, Properties: map[string]any{
		"name": map[string]any{"type": "string"}, "args": map[string]any{"type": "string"},
	}}
}
func (t *Tool) Capabilities() tool.Capabilities {
	return tool.Capabilities{ReadOnly: true, ConcurrentSafe: true, SideEffectScope: "session", RiskLevel: "low", Timeout: 30 * time.Second, MaxOutputBytes: MaxBodyBytes + 4096}
}
func (t *Tool) Execute(_ context.Context, args map[string]any) (string, error) {
	name, _ := args["name"].(string)
	loaded, err := t.loader.Load(name)
	if err != nil {
		return "", err
	}
	if !loaded.Skill.SafeToAutoLoad {
		if t.trust == nil {
			return "", fmt.Errorf("skill %s requires workspace trust", name)
		}
		declarations := []project.Declaration{{Kind: "skill", Path: loaded.Skill.Path, Digest: loaded.Skill.Digest}}
		status, err := t.trust.Review(loaded.Skill.Directory, declarations)
		if err != nil {
			return "", err
		}
		if !status.Trusted {
			return "", fmt.Errorf("skill %s requires trust for digest %s", name, status.BundleDigest)
		}
	}
	content := strings.NewReplacer(
		"${ZCODE_SKILL_DIR}", loaded.BaseDirectory,
		"${CLAUDE_SKILL_DIR}", loaded.BaseDirectory,
	).Replace(loaded.Content)
	return fmt.Sprintf("<skill_content name=%q>\n%s\n\nBase directory: %s\n</skill_content>", loaded.Skill.Name, content, loaded.BaseDirectory), nil
}
