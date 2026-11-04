package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportedProjectSkills(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".agents", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string][]string{
		"docx":          {"setup.sh", "scripts/postcheck.py", "references/design-system.md", "LICENSE.txt"},
		"pdf":           {"scripts/pdf.py", "scripts/design_engine.py", "typesetting/geometry.md", "LICENSE.txt"},
		"pptx":          {"LICENSE.txt"},
		"xlsx":          {"xlsx.py", "templates/base.py", "templates/palettes.py", "quality/pipeline.md", "LICENSE.txt"},
		"visualize":     {"scripts/render.py", "scripts/vendor.py", "assets/visualize.html", "assets/visualize.css", "assets/vendor/manifest.json", "assets/vendor/d3-7.9.0.min.js", "LICENSE.md"},
		"skill-creator": {},
	}

	loader := NewLoader(Options{Roots: []string{root}})
	catalog := loader.Discover()
	if len(catalog.Diagnostics) != 0 {
		t.Fatalf("imported skills have diagnostics: %#v", catalog.Diagnostics)
	}
	if len(catalog.Skills) != len(expected) {
		t.Fatalf("expected %d imported skills, got %#v", len(expected), catalog.Skills)
	}

	for _, skill := range catalog.Skills {
		resources, ok := expected[skill.Name]
		if !ok {
			t.Errorf("unexpected imported skill %q", skill.Name)
			continue
		}
		if !skill.SafeToAutoLoad {
			t.Errorf("imported skill %q unexpectedly requires trust: %#v", skill.Name, skill.RiskFields)
		}
		loaded, err := loader.Load(skill.Name)
		if err != nil {
			t.Errorf("load %q: %v", skill.Name, err)
			continue
		}
		if loaded.Truncated || loaded.Content == "" {
			t.Errorf("invalid loaded body for %q: truncated=%v bytes=%d", skill.Name, loaded.Truncated, loaded.BytesRead)
		}
		expectedDirectory := filepath.Join(root, skill.Name)
		if loaded.BaseDirectory != expectedDirectory {
			t.Errorf("base directory for %q = %q, want %q", skill.Name, loaded.BaseDirectory, expectedDirectory)
		}
		if strings.Contains(loaded.Content, "::zcode-file-citation") {
			t.Errorf("skill %q retains unsupported ZCode file citations", skill.Name)
		}
		if skill.Name == "visualize" && strings.Contains(loaded.Content, "::visualize") {
			t.Error("visualize retains unsupported inline references")
		}
		for _, resource := range resources {
			path := filepath.Join(expectedDirectory, resource)
			info, err := os.Stat(path)
			if err != nil {
				t.Errorf("required resource for %q: %v", skill.Name, err)
				continue
			}
			if !info.Mode().IsRegular() || info.Size() == 0 {
				t.Errorf("required resource for %q is empty or not a file: %s", skill.Name, path)
			}
		}
	}
}
