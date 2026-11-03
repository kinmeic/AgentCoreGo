package workflow

import (
	"agent-core/internal/config"
	"agent-core/internal/model"
	"agent-core/internal/store"
	"agent-core/internal/tool"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// ChildStarter is the shared agent execution boundary used by agent nodes.
type ChildStarter interface {
	StartChildRun(ctx context.Context, req model.ChatRequest, spec model.RunSpec, runID string, interactive bool, out chan<- model.StreamEvent) (*model.ChatResponse, error)
	StopRun(runID string) bool
}

// Engine schedules and resumes declarative workflows.
type Engine struct {
	store    *store.SQLite
	starter  ChildStarter
	registry *tool.Registry
	cfg      config.Config
	mu       sync.Mutex
	active   map[string]context.CancelFunc
}

func NewEngine(store *store.SQLite, starter ChildStarter, registry *tool.Registry, cfg config.Config) *Engine {
	return &Engine{store: store, starter: starter, registry: registry, cfg: cfg, active: make(map[string]context.CancelFunc)}
}

type task struct {
	key     string
	node    Node
	prompt  string
	args    map[string]any
	hash    string
	attempt int
}

type taskResult struct {
	task   task
	status string
	output json.RawMessage
	err    error
}

// Start persists the complete definition before dispatching any node.
func (e *Engine) Start(ctx context.Context, definition Definition) (*RunView, error) {
	if !e.cfg.Features.Workflow || !e.cfg.Workflow.Enabled {
		return nil, fmt.Errorf("workflow is disabled")
	}
	if err := definition.Validate(e.cfg.Workflow.MaxNodes); err != nil {
		return nil, err
	}
	now := time.Now()
	runID := uuid.NewString()
	record := &store.WorkflowDefinitionRecord{ID: runID, DefinitionDigest: definition.Digest(), Status: "running", CreatedAt: now}
	record.Definition, _ = json.Marshal(definition)
	if err := e.store.SaveWorkflow(record); err != nil {
		return nil, err
	}
	spec := model.RunSpec{Workspace: e.cfg.Workspace.Path, Model: e.cfg.LLM.Model,
		Effort: e.cfg.LLM.ReasoningEffort, Mode: e.cfg.Tools.ApprovalMode, TraceID: uuid.NewString(),
		RootRunID: runID, Kind: model.RunKindRoot, WorkflowDigest: record.DefinitionDigest}
	top := &model.Run{ID: runID, ConversationID: runID, Spec: spec, Status: model.RunStatusRunning,
		Input: definition.Name, Source: "workflow", CreatedAt: now, Usage: &model.UsageInfo{}}
	if err := e.store.CreateConversation(&model.Conversation{ID: runID, Title: definition.Name,
		Source: "workflow", CreatedAt: now, UpdatedAt: now}); err != nil {
		return nil, err
	}
	if err := e.store.SaveRun(top); err != nil {
		return nil, err
	}
	return e.execute(ctx, top, record, definition)
}

// Resume replays scheduling while reusing completed nodes with matching input hashes.
func (e *Engine) Resume(ctx context.Context, runID string) (*RunView, error) {
	record, err := e.store.WorkflowDefinition(runID)
	if err != nil || record == nil {
		return nil, fmt.Errorf("workflow run not found")
	}
	if record.Status == "completed" {
		return e.view(record)
	}
	var definition Definition
	if err := json.Unmarshal(record.Definition, &definition); err != nil {
		return nil, err
	}
	top, err := e.store.GetRun(runID)
	if err != nil || top == nil {
		return nil, fmt.Errorf("workflow run metadata not found")
	}
	record.Status = "running"
	if err := e.store.SaveWorkflow(record); err != nil {
		return nil, err
	}
	return e.execute(ctx, top, record, definition)
}

func (e *Engine) execute(parent context.Context, top *model.Run, record *store.WorkflowDefinitionRecord, definition Definition) (*RunView, error) {
	ctx, cancel := context.WithTimeout(parent, time.Duration(e.cfg.Workflow.TimeoutSeconds)*time.Second)
	defer cancel()
	e.mu.Lock()
	e.active[record.ID] = cancel
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.active, record.ID)
		e.mu.Unlock()
	}()
	previous, _ := e.store.WorkflowNodeRecords(record.ID)
	cache := make(map[string]json.RawMessage)
	attempts := make(map[string]int)
	for _, node := range previous {
		if node.Attempt > attempts[node.NodeID] {
			attempts[node.NodeID] = node.Attempt
		}
		if node.Status == "completed" {
			cache[node.InputHash] = node.Result
		}
	}
	tasks := make(map[string]task)
	for _, node := range definition.Nodes {
		if len(node.Items) == 0 {
			tasks[node.ID] = task{key: node.ID, node: node, attempt: attempts[node.ID] + 1}
			continue
		}
		for index, item := range node.Items {
			key := fmt.Sprintf("%s[%d]", node.ID, index)
			args := cloneArgs(node.Args)
			args["_item"] = item
			tasks[key] = task{key: key, node: node, args: args, attempt: attempts[key] + 1}
		}
	}
	status := make(map[string]string)
	outputs := make(map[string]json.RawMessage)
	var agentCalls atomic.Int32
	for len(status) < len(tasks) {
		if err := ctx.Err(); err != nil {
			record.Status = "cancelled"
			_ = e.store.SaveWorkflow(record)
			return e.view(record)
		}
		var ready []task
		progress := false
		for key, current := range tasks {
			if status[key] != "" {
				continue
			}
			resolvedPrompt, resolvedArgs := resolveTemplates(current.node, current.args, outputs)
			current.prompt, current.args = resolvedPrompt, resolvedArgs
			hashInput, _ := json.Marshal(map[string]any{"node": current.node.ID, "prompt": current.prompt, "args": current.args, "item": current.args["_item"]})
			digest := sha256.Sum256(hashInput)
			current.hash = hex.EncodeToString(digest[:])
			tasks[key] = current
			decision := dependencyDecision(current.node, status)
			if decision == "skipped" {
				status[key] = decision
				progress = true
				continue
			}
			if decision == "ready" {
				ready = append(ready, current)
			}
		}
		if len(ready) == 0 {
			if progress {
				continue
			}
			unfinished := false
			for _, current := range tasks {
				if status[current.key] == "" {
					unfinished = true
					break
				}
			}
			if !unfinished {
				break
			}
			record.Status = "failed"
			_ = e.store.SaveWorkflow(record)
			return e.view(record)
		}
		limit := e.cfg.Workflow.MaxConcurrency
		if limit > len(ready) {
			limit = len(ready)
		}
		semaphore := make(chan struct{}, limit)
		results := make(chan taskResult, len(ready))
		var wg sync.WaitGroup
		for _, current := range ready {
			if cached, ok := cache[current.hash]; ok {
				status[current.key] = "completed"
				outputs[current.key] = cached
				progress = true
				continue
			}
			wg.Add(1)
			go func(current task) {
				defer wg.Done()
				semaphore <- struct{}{}
				defer func() { <-semaphore }()
				result := e.runTask(ctx, top, current, &agentCalls)
				results <- result
			}(current)
		}
		wg.Wait()
		close(results)
		for result := range results {
			status[result.task.key] = result.status
			if result.err == nil {
				outputs[result.task.key] = result.output
			}
		}
	}
	record.Status = "completed"
	for _, state := range status {
		if state == "failed" {
			record.Status = "failed"
			break
		}
	}
	if err := e.store.SaveWorkflow(record); err != nil {
		return nil, err
	}
	return e.view(record)
}

