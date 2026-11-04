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
	"sort"
	"strconv"
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
	if !e.cfg.Features.Workflow || !e.cfg.Workflow.Enabled {
		return nil, fmt.Errorf("workflow is disabled")
	}
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
	if err := definition.Validate(e.cfg.Workflow.MaxNodes); err != nil {
		return nil, err
	}
	top, err := e.store.GetRun(runID)
	if err != nil || top == nil {
		return nil, fmt.Errorf("workflow run metadata not found")
	}
	return e.execute(ctx, top, record, definition)
}

func (e *Engine) execute(parent context.Context, top *model.Run, record *store.WorkflowDefinitionRecord, definition Definition) (*RunView, error) {
	ctx, cancel := context.WithCancel(parent)
	if e.cfg.Workflow.TimeoutSeconds > 0 {
		cancel()
		ctx, cancel = context.WithTimeout(parent, time.Duration(e.cfg.Workflow.TimeoutSeconds)*time.Second)
	}
	defer cancel()
	e.mu.Lock()
	if _, exists := e.active[record.ID]; exists {
		e.mu.Unlock()
		return nil, fmt.Errorf("workflow is already running")
	}
	e.active[record.ID] = cancel
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.active, record.ID)
		e.mu.Unlock()
	}()
	record.Status = "running"
	if err := e.store.SaveWorkflow(record); err != nil {
		return nil, err
	}
	top.Status, top.FinishedAt = model.RunStatusRunning, nil
	if err := e.store.SaveRun(top); err != nil {
		return nil, err
	}
	finish := func(status string) (*RunView, error) {
		record.Status = status
		top.Status = map[string]model.RunStatus{"completed": model.RunStatusCompleted, "failed": model.RunStatusFailed, "cancelled": model.RunStatusCancelled}[status]
		now := time.Now()
		top.FinishedAt = &now
		if err := e.store.SaveRun(top); err != nil {
			return nil, err
		}
		if err := e.store.SaveWorkflow(record); err != nil {
			return nil, err
		}
		return e.view(record)
	}
	previous, err := e.store.WorkflowNodeRecords(record.ID)
	if err != nil {
		return nil, err
	}
	cache := make(map[string]json.RawMessage)
	attempts := make(map[string]int)
	for _, node := range previous {
		if node.Attempt > attempts[node.NodeID] {
			attempts[node.NodeID] = node.Attempt
		}
		if node.Status == "completed" {
			cache[node.NodeID+"\x00"+node.InputHash] = node.Result
		}
	}
	tasks := make(map[string]task)
	for _, node := range definition.Nodes {
		if len(node.Items) == 0 {
			tasks[node.ID] = task{key: node.ID, node: node, args: cloneArgs(node.Args), attempt: attempts[node.ID] + 1}
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
	for key := range tasks {
		status[key] = ""
	}
	outputs := make(map[string]json.RawMessage)
	var agentCalls atomic.Int64
	for _, saved := range previous {
		if current, ok := tasks[saved.NodeID]; ok && current.node.Type == "agent" {
			agentCalls.Add(1)
		}
	}
	remaining := len(tasks)
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return finish("cancelled")
		}
		var ready []task
		progress := false
		for key, current := range tasks {
			if status[key] != "" {
				continue
			}
			decision := dependencyDecision(current.node, status)
			if decision == "skipped" {
				status[key] = decision
				remaining--
				progress = true
				continue
			}
			if decision == "ready" {
				current.prompt, current.args = resolveTemplates(current.node, current.args, outputs)
				// Preserve existing hashes for previously completed tasks whose
				// effective inputs did not change. Scope cache entries by task ID
				// so identical fan-out items cannot borrow each other's results.
				var hashArgs any = current.args
				if len(current.args) == 0 {
					hashArgs = nil
				}
				hashInput, _ := json.Marshal(map[string]any{"node": current.node.ID, "prompt": current.prompt, "args": hashArgs, "item": current.args["_item"]})
				digest := sha256.Sum256(hashInput)
				current.hash = hex.EncodeToString(digest[:])
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
			return finish("failed")
		}
		limit := e.cfg.Workflow.MaxConcurrency
		if limit > len(ready) {
			limit = len(ready)
		}
		semaphore := make(chan struct{}, limit)
		results := make(chan taskResult, len(ready))
		var wg sync.WaitGroup
		for _, current := range ready {
			if cached, ok := cache[current.key+"\x00"+current.hash]; ok {
				status[current.key] = "completed"
				remaining--
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
			remaining--
			if result.err == nil {
				outputs[result.task.key] = result.output
			}
		}
	}
	if ctx.Err() != nil {
		return finish("cancelled")
	}
	finalStatus := "completed"
	for _, state := range status {
		if state == "failed" {
			finalStatus = "failed"
			break
		}
	}
	return finish(finalStatus)
}

func dependencyDecision(node Node, status map[string]string) string {
	failed := false
	skipped := false
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
			if state == "skipped" {
				skipped = true
			}
		}
	}
	if failed && node.Condition != "on_failure" && node.Condition != "always" {
		return "skipped"
	}
	if !failed && node.Condition == "on_failure" {
		return "skipped"
	}
	if skipped && !failed && node.Condition != "always" {
		return "skipped"
	}
	return "ready"
}

