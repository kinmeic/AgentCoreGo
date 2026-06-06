package compressor

import (
	"agent-core/internal/model"
	"strings"
)

// MarkImportance returns a boolean slice indicating which messages should be
// preserved during compression. Important messages include:
// - Error messages and tool failures
// - User messages with explicit instructions
// - Assistant messages with significant reasoning
// - Recent messages (last N are always important)
// - Tool results that correspond to important assistant tool_calls (pairing)
func MarkImportance(messages []model.Message) []bool {
	marks := make([]bool, len(messages))
	for i, m := range messages {
		marks[i] = isImportant(m, i, len(messages))
	}

	// Post-processing: propagate importance from assistant tool_call messages
	// to their corresponding tool result messages. This ensures the LLM API
	// always sees valid tool_call/tool_result pairs.
	propagateToolCallImportance(messages, marks)

	return marks
}

// propagateToolCallImportance ensures that tool result messages following
// an important assistant message with tool_calls are also marked important.
// Without this, compression can produce invalid LLM API sequences where
// an assistant.tool_calls references a tool_call ID with no matching role=tool message.
func propagateToolCallImportance(messages []model.Message, marks []bool) {
	// Build a set of tool_call IDs from important assistant messages
	importantToolCallIDs := make(map[string]bool)
	for i, m := range messages {
		if marks[i] && m.Role == model.RoleAssistant && len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				importantToolCallIDs[tc.ID] = true
			}
		}
	}

	// Mark tool results that correspond to important tool_calls
	for i, m := range messages {
		if !marks[i] && m.Role == model.RoleTool && m.ToolCallID != "" {
			if importantToolCallIDs[m.ToolCallID] {
				marks[i] = true
			}
		}
	}
}

func isImportant(m model.Message, index, total int) bool {
	// System messages are always important
	if m.Role == model.RoleSystem {
		return true
	}

	// Recent messages (last 20%) are always important
	recentThreshold := total - total/5
	if index >= recentThreshold {
		return true
	}

	switch m.Role {
	case model.RoleUser:
		return isImportantUserMessage(m.Content)
	case model.RoleAssistant:
		return isImportantAssistantMessage(m)
	case model.RoleTool:
		return isImportantToolResult(m.Content)
	}
	return false
}

func isImportantUserMessage(content string) bool {
	lower := strings.ToLower(content)
	// Explicit instructions / corrections
	instructionPatterns := []string{
		"don't", "do not", "stop", "always", "never",
		"remember", "important", "note that", "make sure",
		"must", "require", "constraint",
	}
	for _, p := range instructionPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	// Short messages with directives (questions, exclamations) are important
	if len(content) < 100 && (strings.Contains(content, "?") || strings.Contains(content, "!")) {
		return true
	}
	return false
}

func isImportantAssistantMessage(m model.Message) bool {
	// Messages with tool calls are important (they represent decisions)
	if len(m.ToolCalls) > 0 {
		return true
	}
	// Messages containing errors or warnings
	lower := strings.ToLower(m.Content)
	errorPatterns := []string{"error", "failed", "warning", "issue", "problem"}
	for _, p := range errorPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

func isImportantToolResult(content string) bool {
	lower := strings.ToLower(content)
	// Error results are always important
	errorPatterns := []string{
		"error:", "failed", "permission denied", "not found",
		"timeout", "refused", "fatal",
	}
	for _, p := range errorPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	// Very short results are likely errors or confirmations
	if len(content) < 50 {
		return true
	}
	return false
}
