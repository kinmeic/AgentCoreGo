package agent

import (
	"agent-core/internal/commands"
	"agent-core/internal/compressor"
	"agent-core/internal/config"
	"agent-core/internal/llm"
	"agent-core/internal/memory"
	"agent-core/internal/model"
	"agent-core/internal/prompt"
	"agent-core/internal/sandbox"
	"agent-core/internal/skills"
	"agent-core/internal/store"
	"agent-core/internal/telemetry"
	"agent-core/internal/tool"
	"agent-core/internal/tool/builtin"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

var (
	ErrConversationNotFound = errors.New("conversation not found")
	ErrConversationArchived = errors.New("conversation is archived")
	ErrRunNotFound          = errors.New("run not found")
	ErrTooManyRuns          = errors.New("maximum concurrent runs reached")
	ErrResumeToolRequired   = errors.New("an interrupted side-effecting tool requires resume strategy retry or skip")
)

type activeRun struct {
	id     string
	convID string
	cancel context.CancelFunc
}

// Orchestrator is the durable agent loop shared by synchronous, SSE and resumed runs.
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
	permissions     *tool.PermissionManager
	skillLoader     *skills.Loader
	commandLoader   *commands.Loader
	recorder        *telemetry.Recorder
	runSlots        chan struct{}

	mu         sync.RWMutex
	activeRuns map[string]*activeRun

	clarifyMu       sync.RWMutex
	clarifyCh       map[string]chan string
	clarifyRequests map[string]string

	backgroundCtx     context.Context
	backgroundCancel  context.CancelFunc
	backgroundWG      sync.WaitGroup
	backgroundMu      sync.Mutex
	backgroundStopped bool
}

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
	ctx, cancel := context.WithCancel(context.Background())
	maxRuns := cfg.Server.MaxConcurrentRuns
	if maxRuns <= 0 {
		maxRuns = 1
	}
	return &Orchestrator{
		llmProvider: llmProvider, toolRegistry: registry, processRegistry: processRegistry,
		store: store, memService: memService, comp: comp, promptBuilder: promptBuilder, cfg: cfg,
		activeRuns: make(map[string]*activeRun), clarifyCh: make(map[string]chan string),
		clarifyRequests: make(map[string]string), backgroundCtx: ctx, backgroundCancel: cancel,
		runSlots: make(chan struct{}, maxRuns),
	}
}

func (o *Orchestrator) SetCheckpointStore(store CheckpointStore) { o.checkpointStore = store }
func (o *Orchestrator) SetPermissionManager(manager *tool.PermissionManager) {
	o.permissions = manager
}
func (o *Orchestrator) SetRecorder(recorder *telemetry.Recorder) { o.recorder = recorder }
func (o *Orchestrator) SetSkillLoader(loader *skills.Loader)     { o.skillLoader = loader }
func (o *Orchestrator) SetCommandLoader(loader *commands.Loader) { o.commandLoader = loader }

// ChatSync handles a synchronous chat request. Interactive questions fail closed.
func (o *Orchestrator) ChatSync(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	response, err := o.startNewRun(ctx, req, false, nil)
	if err != nil {
		return nil, err
	}
	return response, nil
}

// ChatStream handles a streaming chat request.
func (o *Orchestrator) ChatStream(ctx context.Context, req model.ChatRequest, events chan<- model.StreamEvent) {
	defer close(events)
	response, err := o.startNewRun(ctx, req, true, events)
	if err != nil && response == nil {
		events <- model.StreamEvent{Type: "error", ErrorCode: "run_start_failed", Message: err.Error(), Timestamp: time.Now()}
	}
}

func (o *Orchestrator) startNewRun(ctx context.Context, req model.ChatRequest, interactive bool, out chan<- model.StreamEvent) (*model.ChatResponse, error) {
	return o.startRunWithSpec(ctx, req, o.newRootSpec(""), "", interactive, out)
}

// StartChildRun executes a subagent or workflow node with a caller-fixed run ID and lineage.
func (o *Orchestrator) StartChildRun(ctx context.Context, req model.ChatRequest, spec model.RunSpec, runID string, interactive bool, out chan<- model.StreamEvent) (*model.ChatResponse, error) {
	return o.startRunWithSpec(ctx, req, spec, runID, interactive, out)
}

func (o *Orchestrator) startRunWithSpec(ctx context.Context, req model.ChatRequest, spec model.RunSpec, requestedRunID string, interactive bool, out chan<- model.StreamEvent) (*model.ChatResponse, error) {
	select {
	case o.runSlots <- struct{}{}:
		defer func() { <-o.runSlots }()
	default:
		return nil, ErrTooManyRuns
	}
	if o.cfg.LLM.RunTimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(o.cfg.LLM.RunTimeoutSeconds)*time.Second)
		defer cancel()
	}
	convID := req.ConversationID
	if convID == "" {
		convID = uuid.NewString()
	}
	if err := o.ensureConversation(convID, req.ConversationID == "", req.Source, req.Metadata); err != nil {
		return nil, err
	}
	conversation, _ := o.store.GetConversation(convID)
	applyConversationOverrides(&spec, conversation)
	now := time.Now()
	runID := requestedRunID
	if runID == "" {
		runID = uuid.NewString()
	}
	if spec.RootRunID == "" {
		spec.RootRunID = runID
	}
	if spec.TraceID == "" {
		spec.TraceID = uuid.NewString()
	}
	if spec.Kind == "" {
		spec.Kind = model.RunKindRoot
	}
	if spec.Workspace == "" {
		spec.Workspace = o.cfg.Workspace.Path
	}
	run := &model.Run{ID: runID, ConversationID: convID, Spec: spec, Status: model.RunStatusRunning,
		Input: req.Message, Source: req.Source, Metadata: req.Metadata, CreatedAt: now, UpdatedAt: now,
		Usage: &model.UsageInfo{}}
	if err := o.store.SaveRun(run); err != nil {
		return nil, fmt.Errorf("create run: %w", err)
	}
	runCtx, err := o.registerRun(run.ID, convID, ctx)
	if err != nil {
		return nil, err
	}
	defer o.unregisterRun(run.ID)
	emitter := &runEmitter{ctx: runCtx, store: o.store, run: run, out: out}
	if err := emitter.emit(model.StreamEvent{Type: "conv_id"}); err != nil {
		return o.failRun(run, emitter, nil, err, interactive)
	}
	messages, err := o.buildMessages(runCtx, convID, model.Message{Role: model.RoleUser, Content: req.Message})
	if err != nil {
		return o.failRun(run, emitter, nil, err, interactive)
	}
	if err := o.store.AddLog(&model.ConversationLog{ConversationID: convID, RunID: run.ID,
		Role: model.RoleUser, Content: req.Message, Status: "completed", CreatedAt: time.Now()}); err != nil {
		return o.failRun(run, emitter, nil, fmt.Errorf("save user message: %w", err), interactive)
	}
	cp := &Checkpoint{ConversationID: convID, RunID: run.ID, Spec: run.Spec, Provider: o.providerForSpec(run.Spec), Messages: messages}
	if err := o.writeCheckpoint(run, cp); err != nil {
		return o.failRun(run, emitter, cp, err, interactive)
	}
	if err := o.executeRun(runCtx, run, cp, interactive, emitter, ""); err != nil {
		return o.failRun(run, emitter, cp, err, interactive)
	}
	return o.completeRun(run, emitter, cp, interactive)
}

