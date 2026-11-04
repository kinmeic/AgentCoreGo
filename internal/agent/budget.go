package agent

import (
	"agent-core/internal/llm"
	"errors"
	"fmt"
	"log"
	"math"
)

// IterationBudget tracks token consumption and iteration count for an agent run.
// Inspired by hermes-agent's IterationBudget pattern.
type IterationBudget struct {
	MaxIterations int
	MaxTokens     int // 0 means unlimited

	currentIteration int
	totalTokens      int
}

// NewIterationBudget creates a budget with the given limits.
func NewIterationBudget(maxIterations, maxTokens int) *IterationBudget {
	return &IterationBudget{
		MaxIterations: maxIterations,
		MaxTokens:     maxTokens,
	}
}

// RecordIteration increments the iteration counter and adds token usage.
func (b *IterationBudget) RecordIteration(usedTokens int) {
	b.currentIteration++
	b.totalTokens += usedTokens
}

// Remaining returns how many iterations are left.
func (b *IterationBudget) Remaining() int {
	if b.MaxIterations <= 0 {
		return math.MaxInt
	}
	r := b.MaxIterations - b.currentIteration
	if r < 0 {
		return 0
	}
	return r
}

// Exhausted returns true if the budget is fully consumed.
func (b *IterationBudget) Exhausted() bool {
	if b.MaxIterations > 0 && b.currentIteration >= b.MaxIterations {
		return true
	}
	if b.MaxTokens > 0 && b.totalTokens >= b.MaxTokens {
		return true
	}
	return false
}

// ExceededMessage returns a human-readable message about what was exceeded.
func (b *IterationBudget) ExceededMessage() string {
	if b.MaxIterations > 0 && b.currentIteration >= b.MaxIterations {
		return fmt.Sprintf("max tool iterations (%d) exceeded", b.MaxIterations)
	}
	if b.MaxTokens > 0 && b.totalTokens >= b.MaxTokens {
		return fmt.Sprintf("token budget (%d) exceeded, used %d", b.MaxTokens, b.totalTokens)
	}
	return ""
}

// TotalTokens returns the total tokens consumed.
func (b *IterationBudget) TotalTokens() int {
	return b.totalTokens
}

// CurrentIteration returns the current iteration count.
func (b *IterationBudget) CurrentIteration() int {
	return b.currentIteration
}

// classifyAndLog logs a classified API error and returns whether the loop should continue.
func classifyAndLog(err error) (shouldContinue bool) {
	var apiErr *llm.APIError
	if !errors.As(err, &apiErr) {
		log.Printf("[agent] Non-API error: %v", err)
		return false
	}

	switch apiErr.Kind {
	case llm.ErrorRateLimit:
		log.Printf("[agent] Rate limited (429): %s", apiErr.Message)
		return false
	case llm.ErrorServerError:
		log.Printf("[agent] Server error (%d): %s", apiErr.StatusCode, apiErr.Message)
		return false
	case llm.ErrorAuthFailed:
		log.Printf("[agent] Authentication failed: %s", apiErr.Message)
		return false
	case llm.ErrorForbidden:
		log.Printf("[agent] Access forbidden: %s", apiErr.Message)
		return false
	case llm.ErrorContextLength:
		log.Printf("[agent] Context length exceeded — compression needed: %s", apiErr.Message)
		return true // Signal that compression should be attempted
	case llm.ErrorModelOverloaded:
		log.Printf("[agent] Model overloaded: %s", apiErr.Message)
		return false
	default:
		log.Printf("[agent] API error (%s): %s", apiErr.Kind, apiErr.Message)
		return false
	}
}