func dependencyDecision(node Node, status map[string]string) string {
	failed := false
	for _, dependency := range node.DependsOn {
		states := []string{status[dependency]}
		if states[0] == "" {
			states = nil
			for key, state := range status {
				if strings.HasPrefix(key, dependency+"[") {
					states = append(states, state)
				}
			}
		}
		if len(states) == 0 {
			return "waiting"
		}
		for _, state := range states {
			if state == "" {
				return "waiting"
			}
			if state == "failed" {
				failed = true
			}
		}
	}
	if failed && node.Condition != "on_failure" && node.Condition != "always" {
		return "skipped"
	}
	if !failed && node.Condition == "on_failure" {
		return "skipped"
	}
	return "ready"
}

func (e *Engine) runTask(ctx context.Context, top *model.Run, current task, agentCalls *atomic.Int32) taskResult {
	result := taskResult{task: current, status: "completed"}
	var lastErr error
	for attempt := current.attempt; attempt < current.attempt+current.node.Retry+1; attempt++ {
		current.attempt = attempt
		_ = e.store.SaveWorkflowNode(&store.WorkflowNodeRecord{RunID: top.ID, NodeID: current.key,
			Attempt: attempt, InputHash: current.hash, Status: "running", UpdatedAt: time.Now()})
		callCtx := ctx
		if current.node.TimeoutSeconds > 0 {
			var cancel context.CancelFunc
			callCtx, cancel = context.WithTimeout(ctx, time.Duration(current.node.TimeoutSeconds)*time.Second)
			defer cancel()
		}
		var output json.RawMessage
		switch current.node.Type {
		case "agent":
			if agentCalls.Add(1) > int32(e.cfg.Workflow.MaxAgentCalls) {
				lastErr = fmt.Errorf("workflow agent call budget exceeded")
				break
			}
			spec := top.Spec
			spec.ParentRunID = top.ID
			spec.Depth = top.Spec.Depth + 1
			spec.Kind = model.RunKindWorkflowNode
			response, err := e.starter.StartChildRun(callCtx, model.ChatRequest{Message: current.prompt, Source: "workflow"}, spec, uuid.NewString(), false, nil)
			if err != nil {
				lastErr = err
			} else {
				output, _ = json.Marshal(map[string]any{"run_id": response.RunID, "reply": response.Reply, "usage": response.Usage})
			}
		case "tool":
			info := tool.ExecutionInfo{ConversationID: top.ConversationID, RunID: top.ID, ToolCallID: current.key,
				Mode: top.Spec.Mode, Interactive: false}
			text, err := e.registry.Dispatch(tool.WithExecutionInfo(callCtx, info), current.node.Tool, current.args)
			if err != nil {
				lastErr = err
			} else {
				output, _ = json.Marshal(map[string]any{"result": text})
			}
		}
		if lastErr == nil {
			result.output = output
			_ = e.store.SaveWorkflowNode(&store.WorkflowNodeRecord{RunID: top.ID, NodeID: current.key,
				Attempt: attempt, InputHash: current.hash, Status: "completed", Result: output, UpdatedAt: time.Now()})
			return result
		}
		_ = e.store.SaveWorkflowNode(&store.WorkflowNodeRecord{RunID: top.ID, NodeID: current.key,
			Attempt: attempt, InputHash: current.hash, Status: "failed", Error: lastErr.Error(), UpdatedAt: time.Now()})
		if ctx.Err() != nil {
			break
		}
	}
	result.status = "failed"
	result.err = lastErr
	return result
}

