package benchmark

import (
	"agent-core/internal/agent"
	"agent-core/internal/compressor"
	"agent-core/internal/config"
	"agent-core/internal/llm"
	"agent-core/internal/memory"
	"agent-core/internal/model"
	"agent-core/internal/prompt"
	"agent-core/internal/security"
	"agent-core/internal/store"
	"agent-core/internal/tool"
	"agent-core/internal/tool/builtin"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Runner executes and grades isolated benchmark plans.
type Runner struct {
	root   string
	mu     sync.Mutex
	active map[string]context.CancelFunc
}

func NewRunner(root string) (*Runner, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	return &Runner{root: root, active: make(map[string]context.CancelFunc)}, nil
}

// Start persists the complete plan before starting a goroutine.
func (r *Runner) Start(manifest Manifest) (*Plan, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	if manifest.Concurrency <= 0 {
		manifest.Concurrency = 1
	}
	if manifest.Concurrency > 4 {
		manifest.Concurrency = 4
	}
	if manifest.Repetitions <= 0 {
		manifest.Repetitions = 1
	}
	if manifest.Repetitions > 100 {
		return nil, fmt.Errorf("repetitions exceeds 100")
	}
	now := time.Now().UTC()
	plan := &Plan{ID: uuid.NewString(), Name: manifest.Name, Manifest: manifest,
		ManifestDigest: manifest.Digest(), Status: "running", CreatedAt: now, UpdatedAt: now}
	if err := r.savePlan(plan); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	r.active[plan.ID] = cancel
	r.mu.Unlock()
	// The caller may serialize the initial plan while execution updates status
	// and results. Give the worker a separate record to avoid that data race.
	executionPlan := *plan
	go func() {
		defer cancel()
		r.execute(ctx, &executionPlan)
	}()
	return plan, nil
}

// Run executes a plan synchronously.
func (r *Runner) Run(ctx context.Context, manifest Manifest) (*Plan, error) {
	plan, err := r.Start(manifest)
	if err != nil {
		return nil, err
	}
	for {
		current, err := r.Get(plan.ID)
		if err != nil {
			return nil, err
		}
		if current.Status != "running" {
			return current, nil
		}
		select {
		case <-ctx.Done():
			r.Cancel(plan.ID)
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (r *Runner) execute(ctx context.Context, plan *Plan) {
	defer func() {
		r.mu.Lock()
		delete(r.active, plan.ID)
		r.mu.Unlock()
	}()
	jobs := make(chan CaseResult, len(plan.Manifest.Cases)*plan.Manifest.Repetitions)
	for _, item := range plan.Manifest.Cases {
		for repetition := 1; repetition <= plan.Manifest.Repetitions; repetition++ {
			jobs <- CaseResult{CaseID: item.ID, Repetition: repetition}
		}
	}
	close(jobs)
	workers := plan.Manifest.Concurrency
	var wg sync.WaitGroup
	results := make(chan CaseResult, cap(jobs))
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				if ctx.Err() != nil {
					job.Status = "cancelled"
					job.Error = ctx.Err().Error()
					results <- job
					continue
				}
				var item Case
				for _, candidate := range plan.Manifest.Cases {
					if candidate.ID == job.CaseID {
						item = candidate
						break
					}
				}
				results <- r.runCase(ctx, plan, item, job.Repetition)
			}
		}()
	}
	wg.Wait()
	close(results)
	for result := range results {
		plan.Results = append(plan.Results, result)
		if err := r.appendResult(plan.ID, result); err != nil {
			plan.Status = "error"
			plan.UpdatedAt = time.Now().UTC()
			_ = r.savePlan(plan)
			return
		}
	}
	if ctx.Err() != nil {
		plan.Status = "cancelled"
	} else {
		plan.Status = "completed"
	}
	plan.UpdatedAt = time.Now().UTC()
	_ = r.savePlan(plan)
}

func (r *Runner) runCase(ctx context.Context, plan *Plan, item Case, repetition int) CaseResult {
	started := time.Now()
	result := CaseResult{CaseID: item.ID, Repetition: repetition, StartedAt: started}
	caseRoot := filepath.Join(r.root, "workspaces", plan.ID, fmt.Sprintf("%s-%d", sanitizeName(item.ID), repetition))
	if err := os.MkdirAll(caseRoot, 0o700); err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}
	workspace := filepath.Join(caseRoot, "workspace")
	home := filepath.Join(caseRoot, "home")
	_ = os.MkdirAll(workspace, 0o700)
	_ = os.MkdirAll(home, 0o700)
	if plan.Manifest.Fixture != "" {
		if err := copyFixture(plan.Manifest.Fixture, workspace); err != nil {
			result.Status = "failed"
			result.Error = err.Error()
			return result
		}
	}
	caseCtx, cancel := context.WithTimeout(ctx, time.Duration(plan.Manifest.TimeoutSeconds)*time.Second)
	defer cancel()
	reply, runID, conversation, usage, ttfb, err := r.executeAgent(caseCtx, plan, item.Prompt, workspace, home)
	result.TTFBMS = ttfb
	result.RunID, result.Conversation, result.Reply = runID, conversation, reply
	if usage != nil {
		result.PromptTokens, result.OutputTokens, result.TotalTokens = usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens
	}
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
	} else {
		grader := grade(caseCtx, plan, item, workspace, reply)
		result.Grader = &grader
		if grader.Passed {
			result.Status = "passed"
		} else {
			result.Status = "failed"
		}
	}
	result.FinishedAt = time.Now()
	result.DurationMS = result.FinishedAt.Sub(started).Milliseconds()
	return result
}

