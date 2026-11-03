package commands

import (
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

func TestDynamicShellIsHandedOffWithoutExecution(t *testing.T) {
	command := Command{Name: "unsafe", Scope: ScopeProject, Source: SourceZCode}
	expansion, err := Expand(command, "Run !`printf '%s' \"$HOME\"`", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(expansion.Expanded, "/unsafe (project/zcode)") || !strings.Contains(expansion.Expanded, "```bash") || !strings.Contains(expansion.Expanded, "$HOME") {
		t.Fatalf("unexpected hand-off: %q", expansion.Expanded)
	}
}

func TestArgumentTextIsNotInterpretedAsShellBlock(t *testing.T) {
	command := Command{Name: "review", Scope: ScopeProject, Source: SourceZCode}
	expansion, err := Expand(command, "Review $ARGUMENTS", "hello !`rm -rf /tmp/x`")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(expansion.Expanded, "This shell step comes from") {
		t.Fatalf("argument text was incorrectly treated as a shell block: %q", expansion.Expanded)
	}
}

func TestPluginVariableIsNotExpandedToEmptyString(t *testing.T) {
	command := Command{Name: "unsafe", Scope: ScopeProject, Source: SourceZCode}
	expansion, err := Expand(command, "Run !`bash ${CLAUDE_PLUGIN_ROOT}/x.sh`", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(expansion.Expanded, "was not run") || !strings.Contains(expansion.Expanded, "CLAUDE_PLUGIN_ROOT") {
		t.Fatalf("unresolved variable was not preserved: %q", expansion.Expanded)
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
