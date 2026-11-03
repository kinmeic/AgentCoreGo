// Package telemetry captures model wire I/O and execution facts as an
// append-only, exportable trace. Raw conversation logs are deliberately not
// used as a substitute for provider-boundary records.
package telemetry

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

const (
	// SchemaVersion is the version of the on-disk model-io and trace format.
	SchemaVersion = 1

	// RedactionMarker replaces credentials and other sensitive values.
	RedactionMarker = "[REDACTED]"
)

// CapturePolicy controls wire fidelity. Redacted capture is suitable for
// diagnostics; training capture retains complete bodies except credentials in
// HTTP headers, which are always redacted.
type CapturePolicy string

const (
	CaptureRedacted CapturePolicy = "redacted"
	CaptureTraining CapturePolicy = "training"
)

// Fidelity describes the information available in a record or export.
type Fidelity string

const (
	FidelityFullWire Fidelity = "full_wire"
	FidelityRedacted Fidelity = "redacted"
	FidelityPartial  Fidelity = "partial"
)

// Purpose identifies why a physical model request was made.
type Purpose string

const (
	PurposeMain      Purpose = "main"
	PurposeCompact   Purpose = "compact"
	PurposeTitle     Purpose = "title"
	PurposeSubagent  Purpose = "subagent"
	PurposeWorkflow  Purpose = "workflow"
	PurposeEmbedding Purpose = "embedding"
)

// Valid reports whether p is a supported model-call purpose.
func (p Purpose) Valid() bool {
	switch p {
	case PurposeMain, PurposeCompact, PurposeTitle, PurposeSubagent, PurposeWorkflow, PurposeEmbedding:
		return true
	default:
		return false
	}
}

// ModelCallMeta identifies one physical provider attempt. Attempt is one-based.
// RootID defaults to RunID and TraceID defaults to RootID when omitted.
// RequestID is never synthesized: it must correlate with the provider/HTTP request.
type ModelCallMeta struct {
	CallID          string      `json:"call_id,omitempty"`
	TraceID         string      `json:"trace_id,omitempty"`
	RootID          string      `json:"root_id,omitempty"`
	ParentID        string      `json:"parent_id,omitempty"`
	RunID           string      `json:"run_id"`
	RequestID       string      `json:"request_id"`
	Purpose         Purpose     `json:"purpose"`
	Attempt         int         `json:"attempt"`
	Provider        string      `json:"provider"`
	Model           string      `json:"model"`
	RequestHeaders  http.Header `json:"request_headers,omitempty"`
	ResponseHeaders http.Header `json:"response_headers,omitempty"`
	StartedAt       time.Time   `json:"started_at,omitempty"`
}

// TokenUsage is provider-reported usage. A nil usage pointer means the provider
// did not return usage; zero values are not invented during export.
type TokenUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// CallResult contains facts known when a provider call finishes. Error should
// contain the provider/transport error, if any, without credentials.
type CallResult struct {
	FinishedAt   time.Time   `json:"finished_at,omitempty"`
	HTTPStatus   int         `json:"http_status,omitempty"`
	Usage        *TokenUsage `json:"usage,omitempty"`
	FinishReason string      `json:"finish_reason,omitempty"`
	Error        string      `json:"error,omitempty"`
}

// CaptureState records loss explicitly. CaptureComplete is false whenever an
// artifact was dropped or truncated, a required capture was absent, or a write
// failed. MissingFields explains known omissions without manufacturing values.
type CaptureState struct {
	Dropped         bool     `json:"dropped"`
	DroppedCount    uint64   `json:"dropped_count,omitempty"`
	Truncated       bool     `json:"truncated"`
	CaptureComplete bool     `json:"capture_complete"`
	MissingFields   []string `json:"missing_fields,omitempty"`
	CaptureError    string   `json:"capture_error,omitempty"`
}

// ModelCall is the durable metadata for one physical model request. RawRequest,
// RawResponse and RawStream refer to content-addressed artifacts rather than
// embedding raw model content in ordinary logs.
type ModelCall struct {
	SchemaVersion   int           `json:"schema_version"`
	CapturePolicy   CapturePolicy `json:"capture_policy"`
	Fidelity        Fidelity      `json:"fidelity"`
	CallID          string        `json:"call_id"`
	TraceID         string        `json:"trace_id"`
	RootID          string        `json:"root_id"`
	ParentID        string        `json:"parent_id,omitempty"`
	RunID           string        `json:"run_id"`
	RequestID       string        `json:"request_id"`
	Purpose         Purpose       `json:"purpose"`
	Attempt         int           `json:"attempt"`
	Provider        string        `json:"provider"`
	Model           string        `json:"model"`
	StartedAt       time.Time     `json:"started_at"`
	FinishedAt      time.Time     `json:"finished_at"`
	LatencyMS       int64         `json:"latency_ms"`
	RequestHeaders  http.Header   `json:"request_headers"`
	ResponseHeaders http.Header   `json:"response_headers,omitempty"`
	HTTPStatus      int           `json:"http_status,omitempty"`
	RawRequest      *ArtifactRef  `json:"raw_request,omitempty"`
	RawResponse     *ArtifactRef  `json:"raw_response,omitempty"`
	RawStream       *ArtifactRef  `json:"raw_stream,omitempty"`
	Usage           *TokenUsage   `json:"usage,omitempty"`
	FinishReason    string        `json:"finish_reason,omitempty"`
	Error           string        `json:"error,omitempty"`
	CaptureState
}

// ApprovalStatus is the recorded authorization decision for a tool call.
type ApprovalStatus string

const (
	ApprovalNotRequired ApprovalStatus = "not_required"
	ApprovalPending     ApprovalStatus = "pending"
	ApprovalApproved    ApprovalStatus = "approved"
	ApprovalDenied      ApprovalStatus = "denied"
)

