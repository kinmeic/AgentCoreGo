package model

import "time"

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
	ToolCallID string     `json:"tool_call_id,omitempty"`
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
	Type           string     `json:"type"`
	ConversationID string    `json:"conversation_id,omitempty"`
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
	Tool   string `json:"tool"`
	Status string `json:"status"`
	Result string `json:"result,omitempty"`
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
	Reply          string     `json:"reply"`
	Intent         string     `json:"intent"`
	Actions        []Action   `json:"actions"`
	Usage          *UsageInfo `json:"usage,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
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
	ID               int64     `json:"id"`
	ConversationID   string    `json:"conversation_id"`
	Role             string    `json:"role"`
	Content          string    `json:"content"`
	Intent           string    `json:"intent,omitempty"`
	Actions          []Action  `json:"actions,omitempty"`
	PromptTokens     int       `json:"prompt_tokens,omitempty"`
	CompletionTokens int       `json:"completion_tokens,omitempty"`
	ContextTokens    int       `json:"context_tokens,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
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