func (r *Runner) executeAgent(ctx context.Context, plan *Plan, promptText, workspace, home string) (string, string, string, *model.UsageInfo, int64, error) {
	cfg := config.DefaultConfig()
	cfg.Workspace.Path = workspace
	cfg.Tools.WorkDir = workspace
	cfg.Store.DBPath = filepath.Join(home, "agent.db")
	cfg.Store.VectorBackend = "none"
	cfg.LLM.BaseURL = plan.Manifest.BaseURL
	cfg.LLM.APIFormat = plan.Manifest.APIFormat
	cfg.LLM.APIKey = plan.Manifest.APIKey
	cfg.LLM.Model = plan.Manifest.Model
	cfg.LLM.MaxRetries = 0
	cfg.Tools.TerminalEnabled = false
	cfg.Tools.ApprovalMode = plan.Manifest.PermissionMode
	if cfg.Tools.ApprovalMode == "" {
		cfg.Tools.ApprovalMode = "edit"
	}
	cfg.Features.ModelIO = false
	db, err := store.NewSQLite(cfg.Store.DBPath)
	if err != nil {
		return "", "", "", nil, 0, err
	}
	defer db.Close()
	provider := llm.NewClient(cfg.LLM)
	registry := tool.NewRegistry()
	processes := tool.NewProcessRegistry()
	if err := builtin.RegisterAll(registry, workspace, processes, false); err != nil {
		return "", "", "", nil, 0, err
	}
	permissions := tool.NewPermissionManager(cfg.Tools.ApprovalMode)
	registry.SetPolicyCheck(func(ctx context.Context, name string, args map[string]any) error {
		registered, _ := registry.Get(name)
		return permissions.Authorize(ctx, registered, args)
	})
	memService := memory.NewService(db, nil, nil, cfg.Memory)
	orchestrator := agent.NewOrchestrator(provider, registry, processes, db, memService,
		compressor.NewCompressor(cfg.Compression, compressor.NewProviderSummarizer(provider)), prompt.NewBuilder(), cfg)
	orchestrator.SetCheckpointStore(agent.NewSQLiteCheckpointStore(db))
	orchestrator.SetPermissionManager(permissions)
	defer orchestrator.StopAll()
	events := make(chan model.StreamEvent, 64)
	go orchestrator.ChatStream(ctx, model.ChatRequest{Message: promptText, Source: "benchmark"}, events)
	var reply, runID, conversation string
	var usage *model.UsageInfo
	var ttfb int64
	started := time.Now()
	for event := range events {
		if event.Type == "chunk" && ttfb == 0 {
			ttfb = time.Since(started).Milliseconds()
		}
		if event.Type == "conv_id" {
			runID, conversation = event.RunID, event.ConversationID
		}
		if event.Type == "done" {
			reply, usage = event.Reply, event.Usage
		}
		if event.Type == "error" || event.Type == "cancelled" {
			return reply, runID, conversation, usage, ttfb, fmt.Errorf("%s", event.Message)
		}
	}
	if reply == "" {
		return "", runID, conversation, usage, ttfb, fmt.Errorf("agent returned no final response")
	}
	return reply, runID, conversation, usage, ttfb, nil
}

