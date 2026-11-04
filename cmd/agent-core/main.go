package main

import (
	"agent-core/internal/agent"
	"agent-core/internal/benchmark"
	"agent-core/internal/commands"
	"agent-core/internal/compressor"
	"agent-core/internal/config"
	"agent-core/internal/llm"
	"agent-core/internal/mcp"
	"agent-core/internal/memory"
	"agent-core/internal/observability"
	"agent-core/internal/project"
	"agent-core/internal/prompt"
	"agent-core/internal/sandbox"
	"agent-core/internal/security"
	"agent-core/internal/server"
	"agent-core/internal/skills"
	"agent-core/internal/store"
	"agent-core/internal/subagents"
	"agent-core/internal/telemetry"
	"agent-core/internal/tool"
	"agent-core/internal/tool/builtin"
	"agent-core/internal/trust"
	"agent-core/internal/version"
	"agent-core/internal/workflow"
	"agent-core/internal/workspace"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	// Check for subcommands
	if len(os.Args) > 1 && os.Args[1] == "mcp-serve" {
		runMCPServer()
		return
	}
	if len(os.Args) > 1 {
		if handled, code := runCLI(os.Args[1:]); handled {
			if code != 0 {
				os.Exit(code)
			}
			return
		}
	}

	configPath := flag.String("config", "agent-core.yaml", "Path to configuration file")
	flag.Parse()

	log.SetFlags(log.Ltime | log.Lshortfile)
	log.Println("[agent-core] Starting Agent Core...")

	// Load configuration
	cfg, projectReport, err := loadEffectiveConfig(*configPath)
	if err != nil {
		log.Fatalf("[agent-core] Failed to load config: %v", err)
	}

	observability.Init(cfg.Logging.Format, cfg.Logging.Level)

	// Validate configuration (security checks for HTTP server mode)
	if err := cfg.Validate(config.ModeHTTP); err != nil {
		log.Fatalf("[agent-core] Config validation failed: %v", err)
	}

	// Ensure workspace directory and SOUL.md exist
	if err := sandbox.EnsureWorkspace(cfg.Workspace.Path); err != nil {
		log.Fatalf("[agent-core] Failed to create workspace: %v", err)
	}
	if err := sandbox.EnsureSoulFile(cfg.Workspace.Path, cfg.Workspace.SoulFile); err != nil {
		log.Printf("[agent-core] Warning: failed to create SOUL.md: %v", err)
	}
	log.Printf("[agent-core] Workspace initialized at %s", cfg.Workspace.Path)

	// Initialize database
	dbStore, err := store.NewSQLite(cfg.Store.DBPath)
	if err != nil {
		log.Fatalf("[agent-core] Failed to initialize database: %v", err)
	}
	if err := dbStore.InterruptAcceptedCommands(); err != nil {
		log.Fatalf("recover accepted runs: %v", err)
	}
	log.Printf("[agent-core] Database initialized at %s", cfg.Store.DBPath)

	// Initialize tool registry
	toolRegistry := tool.NewRegistry()
	toolRegistry.SetConcurrencyLimit(cfg.ToolConcurrency.MaxConcurrency)
	if len(cfg.Tools.EnabledToolsets) > 0 {
		enabled := make(map[string]bool, len(cfg.Tools.EnabledToolsets))
		for _, name := range cfg.Tools.EnabledToolsets {
			enabled[name] = true
		}
		if err := toolRegistry.ConfigureToolsets(tool.DefaultToolsets(), enabled); err != nil {
			log.Fatalf("[agent-core] Invalid toolsets: %v", err)
		}
	}
	processRegistry := tool.NewProcessRegistry()

	// Initialize security policy engine
	policyEngine := security.NewPolicyEngine(cfg.Security.Policies)
	permissions := tool.NewPermissionManager(cfg.Permission.Mode)
	permissions.ConfigureRules(cfg.Permission.AllowedTools, cfg.Permission.DisallowedTools)
	permissions.SetAutoApproveHighRisk(cfg.Permission.AutoApproveHighRisk)
	permissions.SetAlwaysAskTerminal(cfg.Permission.AlwaysAskTerminal)
	toolRegistry.SetPolicyCheck(func(ctx context.Context, toolName string, args map[string]any) error {
		action := security.Action{Resource: resourceForTool(toolName), Operation: operationForTool(toolName), Target: extractTarget(toolName, args)}
		if info, ok := tool.ExecutionInfoFromContext(ctx); ok {
			action.ConvID = info.ConversationID
			action.Metadata = map[string]string{"run_id": info.RunID, "tool_call_id": info.ToolCallID}
		}
		decision := policyEngine.Evaluate(action)
		if !decision.Allowed {
			return fmt.Errorf("%s", decision.Reason)
		}
		registered, _ := toolRegistry.Get(toolName)
		return permissions.Authorize(ctx, registered, args)
	})

	// Register builtin tools
	if err := builtin.RegisterAll(toolRegistry, cfg.Tools.WorkDir, processRegistry, cfg.Tools.TerminalEnabled); err != nil {
		log.Fatalf("[agent-core] Failed to register builtin tools: %v", err)
	}
	if cfg.Tools.TerminalEnabled {
		log.Printf("[agent-core] WARNING: terminal tool is enabled — it is NOT a true sandbox. " +
			"For production, set server.terminalEnabled: false and use container/OS isolation.")
	}
	log.Printf("[agent-core] Registered %d builtin tools", toolRegistry.Count())

	callbacks, err := dbStore.HostCallbacks()
	if err != nil {
		log.Fatalf("[agent-core] Failed to load host callbacks: %v", err)
	}
	for _, callback := range callbacks {
		if !callback.Enabled {
			continue
		}
		var schema tool.ToolSchema
		if err := json.Unmarshal(callback.Schema, &schema); err != nil {
			log.Printf("[host] skipping %s with invalid schema: %v", callback.Name, err)
			continue
		}
		provider := tool.NewHostCallbackWithOptions(tool.HostToolRegistration{Name: callback.Name,
			Description: callback.Description, Schema: schema, CallbackURL: callback.CallbackURL,
			Secret: callback.Secret, AllowPrivate: callback.AllowPrivate})
		if err := toolRegistry.Register(provider, tool.SourceHostCallback, "host"); err != nil {
			log.Printf("[host] failed to restore %s: %v", callback.Name, err)
		}
	}

	// Initialize MCP manager
	mcpManager := mcp.NewManager(toolRegistry)

	// Restore MCP server connections from database
	mcpServers, _ := dbStore.GetMCPServers()
	for _, srv := range mcpServers {
		if _, declared := cfg.MCP.Servers[srv.Name]; declared {
			continue
		}
		if !cfg.Features.MCP || !srv.Enabled {
			continue
		}
		srvCfg := mcp.RestoreConfig(srv.Name, srv.Transport, []byte(srv.Config))

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := mcpManager.Connect(ctx, srvCfg); err != nil {
			log.Printf("[agent-core] Warning: failed to restore MCP server %s: %v", srv.Name, err)
		}
		cancel()
	}

	mcpManager.ConnectConfigured(context.Background(), cfg.MCP, cfg.Features.MCP)

	if status, err := trust.NewManager(dbStore).Review(cfg.Workspace.Path, projectReport.Declarations); err == nil && status.Trusted {
		mcpManager.ConnectConfigured(context.Background(), projectReport.MCP, cfg.Features.MCP)
	}

	// Initialize model-io before any provider clients.
	var recorder *telemetry.Recorder
	if cfg.Features.ModelIO {
		recorder, err = telemetry.NewRecorder(cfg.Telemetry.Directory, telemetry.CapturePolicy(cfg.Telemetry.Mode), cfg.Telemetry.QueueSize, cfg.Telemetry.MaxArtifactBytes)
		if err != nil {
			log.Fatalf("[agent-core] Failed to initialize model-io: %v", err)
		}
	}

	// Initialize memory service
	memService := memory.NewService(dbStore, cfg.Memory)
	if cfg.Features.Memory && cfg.Memory.Use {
		if err := toolRegistry.Register(memory.NewSaveMemoryTool(memService), tool.SourceBuiltin, "builtin"); err != nil {
			log.Fatalf("[agent-core] Register save_memory: %v", err)
		}
		if err := toolRegistry.Register(memory.NewSearchMemoryTool(memService), tool.SourceBuiltin, "builtin"); err != nil {
			log.Fatalf("[agent-core] Register search_memory: %v", err)
		}
	}
	memCount, _ := memService.Count()
	log.Printf("[agent-core] Memory service initialized (%d memories)", memCount)

	// Initialize LLM client (with optional failover)
	var llmProvider llm.Provider
	llmClient := llm.NewClient(cfg.LLM)
	if recorder != nil {
		llmClient.SetRecorder(recorder)
	}
	if len(cfg.LLM.Fallbacks) > 0 {
		providers := []llm.Provider{llmClient}
		for _, fb := range cfg.LLM.Fallbacks {
			fbCfg := cfg.LLM.FallbackConfig(fb)
			fbCfg.APIKey = os.ExpandEnv(fb.APIKey)
			client := llm.NewClient(fbCfg)
			if recorder != nil {
				client.SetRecorder(recorder)
			}
			providers = append(providers, client)
		}
		llmProvider = llm.NewFailoverClient(providers)
		log.Printf("[agent-core] LLM failover initialized (%d providers)", len(providers))
	} else {
		llmProvider = llmClient
	}
	log.Printf("[agent-core] LLM provider initialized (%s)", llmProvider.ModelName())

	// Initialize compressor and prompt builder
	summarizer := compressor.NewProviderSummarizer(llmProvider)
	comp := compressor.NewCompressor(cfg.Compression, summarizer)
	promptBuilder := prompt.NewBuilder()

	// Initialize orchestrator
	orchestrator := agent.NewOrchestrator(
		llmProvider, toolRegistry, processRegistry, dbStore, memService, comp, promptBuilder, cfg,
	)
	orchestrator.SetPermissionManager(permissions)
	if recorder != nil {
		orchestrator.SetRecorder(recorder)
	}

	// Initialize checkpoint store
	checkpointStore := agent.NewSQLiteCheckpointStore(dbStore)
	orchestrator.SetCheckpointStore(checkpointStore)

	workspaceService, err := workspace.New(workspace.Config{Root: cfg.Workspace.Path,
		StateDir: filepath.Join(filepath.Dir(cfg.Store.DBPath), "workspace")})
	if err != nil {
		log.Fatalf("[agent-core] Failed to initialize workspace service: %v", err)
	}
	trustManager := trust.NewManager(dbStore)
	home, _ := os.UserHomeDir()
	skillLoader := skills.NewLoader(skills.Options{Overrides: config.EnabledOverrides(cfg.SkillOverrides), Roots: cfg.Skills.Roots, ProjectDir: cfg.Workspace.Path, UserDir: home})
	commandLoader := commands.NewLoader(commands.Options{Overrides: config.EnabledOverrides(cfg.CommandOverrides), Roots: cfg.Commands.Roots, ProjectDir: cfg.Workspace.Path, UserDir: home})
	orchestrator.SetSkillLoader(skillLoader)
	orchestrator.SetCommandLoader(commandLoader)
	if cfg.Features.Skills {
		if err := toolRegistry.Register(skills.NewTool(skillLoader, trustManager), tool.SourceBuiltin, "builtin"); err != nil {
			log.Fatalf("[agent-core] Register skill tool: %v", err)
		}
	}
	subagentManager := subagents.NewManager(dbStore, orchestrator)
	if cfg.Features.Subagents {
		if err := toolRegistry.Register(subagents.NewTool(subagentManager), tool.SourceBuiltin, "builtin"); err != nil {
			log.Fatalf("[agent-core] Register subagent tool: %v", err)
		}
	}
	workflowEngine := workflow.NewEngine(dbStore, orchestrator, toolRegistry, cfg)
	benchmarkRunner, err := benchmark.NewRunner(filepath.Join(filepath.Dir(cfg.Store.DBPath), "benchmarks"))
	if err != nil {
		log.Fatalf("[agent-core] Failed to initialize benchmark runner: %v", err)
	}

	// Initialize HTTP server
	srv := server.NewServer(cfg, orchestrator, toolRegistry, mcpManager, dbStore, memService)
	srv.SetServices(server.Services{Workspace: workspaceService, Trust: trustManager,
		Skills: skillLoader, Commands: commandLoader, Subagents: subagentManager,
		Workflow: workflowEngine, Recorder: recorder, Benchmark: benchmarkRunner, Project: projectReport})

	httpServer := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // SSE lifetime is governed by run budgets and cancellation.
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1024 * 1024,
	}

	// Start server
	go func() {
		log.Printf("[agent-core] HTTP server listening on %s", cfg.Addr())
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[agent-core] Server error: %v", err)
		}
	}()

	// Print startup banner
	printBanner(cfg)

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("[agent-core] Received signal %v, shutting down...", sig)

	// Graceful shutdown sequence:
	// 1. Stop all active orchestrator runs (cancel contexts)
	orchestrator.StopAll()
	srv.CloseConnections()

	// 2. Shutdown HTTP server (stops accepting new requests, waits for in-flight)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("[agent-core] Grace period elapsed; continuing to wait for actual request drain: %v", err)
		if err := httpServer.Shutdown(context.Background()); err != nil {
			log.Printf("[agent-core] HTTP shutdown failed: %v", err)
		}
	}

	// 3. Disconnect MCP servers and kill subprocesses
	mcpManager.DisconnectAll()
	processRegistry.KillAll()

	// 4. Flush model-io before closing the database.
	if recorder != nil {
		if err := recorder.Close(); err != nil {
			log.Printf("[agent-core] model-io close warning: %v", err)
		}
	}

	// 5. Close database (after all handlers have finished)
	if err := dbStore.Close(); err != nil {
		log.Printf("[agent-core] Warning: database close error: %v", err)
	}

	log.Println("[agent-core] Shutdown complete")
}

