package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestToolExpandsSkillDirectoryVariables(t *testing.T) {
	workspace := t.TempDir()
	directory := filepath.Join(workspace, ".agents", "skills", "variables")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: variables\ndescription: Test skill variables\n---\nZCode: ${ZCODE_SKILL_DIR}\nClaude: ${CLAUDE_SKILL_DIR}\nKeep: $SKILL_DIR and ${UNKNOWN_SKILL_DIR}"
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	loader := NewLoader(Options{ProjectDir: workspace})
	result, err := NewTool(loader, nil).Execute(context.Background(), map[string]any{"name": "variables"})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"ZCode: " + directory,
		"Claude: " + directory,
		"Keep: $SKILL_DIR and ${UNKNOWN_SKILL_DIR}",
		"Base directory: " + directory,
	} {
		if !strings.Contains(result, expected) {
			t.Fatalf("skill result does not contain %q:\n%s", expected, result)
		}
	}
}
