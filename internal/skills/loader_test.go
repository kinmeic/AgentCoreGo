package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverAndLoadSkill(t *testing.T) {
	workspace := t.TempDir()
	directory := filepath.Join(workspace, ".agents", "skills", "review")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: review\ndescription: Review code carefully\nallowed-tools: Read\n---\nUse exact evidence."
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	loader := NewLoader(Options{ProjectDir: workspace})
	catalog := loader.Discover()
	if len(catalog.Skills) != 1 || catalog.Skills[0].SafeToAutoLoad || len(catalog.Skills[0].RiskFields) != 1 {
		t.Fatalf("unexpected skills: %#v", catalog.Skills)
	}
	loaded, err := loader.Load("review")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Content != "Use exact evidence." || loaded.BaseDirectory != directory || loaded.Skill.Digest == "" {
		t.Fatalf("unexpected loaded skill: %#v", loaded)
	}
	if !strings.Contains(catalog.RenderMetadata(), "Review code carefully") {
		t.Fatal("metadata was not rendered")
	}
}