// ResumeRun resumes a durable checkpoint. Strategy may be "", "retry" or "skip".
func (o *Orchestrator) ResumeRun(ctx context.Context, runID, strategy string, interactive bool, out chan<- model.StreamEvent) (*model.ChatResponse, error) {
	select {
	case o.runSlots <- struct{}{}:
		defer func() { <-o.runSlots }()
	default:
		return nil, ErrTooManyRuns
	}
	if o.cfg.LLM.RunTimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(o.cfg.LLM.RunTimeoutSeconds)*time.Second)
		defer cancel()
	}
	if strategy != "" && strategy != "retry" && strategy != "skip" {
		return nil, fmt.Errorf("invalid resume strategy %q", strategy)
	}
	run, err := o.store.GetRun(runID)
	if err != nil || run == nil {
		if err == nil {
			err = ErrRunNotFound
		}
		return nil, err
	}
	if run.Status == model.RunStatusCompleted || run.Status == model.RunStatusCancelled {
		return nil, fmt.Errorf("run %s is already %s", runID, run.Status)
	}
	if o.checkpointStore == nil {
		return nil, errors.New("checkpoint storage is unavailable")
	}
	cp, err := o.checkpointStore.LoadCheckpoint(run.ID, run.ConversationID)
	if err != nil || cp == nil {
		if err == nil {
			err = errors.New("checkpoint not found")
		}
		return nil, err
	}
	if cp.RunID != "" && cp.RunID != run.ID {
		return nil, errors.New("checkpoint belongs to a different run")
	}
	cp.RunID = run.ID
	if cp.Spec.RootRunID == "" {
		cp.Spec = run.Spec
	} else {
		run.Spec = cp.Spec
	}
	cp.Provider = o.providerForSpec(run.Spec)
	runCtx, err := o.registerRun(run.ID, run.ConversationID, ctx)
	if err != nil {
		return nil, err
	}
	defer o.unregisterRun(run.ID)
	sequence, err := o.store.LastRunEventSequence(run.ID)
	if err != nil {
		return nil, err
	}
	run.Status = model.RunStatusRunning
	if run.Usage == nil {
		run.Usage = &model.UsageInfo{}
	}
	run.Error = ""
	run.FinishedAt = nil
	if err := o.store.SaveRun(run); err != nil {
		return nil, err
	}
	emitter := &runEmitter{ctx: runCtx, store: o.store, run: run, out: out, sequence: sequence}
	if err := emitter.emit(model.StreamEvent{Type: "resumed"}); err != nil {
		return o.failRun(run, emitter, cp, err, interactive)
	}
	if err := o.executeRun(runCtx, run, cp, interactive, emitter, strategy); err != nil {
		return o.failRun(run, emitter, cp, err, interactive)
	}
	return o.completeRun(run, emitter, cp, interactive)
}

