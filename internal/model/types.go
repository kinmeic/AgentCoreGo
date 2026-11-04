package model

import (
	"encoding/json"
	"time"
)

// Role constants for messages
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleSystem    = "system"
	RoleTool      = "tool"
)

// Intent constants for conversation analysis
const (
	IntentQuery   = "query"
	IntentAction  = "action"
	IntentChat    = "chat"
	IntentComplex = "complex"
)

// Message represents a single message in a conversation.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID    string          `json:"tool_call_id,omitempty"`
	IsError       bool            `json:"is_error,omitempty"`
	ProviderState json.RawMessage `json:"provider_state,omitempty"`
}

// ToolCall represents an LLM-generated tool call.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction represents the function details within a tool call.
type ToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// MemorySearchResult is a local text-retrieval result, not a vector/embedding.
type MemorySearchResult struct {
	SourceType     string  `json:"source_type"`
	SourceID       string  `json:"source_id"`
	Content        string  `json:"content"`
	ContentSummary string  `json:"content_summary"`
	Score          float32 `json:"score"`
}

// ToolResult represents the result of executing a tool call.
type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	Content    string `json:"content"`
	IsError    bool   `json:"is_error,omitempty"`
}

// ToolDefinition describes a tool available to the LLM.
type ToolDefinition struct {
	Type     string  `json:"type"`
	Function FuncDef `json:"function"`
}

// FuncDef describes a function callable by the LLM.
type FuncDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// StreamEvent represents a server-sent event during streaming.
type StreamEvent struct {
	SchemaVersion  int        `json:"schema_version,omitempty"`
	EventID        string     `json:"event_id,omitempty"`
	Sequence       uint64     `json:"sequence,omitempty"`
	RunID          string     `json:"run_id,omitempty"`
	ToolCallID     string     `json:"tool_call_id,omitempty"`
	RequestID      string     `json:"request_id,omitempty"`
	Timestamp      time.Time  `json:"timestamp,omitempty"`
	ErrorCode      string     `json:"error_code,omitempty"`
	Type           string     `json:"type"`
	ConversationID string     `json:"conversation_id,omitempty"`
	Content        string     `json:"content,omitempty"`
	Tools          []string   `json:"tools,omitempty"`
	ToolName       string     `json:"tool,omitempty"`
	Preview        string     `json:"preview,omitempty"`
	Summary        string     `json:"summary,omitempty"`
	Iteration      int        `json:"iteration,omitempty"`
	Intent         string     `json:"intent,omitempty"`
	Actions        []Action   `json:"actions,omitempty"`
	Reply          string     `json:"reply,omitempty"`
	Usage          *UsageInfo `json:"usage,omitempty"`
	Message        string     `json:"message,omitempty"`

	// Clarify fields (emitted when type == "clarify")
	ClarifyKind    string   `json:"clarify_kind,omitempty"`
	ClarifyMessage string   `json:"clarify_message,omitempty"`
	ClarifyOptions []string `json:"clarify_options,omitempty"`
}

