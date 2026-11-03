package tool

import (
	"context"
	"time"

	"agent-core/internal/model"
	"agent-core/internal/telemetry"
)

// Capabilities declares the security and execution properties of a tool.
type Capabilities struct {
	ReadOnly        bool          `json:"read_only"`
	Destructive     bool          `json:"destructive"`
	ConcurrentSafe  bool          `json:"concurrent_safe"`
	SideEffectScope string        `json:"side_effect_scope"`
	RiskLevel       string        `json:"risk_level"`
	NeedsApproval   bool          `json:"needs_approval"`
	Timeout         time.Duration `json:"timeout"`
	MaxOutputBytes  int           `json:"max_output_bytes"`
}

// CapabilityProvider lets a tool replace the conservative defaults.
type CapabilityProvider interface {
	Capabilities() Capabilities
}

// RawSchemaProvider preserves a complete JSON Schema, including external tools.
type RawSchemaProvider interface {
	JSONSchema() map[string]any
}

// DefaultCapabilities intentionally treats unknown external tools as side-effecting.
func DefaultCapabilities(source Source) Capabilities {
	caps := Capabilities{
		SideEffectScope: "external",
		RiskLevel:       "high",
		NeedsApproval:   true,
		Timeout:         2 * time.Minute,
		MaxOutputBytes:  100_000,
	}
	if source == SourceBuiltin {
		caps.SideEffectScope = "workspace"
		caps.RiskLevel = "medium"
	}
	return caps
}

// ExecutionInfo correlates a tool invocation with its durable run.
type ExecutionInfo struct {
	ConversationID   string
	RunID            string
	RootRunID        string
	ParentRunID      string
	TraceID          string
	ToolCallID       string
	Interactive      bool
	Mode             string
	ReadOnly         bool
	Discovery        *DiscoveryState
	Emit             func(model.StreamEvent) error
	OnStatus         func(model.RunStatus)
	RecordFileChange func(telemetry.FileChange, string) error
}

type executionInfoKey struct{}

// WithExecutionInfo attaches run correlation to a tool context.
func WithExecutionInfo(ctx context.Context, info ExecutionInfo) context.Context {
	return context.WithValue(ctx, executionInfoKey{}, info)
}

// ExecutionInfoFromContext returns the current invocation correlation.
func ExecutionInfoFromContext(ctx context.Context) (ExecutionInfo, bool) {
	info, ok := ctx.Value(executionInfoKey{}).(ExecutionInfo)
	return info, ok
}