func (o *Orchestrator) executeRun(ctx context.Context, run *model.Run, cp *Checkpoint, interactive bool, emitter *runEmitter, resumeStrategy string) error {
	if cp.Discovery == nil {
		cp.Discovery = tool.NewDiscoveryState(cp.Discovered, o.cfg.Tools.ProgressiveTools, o.cfg.Tools.ToolSearchThreshold)
	}
	if cp.InFlight != nil {
		if err := o.resolveInterruptedTool(run, cp, resumeStrategy); err != nil {
			return err
		}
	}
	iterations := o.cfg.LLM.MaxToolIterations
	// ZCode runtime/methods/subagent.ts 对子代理默认使用 4 turns；主循环无限制。
	if iterations == 0 && run.Spec.Kind == model.RunKindSubagent {
		iterations = 4
	}
	budget := NewIterationBudget(iterations, o.cfg.LLM.MaxRunTokens)
	budget.currentIteration = cp.Iteration
	budget.totalTokens = cp.TotalTokens
	for {
		if len(cp.Pending) > 0 {
			if err := o.executePendingTools(ctx, run, cp, interactive, emitter); err != nil {
				return err
			}
			if err := o.warnModelAnomalies(cp, emitter); err != nil {
				return err
			}
			continue
		}
		if budget.Exhausted() {
			return errors.New(budget.ExceededMessage())
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		cp.Iteration = budget.CurrentIteration()
		if o.cfg.Features.Compact {
			compressed := o.comp.Compress(ctx, cp.Messages, o.llmProvider.MaxContextWindow(), o.toolRegistry.ToolDefinitions())
			if compressed.ShouldCompress {
				cp.Messages = repairToolCallPairing(compressed.Messages)
			}
		}
		if err := emitter.emit(model.StreamEvent{Type: "thinking", Iteration: budget.CurrentIteration() + 1}); err != nil {
			return err
		}
		response, usage, err := o.complete(llm.WithCallMetadata(ctx, llm.CallMetadata{RunID: run.ID,
			RootID: run.Spec.RootRunID, ParentID: run.Spec.ParentRunID, TraceID: run.Spec.TraceID,
			Purpose: purposeForRunKind(run.Spec.Kind)}), cp, interactive, emitter)
		if err != nil {
			if classifyAndLog(err) && o.cfg.Features.Compact {
				budget.RecordIteration(0)
				compressed := o.emergencyCompress(ctx, cp.Messages)
				if len(compressed) >= len(cp.Messages) {
					return errors.New("context length exceeded and compression failed to reduce messages")
				}
				cp.Messages = compressed
				continue
			}
			return fmt.Errorf("LLM call failed: %w", err)
		}
		addUsage(run.Usage, usage)
		budget.RecordIteration(tokenTotal(usage))
		cp.Iteration = budget.CurrentIteration()
		cp.PromptTokens, cp.ComplTokens, cp.TotalTokens = run.Usage.PromptTokens, run.Usage.CompletionTokens, run.Usage.TotalTokens
		if len(response.ToolCalls) == 0 {
			run.Reply = response.Content
			run.ProviderState = response.ProviderState
			return nil
		}
		for _, call := range response.ToolCalls {
			if call.ID == "" || call.Function.Name == "" {
				return errors.New("model returned an incomplete tool call")
			}
		}
		cp.Messages = append(cp.Messages, *response)
		if err := o.store.AddLog(&model.ConversationLog{ConversationID: run.ConversationID, RunID: run.ID,
			Role: model.RoleAssistant, Content: response.Content, ToolCalls: response.ToolCalls,
			ProviderState: response.ProviderState, Status: "tool_calls", CreatedAt: time.Now()}); err != nil {
			return fmt.Errorf("save assistant tool calls: %w", err)
		}
		cp.Pending = append([]model.ToolCall(nil), response.ToolCalls...)
		names := make([]string, len(response.ToolCalls))
		for i, call := range response.ToolCalls {
			names[i] = call.Function.Name
		}
		if err := emitter.emit(model.StreamEvent{Type: "tool_call", Tools: names}); err != nil {
			return err
		}
		if err := o.writeCheckpoint(run, cp); err != nil {
			return err
		}
	}
}

func (o *Orchestrator) complete(ctx context.Context, cp *Checkpoint, stream bool, emitter *runEmitter) (*model.Message, *model.UsageInfo, error) {
	provider := o.llmProvider
	if cp.Provider != nil {
		provider = cp.Provider
	}
	definitions := o.toolRegistry.ToolDefinitionsForRun(cp.Discovery)
	if !stream {
		response, usage, err := provider.Complete(ctx, cp.Messages, definitions)
		if response != nil {
			response.Content = FilterThinkTags(response.Content)
		}
		return response, usage, err
	}
	chunks, err := provider.CompleteStream(ctx, cp.Messages, definitions)
	if err != nil {
		return nil, nil, err
	}
	var content strings.Builder
	collector := newToolCallCollector()
	usage := &model.UsageInfo{}
	thinkFilter := &StreamingThinkFilter{}
	var providerState json.RawMessage
	emitSegment := func(segment ThinkSegment) error {
		if segment.Reasoning {
			if o.cfg.LLM.ExposeReasoning {
				return emitter.emit(model.StreamEvent{Type: "reasoning", Content: segment.Text})
			}
			return nil
		}
		content.WriteString(segment.Text)
		return emitter.emit(model.StreamEvent{Type: "chunk", Content: segment.Text})
	}
	for chunk := range chunks {
		if chunk.Err != nil {
			return nil, nil, chunk.Err
		}
		if chunk.Reasoning != "" && o.cfg.LLM.ExposeReasoning {
			if err := emitter.emit(model.StreamEvent{Type: "reasoning", Content: chunk.Reasoning}); err != nil {
				return nil, nil, err
			}
		}
		if len(chunk.ProviderState) != 0 {
			providerState = chunk.ProviderState
		}
		if chunk.Content != "" {
			for _, segment := range thinkFilter.ProcessSegments(chunk.Content) {
				if err := emitSegment(segment); err != nil {
					return nil, nil, err
				}
			}
		}
		collector.add(chunk.ToolCalls)
		if chunk.Usage != nil {
			usage = &model.UsageInfo{PromptTokens: chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens, TotalTokens: chunk.Usage.TotalTokens}
		}
	}
	for _, segment := range thinkFilter.FlushSegments() {
		if err := emitSegment(segment); err != nil {
			return nil, nil, err
		}
	}
	calls := collector.build()
	if len(calls) != len(collector.calls) {
		return nil, nil, errors.New("stream ended with incomplete tool calls")
	}
	return &model.Message{Role: model.RoleAssistant, Content: content.String(), ToolCalls: calls, ProviderState: providerState}, usage, nil
}

func (o *Orchestrator) executePendingTools(ctx context.Context, run *model.Run, cp *Checkpoint, interactive bool, emitter *runEmitter) error {
	for len(cp.Pending) > 0 {
		call := cp.Pending[0]
		cp.InFlight = &call
		if err := o.writeCheckpoint(run, cp); err != nil {
			return err
		}
		args, parseErr := tool.UnmarshalArgs(call.Function.Arguments)
		preview := truncateStr(call.Function.Arguments, 200)
		if parseErr == nil {
			preview = tool.PreviewArgs(args)
		}
		if err := emitter.emit(model.StreamEvent{Type: "tool.started", ToolName: call.Function.Name,
			ToolCallID: call.ID, Preview: preview}); err != nil {
			return err
		}
		var result string
		startedAt := time.Now()
		execErr := parseErr
		if execErr == nil {
			var statusErr error
			toolCtx := tool.WithExecutionInfo(ctx, tool.ExecutionInfo{
				ConversationID: run.ConversationID, RunID: run.ID, RootRunID: run.Spec.RootRunID,
				ParentRunID: run.Spec.ParentRunID, TraceID: run.Spec.TraceID, ToolCallID: call.ID, Interactive: interactive,
				Discovery: cp.Discovery, Mode: run.Spec.Mode, ReadOnly: run.Spec.ReadOnly, Emit: emitter.emit, OnStatus: func(status model.RunStatus) {
					run.Status = status
					if err := o.store.SaveRun(run); err != nil && statusErr == nil {
						statusErr = fmt.Errorf("persist run status: %w", err)
					}
				},
				RecordFileChange: o.recordFileChange,
			})
			result, execErr = o.toolRegistry.Dispatch(toolCtx, call.Function.Name, args)
			if statusErr != nil && execErr == nil {
				execErr = statusErr
			}
		}
		if execErr == nil && builtin.IsClarifyRequest(result) {
			result, execErr = o.handleClarify(ctx, run, call.ID, result, interactive, emitter)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if execErr != nil {
			result = "Error: " + execErr.Error()
		}
		if o.recorder != nil {
			input, _ := json.Marshal(args)
			if parseErr != nil {
				input, _ = json.Marshal(map[string]string{"raw": call.Function.Arguments})
			}
			output, _ := json.Marshal(map[string]string{"result": result})
			registered, _ := o.toolRegistry.Get(call.Function.Name)
			capabilities := []string{}
			if registered != nil {
				capabilities = append(capabilities, "scope="+registered.Capabilities.SideEffectScope, "risk="+registered.Capabilities.RiskLevel)
			}
			invocation := telemetry.ToolInvocation{TraceID: run.Spec.TraceID, RootID: run.Spec.RootRunID,
				ParentID: run.Spec.ParentRunID, RunID: run.ID, ToolCallID: call.ID, Name: call.Function.Name,
				Input: input, Output: output, Status: toolStatusForError(execErr), Capabilities: capabilities,
				Approval: telemetry.ApprovalRecord{Status: telemetry.ApprovalNotRequired}, StartedAt: startedAt, FinishedAt: time.Now()}
			if err := o.recorder.RecordTool(ctx, invocation); err != nil && o.recorder.Policy() == telemetry.CaptureTraining {
				return err
			}
		}
		cp.Actions = append(cp.Actions, model.Action{RunID: run.ID, ToolCallID: call.ID,
			Tool: call.Function.Name, Status: statusFromErr(execErr), Result: truncateStr(result, 500)})
		cp.Messages = append(cp.Messages, model.Message{Role: model.RoleTool, Content: result, ToolCallID: call.ID, IsError: execErr != nil})
		cp.Pending = cp.Pending[1:]
		cp.InFlight = nil
		toolLog := &model.ConversationLog{ConversationID: run.ConversationID, RunID: run.ID,
			Role: model.RoleTool, Content: result, ToolCallID: call.ID,
			Status: statusFromErr(execErr), CreatedAt: time.Now()}
		if err := o.commitToolBoundary(run, cp, toolLog); err != nil {
			return err
		}
		if err := emitter.emit(model.StreamEvent{Type: "progress", ToolCallID: call.ID,
			ToolName: call.Function.Name, Summary: truncateStr(result, 500)}); err != nil {
			return err
		}
	}
	return nil
}

func (o *Orchestrator) resolveInterruptedTool(run *model.Run, cp *Checkpoint, strategy string) error {
	call := *cp.InFlight
	registered, ok := o.toolRegistry.Get(call.Function.Name)
	readOnly := ok && registered.Capabilities.ReadOnly
	if readOnly || strategy == "retry" {
		found := false
		for _, pending := range cp.Pending {
			if pending.ID == call.ID {
				found = true
				break
			}
		}
		if !found {
			cp.Pending = append([]model.ToolCall{call}, cp.Pending...)
		}
		return nil
	}
	if strategy != "skip" {
		return fmt.Errorf("%w: %s (%s)", ErrResumeToolRequired, call.Function.Name, call.ID)
	}
	result := "Error: tool was interrupted and explicitly skipped during resume"
	cp.Actions = append(cp.Actions, model.Action{RunID: run.ID, ToolCallID: call.ID,
		Tool: call.Function.Name, Status: "cancelled", Result: result})
	cp.Messages = append(cp.Messages, model.Message{Role: model.RoleTool, ToolCallID: call.ID, Content: result})
	if len(cp.Pending) > 0 && cp.Pending[0].ID == call.ID {
		cp.Pending = cp.Pending[1:]
	}
	cp.InFlight = nil
	return o.commitToolBoundary(run, cp, &model.ConversationLog{ConversationID: run.ConversationID,
		RunID: run.ID, Role: model.RoleTool, ToolCallID: call.ID, Content: result,
		Status: "cancelled", CreatedAt: time.Now()})
}

func (o *Orchestrator) handleClarify(ctx context.Context, run *model.Run, callID, result string, interactive bool, emitter *runEmitter) (string, error) {
	request, err := builtin.ParseClarifyRequest(result)
	if err != nil || request == nil {
		return result, nil
	}
	if !interactive {
		return "", errors.New("clarification requires the streaming API")
	}
	requestID := uuid.NewString()
	if err := emitter.emit(model.StreamEvent{Type: "clarify", RequestID: requestID, ToolCallID: callID,
		ClarifyKind: string(request.Kind), ClarifyMessage: request.Message, ClarifyOptions: request.Options}); err != nil {
		return "", err
	}
	run.Status = model.RunStatusWaitingClarify
	if err := o.store.SaveRun(run); err != nil {
		return "", err
	}
	response, ok := o.waitForClarify(ctx, run.ConversationID, requestID, 5*time.Minute)
	run.Status = model.RunStatusRunning
	if err := o.store.SaveRun(run); err != nil {
		return "", err
	}
	if !ok {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("clarification request timed out")
	}
	return "User responded: " + response, nil
}

func (o *Orchestrator) completeRun(run *model.Run, emitter *runEmitter, cp *Checkpoint, interactive bool) (*model.ChatResponse, error) {
	intent := classifyIntent(run.Reply)
	if err := o.store.AddLog(&model.ConversationLog{ConversationID: run.ConversationID, RunID: run.ID,
		Role: model.RoleAssistant, Content: run.Reply, Intent: intent, Actions: cp.Actions,
		ProviderState: run.ProviderState, Status: "completed", PromptTokens: run.Usage.PromptTokens, CompletionTokens: run.Usage.CompletionTokens,
		ContextTokens: run.Usage.TotalTokens, CreatedAt: time.Now()}); err != nil {
		return o.failRun(run, emitter, cp, fmt.Errorf("save assistant response: %w", err), interactive)
	}
	now := time.Now()
	run.Status, run.FinishedAt, run.Error = model.RunStatusCompleted, &now, ""
	if err := o.store.SaveRun(run); err != nil {
		return o.failRun(run, emitter, cp, err, interactive)
	}
	if o.checkpointStore != nil {
		if err := o.checkpointStore.DeleteCheckpoint(run.ID); err != nil {
			log.Printf("[agent] completed run %s but could not delete checkpoint: %v", run.ID, err)
		}
	}
	if err := emitter.emit(model.StreamEvent{Type: "done", Intent: intent, Actions: cp.Actions,
		Reply: run.Reply, Usage: run.Usage}); err != nil {
		return nil, err
	}
	o.autoTitle(run)
	return &model.ChatResponse{ConversationID: run.ConversationID, RunID: run.ID,
		Status: model.RunStatusCompleted, Reply: run.Reply, Intent: intent, Actions: cp.Actions,
		Usage: run.Usage, CreatedAt: now}, nil
}

func (o *Orchestrator) failRun(run *model.Run, emitter *runEmitter, cp *Checkpoint, runErr error, interactive bool) (*model.ChatResponse, error) {
	status := model.RunStatusFailed
	eventType, code := "error", "run_failed"
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		status, eventType, code = model.RunStatusCancelled, "cancelled", "cancelled"
	}
	now := time.Now()
	run.Status, run.Error, run.FinishedAt = status, runErr.Error(), &now
	if cp != nil {
		run.Reply = ""
	}
	if err := o.store.SaveRun(run); err != nil {
		log.Printf("[agent] failed to persist %s state for %s: %v", status, run.ID, err)
	}
	_ = emitter.emit(model.StreamEvent{Type: eventType, ErrorCode: code, Message: runErr.Error(), Usage: run.Usage})
	response := &model.ChatResponse{ConversationID: run.ConversationID, RunID: run.ID,
		Status: status, Usage: run.Usage, CreatedAt: now}
	if interactive {
		return response, runErr
	}
	return nil, runErr
}

func (o *Orchestrator) writeCheckpoint(run *model.Run, cp *Checkpoint) error {
	if o.checkpointStore == nil {
		return nil
	}
	cp.ConversationID, cp.RunID, cp.SavedAt = run.ConversationID, run.ID, time.Now()
	cp.Spec = run.Spec
	if cp.Discovery != nil {
		cp.Discovered = cp.Discovery.Names()
	}
	cp.PromptTokens, cp.ComplTokens, cp.TotalTokens = run.Usage.PromptTokens, run.Usage.CompletionTokens, run.Usage.TotalTokens
	if err := o.checkpointStore.SaveCheckpoint(cp); err != nil {
		return fmt.Errorf("save checkpoint: %w", err)
	}
	run.Iteration = cp.Iteration
	return o.store.SaveRun(run)
}

func (o *Orchestrator) commitToolBoundary(run *model.Run, cp *Checkpoint, toolLog *model.ConversationLog) error {
	if cp.Discovery != nil {
		cp.Discovered = cp.Discovery.Names()
	}
	cp.SavedAt = time.Now()
	data, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	if o.checkpointStore == nil {
		return o.store.AddLog(toolLog)
	}
	if err := o.store.CommitToolResult(toolLog, data); err != nil {
		return fmt.Errorf("commit tool boundary: %w", err)
	}
	run.Iteration = cp.Iteration
	return o.store.SaveRun(run)
}

// CompactConversation writes an explicit durable summary through the latest log.
func (o *Orchestrator) CompactConversation(ctx context.Context, convID, instructions string) (string, error) {
	logs, err := o.store.GetLogs(convID, 200)
	if err != nil {
		return "", err
	}
	if len(logs) == 0 {
		return "", errors.New("conversation has no messages to compact")
	}
	messages := make([]model.Message, 0, len(logs)+1)
	for _, entry := range logs {
		messages = append(messages, conversationLogMessage(entry))
	}
	if instructions != "" {
		messages = append(messages, model.Message{Role: model.RoleUser, Content: "Summary instructions: " + instructions})
	}
	summary, _ := o.comp.Summarize(ctx, messages)
	if summary == "" {
		return "", errors.New("empty conversation summary")
	}
	return summary, o.store.SaveConversationSummary(convID, summary, logs[len(logs)-1].ID)
}

func (o *Orchestrator) buildMessages(ctx context.Context, convID string, userMsg model.Message) ([]model.Message, error) {
	logs, err := o.store.GetLogs(convID, 200)
	if err != nil {
		return nil, fmt.Errorf("load conversation logs: %w", err)
	}
	summary, through, err := o.store.ConversationSummary(convID)
	if err != nil {
		return nil, fmt.Errorf("load conversation summary: %w", err)
	}
	visibleLogs := make([]*model.ConversationLog, 0, len(logs))
	for _, entry := range logs {
		if entry.ID > through {
			visibleLogs = append(visibleLogs, entry)
		}
	}
	var memories []model.MemorySearchResult
	if o.memService != nil && o.cfg.Features.Memory && o.cfg.Memory.Use {
		memories, err = o.memService.Search(ctx, userMsg.Content, 5)
		if err != nil {
			log.Printf("[memory] recall degraded: %v", err)
		}
	}
	soulContent, _ := sandbox.ReadSoulFile(o.cfg.Workspace.Path, o.cfg.Workspace.SoulFile)
	systemMessage := o.promptBuilder.BuildSystemMessage(soulContent, memories, o.toolRegistry.Names())
	if o.skillLoader != nil && o.cfg.Features.Skill && o.cfg.Skills.Enabled && o.cfg.Skills.IncludeInstructions {
		if metadata := o.skillLoader.Discover().RenderMetadataBudget(o.cfg.Skills.MetadataBudget); metadata != "" {
			systemMessage.Content += "\n\n## Available Skills\n" + metadata
		}
	}
	if o.cfg.Workspace.AllowOutsideWorkspace {
		systemMessage.Content += "\n\n## Filesystem Access Policy\nBuilt-in file tools and the initial terminal working directory may use paths outside the configured workspace. Relative paths start there. Use external paths when the user's task identifies them; do not assume every external file is part of the task. Terminal commands still run with AgentCore's operating-system permissions."
	} else {
		systemMessage.Content += "\n\n## Filesystem Access Policy\nBuilt-in file tools reject paths outside the configured workspace, and the initial terminal working directory must be inside it. Relative paths start there. Terminal commands still run with AgentCore's operating-system permissions and are not confined to the workspace."
	}
	messages := []model.Message{systemMessage}
	if summary != "" {
		messages = append(messages, model.Message{Role: model.RoleSystem, Content: summary})
	}
	for _, entry := range visibleLogs {
		messages = append(messages, conversationLogMessage(entry))
	}
	messages = append(messages, userMsg)
	if !o.cfg.Features.Compact {
		return repairToolCallPairing(messages), nil
	}
	definitions := o.toolRegistry.ToolDefinitions()
	result := o.comp.Compress(ctx, messages, o.llmProvider.MaxContextWindow(), definitions)
	if result.ShouldCompress && len(visibleLogs) > 40 {
		boundary := len(visibleLogs) - 20
		older := make([]model.Message, 0, boundary+1)
		if summary != "" {
			older = append(older, model.Message{Role: model.RoleSystem, Content: summary})
		}
		for _, entry := range visibleLogs[:boundary] {
			older = append(older, conversationLogMessage(entry))
		}
		durableSummary, generated := o.comp.Summarize(ctx, older)
		if durableSummary != "" {
			if err := o.store.SaveConversationSummary(convID, durableSummary, visibleLogs[boundary-1].ID); err != nil {
				return nil, fmt.Errorf("persist conversation summary: %w", err)
			}
			messages = []model.Message{systemMessage, {Role: model.RoleSystem, Content: durableSummary}}
			for _, entry := range visibleLogs[boundary:] {
				messages = append(messages, conversationLogMessage(entry))
			}
			messages = append(messages, userMsg)
			result = o.comp.Compress(ctx, messages, o.llmProvider.MaxContextWindow(), definitions)
			log.Printf("[agent] durable summary persisted generated=%v through_log=%d", generated, visibleLogs[boundary-1].ID)
		}
	}
	if result.ShouldCompress {
		messages = repairToolCallPairing(result.Messages)
		log.Printf("[agent] context compressed level=%d tokens=%d→%d summary=%v target=%v", result.Level,
			result.BeforeTokenEstimate, result.TokenEstimate, result.SummaryUsed, result.TargetReached)
	}
	return repairToolCallPairing(messages), nil
}

func conversationLogMessage(entry *model.ConversationLog) model.Message {
	return model.Message{Role: entry.Role, Content: entry.Content, ToolCalls: entry.ToolCalls, ToolCallID: entry.ToolCallID, IsError: entry.Role == model.RoleTool && entry.Status == "error", ProviderState: entry.ProviderState}
}

func (o *Orchestrator) emergencyCompress(ctx context.Context, messages []model.Message) []model.Message {
	result := o.comp.Compress(ctx, messages, o.llmProvider.MaxContextWindow()*3/4, o.toolRegistry.ToolDefinitions())
	if result.ShouldCompress {
		return repairToolCallPairing(result.Messages)
	}
	if len(messages) <= 12 {
		return messages
	}
	compressed := append([]model.Message(nil), messages[:2]...)
	compressed = append(compressed, model.Message{Role: model.RoleSystem,
		Content: "[Earlier conversation compressed after a context-length error.]"})
	compressed = append(compressed, messages[len(messages)-10:]...)
	return repairToolCallPairing(compressed)
}

func repairToolCallPairing(messages []model.Message) []model.Message {
	assistantIDs, resultIDs := make(map[string]bool), make(map[string]bool)
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			assistantIDs[call.ID] = true
		}
		if message.Role == model.RoleTool {
			resultIDs[message.ToolCallID] = true
		}
	}
	result := make([]model.Message, 0, len(messages))
	for _, message := range messages {
		matched := true
		for _, call := range message.ToolCalls {
			matched = matched && resultIDs[call.ID]
		}
		if !matched || (message.Role == model.RoleTool && !assistantIDs[message.ToolCallID]) {
			continue
		}
		result = append(result, message)
	}
	return result
}

