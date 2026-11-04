package server

import (
	"agent-core/internal/benchmark"
	"agent-core/internal/commands"
	"agent-core/internal/model"
	"agent-core/internal/workflow"
	"agent-core/internal/workspace"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

type commandResult struct {
	Command string `json:"command"`
	Action  string `json:"action"`
	Data    any    `json:"data"`
	Reply   string `json:"reply"`
}

func (s *Server) resolveChat(ctx context.Context, req model.ChatRequest) (model.ChatRequest, *commandResult, error) {
	trimmed := strings.TrimSpace(req.Message)
	if !strings.HasPrefix(trimmed, "/") || s.services.Commands == nil {
		return req, nil, nil
	}
	name, args := commandName(trimmed)
	builtin, isBuiltin := commands.Builtin(name)
	if !isBuiltin {
		expansion, err := s.services.Commands.Expand(name, args)
		if err != nil {
			return req, nil, err
		}
		req.Message = expansion.Expanded
		return req, nil, nil
	}
	if name == "help" {
		return req, &commandResult{Command: name, Action: "help", Data: map[string]any{"builtin": commands.Builtins(), "custom": s.services.Commands.Discover()}, Reply: "Available commands returned"}, nil
	}
	if name == "login" || name == "logout" || name == "plugins" || name == "plugin" {
		return req, nil, fmt.Errorf("/%s requires the account/plugin capability, which is not enabled in AgentCore", name)
	}
	switch name {
	case "new", "clear":
		conversation := &model.Conversation{ID: newUUID(), Source: "command", CreatedAt: time.Now(), UpdatedAt: time.Now()}
		if err := s.store.CreateConversation(conversation); err != nil {
			return req, nil, err
		}
		return req, &commandResult{Command: name, Action: "conversation", Data: conversation, Reply: "New conversation created"}, nil
	case "resume", "continue":
		if args == "" {
			conversations, err := s.store.ListConversations("", 50, 0)
			return req, &commandResult{Command: name, Action: "list", Data: conversations, Reply: "Saved conversations returned"}, err
		}
		conversation, err := s.store.GetConversation(args)
		if err != nil || conversation == nil {
			return req, nil, fmt.Errorf("conversation not found")
		}
		return req, &commandResult{Command: name, Action: "conversation", Data: conversation, Reply: "Conversation loaded"}, nil
	case "compact":
		if !s.cfg.Features.Compact {
			return req, nil, fmt.Errorf("features.compact is disabled")
		}
		if req.ConversationID == "" {
			return req, nil, fmt.Errorf("conversation_id is required for /compact")
		}
		summary, err := s.orchestrator.CompactConversation(ctx, req.ConversationID, args)
		return req, &commandResult{Command: name, Action: "compact", Data: map[string]string{"summary": summary}, Reply: "Conversation compacted"}, err
	case "skill":
		if !s.cfg.Features.Skill || !s.cfg.Skills.Enabled {
			return req, nil, fmt.Errorf("skills are disabled")
		}
		if args == "" {
			return req, &commandResult{Command: name, Action: "skills", Data: s.services.Skills.Discover(), Reply: "Skills returned"}, nil
		}
		skillName, task := splitCommandArgs(args)
		if task == "" {
			task = "Load the skill and follow its instructions."
		}
		req.Message = fmt.Sprintf("Use the skill tool with name %q before doing this task. Task: %s", skillName, task)
		return req, nil, nil
	case "init":
		req.Message = "Inspect the current workspace and create or update AGENTS.md with project-specific guidance. Preserve useful existing instructions. Notes: " + args
		return req, nil, nil
	case "workflow":
		req.Message = "Design a declarative YAML DAG workflow for this task, using agent/tool nodes and bounded dependencies. Present it for user confirmation before running: " + args
		return req, nil, nil
	case "expert":
		if args == "" || strings.HasPrefix(args, "status") || strings.HasPrefix(args, "list") {
			return s.workflowCommand(ctx, req, name, "list", args)
		}
		if strings.HasPrefix(args, "resume ") || strings.HasPrefix(args, "cancel ") || strings.HasPrefix(args, "stop ") {
			return s.workflowCommand(ctx, req, name, strings.Split(args, " ")[0], strings.TrimSpace(strings.SplitN(args, " ", 2)[1]))
		}
		definition := workflow.Definition{SchemaVersion: 1, Name: "expert-review", Description: "Review and verify", Nodes: []workflow.Node{
			{ID: "review", Type: "agent", Prompt: "Review the task and produce concrete findings: " + args},
			{ID: "verify", Type: "agent", Prompt: "Verify the review findings and provide a final answer. Findings: {{nodes.review.reply}}", DependsOn: []string{"review"}},
		}}
		result, err := s.services.Workflow.Start(ctx, definition)
		return req, &commandResult{Command: name, Action: "workflow", Data: result, Reply: "Expert workflow completed"}, err
	case "dwf":
		return s.workflowCommand(ctx, req, name, "list", args)
	case "mcp":
		if !s.cfg.Features.MCP {
			return req, nil, fmt.Errorf("MCP is disabled")
		}
		if args == "" || args == "list" || args == "status" {
			return req, &commandResult{Command: name, Action: "mcp", Data: s.mcpManager.ListServers(), Reply: "MCP servers returned"}, nil
		}
		parts := strings.Fields(args)
		if len(parts) == 2 && parts[0] == "disconnect" {
			return req, &commandResult{Command: name, Action: "disconnect", Data: parts[1], Reply: "MCP server disconnected"}, s.mcpManager.Disconnect(parts[1])
		}
		return req, nil, fmt.Errorf("use /v1/mcp with transport config to connect; disconnect requires a server name")
	case "effort", "locale", "language", "mode", "model", "goal", "target":
		return s.conversationPreference(req, builtin.Name, args)
	case "trust":
		return s.trustCommand(ctx, req, args)
	case "snapshot", "rewind":
		if !s.cfg.Features.Rewind {
			return req, nil, fmt.Errorf("rewind is disabled")
		}
		return s.snapshotCommand(ctx, req, name, args)
	case "fork":
		return s.forkCommand(req, args)
	case "agents":
		if !s.cfg.Features.Subagent {
			return req, nil, fmt.Errorf("subagents are disabled")
		}
		parts := strings.Fields(args)
		if len(parts) == 2 && parts[0] == "stop" {
			if !s.services.Subagents.Stop(parts[1]) {
				return req, nil, fmt.Errorf("active agent not found")
			}
			return req, &commandResult{Command: name, Action: "stop", Data: parts[1], Reply: "Agent stopped"}, nil
		}
		runs, err := s.services.Subagents.List("", 100)
		return req, &commandResult{Command: name, Action: "agents", Data: runs, Reply: "Agents returned"}, err
	case "trace":
		if s.services.Recorder == nil {
			return req, nil, fmt.Errorf("model-io is disabled")
		}
		if req.ConversationID == "" {
			return req, nil, fmt.Errorf("conversation_id is required for /trace")
		}
		run, err := s.store.LatestRun(req.ConversationID)
		if err != nil || run == nil {
			return req, nil, fmt.Errorf("run not found")
		}
		manifest, err := s.services.Recorder.ExportTrace(run.ID, s.cfg.Telemetry.Directory+"/exports/"+run.ID)
		return req, &commandResult{Command: name, Action: "trace", Data: manifest, Reply: "Trace exported"}, err
	case "benchmark":
		if args == "" {
			return req, nil, fmt.Errorf("usage: /benchmark <manifest.json>")
		}
		data, err := os.ReadFile(args)
		if err != nil {
			return req, nil, err
		}
		var manifest benchmark.Manifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			return req, nil, err
		}
		plan, err := s.services.Benchmark.Start(manifest)
		return req, &commandResult{Command: name, Action: "benchmark", Data: plan, Reply: "Benchmark started"}, err
	default:
		return req, nil, fmt.Errorf("unsupported built-in command /%s", name)
	}
}

