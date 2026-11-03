package config

import "fmt"

const (
	APIFormatOpenAI    = "openai"
	APIFormatAnthropic = "anthropic"
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

// FallbackConfig carries shared execution budgets into either wire protocol.
func (c LLMConfig) FallbackConfig(fallback FallbackProvider) LLMConfig {
	result := c
	result.APIFormat = fallback.APIFormat
	result.BaseURL, result.APIKey, result.Model = fallback.BaseURL, fallback.APIKey, fallback.Model
	result.ContextWindow, result.Temperature = fallback.ContextWindow, fallback.Temperature
	result.Fallbacks = nil
	return result
}