// HasActiveConversation reports whether any run for the conversation is still
// executing in memory, including runs that are finishing after a terminal state.
func (o *Orchestrator) HasActiveConversation(conversationID string) bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	for _, candidate := range o.activeRuns {
		if candidate.convID == conversationID {
			return true
		}
	}
	return false
}

// Stop interrupts all active runs for a conversation during replacement races.
func (o *Orchestrator) Stop(conversationID string) bool {
	o.mu.RLock()
	var runIDs []string
	for _, candidate := range o.activeRuns {
		if candidate.convID == conversationID {
			runIDs = append(runIDs, candidate.id)
		}
	}
	o.mu.RUnlock()
	stopped := false
	for _, id := range runIDs {
		stopped = o.StopRun(id) || stopped
	}
	return stopped
}

// StopRun cancels one exact run and its associated process tree.
func (o *Orchestrator) StopRun(runID string) bool {
	o.mu.RLock()
	active := o.activeRuns[runID]
	o.mu.RUnlock()
	if active == nil {
		return false
	}
	// Terminal processes inherit the exact run context and cancel their process group.
	active.cancel()
	return true
}

func (o *Orchestrator) StopAll() {
	o.mu.Lock()
	runs := make([]*activeRun, 0, len(o.activeRuns))
	for _, active := range o.activeRuns {
		runs = append(runs, active)
	}
	o.activeRuns = make(map[string]*activeRun)
	o.mu.Unlock()
	for _, active := range runs {
		active.cancel()
		if o.processRegistry != nil {
			o.processRegistry.KillByConversation(active.convID)
		}
	}
	o.backgroundMu.Lock()
	o.backgroundStopped = true
	o.backgroundCancel()
	o.backgroundMu.Unlock()
	o.backgroundWG.Wait()
}

