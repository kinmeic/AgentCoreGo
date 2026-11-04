package llm

import (
	"agent-core/internal/model"
	"context"
)

// Provider defines the interface for LLM backend providers.
// This abstraction allows switching between different LLM services
// (OpenAI-compatible APIs, native Anthropic Messages, local models, etc.) without changing
// the agent loop code.
type Provider interface {
	// Complete sends a non-streaming completion request.
	Complete(ctx context.Context, messages []model.Message, tools []model.ToolDefinition) (*model.Message, *model.UsageInfo, error)

	// CompleteStream sends a streaming completion request.
	CompleteStream(ctx context.Context, messages []model.Message, tools []model.ToolDefinition) (<-chan StreamChunk, error)

	// Name returns the provider identifier (e.g., "openai", "openrouter").
	Name() string

	// ModelName returns the model being used (e.g., "gpt-4o").
	ModelName() string

	// MaxContextWindow returns the maximum context window in tokens.
	MaxContextWindow() int
}
