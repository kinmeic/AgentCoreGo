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
	defer dbStore.Close()
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

	// Register builtin tools
	builtin.RegisterAll(toolRegistry, cfg.Tools.WorkDir)
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

	// Initialize LLM client
	llmClient := llm.NewClient(cfg.LLM)
	log.Printf("[agent-core] LLM client initialized (%s)", cfg.LLM.Model)

	// Initialize compressor and prompt builder
	comp := compressor.NewCompressor(cfg.Compression)
	promptBuilder := prompt.NewBuilder()

	// Initialize orchestrator
	orchestrator := agent.NewOrchestrator(
		llmClient, toolRegistry, dbStore, memService, comp, promptBuilder, cfg,
	)

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

	// Graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	mcpManager.DisconnectAll()
	httpServer.Shutdown(shutdownCtx)

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