func (o *Orchestrator) registerRun(runID, convID string, parent context.Context) (context.Context, error) {
	runCtx, cancel := context.WithCancel(parent)
	active := &activeRun{id: runID, convID: convID, cancel: cancel}
	o.mu.Lock()
	var previous *activeRun
	for _, candidate := range o.activeRuns {
		if candidate.convID == convID {
			previous = candidate
			break
		}
	}
	o.activeRuns[runID] = active
	o.mu.Unlock()
	if previous != nil && previous.id != runID {
		previous.cancel()
	}
	return runCtx, nil
}

func (o *Orchestrator) unregisterRun(runID string) {
	o.mu.Lock()
	delete(o.activeRuns, runID)
	o.mu.Unlock()
}

func (o *Orchestrator) ensureConversation(id string, create bool, source string, metadata map[string]string) error {
	existing, err := o.store.GetConversation(id)
	if err != nil {
		return err
	}
	if existing != nil {
		if existing.ArchivedAt != nil {
			return ErrConversationArchived
		}
		if create {
			return fmt.Errorf("conversation already exists")
		}
		return nil
	}
	if !create {
		return ErrConversationNotFound
	}
	now := time.Now()
	return o.store.CreateConversation(&model.Conversation{ID: id, Source: source,
		Metadata: metadata, CreatedAt: now, UpdatedAt: now})
}

