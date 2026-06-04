package prompt

import (
	"agent-core/internal/model"
	"agent-core/internal/store/vector"
	"strings"
)

// Builder constructs the system prompt from layered components.
type Builder struct {
	basePrompt string
}

// NewBuilder creates a new prompt builder with a base system prompt.
func NewBuilder() *Builder {
	return &Builder{
		basePrompt: defaultBasePrompt,
	}
}

// Build constructs the full system prompt with context.
func (b *Builder) Build(soulContent string, memories []vector.SearchResult, toolNames []string) string {
	var sb strings.Builder

	// Base prompt
	sb.WriteString(b.basePrompt)

	// Soul (agent guidelines from SOUL.md)
	if soulContent != "" {
		sb.WriteString("\n\n## Agent Guidelines\n")
		sb.WriteString(soulContent)
	}

	// Memory context
	if len(memories) > 0 {
		sb.WriteString("\n\n## Relevant Memories\n")
		sb.WriteString("The following memories may be relevant to the current conversation:\n")
		for i, m := range memories {
			if i >= 10 {
				break // Limit memory context
			}
			sb.WriteString("- ")
			sb.WriteString(m.Content)
			sb.WriteString("\n")
		}
	}

	// Available tools
	if len(toolNames) > 0 {
		sb.WriteString("\n\n## Available Tools\n")
		sb.WriteString("You have access to the following tools: ")
		sb.WriteString(strings.Join(toolNames, ", "))
		sb.WriteString("\nUse these tools when they can help answer the user's request.")
	}

	return sb.String()
}

// BuildSystemMessage creates the system message for the conversation.
func (b *Builder) BuildSystemMessage(soulContent string, memories []vector.SearchResult, toolNames []string) model.Message {
	return model.Message{
		Role:    model.RoleSystem,
		Content: b.Build(soulContent, memories, toolNames),
	}
}

const defaultBasePrompt = `You are a helpful AI assistant powered by Agent Core. You are intelligent, accurate, and helpful.

Guidelines:
- Be concise and direct in your responses
- Use tools when they can help provide better answers
- If you're unsure about something, say so
- When using tools, explain what you're doing and why
- Format code blocks with appropriate language tags
- If a task requires multiple steps, plan them out before executing`
