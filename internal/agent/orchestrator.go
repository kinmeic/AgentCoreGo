package agent

import (
	"agent-core/internal/compressor"
	"agent-core/internal/config"
	"agent-core/internal/llm"
	"agent-core/internal/memory"
	"agent-core/internal/model"
	"agent-core/internal/prompt"
	"agent-core/internal/sandbox"
	"agent-core/internal/store"
	"agent-core/internal/store/vector"
	"agent-core/internal/tool"
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Orchestrator is the core agent loop that manages conversations.
type Orchestrator struct {
	llmClient     *llm.Client
	toolRegistry  *tool.Registry
	store         *store.SQLite
	memService    *memory.Service
	comp          *compressor.Compressor
	promptBuilder *prompt.Builder
	cfg           config.Config

	// Per-conversation interrupt control
	mu         sync.RWMutex
	activeRuns map[string]context.CancelFunc
}

// NewOrchestrator creates a new agent orchestrator.
func NewOrchestrator(
	llmClient *llm.Client,
	registry *tool.Registry,
	store *store.SQLite,
	memService *memory.Service,
	comp *compressor.Compressor,
	promptBuilder *prompt.Builder,
	cfg config.Config,
) *Orchestrator {
	return &Orchestrator{
		llmClient:     llmClient,
		toolRegistry:  registry,
		store:         store,
		memService:    memService,
		comp:          comp,
		promptBuilder: promptBuilder,
		cfg:           cfg,
		activeRuns:    make(map[string]context.CancelFunc),
	}
}

// ChatSync handles a synchronous chat request.
func (o *Orchestrator) ChatSync(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	convID := req.ConversationID
	if convID == "" {
		convID = uuid.New().String()
		if err := o.ensureConversation(convID, req.Source, req.Metadata); err != nil {
			return nil, err
		}
	}

	// Register this run for interruption
	runCtx, cancel := context.WithCancel(ctx)
	o.registerRun(convID, cancel)
	defer o.unregisterRun(convID)

	// Log user message
	userMsg := model.Message{Role: model.RoleUser, Content: req.Message}
	o.store.AddLog(&model.ConversationLog{
		ConversationID: convID,
		Role:           model.RoleUser,
		Content:        req.Message,
		CreatedAt:      time.Now(),
	})

	// Build messages and run agent loop
	messages, err := o.buildMessages(runCtx, convID, userMsg)
	if err != nil {
		return nil, err
	}

	reply, intent, actions, usage, err := o.runLoop(runCtx, messages)
	if err != nil {
		return nil, err
	}

	// Log assistant response
	o.store.AddLog(&model.ConversationLog{
		ConversationID:   convID,
		Role:             model.RoleAssistant,
		Content:          reply,
		Intent:           intent,
		Actions:          actions,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		ContextTokens:    usage.TotalTokens,
		CreatedAt:        time.Now(),
	})

	// Auto-title conversation
	o.autoTitle(convID, req.Message)

	return &model.ChatResponse{
		ConversationID: convID,
		Reply:          reply,
		Intent:         intent,
		Actions:        actions,
		Usage:          usage,
		CreatedAt:      time.Now(),
	}, nil
}

// ChatStream handles a streaming chat request, sending events to the channel.
func (o *Orchestrator) ChatStream(ctx context.Context, req model.ChatRequest, events chan<- model.StreamEvent) {
	defer close(events)

	convID := req.ConversationID
	if convID == "" {
		convID = uuid.New().String()
		if err := o.ensureConversation(convID, req.Source, req.Metadata); err != nil {
			events <- model.StreamEvent{Type: "error", Message: err.Error()}
			return
		}
	}

	// Send conversation ID
	events <- model.StreamEvent{Type: "conv_id", ConversationID: convID}

	// Register this run for interruption
	runCtx, cancel := context.WithCancel(ctx)
	o.registerRun(convID, cancel)
	defer o.unregisterRun(convID)

	// Log user message
	userMsg := model.Message{Role: model.RoleUser, Content: req.Message}
	o.store.AddLog(&model.ConversationLog{
		ConversationID: convID,
		Role:           model.RoleUser,
		Content:        req.Message,
		CreatedAt:      time.Now(),
	})

	// Build messages
	messages, err := o.buildMessages(runCtx, convID, userMsg)
	if err != nil {
		events <- model.StreamEvent{Type: "error", Message: err.Error()}
		return
	}

	// Run the streaming agent loop
	reply, intent, actions, usage, err := o.runStreamLoop(runCtx, messages, events)
	if err != nil {
		events <- model.StreamEvent{Type: "error", Message: err.Error()}
		return
	}

	// Log assistant response
	o.store.AddLog(&model.ConversationLog{
		ConversationID:   convID,
		Role:             model.RoleAssistant,
		Content:          reply,
		Intent:           intent,
		Actions:          actions,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		ContextTokens:    usage.TotalTokens,
		CreatedAt:        time.Now(),
	})

	// Send done event
	events <- model.StreamEvent{
		Type:    "done",
		Intent:  intent,
		Actions: actions,
		Reply:   reply,
		Usage:   usage,
	}

	// Auto-title conversation
	o.autoTitle(convID, req.Message)
}

// Stop interrupts a running conversation.
func (o *Orchestrator) Stop(conversationID string) bool {
	o.mu.RLock()
	cancel, ok := o.activeRuns[conversationID]
	o.mu.RUnlock()

	if ok {
		cancel()
		return true
	}
	return false
}

// --- Internal methods ---

func (o *Orchestrator) registerRun(convID string, cancel context.CancelFunc) {
	o.mu.Lock()
	defer o.mu.Unlock()
	// Cancel any existing run for this conversation
	if existing, ok := o.activeRuns[convID]; ok {
		existing()
	}
	o.activeRuns[convID] = cancel
}

func (o *Orchestrator) unregisterRun(convID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.activeRuns, convID)
}

