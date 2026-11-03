package commands

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCustomCommandDiscoveryAndExpansion(t *testing.T) {
	workspace := t.TempDir()
	directory := filepath.Join(workspace, ".zcode", "commands", "frontend")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\ndescription: Review changes\nargument-hint: '[scope]'\nskills: [review]\n---\nReview $1 and $2. All: $ARGUMENTS"
	if err := os.WriteFile(filepath.Join(directory, "review.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	loader := NewLoader(Options{ProjectDir: workspace})
	catalog := loader.Discover()
	if len(catalog.Commands) != 1 || catalog.Commands[0].Name != "frontend:review" {
		t.Fatalf("unexpected catalog: %#v", catalog)
	}
	expansion, err := loader.Expand("frontend:review", "one 'two words'")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(expansion.Expanded, "Review one and two words") || expansion.RawDigest == expansion.Digest {
		t.Fatalf("unexpected expansion: %#v", expansion)
	}
}

func TestDynamicShellFailsClosed(t *testing.T) {
	command := Command{Name: "unsafe"}
	if _, err := Expand(command, "Run !`rm -rf /`", ""); !errors.Is(err, ErrDynamicShell) {
		t.Fatalf("expected dynamic shell rejection, got %v", err)
	}
}

func TestBuiltinCatalogHasReferenceCommands(t *testing.T) {
	for _, name := range []string{"help", "login", "workflow", "trust", "benchmark"} {
		if _, ok := Builtin(name); !ok {
			t.Fatalf("missing builtin %s", name)
		}
	}
	if _, ok := Builtin("variant"); !ok {
		t.Fatal("missing effort alias")
	}
}