func (s *Server) conversationPreference(req model.ChatRequest, name, args string) (model.ChatRequest, *commandResult, error) {
	if req.ConversationID == "" {
		return req, nil, fmt.Errorf("conversation_id is required for /%s", name)
	}
	if args == "" {
		conversation, _ := s.store.GetConversation(req.ConversationID)
		return req, &commandResult{Command: name, Action: "preference", Data: conversation, Reply: "Conversation preference returned"}, nil
	}
	updates := map[string]string{}
	switch name {
	case "effort":
		updates["effort"] = args
	case "locale", "language":
		if args != "auto" && args != "en-US" && args != "zh-CN" {
			return req, nil, fmt.Errorf("locale must be auto, en-US, or zh-CN")
		}
		updates["locale"] = args
	case "mode":
		if permissionRank(args) < 0 {
			return req, nil, fmt.Errorf("mode is invalid")
		}
		updates["mode"] = args
	case "model":
		updates["model"] = args
	case "goal", "target":
		if args == "clear" {
			updates["goal"] = ""
			updates["goal_status"] = "cleared"
		} else if args == "pause" {
			updates["goal_status"] = "paused"
		} else if args == "resume" {
			updates["goal_status"] = "active"
		} else {
			updates["goal"] = strings.TrimPrefix(args, "replace ")
			updates["goal_status"] = "active"
		}
	}
	if err := s.store.UpdateConversationMetadata(req.ConversationID, updates); err != nil {
		return req, nil, err
	}
	conversation, _ := s.store.GetConversation(req.ConversationID)
	return req, &commandResult{Command: name, Action: "preference", Data: conversation, Reply: "Conversation preference updated"}, nil
}