func (o *Orchestrator) ensureConversation(id, source string, metadata map[string]string) error {
	existing, err := o.store.GetConversation(id)
	if err != nil {
		return err
	}
	if existing != nil {
		return nil
	}

	return o.store.CreateConversation(&model.Conversation{
		ID:        id,
		Title:     "",
		Source:    source,
		Metadata:  metadata,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
}

// buildMessages constructs the full message list for the LLM.
func (o *Orchestrator) buildMessages(ctx context.Context, convID string, userMsg model.Message) ([]model.Message, error) {
	// Load conversation history
	logs, err := o.store.GetLogs(convID, 100)
	if err != nil {
		return nil, fmt.Errorf("load conversation logs: %w", err)
	}

	// Convert logs to messages
	var messages []model.Message

	// Add system prompt with memories
	var memResults []vector.SearchResult
	if o.memService != nil {
		memResults, _ = o.memService.Search(ctx, userMsg.Content, 5)
	}
	toolNames := o.toolRegistry.Names()

	// Read SOUL.md on every request (no caching, graceful degradation)
	soulContent, _ := sandbox.ReadSoulFile(o.cfg.Workspace.Path, o.cfg.Workspace.SoulFile)

	systemMsg := o.promptBuilder.BuildSystemMessage(soulContent, memResults, toolNames)
	messages = append(messages, systemMsg)

	// Add history
	for _, l := range logs {
		messages = append(messages, model.Message{
			Role:    l.Role,
			Content: l.Content,
		})
	}

	// Add current user message
	messages = append(messages, userMsg)

	// Compress if needed
	userCount, assistantCount := 0, 0
	for _, m := range messages {
		switch m.Role {
		case model.RoleUser:
			userCount++
		case model.RoleAssistant:
			assistantCount++
		}
	}
	result := o.comp.Compress(len(messages), userCount, assistantCount)
	if result.ShouldCompress {
		compressed := make([]model.Message, 0, result.KeepFirst+1+result.KeepLast)
		compressed = append(compressed, messages[:result.KeepFirst]...)
		compressed = append(compressed, model.Message{
			Role:    model.RoleSystem,
			Content: result.Summary,
		})
		compressed = append(compressed, messages[len(messages)-result.KeepLast:]...)
		messages = compressed
	}

	return messages, nil
}

// runLoop executes the non-streaming agent loop (for sync chat).
func (o *Orchestrator) runLoop(ctx context.Context, messages []model.Message) (reply, intent string, actions []model.Action, usage *model.UsageInfo, err error) {
	tools := o.toolRegistry.ToolDefinitions()
	totalUsage := &model.UsageInfo{}

	for iteration := 0; iteration < o.cfg.LLM.MaxToolIterations; iteration++ {
		select {
		case <-ctx.Done():
			return reply, model.IntentChat, actions, totalUsage, ctx.Err()
		default:
		}

		resp, usageInfo, err := o.llmClient.Complete(ctx, messages, tools)
		if err != nil {
			return "", model.IntentChat, actions, totalUsage, fmt.Errorf("LLM call failed: %w", err)
		}

		totalUsage.PromptTokens += usageInfo.PromptTokens
		totalUsage.CompletionTokens += usageInfo.CompletionTokens
		totalUsage.TotalTokens += usageInfo.TotalTokens

		// No tool calls - we have our final response
		if len(resp.ToolCalls) == 0 {
			reply = resp.Content
			intent = classifyIntent(reply)
			return reply, intent, actions, totalUsage, nil
		}

		// Add assistant message with tool calls to history
		messages = append(messages, *resp)

		// Execute each tool call
		for _, tc := range resp.ToolCalls {
			args, parseErr := tool.UnmarshalArgs(tc.Function.Arguments)
			if parseErr != nil {
				args = map[string]any{}
			}

			result, execErr := o.toolRegistry.Dispatch(ctx, tc.Function.Name, args)
			if execErr != nil {
				result = fmt.Sprintf("Error: %v", execErr)
			}

			actions = append(actions, model.Action{
				Tool:   tc.Function.Name,
				Status: statusFromErr(execErr),
				Result: truncateStr(result, 200),
			})

			// Add tool result to messages
			messages = append(messages, model.Message{
				Role:       model.RoleTool,
				Content:    result,
				ToolCallID: tc.ID,
			})
		}
	}

	return reply, intent, actions, totalUsage, fmt.Errorf("max tool iterations (%d) exceeded", o.cfg.LLM.MaxToolIterations)
}

// runStreamLoop executes the streaming agent loop.
func (o *Orchestrator) runStreamLoop(ctx context.Context, messages []model.Message, events chan<- model.StreamEvent) (reply, intent string, actions []model.Action, usage *model.UsageInfo, err error) {
	tools := o.toolRegistry.ToolDefinitions()
	totalUsage := &model.UsageInfo{}

	for iteration := 0; iteration < o.cfg.LLM.MaxToolIterations; iteration++ {
		select {
		case <-ctx.Done():
			return reply, model.IntentChat, actions, totalUsage, ctx.Err()
		default:
		}

		events <- model.StreamEvent{Type: "thinking", Iteration: iteration + 1}

		chunks, err := o.llmClient.CompleteStream(ctx, messages, tools)
		if err != nil {
			return "", model.IntentChat, actions, totalUsage, fmt.Errorf("LLM stream failed: %w", err)
		}

		// Collect the full response from stream chunks
		var contentBuilder strings.Builder
		var toolCallCollector = newToolCallCollector()
		var streamUsage *model.UsageInfo

		for chunk := range chunks {
			if chunk.Err != nil {
				log.Printf("[agent] Stream error: %v", chunk.Err)
				continue
			}

			// Content chunk
			if chunk.Content != "" {
				contentBuilder.WriteString(chunk.Content)
				events <- model.StreamEvent{Type: "chunk", Content: chunk.Content}
			}

			// Tool call deltas
			if len(chunk.ToolCalls) > 0 {
				toolCallCollector.add(chunk.ToolCalls)
			}

			// Usage
			if chunk.Usage != nil {
				streamUsage = &model.UsageInfo{
					PromptTokens:     chunk.Usage.PromptTokens,
					CompletionTokens: chunk.Usage.CompletionTokens,
					TotalTokens:      chunk.Usage.TotalTokens,
				}
			}
		}

		if streamUsage != nil {
			totalUsage.PromptTokens += streamUsage.PromptTokens
			totalUsage.CompletionTokens += streamUsage.CompletionTokens
			totalUsage.TotalTokens += streamUsage.TotalTokens
		}

		// Build the complete assistant message
		toolCalls := toolCallCollector.build()
		reply = contentBuilder.String()

		// No tool calls - we have our final response
		if len(toolCalls) == 0 {
			intent = classifyIntent(reply)
			return reply, intent, actions, totalUsage, nil
		}

		// Emit tool_call event
		tNames := make([]string, len(toolCalls))
		for i, tc := range toolCalls {
			tNames[i] = tc.Function.Name
		}
		events <- model.StreamEvent{Type: "tool_call", Tools: tNames}

		// Add assistant message with tool calls
		assistantMsg := model.Message{
			Role:      model.RoleAssistant,
			Content:   reply,
			ToolCalls: toolCalls,
		}
		messages = append(messages, assistantMsg)

		// Execute tools
		for _, tc := range toolCalls {
			events <- model.StreamEvent{
				Type:     "tool.started",
				ToolName: tc.Function.Name,
				Preview:  truncateStr(tc.Function.Arguments, 100),
			}

			args, parseErr := tool.UnmarshalArgs(tc.Function.Arguments)
			if parseErr != nil {
				args = map[string]any{}
			}

			result, execErr := o.toolRegistry.Dispatch(ctx, tc.Function.Name, args)
			if execErr != nil {
				result = fmt.Sprintf("Error: %v", execErr)
			}

			actions = append(actions, model.Action{
				Tool:   tc.Function.Name,
				Status: statusFromErr(execErr),
				Result: truncateStr(result, 200),
			})

			events <- model.StreamEvent{
				Type:    "progress",
				Summary: truncateStr(result, 200),
			}

			messages = append(messages, model.Message{
				Role:       model.RoleTool,
				Content:    result,
				ToolCallID: tc.ID,
			})
		}
	}

	return reply, intent, actions, totalUsage, fmt.Errorf("max tool iterations (%d) exceeded", o.cfg.LLM.MaxToolIterations)
}

// autoTitle generates a title for new conversations.
func (o *Orchestrator) autoTitle(convID, firstMessage string) {
	conv, err := o.store.GetConversation(convID)
	if err != nil || conv == nil || conv.Title != "" {
		return
	}

	title := firstMessage
	if len(title) > 50 {
		title = title[:50] + "..."
	}
	o.store.UpdateConversationTitle(convID, title)
}

// --- Helpers ---

func classifyIntent(reply string) string {
	lower := strings.ToLower(reply)
	if strings.Contains(lower, "i've") || strings.Contains(lower, "done") || strings.Contains(lower, "completed") {
		return model.IntentAction
	}
	return model.IntentQuery
}

func statusFromErr(err error) string {
	if err != nil {
		return "error"
	}
	return "success"
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// toolCallCollector accumulates streaming tool call deltas into complete tool calls.
type toolCallCollector struct {
	calls map[int]*toolCallBuilder
}

type toolCallBuilder struct {
	index     int
	id        string
	typ       string
	name      string
	arguments strings.Builder
}

func newToolCallCollector() *toolCallCollector {
	return &toolCallCollector{
		calls: make(map[int]*toolCallBuilder),
	}
}

func (c *toolCallCollector) add(deltas []llm.ToolCallDelta) {
	for _, d := range deltas {
		b, ok := c.calls[d.Index]
		if !ok {
			b = &toolCallBuilder{index: d.Index}
			c.calls[d.Index] = b
		}

		if d.ID != "" {
			b.id = d.ID
		}
		if d.Type != "" {
			b.typ = d.Type
		}
		if d.Name != "" {
			b.name = d.Name
		}
		if d.Arguments != "" {
			b.arguments.WriteString(d.Arguments)
		}
	}
}

func (c *toolCallCollector) build() []model.ToolCall {
	if len(c.calls) == 0 {
		return nil
	}

	result := make([]model.ToolCall, 0, len(c.calls))
	for _, b := range c.calls {
		if b.id == "" {
			continue
		}
		result = append(result, model.ToolCall{
			ID:   b.id,
			Type: "function",
			Function: model.ToolFunction{
				Name:      b.name,
				Arguments: b.arguments.String(),
			},
		})
	}
	return result
}