func (o *Orchestrator) RespondClarify(id, response string) bool {
	o.clarifyMu.RLock()
	requestID := id
	if mapped := o.clarifyRequests[id]; mapped != "" {
		requestID = mapped
	}
	ch := o.clarifyCh[requestID]
	o.clarifyMu.RUnlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- response:
		return true
	default:
		return false
	}
}

func (o *Orchestrator) waitForClarify(ctx context.Context, convID, requestID string, timeout time.Duration) (string, bool) {
	ch := make(chan string, 1)
	o.clarifyMu.Lock()
	o.clarifyCh[requestID] = ch
	o.clarifyRequests[convID] = requestID
	o.clarifyMu.Unlock()
	defer func() {
		o.clarifyMu.Lock()
		delete(o.clarifyCh, requestID)
		delete(o.clarifyRequests, convID)
		o.clarifyMu.Unlock()
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case response := <-ch:
		return response, true
	case <-ctx.Done():
		return "", false
	case <-timer.C:
		return "", false
	}
}

func (o *Orchestrator) RespondApproval(id string, response tool.ApprovalResponse) bool {
	return o.permissions != nil && o.permissions.Respond(id, response)
}

func (o *Orchestrator) PendingApprovals() []tool.ApprovalRequest {
	if o.permissions == nil {
		return nil
	}
	return o.permissions.Pending()
}

