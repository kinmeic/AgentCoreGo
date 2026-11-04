package config

import "fmt"

const (
	APIFormatOpenAI    = "openai"
	APIFormatAnthropic = "anthropic"

	ThinkingTypeDisabled = "disabled"
	ThinkingTypeEnabled  = "enabled"
	ThinkingTypeAdaptive = "adaptive"
	ThinkingTypeAuto     = "auto" // Omit thinking and use the provider's default.
)

// An omitted format preserves compatibility with existing OpenAI configurations.
func ValidateAPIFormat(format string) error {
	switch format {
	case "", APIFormatOpenAI, APIFormatAnthropic:
		return nil
	default:
		return fmt.Errorf("must be openai or anthropic, got %q", format)
	}
}

func ValidateThinking(c LLMConfig) error {
	typeName := c.ThinkingType
	if typeName == "" {
		typeName = ThinkingTypeAuto
	}
	switch typeName {
	case ThinkingTypeAuto, ThinkingTypeDisabled, ThinkingTypeAdaptive:
		return nil
	case ThinkingTypeEnabled:
		if c.ThinkingBudgetTokens < 1024 {
			return fmt.Errorf("manual thinking budget must be at least 1024 tokens")
		}
		if c.ThinkingBudgetTokens >= c.MaxOutputTokens {
			return fmt.Errorf("manual thinking budget must be less than max_output_tokens")
		}
		return nil
	default:
		return fmt.Errorf("thinking type must be auto, disabled, enabled, or adaptive, got %q", typeName)
	}
}

// FallbackConfig carries shared execution budgets into either wire protocol.
func (c LLMConfig) FallbackConfig(fallback FallbackProvider) LLMConfig {
	result := c
	result.APIFormat = fallback.APIFormat
	result.BaseURL, result.APIKey, result.Model = fallback.BaseURL, fallback.APIKey, fallback.Model
	result.ContextWindow, result.Temperature = fallback.ContextWindow, fallback.Temperature
	result.Fallbacks = nil
	return result
}
