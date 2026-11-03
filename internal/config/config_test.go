package config

import (
	"agent-core/internal/security"
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWorkspaceIsSingleSource(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	cfg, err := Load(writeConfig(t, "workspace:\n  path: "+workspace+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tools.WorkDir != cfg.Workspace.Path {
		t.Fatalf("tool directory diverged: %q != %q", cfg.Tools.WorkDir, cfg.Workspace.Path)
	}
}

func TestLegacyWorkDirMapsToWorkspace(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "legacy")
	cfg, err := Load(writeConfig(t, "tools:\n  work_dir: "+workspace+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Workspace.Path != workspace || cfg.Tools.WorkDir != workspace {
		t.Fatalf("legacy mapping failed: %#v", cfg.Workspace)
	}
}

func TestConflictingWorkspacePathsFail(t *testing.T) {
	_, err := Load(writeConfig(t, "workspace:\n  path: ./one\ntools:\n  work_dir: ./two\n"))
	if err == nil {
		t.Fatal("expected conflicting paths to fail")
	}
}

func TestUnknownYAMLFieldFails(t *testing.T) {
	_, err := Load(writeConfig(t, "unknown_setting: true\n"))
	if err == nil {
		t.Fatal("expected unknown field to fail")
	}
}

func TestDirectAuthTokenEnvironmentOverride(t *testing.T) {
	t.Setenv("AUTH_TOKEN", "environment-secret")
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.AuthToken != "environment-secret" {
		t.Fatal("AUTH_TOKEN was not applied without YAML expansion")
	}
	if cfg.Redacted().Server.AuthToken != "***" {
		t.Fatal("effective config was not redacted")
	}
}

func TestDefaultConfigValidates(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(ModeHTTP); err != nil {
		t.Fatalf("default config is invalid: %v", err)
	}
}

func TestRegexPolicyRequiresValues(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Security.Policies = []security.Policy{{Name: "invalid", Effect: security.EffectDeny,
		Resource: security.ResourceTerminal, Conditions: []security.Condition{{Field: "target", Operator: "regex"}}}}
	if err := cfg.Validate(ModeHTTP); err == nil {
		t.Fatal("expected an empty regex policy to fail validation")
	}
}