func (o *Orchestrator) autoTitle(run *model.Run) {
	convID, firstMessage := run.ConversationID, run.Input
	conv, err := o.store.GetConversation(convID)
	if err != nil || conv == nil || conv.Title != "" {
		return
	}
	o.backgroundMu.Lock()
	if o.backgroundStopped || o.backgroundCtx.Err() != nil {
		o.backgroundMu.Unlock()
		return
	}
	o.backgroundWG.Add(1)
	o.backgroundMu.Unlock()
	go func() {
		defer o.backgroundWG.Done()
		defer func() { _ = recover() }()
		ctx, cancel := context.WithTimeout(o.backgroundCtx, 10*time.Second)
		defer cancel()
		ctx = llm.WithCallMetadata(ctx, llm.CallMetadata{RunID: run.ID, RootID: run.Spec.RootRunID,
			ParentID: run.Spec.ParentRunID, TraceID: run.Spec.TraceID, Purpose: telemetry.PurposeTitle})
		messages := []model.Message{{Role: model.RoleUser,
			Content: "Generate a very short title (max 8 words, no quotes) for: " + firstMessage}}
		response, _, err := o.llmProvider.Complete(ctx, messages, nil)
		title := ""
		if err == nil && response != nil {
			title = strings.Trim(strings.TrimSpace(response.Content), "\"'")
		}
		if title == "" {
			title = firstMessage
		}
		runes := []rune(title)
		if len(runes) > 80 {
			title = string(runes[:80])
		}
		if err := o.store.UpdateConversationTitle(convID, title); err != nil && ctx.Err() == nil {
			log.Printf("[agent] update title: %v", err)
		}
	}()
}

func purposeForRunKind(kind model.RunKind) telemetry.Purpose {
	switch kind {
	case model.RunKindSubagent:
		return telemetry.PurposeSubagent
	case model.RunKindWorkflowNode:
		return telemetry.PurposeWorkflow
	default:
		return telemetry.PurposeMain
	}
}