// Action represents a recorded tool action taken during the conversation.
type Action struct {
	RunID      string `json:"run_id,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Tool       string `json:"tool"`
	Status     string `json:"status"`
	Result     string `json:"result,omitempty"`
}

// UsageInfo tracks token usage for a conversation turn.
type UsageInfo struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatRequest represents an incoming chat request.
type ChatRequest struct {
	ConversationID string            `json:"conversation_id,omitempty"`
	Message        string            `json:"message"`
	Source         string            `json:"source,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

// ChatResponse represents a synchronous chat response.
type ChatResponse struct {
	ConversationID string     `json:"conversation_id"`
	RunID          string     `json:"run_id,omitempty"`
	Status         RunStatus  `json:"status,omitempty"`
	Reply          string     `json:"reply"`
	Intent         string     `json:"intent"`
	Actions        []Action   `json:"actions"`
	Usage          *UsageInfo `json:"usage,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

// RunStatus is the persisted lifecycle state of one agent execution.
type RunStatus string

const (
	RunStatusPending         RunStatus = "pending"
	RunStatusRunning         RunStatus = "running"
	RunStatusWaitingClarify  RunStatus = "waiting_clarification"
	RunStatusWaitingApproval RunStatus = "waiting_approval"
	RunStatusCompleted       RunStatus = "completed"
	RunStatusFailed          RunStatus = "failed"
	RunStatusCancelled       RunStatus = "cancelled"
)

// RunKind identifies the execution role within a task tree.
type RunKind string

const (
	RunKindRoot         RunKind = "root"
	RunKindSubagent     RunKind = "subagent"
	RunKindWorkflowNode RunKind = "workflow_node"
)

// RunSpec is the immutable execution manifest used by replay and resume.
type RunSpec struct {
	Workspace      string  `json:"workspace"`
	ConfigDigest   string  `json:"config_digest,omitempty"`
	TrustDigest    string  `json:"trust_digest,omitempty"`
	ToolsDigest    string  `json:"tools_digest,omitempty"`
	SkillsDigest   string  `json:"skills_digest,omitempty"`
	CommandsDigest string  `json:"commands_digest,omitempty"`
	WorkflowDigest string  `json:"workflow_digest,omitempty"`
	Model          string  `json:"model,omitempty"`
	Effort         string  `json:"effort,omitempty"`
	Mode           string  `json:"mode,omitempty"`
	ReadOnly       bool    `json:"read_only,omitempty"`
	TraceID        string  `json:"trace_id"`
	RootRunID      string  `json:"root_run_id"`
	ParentRunID    string  `json:"parent_run_id,omitempty"`
	Depth          int     `json:"depth"`
	Kind           RunKind `json:"kind"`
}

// Run is the durable execution record used by stop, resume and diagnostics APIs.
type Run struct {
	ID             string            `json:"id"`
	ConversationID string            `json:"conversation_id"`
	Spec           RunSpec           `json:"spec"`
	Status         RunStatus         `json:"status"`
	Input          string            `json:"input"`
	Source         string            `json:"source,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	Iteration      int               `json:"iteration"`
	Reply          string            `json:"reply,omitempty"`
	Error          string            `json:"error,omitempty"`
	Usage          *UsageInfo        `json:"usage,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
	FinishedAt     *time.Time        `json:"finished_at,omitempty"`
	ProviderState  json.RawMessage   `json:"-"`
}

// RunEvent is the durable form of a stream event.
type RunEvent struct {
	EventID        string          `json:"event_id"`
	RunID          string          `json:"run_id"`
	RootRunID      string          `json:"root_run_id,omitempty"`
	ParentRunID    string          `json:"parent_run_id,omitempty"`
	TraceID        string          `json:"trace_id,omitempty"`
	ConversationID string          `json:"conversation_id"`
	Sequence       uint64          `json:"sequence"`
	Type           string          `json:"type"`
	Payload        json.RawMessage `json:"payload"`
	CreatedAt      time.Time       `json:"created_at"`
}

// Conversation represents a conversation with metadata.
type Conversation struct {
	ID         string            `json:"id"`
	Title      string            `json:"title"`
	Source     string            `json:"source,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
	ArchivedAt *time.Time        `json:"archived_at,omitempty"`
}

// ConversationLog represents a single log entry in a conversation.
type ConversationLog struct {
	ID               int64      `json:"id"`
	ConversationID   string     `json:"conversation_id"`
	RunID            string     `json:"run_id,omitempty"`
	Role             string     `json:"role"`
	Content          string     `json:"content"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	Status           string     `json:"status,omitempty"`
	Intent           string     `json:"intent,omitempty"`
	Actions         []Action        `json:"actions,omitempty"`
	ProviderState   json.RawMessage `json:"-"`
	PromptTokens     int        `json:"prompt_tokens,omitempty"`
	CompletionTokens int        `json:"completion_tokens,omitempty"`
	ContextTokens    int        `json:"context_tokens,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
}

// Memory represents an agent memory entry.
type Memory struct {
	ID         int64     `json:"id"`
	Content    string    `json:"content"`
	Category   string    `json:"category"`
	Source     string    `json:"source"`
	Importance float64   `json:"importance"`
	Enabled    bool      `json:"enabled"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// MCPServer represents a configured MCP server.
type MCPServer struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Config    string `json:"config"`
	Enabled   bool   `json:"enabled"`
}

// HostCallback is the durable configuration for a host-provided tool.
type HostCallback struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Schema       json.RawMessage `json:"schema"`
	CallbackURL  string          `json:"callback_url"`
	Secret       string          `json:"secret,omitempty"`
	AllowPrivate bool            `json:"allow_private"`
	Enabled      bool            `json:"enabled"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}