func (s *Server) trustCommand(ctx context.Context, req model.ChatRequest, args string) (model.ChatRequest, *commandResult, error) {
	status, err := s.services.Trust.Review(s.services.Project.Workspace, s.services.Project.Declarations)
	if err != nil {
		return req, nil, err
	}
	switch strings.Fields(args + " status")[0] {
	case "grant":
		status, err = s.services.Trust.Grant(status.Workspace, status.BundleDigest, status.Declarations)
	case "revoke":
		err = s.services.Trust.Revoke(status.Workspace, status.BundleDigest)
	}
	return req, &commandResult{Command: "trust", Action: "trust", Data: status, Reply: "Workspace trust status returned"}, err
}

func (s *Server) snapshotCommand(ctx context.Context, req model.ChatRequest, name, args string) (model.ChatRequest, *commandResult, error) {
	parts := strings.Fields(args)
	action := "list"
	if len(parts) > 0 {
		action = parts[0]
	}
	switch action {
	case "create":
		snapshot, err := s.services.Workspace.CreateSnapshot(ctx)
		return req, &commandResult{Command: name, Action: "create", Data: snapshot, Reply: "Snapshot created"}, err
	case "list", "latest", "":
		if action == "latest" || name == "rewind" && len(parts) == 1 {
			return s.restoreSnapshot(ctx, req, name, "latest", false)
		}
		snapshots, err := s.services.Workspace.ListSnapshots(ctx, 100)
		return req, &commandResult{Command: name, Action: "list", Data: snapshots, Reply: "Snapshots returned"}, err
	case "restore":
		if len(parts) < 2 {
			return req, nil, fmt.Errorf("snapshot ID is required")
		}
		return s.restoreSnapshot(ctx, req, name, parts[1], len(parts) > 2 && parts[2] == "--allow-changes")
	default:
		return s.restoreSnapshot(ctx, req, name, action, false)
	}
}

func (s *Server) restoreSnapshot(ctx context.Context, req model.ChatRequest, name, id string, allow bool) (model.ChatRequest, *commandResult, error) {
	if id == "latest" {
		snapshots, err := s.services.Workspace.ListSnapshots(ctx, 1)
		if err != nil || len(snapshots) == 0 {
			return req, nil, fmt.Errorf("no snapshots available")
		}
		id = snapshots[0].ID
	}
	compensation, err := s.services.Workspace.RestoreSnapshot(ctx, id, workspace.RestoreOptions{AllowChanges: allow})
	return req, &commandResult{Command: name, Action: "restore", Data: compensation, Reply: "Snapshot restored with compensation"}, err
}

func (s *Server) forkCommand(req model.ChatRequest, args string) (model.ChatRequest, *commandResult, error) {
	if req.ConversationID == "" {
		return req, nil, fmt.Errorf("conversation_id is required for /fork")
	}
	clone, err := s.store.CloneConversation(req.ConversationID)
	if err != nil {
		return req, nil, err
	}
	return req, &commandResult{Command: "fork", Action: "fork", Data: clone, Reply: "Conversation forked; restore a snapshot explicitly to rewind the shared workspace"}, nil
}

func (s *Server) workflowCommand(ctx context.Context, req model.ChatRequest, name, action, args string) (model.ChatRequest, *commandResult, error) {
	if s.services.Workflow == nil {
		return model.ChatRequest{}, nil, fmt.Errorf("workflow unavailable")
	}
	switch action {
	case "resume":
		result, err := s.services.Workflow.Resume(ctx, args)
		return req, &commandResult{Command: name, Action: action, Data: result, Reply: "Workflow resumed"}, err
	case "cancel", "stop":
		if !s.services.Workflow.Cancel(args) {
			return req, nil, fmt.Errorf("active workflow not found")
		}
		return req, &commandResult{Command: name, Action: action, Data: args, Reply: "Workflow cancelled"}, nil
	default:
		result, err := s.services.Workflow.List(100)
		return req, &commandResult{Command: name, Action: "list", Data: result, Reply: "Workflows returned"}, err
	}
}

func commandName(input string) (string, string) {
	fields := strings.SplitN(strings.TrimSpace(input), " ", 2)
	name := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	if len(fields) == 1 {
		return name, ""
	}
	return name, strings.TrimSpace(fields[1])
}
func splitCommandArgs(input string) (string, string) {
	fields := strings.SplitN(input, " ", 2)
	if len(fields) == 1 {
		return fields[0], ""
	}
	return fields[0], strings.TrimSpace(fields[1])
}
func permissionRank(mode string) int {
	switch mode {
	case "plan", "auto":
		return 0
	case "build", "smart":
		return 1
	case "edit":
		return 2
	case "guarded":
		return 3
	case "yolo":
		return 4
	default:
		return -1
	}
}
func newUUID() string { return uuid.NewString() }