func (o *Orchestrator) skillsDigest() string {
	if o.skillLoader == nil {
		return ""
	}
	catalog := o.skillLoader.Discover()
	data, _ := json.Marshal(catalog.Skills)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func (o *Orchestrator) commandsDigest() string {
	if o.commandLoader == nil {
		return ""
	}
	catalog := o.commandLoader.Discover()
	data, _ := json.Marshal(catalog.Commands)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func applyConversationOverrides(spec *model.RunSpec, conversation *model.Conversation) {
	if conversation == nil {
		return
	}
	if value := conversation.Metadata["model"]; value != "" {
		spec.Model = value
	}
	if value := conversation.Metadata["effort"]; value != "" {
		spec.Effort = value
	}
	if value := conversation.Metadata["mode"]; value != "" {
		spec.Mode = value
	}
}

func (o *Orchestrator) providerForSpec(spec model.RunSpec) llm.Provider {
	if (spec.Model == "" || spec.Model == o.llmProvider.ModelName()) && (spec.Effort == "" || spec.Effort == o.cfg.LLM.ReasoningEffort) {
		return o.llmProvider
	}
	if configurable, ok := o.llmProvider.(interface {
		WithModelOptions(string, string) llm.Provider
	}); ok {
		return configurable.WithModelOptions(spec.Model, spec.Effort)
	}
	return o.llmProvider
}

func (o *Orchestrator) newRootSpec(runID string) model.RunSpec {
	configData, _ := json.Marshal(o.cfg.Redacted())
	configDigest := sha256.Sum256(configData)
	toolData, _ := json.Marshal(o.toolRegistry.Names())
	toolDigest := sha256.Sum256(toolData)
	mode := o.cfg.Tools.ApprovalMode
	if o.permissions != nil {
		mode = o.permissions.Mode()
	}
	return model.RunSpec{Workspace: o.cfg.Workspace.Path,
		ConfigDigest: hex.EncodeToString(configDigest[:]), ToolsDigest: hex.EncodeToString(toolDigest[:]),
		SkillsDigest: o.skillsDigest(), CommandsDigest: o.commandsDigest(),
		Model: o.llmProvider.ModelName(), Effort: o.cfg.LLM.ReasoningEffort, Mode: mode,
		TraceID: uuid.NewString(), RootRunID: runID, Kind: model.RunKindRoot}
}

func classifyIntent(reply string) string {
	lower := strings.ToLower(reply)
	for _, marker := range []string{"i've", "done", "completed", "已创建", "已修改", "已完成", "已写入"} {
		if strings.Contains(lower, marker) {
			return model.IntentAction
		}
	}
	return model.IntentQuery
}

func toolStatusForError(err error) telemetry.ToolStatus {
	if err == nil {
		return telemetry.ToolSucceeded
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return telemetry.ToolCancelled
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "permission denied") || strings.Contains(message, "blocked by policy") {
		return telemetry.ToolDenied
	}
	return telemetry.ToolFailed
}

func (o *Orchestrator) recordFileChange(change telemetry.FileChange, patch string) error {
	if o.recorder == nil {
		return nil
	}
	if patch != "" {
		ref, err := o.recorder.Store().Put("text/x-diff", []byte(patch))
		if err != nil {
			return err
		}
		change.DiffArtifact = &ref
	}
	return o.recorder.RecordFileChange(context.Background(), change)
}

func statusFromErr(err error) string {
	if err != nil {
		return "error"
	}
	return "success"
}

func addUsage(total *model.UsageInfo, usage *model.UsageInfo) {
	if total == nil || usage == nil {
		return
	}
	total.PromptTokens += usage.PromptTokens
	total.CompletionTokens += usage.CompletionTokens
	total.TotalTokens += usage.TotalTokens
}

func tokenTotal(usage *model.UsageInfo) int {
	if usage == nil {
		return 0
	}
	return usage.TotalTokens
}

func truncateStr(value string, maximum int) string {
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum]) + "..."
}

type runEmitter struct {
	ctx      context.Context
	store    *store.SQLite
	run      *model.Run
	out      chan<- model.StreamEvent
	mu       sync.Mutex
	sequence uint64
}

func (e *runEmitter) emit(event model.StreamEvent) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sequence++
	event.SchemaVersion = 1
	event.EventID = uuid.NewString()
	event.Sequence = e.sequence
	event.RunID = e.run.ID
	event.ConversationID = e.run.ConversationID
	event.Timestamp = time.Now()
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if err := e.store.AppendRunEvent(&model.RunEvent{EventID: event.EventID, RunID: e.run.ID,
		RootRunID: e.run.Spec.RootRunID, ParentRunID: e.run.Spec.ParentRunID, TraceID: e.run.Spec.TraceID,
		ConversationID: e.run.ConversationID, Sequence: event.Sequence, Type: event.Type,
		Payload: payload, CreatedAt: event.Timestamp}); err != nil {
		return fmt.Errorf("persist stream event: %w", err)
	}
	if e.out != nil {
		select {
		case e.out <- event:
		case <-e.ctx.Done():
			return e.ctx.Err()
		}
	}
	return nil
}

type toolCallCollector struct {
	calls map[int]*toolCallBuilder
	order []int
}

type toolCallBuilder struct {
	id        string
	name      string
	arguments strings.Builder
}

func newToolCallCollector() *toolCallCollector {
	return &toolCallCollector{calls: make(map[int]*toolCallBuilder)}
}

func (c *toolCallCollector) add(deltas []llm.ToolCallDelta) {
	for _, delta := range deltas {
		builder := c.calls[delta.Index]
		if builder == nil {
			builder = &toolCallBuilder{}
			c.calls[delta.Index] = builder
			c.order = append(c.order, delta.Index)
		}
		if delta.ID != "" {
			builder.id = delta.ID
		}
		if delta.Name != "" {
			builder.name = delta.Name
		}
		builder.arguments.WriteString(delta.Arguments)
	}
}

func (c *toolCallCollector) build() []model.ToolCall {
	calls := make([]model.ToolCall, 0, len(c.calls))
	for _, index := range c.order {
		builder := c.calls[index]
		calls = append(calls, model.ToolCall{ID: builder.id, Type: "function",
			Function: model.ToolFunction{Name: builder.name, Arguments: builder.arguments.String()}})
	}
	return calls
}

// StartAcceptedRun executes a server-owned root task with a durable admission ID.
func (o *Orchestrator) StartAcceptedRun(ctx context.Context, req model.ChatRequest, runID string) (*model.ChatResponse, error) {
	return o.startRunWithSpec(ctx, req, o.newRootSpec(runID), runID, true, nil)
}