func loadEffectiveConfig(path string) (config.Config, project.Report, error) {
	home, _ := os.UserHomeDir()
	userPath := filepath.Join(home, ".agent-core", "config.yaml")
	cfg, err := config.LoadLayered(path, userPath)
	if err != nil {
		return cfg, project.Report{}, err
	}
	report, err := project.LoadAndApply(&cfg, cfg.Workspace.Path)
	if err != nil {
		return cfg, report, err
	}
	return cfg, report, nil
}

func printBanner(cfg config.Config) {
	banner := fmt.Sprintf(`
  ___             _         ____               
 / _ \           | |       / ____|              
/ /_\ \ __ _  ___| |_ ___ | |     ___  _ __ ___ 
|  _  |/ _`+"`"+` |/ _ \ __/ _ \| |    / _ \| '__/ _ \
| | | | (_| |  __/ || (_) | |___| (_) | | |  __/
\_| |_/\__, |\___|\__\___/ \____/\___/|_|  \___|
        __/ |                                    
       |___/                                     

  Agent Core v%s
  Listen:    %s
  Model:     %s
  DB:        %s
  Workspace: %s
  Tools:     builtin + MCP + host callback
`, version.Version, cfg.Addr(), cfg.LLM.Model, cfg.Store.DBPath, cfg.Workspace.Path)

	fmt.Println(banner)
}

