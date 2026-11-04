package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestZCodeDefaultsAndRemovedConfiguration(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Permission.Mode != "build" || cfg.ModelStream.IdleTimeoutMs != 600000 || cfg.Network.Timeout != 180000 || cfg.ToolConcurrency.MaxConcurrency != 10 || cfg.Skills.MetadataBudget != 20000 || cfg.LLM.MaxRetries != 10 || cfg.LLM.ContextWindow != 200000 || cfg.LLM.MaxOutputTokens != 32000 || cfg.UI.Locale != "en-US" || cfg.UI.Theme != "auto" {
		t.Fatalf("ZCode defaults drifted: %+v", cfg)
	}
	if cfg.LLM.MaxToolIterations != 0 || cfg.LLM.MaxRunTokens != 0 || cfg.LLM.RunTimeoutSeconds != 0 {
		t.Fatal("main run has a hidden default hard stop")
	}
	if !cfg.Features.Compact || !cfg.Features.Rewind || !cfg.Features.Subagent || !cfg.Features.Memory || !cfg.Features.Skill || !cfg.Features.MCP || !cfg.Memory.Use || !cfg.Skills.Enabled || !cfg.Skills.IncludeInstructions {
		t.Fatal("feature defaults drifted")
	}
	for _, key := range []string{"embedding:\n  model: test\n", "llm:\n  max_tool_iterations: 15\n", "skills:\n  metadata_budget: 8192\n", "features:\n  skills: true\n", "memory:\n  capacity_limit: 200\n"} {
		if _, err := Load(writeConfig(t, key)); err == nil || !strings.Contains(err.Error(), "removed configuration") {
			t.Fatalf("migration error missing for %q: %v", key, err)
		}
	}
	encoded, _ := json.Marshal(cfg)
	for _, removed := range []string{"max_tool_iterations", "max_run_tokens", "run_timeout_seconds", "embedding", "vector_backend", "capacity_limit", "metadata_budget"} {
		if strings.Contains(string(encoded), removed) {
			t.Fatalf("removed field still public: %s", removed)
		}
	}
}
func TestZCodeEnvironmentAndMCPSecrets(t *testing.T) {
	t.Setenv("ZCODE_HTTP_TIMEOUT", "120000")
	t.Setenv("ZCODE_MAX_TOOL_CONCURRENCY", "2")
	t.Setenv("ZCODE_MODEL_RETRY_MAX_RETRIES", "0")
	t.Setenv("ZCODE_MODEL_RETRY_BASE_DELAY_MS", "25")
	cfg, err := Load(writeConfig(t, "mcp:\n  servers:\n    tools:\n      type: http\n      url: https://mcp.example.com/mcp\n      headers: {Authorization: secret}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.RequestTimeoutMs != 120000 || cfg.ToolConcurrency.MaxConcurrency != 2 || cfg.LLM.MaxRetries != 0 || cfg.LLM.RetryBaseDelayMs != 25 {
		t.Fatal("environment did not affect effective runtime")
	}
	redacted := cfg.Redacted()
	if redacted.MCP.Servers["tools"].Headers["Authorization"] != "***" || cfg.MCP.Servers["tools"].Headers["Authorization"] != "secret" {
		t.Fatal("redaction leaked or mutated a secret")
	}
	t.Setenv("ZCODE_HTTP_TIMEOUT", "not-a-number")
	cfg, err = Load(filepath.Join(t.TempDir(), "missing"))
	if err == nil && cfg.Validate(ModeHTTP) == nil {
		t.Fatal("invalid environment silently accepted")
	}
}
func TestShippedConfigurationLoadsAndValidates(t *testing.T) {
	t.Setenv("AUTH_TOKEN", "test-secret")
	for _, name := range []string{"../../agent-core.yaml", "../../deploy/container.yaml"} {
		if _, err := os.Stat(name); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := cfg.Validate(ModeHTTP); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