func (e *Engine) runTask(ctx context.Context, top *model.Run, current task, agentCalls *atomic.Int64) taskResult {
	result := taskResult{task: current, status: "completed"}
	var lastErr error
	for offset := 0; offset <= current.node.Retry; offset++ {
		if lastErr = ctx.Err(); lastErr != nil {
			break
		}
		attempt := current.attempt + offset
		if err := e.store.SaveWorkflowNode(&store.WorkflowNodeRecord{RunID: top.ID, NodeID: current.key,
			Attempt: attempt, InputHash: current.hash, Status: "running", UpdatedAt: time.Now()}); err != nil {
			lastErr = err
			break
		}
		callCtx := ctx
		cancel := func() {}
		if current.node.TimeoutSeconds > 0 {
			callCtx, cancel = context.WithTimeout(ctx, time.Duration(current.node.TimeoutSeconds)*time.Second)
		}
		var output json.RawMessage
		switch current.node.Type {
		case "agent":
			if calls := agentCalls.Add(1); e.cfg.Workflow.MaxAgentCalls > 0 && calls > int64(e.cfg.Workflow.MaxAgentCalls) {
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
			args := cloneArgs(current.args)
			delete(args, "_item")
			text, err := e.registry.Dispatch(tool.WithExecutionInfo(callCtx, info), current.node.Tool, args)
			if err != nil {
				lastErr = err
			} else {
				output, _ = json.Marshal(map[string]any{"result": text})
			}
		}
		cancel()
		if lastErr == nil {
			result.output = output
			if err := e.store.SaveWorkflowNode(&store.WorkflowNodeRecord{RunID: top.ID, NodeID: current.key,
				Attempt: attempt, InputHash: current.hash, Status: "completed", Result: output, UpdatedAt: time.Now()}); err != nil {
				lastErr = err
				break
			}
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
	var replacements []string
	type fanoutValue struct {
		index int
		text  string
	}
	groups := make(map[string][]fanoutValue)
	ids := make([]string, 0, len(outputs))
	for id := range outputs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		var value map[string]any
		_ = json.Unmarshal(outputs[id], &value)
		for _, key := range []string{"reply", "result"} {
			if text, ok := value[key].(string); ok {
				replacements = append(replacements, "{{nodes."+id+"."+key+"}}", text)
				if bracket := strings.LastIndexByte(id, '['); bracket >= 0 && strings.HasSuffix(id, "]") {
					if index, err := strconv.Atoi(id[bracket+1 : len(id)-1]); err == nil {
						group := id[:bracket] + "." + key
						groups[group] = append(groups[group], fanoutValue{index: index, text: text})
					}
				}
			}
		}
	}
	for group, values := range groups {
		sort.Slice(values, func(i, j int) bool { return values[i].index < values[j].index })
		texts := make([]string, len(values))
		for i, value := range values {
			texts[i] = value.text
		}
		replacements = append(replacements, "{{nodes."+group+"}}", strings.Join(texts, "\n"))
	}
	if item, ok := args["_item"]; ok {
		replacements = append(replacements, "{{item}}", fmt.Sprint(item))
	}
	replacer := strings.NewReplacer(replacements...)
	var expand func(any) any
	expand = func(value any) any {
		switch v := value.(type) {
		case string:
			return replacer.Replace(v)
		case map[string]any:
			result := make(map[string]any, len(v))
			for key, child := range v {
				result[key] = expand(child)
			}
			return result
		case []any:
			result := make([]any, len(v))
			for i, child := range v {
				result[i] = expand(child)
			}
			return result
		default:
			return value
		}
	}
	return replacer.Replace(node.Prompt), expand(args).(map[string]any)
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

// HasActive reports whether the workflow is still executing or finishing.
func (e *Engine) HasActive(runID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, active := e.active[runID]
	return active
}

func (e *Engine) Cancel(runID string) bool {
	e.mu.Lock()
	cancel := e.active[runID]
	e.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}
