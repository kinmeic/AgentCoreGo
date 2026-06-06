package main

import (
	"agent-core/internal/agent"
	"agent-core/internal/compressor"
	"agent-core/internal/config"
	"agent-core/internal/llm"
	"agent-core/internal/mcp"
	"agent-core/internal/memory"
	"agent-core/internal/prompt"
	"agent-core/internal/sandbox"
	"agent-core/internal/security"
	"agent-core/internal/server"
	"agent-core/internal/store"
	"agent-core/internal/store/vector"
	"agent-core/internal/tool"
	"agent-core/internal/tool/builtin"
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	// Check for subcommands
	if len(os.Args) > 1 && os.Args[1] == "mcp-serve" {
		runMCPServer()
		return
	}

	configPath := flag.String("config", "agent-core.yaml", "Path to configuration file")
	flag.Parse()

	log.SetFlags(log.Ltime | log.Lshortfile)
	log.Println("[agent-core] Starting Agent Core...")

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("[agent-core] Failed to load config: %v", err)
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
	log.Printf("[agent-core] Database initialized at %s", cfg.Store.DBPath)

	// Initialize embedding client
	var embClient *vector.EmbeddingClient
	if cfg.Embedding.APIKey != "" {
		embClient = vector.NewEmbeddingClient(
			cfg.Embedding.BaseURL,
			cfg.Embedding.APIKey,
			cfg.Embedding.Model,
			cfg.Embedding.Dimensions,
		)
		log.Printf("[agent-core] Embedding client initialized (%s, dim=%d)", cfg.Embedding.Model, cfg.Embedding.Dimensions)
	}

	// Initialize vector store
	var vecStore vector.Store
	vecStore, err = vector.NewSQLiteVec(dbStore.DB(), cfg.Embedding.Dimensions)
	if err != nil {
		log.Printf("[agent-core] Warning: vector store initialization failed: %v", err)
		vecStore = nil
	}

	// Initialize tool registry
	toolRegistry := tool.NewRegistry()
	processRegistry := tool.NewProcessRegistry()

	// Initialize security policy engine
	policyEngine := security.NewPolicyEngine(nil)
	toolRegistry.SetPolicyCheck(func(toolName string, args map[string]any) error {
		action := security.Action{
			Resource:  resourceForTool(toolName),
			Operation: "execute",
			Target:    extractTarget(toolName, args),
		}
		decision := policyEngine.Evaluate(action)
		if !decision.Allowed {
			return fmt.Errorf("%s", decision.Reason)
		}
		return nil
	})

	// Register builtin tools
	builtin.RegisterAll(toolRegistry, cfg.Tools.WorkDir, processRegistry)
	log.Printf("[agent-core] Registered %d builtin tools", toolRegistry.Count())

	// Initialize MCP manager
	mcpManager := mcp.NewManager(toolRegistry)

	// Restore MCP server connections from database
	mcpServers, _ := dbStore.GetMCPServers()
	for _, srv := range mcpServers {
		if !srv.Enabled {
			continue
		}
		var srvCfg mcp.ServerConfig
		srvCfg.Name = srv.Name
		srvCfg.Transport = srv.Transport
		srvCfg.Config = []byte(srv.Config)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := mcpManager.Connect(ctx, srvCfg); err != nil {
			log.Printf("[agent-core] Warning: failed to restore MCP server %s: %v", srv.Name, err)
		}
		cancel()
	}

	// Initialize memory service
	memService := memory.NewService(dbStore, vecStore, embClient, cfg.Memory)
	memCount, _ := memService.Count()
	log.Printf("[agent-core] Memory service initialized (%d memories)", memCount)

	// Initialize LLM client (with optional failover)
	var llmProvider llm.Provider
	llmClient := llm.NewClient(cfg.LLM)
	if len(cfg.LLM.Fallbacks) > 0 {
		providers := []llm.Provider{llmClient}
		for _, fb := range cfg.LLM.Fallbacks {
			fbCfg := config.LLMConfig{
				BaseURL:          fb.BaseURL,
				APIKey:           os.ExpandEnv(fb.APIKey),
				Model:            fb.Model,
				ContextWindow:    fb.ContextWindow,
				Temperature:      fb.Temperature,
				MaxRetries:       cfg.LLM.MaxRetries,
				MaxToolIterations: cfg.LLM.MaxToolIterations,
			}
			providers = append(providers, llm.NewClient(fbCfg))
		}
		llmProvider = llm.NewFailoverClient(providers)
		log.Printf("[agent-core] LLM failover initialized (%d providers)", len(providers))
	} else {
		llmProvider = llmClient
	}
	log.Printf("[agent-core] LLM provider initialized (%s)", llmProvider.ModelName())

	// Initialize compressor and prompt builder
	var summarizer *compressor.Summarizer
	if cfg.LLM.APIKey != "" {
		summarizer = compressor.NewSummarizer(cfg.LLM.BaseURL, cfg.LLM.APIKey, cfg.LLM.Model)
	}
	comp := compressor.NewCompressor(cfg.Compression, summarizer)
	promptBuilder := prompt.NewBuilder()

	// Initialize orchestrator
	orchestrator := agent.NewOrchestrator(
		llmProvider, toolRegistry, processRegistry, dbStore, memService, comp, promptBuilder, cfg,
	)

	// Initialize checkpoint store
	checkpointStore := agent.NewSQLiteCheckpointStore(dbStore)
	orchestrator.SetCheckpointStore(checkpointStore)

	// Initialize HTTP server
	srv := server.NewServer(cfg, orchestrator, toolRegistry, mcpManager, dbStore, memService)

	httpServer := &http.Server{
		Addr:         cfg.Addr(),
		Handler:      srv.Handler(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 10 * time.Minute, // Long timeout for SSE
		IdleTimeout:  120 * time.Second,
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

	// 2. Shutdown HTTP server (stops accepting new requests, waits for in-flight)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	httpServer.Shutdown(shutdownCtx)

	// 3. Disconnect MCP servers and kill subprocesses
	mcpManager.DisconnectAll()
	processRegistry.KillAll()

	// 4. Close database (after all handlers have finished)
	if err := dbStore.Close(); err != nil {
		log.Printf("[agent-core] Warning: database close error: %v", err)
	}

	log.Println("[agent-core] Shutdown complete")
}

func printBanner(cfg config.Config) {
	banner := fmt.Sprintf(`
  ___             _         ____               
 / _ \           | |       / ____|              
/ /_\ \ __ _  ___| |_ ___ | |     ___  _ __ ___ 
|  _  |/ _` + "`" + ` |/ _ \ __/ _ \| |    / _ \| '__/ _ \
| | | | (_| |  __/ || (_) | |___| (_) | | |  __/
\_| |_/\__, |\___|\__\___/ \____/\___/|_|  \___|
        __/ |                                    
       |___/                                     

  Agent Core v1.0.0
  Listen:    %s
  Model:     %s
  DB:        %s
  Workspace: %s
  Tools:     builtin + MCP + host callback
`, cfg.Addr(), cfg.LLM.Model, cfg.Store.DBPath, cfg.Workspace.Path)

	fmt.Println(banner)
}

// resourceForTool maps a tool name to a security resource type.
func resourceForTool(toolName string) security.Resource {
	switch toolName {
	case "read_file", "write_file", "list_dir":
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

// extractTarget extracts the target string from tool arguments for policy evaluation.
func extractTarget(toolName string, args map[string]any) string {
	switch toolName {
	case "read_file", "write_file", "list_dir":
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

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Minimal initialization for MCP server mode (no DB, no LLM needed)
	toolRegistry := tool.NewRegistry()
	processRegistry := tool.NewProcessRegistry()
	builtin.RegisterAll(toolRegistry, cfg.Tools.WorkDir, processRegistry)

	// Restore MCP connections to include their tools
	var mcpManager *mcp.Manager
	if dbStore, err := store.NewSQLite(cfg.Store.DBPath); err == nil {
		mcpManager = mcp.NewManager(toolRegistry)
		mcpServers, _ := dbStore.GetMCPServers()
		for _, srv := range mcpServers {
			if !srv.Enabled {
				continue
			}
			var srvCfg mcp.ServerConfig
			srvCfg.Name = srv.Name
			srvCfg.Transport = srv.Transport
			srvCfg.Config = []byte(srv.Config)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := mcpManager.Connect(ctx, srvCfg); err != nil {
				log.Printf("[mcp-serve] Warning: failed to connect MCP server %s: %v", srv.Name, err)
			}
			cancel()
		}
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