// resourceForTool maps a tool name to a security resource type.
func resourceForTool(toolName string) security.Resource {
	switch toolName {
	case "read_file", "write_file", "list_files":
		return security.ResourceFile
	case "run_terminal":
		return security.ResourceTerminal
	case "web_fetch", "web_search":
		return security.ResourceWeb
	case "save_memory", "search_memory":
		return security.ResourceMemory
	default:
		return "other"
	}
}

// operationForTool maps a tool name to the operation type for policy evaluation.
func operationForTool(toolName string) string {
	switch toolName {
	case "read_file", "list_files":
		return "read"
	case "write_file":
		return "write"
	case "run_terminal":
		return "execute"
	case "web_fetch":
		return "fetch"
	case "save_memory":
		return "write"
	case "search_memory":
		return "read"
	default:
		return "execute"
	}
}

// extractTarget extracts the target string from tool arguments for policy evaluation.
func extractTarget(toolName string, args map[string]any) string {
	switch toolName {
	case "read_file", "write_file", "list_files":
		if path, ok := args["path"].(string); ok {
			return path
		}
	case "run_terminal":
		if cmd, ok := args["command"].(string); ok {
			return cmd
		}
	case "web_fetch":
		if url, ok := args["url"].(string); ok {
			return url
		}
	}
	return ""
}

