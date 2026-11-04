package config

import "testing"

func TestThinkingConfigurationDefaultsAndValidation(t *testing.T) {
	cfg := DefaultConfig().LLM
	if !cfg.ExposeReasoning || cfg.ThinkingType != ThinkingTypeDisabled || cfg.ThinkingBudgetTokens != 16000 {
		t.Fatalf("unexpected reasoning defaults: %#v", cfg)
	}
	cfg.MaxOutputTokens = 32000
	cfg.ThinkingType = ThinkingTypeEnabled
	if err := ValidateThinking(cfg); err != nil {
		t.Fatal(err)
	}
	cfg.ThinkingBudgetTokens = 1023
	if err := ValidateThinking(cfg); err == nil {
		t.Fatal("accepted a manual budget below 1024")
	}
	cfg.ThinkingBudgetTokens = cfg.MaxOutputTokens
	if err := ValidateThinking(cfg); err == nil {
		t.Fatal("accepted a manual budget equal to max output")
	}
	cfg.ThinkingType = ThinkingTypeAdaptive
	cfg.ThinkingBudgetTokens = 0
	if err := ValidateThinking(cfg); err != nil {
		t.Fatal(err)
	}
	cfg.ThinkingType = "unknown"
	if err := ValidateThinking(cfg); err == nil {
		t.Fatal("accepted an unknown thinking type")
	}
}
