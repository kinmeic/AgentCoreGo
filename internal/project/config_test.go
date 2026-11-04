package project

import (
	"agent-core/internal/config"
	"os"
	"path/filepath"
	"testing"
)

func TestProjectLayerIsRestrictedAndContentAddressed(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".agent-core"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, ".agent-core", "config.yaml")
	if err := os.WriteFile(path, []byte("model: project-model\neffort: high\nmode: yolo\nfeatures:\n  skills: false\nskills:\n  roots: [skills]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Tools.ApprovalMode = "build"
	report, err := LoadAndApply(&cfg, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.Model != "" || cfg.LLM.ReasoningEffort != "" {
		t.Fatalf("project changed protected model: %#v", cfg.LLM)
	}
	if cfg.Tools.ApprovalMode != "build" {
		t.Fatal("project escalated permission mode")
	}
	if !cfg.Features.Skills {
		t.Fatal("project changed protected feature flag")
	}
	if len(cfg.Skills.Roots) != 0 {
		t.Fatal("project changed skill roots")
	}
	if report.BundleDigest == "" || len(report.Declarations) != 1 {
		t.Fatalf("missing trust evidence: %#v", report)
	}
	if err := os.WriteFile(path, []byte("model: changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := LoadAndApply(&cfg, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if changed.BundleDigest == report.BundleDigest {
		t.Fatal("content change did not change trust bundle")
	}
}
