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
	"agent-core/internal/tool/builtin"
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
	llmProvider     llm.Provider
	toolRegistry    *tool.Registry
	processRegistry *tool.ProcessRegistry
	store           *store.SQLite
	memService      *memory.Service
	comp            *compressor.Compressor
	promptBuilder   *prompt.Builder
	cfg             config.Config
	checkpointStore CheckpointStore

	// Per-conversation interrupt control
	mu         sync.RWMutex
	activeRuns map[string]context.CancelFunc

	// Per-conversation clarify response channels
	clarifyMu sync.RWMutex
	clarifyCh map[string]chan string
}

// NewOrchestrator creates a new agent orchestrator.
func NewOrchestrator(
	llmProvider llm.Provider,
	registry *tool.Registry,
	processRegistry *tool.ProcessRegistry,
	store *store.SQLite,
	memService *memory.Service,
	comp *compressor.Compressor,
	promptBuilder *prompt.Builder,
	cfg config.Config,
) *Orchestrator {
	return &Orchestrator{
		llmProvider:     llmProvider,
		toolRegistry:    registry,
		processRegistry: processRegistry,
		store:           store,
		memService:      memService,
		comp:            comp,
		promptBuilder:   promptBuilder,
		cfg:             cfg,
		activeRuns:      make(map[string]context.CancelFunc),
		clarifyCh:       make(map[string]chan string),
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

	// Build messages first (before logging user message, to avoid duplication
	// since buildMessages loads history from DB and appends userMsg)
	userMsg := model.Message{Role: model.RoleUser, Content: req.Message}
	messages, err := o.buildMessages(runCtx, convID, userMsg)
	if err != nil {
		return nil, err
	}

	// Now log user message (after buildMessages so it's not duplicated)
	o.store.AddLog(&model.ConversationLog{
		ConversationID: convID,
		Role:           model.RoleUser,
		Content:        req.Message,
		CreatedAt:      time.Now(),
	})

	reply, intent, actions, usage, err := o.runLoop(runCtx, convID, messages)
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

	// Build messages first (before logging user message, to avoid duplication)
	userMsg := model.Message{Role: model.RoleUser, Content: req.Message}
	messages, err := o.buildMessages(runCtx, convID, userMsg)
	if err != nil {
		events <- model.StreamEvent{Type: "error", Message: err.Error()}
		return
	}

	// Now log user message (after buildMessages so it's not duplicated)
	o.store.AddLog(&model.ConversationLog{
		ConversationID: convID,
		Role:           model.RoleUser,
		Content:        req.Message,
		CreatedAt:      time.Now(),
	})

	// Run the streaming agent loop
	reply, intent, actions, usage, err := o.runStreamLoop(runCtx, convID, messages, events)
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

// SetCheckpointStore sets the checkpoint persistence store.
func (o *Orchestrator) SetCheckpointStore(store CheckpointStore) {
	o.checkpointStore = store
}

// Stop interrupts a running conversation.
func (o *Orchestrator) Stop(conversationID string) bool {	o.mu.RLock()
	cancel, ok := o.activeRuns[conversationID]
	o.mu.RUnlock()

	if ok {
		cancel()
		// Kill all subprocesses for this conversation
		if o.processRegistry != nil {
			killed := o.processRegistry.KillByConversation(conversationID)
			if killed > 0 {
				log.Printf("[agent] Killed %d subprocess(es) for conversation %s", killed, conversationID)
			}
		}
		return true
	}
	return false
}

// StopAll interrupts all running conversations. Called during graceful shutdown.
func (o *Orchestrator) StopAll() {
	o.mu.Lock()
	defer o.mu.Unlock()

	count := len(o.activeRuns)
	for convID, cancel := range o.activeRuns {
		cancel()
		// Kill all subprocesses for this conversation
		if o.processRegistry != nil {
			o.processRegistry.KillByConversation(convID)
		}
		delete(o.activeRuns, convID)
	}

	if count > 0 {
		log.Printf("[agent] Stopped %d active conversation(s)", count)
	}
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

	// Compress if needed using the new intelligent compressor
	result := o.comp.Compress(ctx, messages, o.cfg.LLM.ContextWindow)
	if result.ShouldCompress {
		messages = result.Messages
		log.Printf("[agent] Context compressed (level=%d, tokens: %d→%d, summary=%v)",
			result.Level, result.TokenEstimate, result.TokenEstimate, result.SummaryUsed)
	}

	return messages, nil
}

// runLoop executes the non-streaming agent loop (for sync chat).
func (o *Orchestrator) runLoop(ctx context.Context, convID string, messages []model.Message) (reply, intent string, actions []model.Action, usage *model.UsageInfo, err error) {
	tools := o.toolRegistry.ToolDefinitions()
	totalUsage := &model.UsageInfo{}
	budget := NewIterationBudget(o.cfg.LLM.MaxToolIterations, 0)

	for !budget.Exhausted() {
		select {
		case <-ctx.Done():
			return reply, model.IntentChat, actions, totalUsage, ctx.Err()
		default:
		}

		resp, usageInfo, err := o.llmProvider.Complete(ctx, messages, tools)
		if err != nil {
			// Classify and log the error
			shouldContinue := classifyAndLog(err)
			if shouldContinue {
				// Context length exceeded — try recompressing and retrying
				budget.RecordIteration(0)
				if budget.Exhausted() {
					return "", model.IntentChat, actions, totalUsage, fmt.Errorf("%s", budget.ExceededMessage())
				}
				log.Printf("[agent] Attempting emergency compression after context length error")
				compressed := o.emergencyCompress(ctx, messages)
				if len(compressed) == len(messages) {
					return "", model.IntentChat, actions, totalUsage, fmt.Errorf("context length exceeded and compression failed to reduce messages")
				}
				messages = compressed
				continue
			}
			return "", model.IntentChat, actions, totalUsage, fmt.Errorf("LLM call failed: %w", err)
		}

		totalUsage.PromptTokens += usageInfo.PromptTokens
		totalUsage.CompletionTokens += usageInfo.CompletionTokens
		totalUsage.TotalTokens += usageInfo.TotalTokens
		budget.RecordIteration(usageInfo.TotalTokens)

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

			// Check for clarify request
			if builtin.IsClarifyRequest(result) {
				if clarifyReq, _ := builtin.ParseClarifyRequest(result); clarifyReq != nil {
					userResponse, ok := o.waitForClarify(ctx, convID, 5*time.Minute)
					if ok {
						result = fmt.Sprintf("User responded: %s", userResponse)
					} else {
						result = "User did not respond. Proceed with best judgment."
					}
				}
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

		// Save checkpoint after tool execution batch
		o.saveCheckpoint(convID, budget.CurrentIteration(), messages, actions, totalUsage)
	}

	return "", intent, actions, totalUsage, fmt.Errorf("%s", budget.ExceededMessage())
}

// runStreamLoop executes the streaming agent loop.
func (o *Orchestrator) runStreamLoop(ctx context.Context, convID string, messages []model.Message, events chan<- model.StreamEvent) (reply, intent string, actions []model.Action, usage *model.UsageInfo, err error) {
	tools := o.toolRegistry.ToolDefinitions()
	totalUsage := &model.UsageInfo{}
	budget := NewIterationBudget(o.cfg.LLM.MaxToolIterations, 0)

	for !budget.Exhausted() {
		select {
		case <-ctx.Done():
			return reply, model.IntentChat, actions, totalUsage, ctx.Err()
		default:
		}

		events <- model.StreamEvent{Type: "thinking", Iteration: budget.CurrentIteration() + 1}

		chunks, err := o.llmProvider.CompleteStream(ctx, messages, tools)
		if err != nil {
			// Classify and log the error
			shouldContinue := classifyAndLog(err)
			if shouldContinue {
				budget.RecordIteration(0)
				if budget.Exhausted() {
					return "", model.IntentChat, actions, totalUsage, fmt.Errorf("%s", budget.ExceededMessage())
				}
				log.Printf("[agent] Attempting emergency compression after context length error")
				compressed := o.emergencyCompress(ctx, messages)
				if len(compressed) == len(messages) {
					return "", model.IntentChat, actions, totalUsage, fmt.Errorf("context length exceeded and compression failed to reduce messages")
				}
				messages = compressed
				continue
			}
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

		var iterTokens int
		if streamUsage != nil {
			iterTokens = streamUsage.TotalTokens
			totalUsage.PromptTokens += streamUsage.PromptTokens
			totalUsage.CompletionTokens += streamUsage.CompletionTokens
			totalUsage.TotalTokens += streamUsage.TotalTokens
		}
		budget.RecordIteration(iterTokens)

		// Build the complete assistant message
		toolCalls := toolCallCollector.build()
		reply = contentBuilder.String()

		// Validate tool calls for completeness (stream may have been interrupted)
		if len(toolCalls) > 0 {
			valid := true
			for _, tc := range toolCalls {
				if tc.Function.Name == "" || tc.ID == "" {
					valid = false
					log.Printf("[agent] Incomplete tool call detected (name=%q, id=%q), treating as failed response", tc.Function.Name, tc.ID)
					break
				}
			}
			if !valid {
				// Don't add incomplete tool calls to history or execute them
				return reply, model.IntentChat, actions, totalUsage, fmt.Errorf("stream interrupted: incomplete tool calls")
			}
		}

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

			// Check for clarify request and emit SSE event
			if tc.Function.Name == "clarify" && builtin.IsClarifyRequest(result) {
				if clarifyReq, parseErr := builtin.ParseClarifyRequest(result); parseErr == nil && clarifyReq != nil {
					events <- model.StreamEvent{
						Type:           "clarify",
						ClarifyKind:    string(clarifyReq.Kind),
						ClarifyMessage: clarifyReq.Message,
						ClarifyOptions: clarifyReq.Options,
					}
					userResponse, ok := o.waitForClarify(ctx, convID, 5*time.Minute)
					if ok {
						result = fmt.Sprintf("User responded: %s", userResponse)
					} else {
						result = "User did not respond. Proceed with best judgment."
					}
				}
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

		// Save checkpoint after tool execution batch
		o.saveCheckpoint(convID, budget.CurrentIteration(), messages, actions, totalUsage)
	}

	return "", intent, actions, totalUsage, fmt.Errorf("%s", budget.ExceededMessage())
}

// autoTitle generates a title for new conversations.
// It attempts to use the LLM for a meaningful title, falling back to truncation.
func (o *Orchestrator) autoTitle(convID, firstMessage string) {
	conv, err := o.store.GetConversation(convID)
	if err != nil || conv == nil || conv.Title != "" {
		return
	}

	// Try LLM-generated title (async, non-blocking)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[agent] autoTitle panic recovered: %v", r)
			}
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		messages := []model.Message{
			{
				Role:    model.RoleUser,
				Content: "Generate a very short title (max 8 words, no quotes) for a conversation that starts with: " + firstMessage,
			},
		}
		resp, _, err := o.llmProvider.Complete(ctx, messages, nil)
		if err == nil && resp != nil && resp.Content != "" {
			title := strings.TrimSpace(resp.Content)
			// Safe UTF-8 truncation
			runes := []rune(title)
			if len(runes) > 80 {
				title = string(runes[:80])
			}
			title = strings.Trim(title, "\"'")
			if title != "" {
				o.store.UpdateConversationTitle(convID, title)
				return
			}
		}

		// Fallback: truncate the first message (safe UTF-8)
		runes := []rune(firstMessage)
		title := string(runes)
		if len(runes) > 50 {
			title = string(runes[:50]) + "..."
		}
		o.store.UpdateConversationTitle(convID, title)
	}()
}

// emergencyCompress aggressively compresses messages when context length is exceeded.
func (o *Orchestrator) emergencyCompress(ctx context.Context, messages []model.Message) []model.Message {
	result := o.comp.Compress(ctx, messages, o.cfg.LLM.ContextWindow*3/4) // Target 75% of window
	if result.ShouldCompress {
		log.Printf("[agent] Emergency compression applied: %d → %d messages", len(messages), len(result.Messages))
		return repairToolCallPairing(result.Messages)
	}
	// Fallback: just keep first 2 + last 10 messages (with overlap guard)
	if len(messages) > 3 {
		keepFirst := 2
		keepLast := 10

		// Guard against overlap: if keepFirst+keepLast >= len(messages), reduce keepLast
		if keepFirst+keepLast >= len(messages) {
			keepLast = len(messages) - keepFirst - 1
			if keepLast < 1 {
				keepLast = 1
			}
		}

		compressed := make([]model.Message, 0, keepFirst+1+keepLast)
		compressed = append(compressed, messages[:keepFirst]...)
		compressed = append(compressed, model.Message{
			Role:    model.RoleSystem,
			Content: "[Earlier conversation was compressed due to context length limits. Key context preserved below.]",
		})
		compressed = append(compressed, messages[len(messages)-keepLast:]...)
		return repairToolCallPairing(compressed)
	}
	return messages
}

// repairToolCallPairing removes orphaned tool calls and tool results from a message list.
// This ensures the LLM API always sees valid assistant.tool_calls → role=tool pairings.
func repairToolCallPairing(messages []model.Message) []model.Message {
	// Build sets of tool_call IDs from assistant messages and tool results
	assistantIDs := make(map[string]bool)
	resultIDs := make(map[string]bool)

	for _, m := range messages {
		if m.Role == model.RoleAssistant && len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				assistantIDs[tc.ID] = true
			}
		}
		if m.Role == model.RoleTool && m.ToolCallID != "" {
			resultIDs[m.ToolCallID] = true
		}
	}

	// Filter: keep only messages with valid pairings
	result := make([]model.Message, 0, len(messages))
	for _, m := range messages {
		if m.Role == model.RoleAssistant && len(m.ToolCalls) > 0 {
			// Keep only if ALL tool calls have matching results
			allMatched := true
			for _, tc := range m.ToolCalls {
				if !resultIDs[tc.ID] {
					allMatched = false
					break
				}
			}
			if !allMatched {
				// Drop this assistant message (orphaned tool calls)
				continue
			}
		}
		if m.Role == model.RoleTool && m.ToolCallID != "" {
			// Keep only if there's a matching assistant tool call
			if !assistantIDs[m.ToolCallID] {
				// Drop this tool result (orphaned result)
				continue
			}
		}
		result = append(result, m)
	}
	return result
}

// RespondClarify delivers a user's response to a pending clarify request.
func (o *Orchestrator) RespondClarify(convID, response string) bool {
	o.clarifyMu.RLock()
	ch, ok := o.clarifyCh[convID]
	o.clarifyMu.RUnlock()

	if !ok {
		return false
	}

	// Use a brief timeout to handle the race between the tool completing
	// and waitForClarify starting to listen on the channel
	select {
	case ch <- response:
		return true
	case <-time.After(1 * time.Second):
		return false
	}
}

// waitForClarify blocks until the user responds to a clarify request, the context
// is cancelled, or timeout expires.
func (o *Orchestrator) waitForClarify(ctx context.Context, convID string, timeout time.Duration) (string, bool) {
	ch := make(chan string, 1)
	o.clarifyMu.Lock()
	o.clarifyCh[convID] = ch
	o.clarifyMu.Unlock()

	defer func() {
		o.clarifyMu.Lock()
		delete(o.clarifyCh, convID)
		o.clarifyMu.Unlock()
	}()

	select {
	case response := <-ch:
		return response, true
	case <-ctx.Done():
		return "", false
	case <-time.After(timeout):
		return "", false
	}
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
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

// toolCallCollector accumulates streaming tool call deltas into complete tool calls.
type toolCallCollector struct {
	calls map[int]*toolCallBuilder
	order []int // Track insertion order for deterministic output
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
		order: make([]int, 0),
	}
}

func (c *toolCallCollector) add(deltas []llm.ToolCallDelta) {
	for _, d := range deltas {
		b, ok := c.calls[d.Index]
		if !ok {
			b = &toolCallBuilder{index: d.Index}
			c.calls[d.Index] = b
			c.order = append(c.order, d.Index)
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
	for _, idx := range c.order {
		b := c.calls[idx]
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
