package llm

import (
	"agent-core/internal/model"
	"context"
	"fmt"
	"log"
	"sync/atomic"
)

// FailoverClient wraps multiple providers and automatically switches
// to a fallback when the primary provider fails.
type FailoverClient struct {
	providers []Provider
	current   atomic.Int32 // Index of the currently active provider
}

// Verify FailoverClient implements Provider at compile time.
var _ Provider = (*FailoverClient)(nil)

// NewFailoverClient creates a failover client with the given providers.
// Providers are tried in order; the first one is the primary.
// Panics if no providers are given.
func NewFailoverClient(providers []Provider) *FailoverClient {
	if len(providers) == 0 {
		panic("NewFailoverClient requires at least one provider")
	}
	return &FailoverClient{
		providers: providers,
	}
}

// active returns the currently active provider.
func (f *FailoverClient) active() Provider {
	idx := int(f.current.Load())
	if idx >= len(f.providers) {
		idx = len(f.providers) - 1
	}
	return f.providers[idx]
}

// Complete tries the active provider first, then falls back on failure.
func (f *FailoverClient) Complete(ctx context.Context, messages []model.Message, tools []model.ToolDefinition) (*model.Message, *model.UsageInfo, error) {
	startIdx := int(f.current.Load())

	for i := startIdx; i < len(f.providers); i++ {
		p := f.providers[i]
		resp, usage, err := p.Complete(ctx, messages, tools)
		if err == nil {
			// Success — reset to primary for next request
			if i > 0 {
				f.current.Store(0)
			}
			return resp, usage, nil
		}

		// Don't failover on non-retryable errors (auth, bad request)
		if !shouldFailover(err) {
			return nil, nil, err
		}

		log.Printf("[llm] Provider %q failed: %v, trying next...", p.Name(), err)
		// Only advance if not at the last provider
		if i+1 < len(f.providers) {
			f.current.Store(int32(i + 1))
		}
	}

	return nil, nil, fmt.Errorf("all %d providers failed", len(f.providers))
}

// CompleteStream tries the active provider first, then falls back on failure.
func (f *FailoverClient) CompleteStream(ctx context.Context, messages []model.Message, tools []model.ToolDefinition) (<-chan StreamChunk, error) {
	startIdx := int(f.current.Load())

	for i := startIdx; i < len(f.providers); i++ {
		p := f.providers[i]
		ch, err := p.CompleteStream(ctx, messages, tools)
		if err == nil {
			// Success — reset to primary for next request
			if i > 0 {
				f.current.Store(0)
			}
			return ch, nil
		}

		if !shouldFailover(err) {
			return nil, err
		}

		log.Printf("[llm] Provider %q stream failed: %v, trying next...", p.Name(), err)
		// Only advance if not at the last provider
		if i+1 < len(f.providers) {
			f.current.Store(int32(i + 1))
		}
	}

	return nil, fmt.Errorf("all %d providers failed", len(f.providers))
}

// Name returns the active provider's name with failover indicator.
func (f *FailoverClient) Name() string {
	idx := int(f.current.Load())
	if idx > 0 && idx < len(f.providers) {
		return f.providers[idx].Name() + " (failover)"
	}
	return f.providers[0].Name()
}

// ModelName returns the active provider's model name.
func (f *FailoverClient) ModelName() string {
	return f.active().ModelName()
}

// MaxContextWindow returns the active provider's context window.
func (f *FailoverClient) MaxContextWindow() int {
	return f.active().MaxContextWindow()
}

// Reset switches back to the primary provider.
func (f *FailoverClient) Reset() {
	f.current.Store(0)
}

// shouldFailover determines whether an error warrants provider failover.
func shouldFailover(err error) bool {
	apiErr, ok := err.(*APIError)
	if !ok {
		// Non-API errors (network timeouts, etc.) should trigger failover
		return true
	}
	switch apiErr.Kind {
	case ErrorServerError, ErrorRateLimit, ErrorModelOverloaded:
		return true
	case ErrorAuthFailed, ErrorForbidden, ErrorBadRequest, ErrorNotFound:
		return false
	default:
		return true
	}
}