// runMCPServer starts AgentCore as an MCP server (stdio transport).
// Usage: ./agent-core mcp-serve [--config agent-core.yaml]
func runMCPServer() {
	mcpFlags := flag.NewFlagSet("mcp-serve", flag.ExitOnError)
	configPath := mcpFlags.String("config", "agent-core.yaml", "Path to configuration file")
	transport := mcpFlags.String("transport", "stdio", "Transport type (stdio)")
	mcpFlags.Parse(os.Args[2:])

	cfg, _, err := loadEffectiveConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Validate configuration (MCP mode: skip network binding checks)
	if err := cfg.Validate(config.ModeMCP); err != nil {
		fmt.Fprintf(os.Stderr, "Config validation failed: %v\n", err)
		os.Exit(1)
	}

	// Minimal initialization for MCP server mode (no DB, no LLM needed)
	toolRegistry := tool.NewRegistry()
	toolRegistry.SetConcurrencyLimit(cfg.ToolConcurrency.MaxConcurrency)
	if len(cfg.Tools.EnabledToolsets) > 0 {
		enabled := make(map[string]bool, len(cfg.Tools.EnabledToolsets))
		for _, name := range cfg.Tools.EnabledToolsets {
			enabled[name] = true
		}
		if err := toolRegistry.ConfigureToolsets(tool.DefaultToolsets(), enabled); err != nil {
			log.Fatalf("[agent-core] Invalid toolsets: %v", err)
		}
	}
	processRegistry := tool.NewProcessRegistry()

	// Initialize security policy engine (same as HTTP mode)
	policyEngine := security.NewPolicyEngine(cfg.Security.Policies)
	permissions := tool.NewPermissionManager(cfg.Permission.Mode)
	permissions.ConfigureRules(cfg.Permission.AllowedTools, cfg.Permission.DisallowedTools)
	permissions.SetAutoApproveHighRisk(cfg.Permission.AutoApproveHighRisk)
	permissions.SetAlwaysAskTerminal(cfg.Permission.AlwaysAskTerminal)
	toolRegistry.SetPolicyCheck(func(ctx context.Context, toolName string, args map[string]any) error {
		action := security.Action{Resource: resourceForTool(toolName), Operation: operationForTool(toolName), Target: extractTarget(toolName, args)}
		if info, ok := tool.ExecutionInfoFromContext(ctx); ok {
			action.ConvID = info.ConversationID
			action.Metadata = map[string]string{"run_id": info.RunID, "tool_call_id": info.ToolCallID}
		}
		decision := policyEngine.Evaluate(action)
		if !decision.Allowed {
			return fmt.Errorf("%s", decision.Reason)
		}
		registered, _ := toolRegistry.Get(toolName)
		return permissions.Authorize(ctx, registered, args)
	})

	if err := builtin.RegisterAll(toolRegistry, cfg.Tools.WorkDir, processRegistry, cfg.Tools.TerminalEnabled); err != nil {
		log.Fatalf("[agent-core] Failed to register builtin tools: %v", err)
	}

	// Restore MCP connections to include their tools
	var mcpManager *mcp.Manager
	if dbStore, err := store.NewSQLite(cfg.Store.DBPath); err == nil {
		mcpManager = mcp.NewManager(toolRegistry)
		mcpServers, _ := dbStore.GetMCPServers()
		for _, srv := range mcpServers {
			if _, declared := cfg.MCP.Servers[srv.Name]; declared {
				continue
			}
			if !cfg.Features.MCP || !srv.Enabled {
				continue
			}
			var srvCfg mcp.ServerConfig
			srvCfg.Name = srv.Name
			srvCfg.Transport = srv.Transport
			srvCfg.Config = []byte(srv.Config)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := mcpManager.Connect(ctx, srvCfg); err != nil {
				log.Printf("[mcp-serve] Warning: failed to connect MCP server %s: %v", srv.Name, err)
			}
			cancel()
		}
		mcpManager.ConnectConfigured(context.Background(), cfg.MCP, cfg.Features.MCP)
		dbStore.Close()
	}

	server := mcp.NewServer(toolRegistry, *transport)

	// Handle shutdown signals (server.Serve blocks on stdin)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("[mcp-serve] Shutting down...")
		if mcpManager != nil {
			mcpManager.DisconnectAll()
		}
		processRegistry.KillAll()
		os.Exit(0)
	}()

	if err := server.Serve(); err != nil {
		fmt.Fprintf(os.Stderr, "MCP server error: %v\n", err)
	}

	// Cleanup: disconnect all MCP connections and kill subprocesses
	if mcpManager != nil {
		mcpManager.DisconnectAll()
	}
	processRegistry.KillAll()
}