func grade(ctx context.Context, plan *Plan, item Case, workspace, reply string) GraderResult {
	expect := item.Expect
	switch expect.Type {
	case "exact":
		return GraderResult{Passed: strings.TrimSpace(reply) == expect.Value}
	case "contains":
		return GraderResult{Passed: strings.Contains(reply, expect.Value)}
	case "regex":
		matched, err := regexp.MatchString(expect.Value, reply)
		return GraderResult{Passed: matched, Error: errorString(err)}
	case "json_schema":
		var value any
		if err := json.Unmarshal([]byte(reply), &value); err != nil {
			return GraderResult{Passed: false, Error: err.Error()}
		}
		result, err := security.ValidateJSONSchema(expect.Schema, value)
		if err != nil {
			return GraderResult{Passed: false, Error: err.Error()}
		}
		if result.Valid() {
			return GraderResult{Passed: true}
		}
		if len(result.Errors()) > 0 {
			return GraderResult{Passed: false, Output: result.Errors()[0].String()}
		}
		return GraderResult{Passed: false, Error: "schema validation failed"}
	case "file_exists":
		info, err := os.Stat(filepath.Join(workspace, expect.Path))
		return GraderResult{Passed: err == nil && !info.IsDir(), Error: errorString(err)}
	case "argv":
		return runArgvGrader(ctx, expect.Argv, workspace, reply)
	default:
		return GraderResult{Passed: false, Error: "unknown grader"}
	}
}

func runArgvGrader(ctx context.Context, argv []string, workspace, reply string) GraderResult {
	if len(argv) == 0 {
		return GraderResult{Passed: false, Error: "empty grader argv"}
	}
	args := make([]string, len(argv))
	for i, value := range argv {
		args[i] = strings.ReplaceAll(strings.ReplaceAll(value, "{reply}", reply), "{workspace}", workspace)
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = workspace
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "LC_ALL=C"}
	output, err := cmd.CombinedOutput()
	return GraderResult{Passed: err == nil, Output: string(output), Error: errorString(err)}
}

func (r *Runner) Get(id string) (*Plan, error) {
	data, err := os.ReadFile(filepath.Join(r.root, "reports", id, "plan.json"))
	if err != nil {
		return nil, err
	}
	var plan Plan
	if err := json.Unmarshal(data, &plan); err != nil {
		return nil, err
	}
	return &plan, nil
}

func (r *Runner) savePlan(plan *Plan) error {
	directory := filepath.Join(r.root, "reports", plan.ID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(plan, "", "  ")
	temp, err := os.CreateTemp(directory, "plan-*")
	if err != nil {
		return err
	}
	path := temp.Name()
	defer os.Remove(path)
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(path, filepath.Join(directory, "plan.json"))
}

func (r *Runner) appendResult(id string, result CaseResult) error {
	directory := filepath.Join(r.root, "reports", id)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(directory, "results.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	data, _ := json.Marshal(result)
	_, err = file.Write(append(data, '\n'))
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func (r *Runner) Cancel(id string) bool {
	r.mu.Lock()
	cancel := r.active[id]
	r.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

func copyFixture(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(source, path)
		if relative == "." {
			return nil
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
}

func sanitizeName(value string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return '_'
	}, value)
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