// Valid reports whether s is a supported approval state.
func (s ApprovalStatus) Valid() bool {
	switch s {
	case ApprovalNotRequired, ApprovalPending, ApprovalApproved, ApprovalDenied:
		return true
	default:
		return false
	}
}

// ApprovalRecord preserves the decision and its provenance. It does not grant
// permissions by itself.
type ApprovalRecord struct {
	Required  bool           `json:"required"`
	Status    ApprovalStatus `json:"status"`
	Reason    string         `json:"reason,omitempty"`
	DecidedBy string         `json:"decided_by,omitempty"`
	DecidedAt *time.Time     `json:"decided_at,omitempty"`
}

// ToolStatus is the actual outcome of a tool invocation.
type ToolStatus string

const (
	ToolSucceeded ToolStatus = "succeeded"
	ToolFailed    ToolStatus = "failed"
	ToolCancelled ToolStatus = "cancelled"
	ToolDenied    ToolStatus = "denied"
)

// Valid reports whether s is a supported terminal tool status.
func (s ToolStatus) Valid() bool {
	switch s {
	case ToolSucceeded, ToolFailed, ToolCancelled, ToolDenied:
		return true
	default:
		return false
	}
}

// ToolInvocation is a structured execution fact. Input and output are JSON
// values; a nil input is missing, while json.RawMessage("null") is explicit.
// Output may be absent only for a non-successful invocation with an error.
type ToolInvocation struct {
	SchemaVersion int             `json:"schema_version"`
	CapturePolicy CapturePolicy   `json:"capture_policy"`
	InvocationID  string          `json:"invocation_id"`
	TraceID       string          `json:"trace_id"`
	RootID        string          `json:"root_id"`
	ParentID      string          `json:"parent_id,omitempty"`
	RunID         string          `json:"run_id"`
	ModelCallID   string          `json:"model_call_id,omitempty"`
	RequestID     string          `json:"request_id,omitempty"`
	ToolCallID    string          `json:"tool_call_id"`
	Name          string          `json:"name"`
	Input         json.RawMessage `json:"input"`
	Status        ToolStatus      `json:"status"`
	Capabilities  []string        `json:"capabilities"`
	Approval      ApprovalRecord  `json:"approval"`
	StartedAt     time.Time       `json:"started_at"`
	FinishedAt    time.Time       `json:"finished_at"`
	DurationMS    int64           `json:"duration_ms"`
	Output        json.RawMessage `json:"output,omitempty"`
	Error         string          `json:"error,omitempty"`
	CaptureState
}

// FileOperation describes a workspace mutation, not a claimed rollback.
type FileOperation string

const (
	FileCreate FileOperation = "create"
	FileUpdate FileOperation = "update"
	FileDelete FileOperation = "delete"
	FileRename FileOperation = "rename"
)

// Valid reports whether op is a supported file mutation.
func (op FileOperation) Valid() bool {
	switch op {
	case FileCreate, FileUpdate, FileDelete, FileRename:
		return true
	default:
		return false
	}
}

// FileChange captures first-before/final-after content and revision facts.
// AddedLines and DeletedLines are pointers so omitted counts fail validation
// instead of being silently exported as zero. Revision strings may contain a
// workspace revision, Git object ID, or another caller-defined stable revision.
type FileChange struct {
	SchemaVersion    int           `json:"schema_version"`
	CapturePolicy    CapturePolicy `json:"capture_policy"`
	ChangeID         string        `json:"change_id"`
	TraceID          string        `json:"trace_id"`
	RootID           string        `json:"root_id"`
	ParentID         string        `json:"parent_id,omitempty"`
	RunID            string        `json:"run_id"`
	ToolInvocationID string        `json:"tool_invocation_id,omitempty"`
	Path             string        `json:"path"`
	PreviousPath     string        `json:"previous_path,omitempty"`
	Operation        FileOperation `json:"operation"`
	FirstBefore      *ArtifactRef  `json:"first_before,omitempty"`
	FinalAfter       *ArtifactRef  `json:"final_after,omitempty"`
	DiffArtifact     *ArtifactRef  `json:"diff_artifact,omitempty"`
	BeforeRevision   string        `json:"before_revision,omitempty"`
	AfterRevision    string        `json:"after_revision,omitempty"`
	AddedLines       *int          `json:"added_lines"`
	DeletedLines     *int          `json:"deleted_lines"`
	ChangedAt        time.Time     `json:"changed_at"`
	CaptureState
}

var (
	// ErrClosed is returned when capture is attempted after Recorder.Close.
	ErrClosed = errors.New("telemetry: recorder is closed")
	// ErrMissingField identifies required trace data that was not captured.
	ErrMissingField = errors.New("telemetry: required field is missing")
	// ErrIncompleteCapture prevents partial training samples from being exported.
	ErrIncompleteCapture = errors.New("telemetry: capture is incomplete")
)

// MissingFieldError identifies the exact record and field that made validation
// or export fail.
type MissingFieldError struct {
	RecordType string
	RecordID   string
	Field      string
}

func (e *MissingFieldError) Error() string {
	if e.RecordID == "" {
		return fmt.Sprintf("%s: %s: %v", e.RecordType, e.Field, ErrMissingField)
	}
	return fmt.Sprintf("%s %q: %s: %v", e.RecordType, e.RecordID, e.Field, ErrMissingField)
}

// Unwrap supports errors.Is(err, ErrMissingField).
func (e *MissingFieldError) Unwrap() error { return ErrMissingField }

func missingField(recordType, recordID, field string) error {
	return &MissingFieldError{RecordType: recordType, RecordID: recordID, Field: field}
}