func resolveTemplates(node Node, args map[string]any, outputs map[string]json.RawMessage) (string, map[string]any) {
	prompt := node.Prompt
	for id, output := range outputs {
		var value map[string]any
		_ = json.Unmarshal(output, &value)
		for _, key := range []string{"reply", "result"} {
			if text, ok := value[key].(string); ok {
				prompt = strings.ReplaceAll(prompt, "{{nodes."+id+"."+key+"}}", text)
			}
		}
	}
	if item, ok := args["_item"]; ok {
		prompt = strings.ReplaceAll(prompt, "{{item}}", fmt.Sprint(item))
	}
	return prompt, args
}

func cloneArgs(args map[string]any) map[string]any {
	if args == nil {
		return map[string]any{}
	}
	data, _ := json.Marshal(args)
	var clone map[string]any
	_ = json.Unmarshal(data, &clone)
	return clone
}

func (e *Engine) view(record *store.WorkflowDefinitionRecord) (*RunView, error) {
	nodes, err := e.store.WorkflowNodeRecords(record.ID)
	if err != nil {
		return nil, err
	}
	var definition Definition
	if err := json.Unmarshal(record.Definition, &definition); err != nil {
		return nil, err
	}
	return &RunView{Record: record, Definition: definition, Nodes: nodes}, nil
}

func (e *Engine) Get(runID string) (*RunView, error) {
	record, err := e.store.WorkflowDefinition(runID)
	if err != nil || record == nil {
		return nil, fmt.Errorf("workflow run not found")
	}
	return e.view(record)
}
func (e *Engine) List(limit int) ([]*store.WorkflowDefinitionRecord, error) {
	return e.store.ListWorkflows(limit)
}
func (e *Engine) Cancel(runID string) bool {
	e.mu.Lock()
	cancel := e.active[runID]
	e.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	if record, _ := e.store.WorkflowDefinition(runID); record != nil {
		record.Status = "cancelled"
		_ = e.store.SaveWorkflow(record)
	}
	return true
}
