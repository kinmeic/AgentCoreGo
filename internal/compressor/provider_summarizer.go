package compressor

import (
	"agent-core/internal/llm"
	"agent-core/internal/model"
	"agent-core/internal/telemetry"
	"context"
	"errors"
	"strings"
)

// ProviderSummarizer reuses the agent's provider, failover and cancellation policy.
type ProviderSummarizer struct{ provider llm.Provider }

func NewProviderSummarizer(provider llm.Provider) *ProviderSummarizer {
	return &ProviderSummarizer{provider: provider}
}

func (s *ProviderSummarizer) SummarizeMessages(ctx context.Context, messages []model.Message) SummaryResult {
	if len(messages) == 0 {
		return SummaryResult{}
	}
	prompt := buildSummaryPrompt(formatConversationForSummary(messages))
	metadata := llm.CallMetadataFromContext(ctx)
	metadata.Purpose = telemetry.PurposeCompact
	response, _, err := s.provider.Complete(llm.WithCallMetadata(ctx, metadata), []model.Message{{Role: model.RoleUser, Content: prompt}}, nil)
	if err != nil || response == nil || strings.TrimSpace(response.Content) == "" {
		if err == nil {
			err = errors.New("empty summary response")
		}
		return SummaryResult{Summary: fallbackSummary(messages), Error: err}
	}
	return SummaryResult{Summary: strings.TrimSpace(response.Content), Generated: true}
}
