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

func TestLLMAPIFormatDefaultsValidationAndEnvironment(t *testing.T) {
	if DefaultConfig().LLM.APIFormat != APIFormatOpenAI {
		t.Fatal("OpenAI default changed")
	}
	for _, format := range []string{"", APIFormatOpenAI, APIFormatAnthropic, "unsupported"} {
		cfg := DefaultConfig()
		cfg.LLM.APIFormat = format
		if err := cfg.Validate(ModeHTTP); (err != nil) != (format == "unsupported") {
			t.Fatalf("format %q validation: %v", format, err)
		}
		cfg = DefaultConfig()
		cfg.LLM.Fallbacks = []FallbackProvider{{Name: "fallback", APIFormat: format, BaseURL: "https://example.test/v1", Model: "mock", ContextWindow: 1000}}
		if err := cfg.Validate(ModeHTTP); (err != nil) != (format == "unsupported") {
			t.Fatalf("fallback format %q validation: %v", format, err)
		}
	}
	t.Setenv("LLM_API_FORMAT", APIFormatAnthropic)
	cfg, err := Load(writeConfig(t, "llm:\n  api_format: openai\n"))
	if err != nil || cfg.LLM.APIFormat != APIFormatAnthropic {
		t.Fatalf("format env override not applied: %#v %v", cfg.LLM, err)
	}
}

func TestFallbackConfigPreservesBudgetsAndChoosesIndependentProtocol(t *testing.T) {
	primary := DefaultConfig().LLM
	primary.APIFormat = APIFormatAnthropic
	primary.ReasoningEffort = "high"
	fallback := primary.FallbackConfig(FallbackProvider{BaseURL: "https://example.test/v1", APIKey: "fallback-key", Model: "fallback-model", ContextWindow: 50000})
	if fallback.APIFormat != "" || fallback.MaxOutputTokens != primary.MaxOutputTokens || fallback.MaxRunTokens != primary.MaxRunTokens || fallback.MaxRetries != primary.MaxRetries || fallback.ReasoningEffort != "high" || fallback.Model != "fallback-model" || fallback.APIKey != "fallback-key" {
		t.Fatalf("fallback lost protocol or budgets: %#v", fallback)
	}
	fallback = primary.FallbackConfig(FallbackProvider{APIFormat: APIFormatAnthropic})
	if fallback.APIFormat != APIFormatAnthropic {
		t.Fatal("native fallback format lost")
	}
}
