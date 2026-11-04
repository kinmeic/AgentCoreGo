package server

import (
	"agent-core/internal/agent"
	"agent-core/internal/config"
	"agent-core/internal/mcp"
	"agent-core/internal/memory"
	"agent-core/internal/model"
	"agent-core/internal/security"
	"agent-core/internal/store"
	"agent-core/internal/tool"
	"context"
	"crypto/subtle"
	"encoding/json"
	"github.com/coder/websocket"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Server is the HTTP server for Agent Core.
type Server struct {
	backgroundCtx    context.Context
	backgroundCancel context.CancelFunc
	backgroundMu     sync.Mutex
	backgroundRuns   map[string]context.CancelFunc
	backgroundWG     sync.WaitGroup
	sockets          map[*websocket.Conn]bool
	cfg              config.Config
	orchestrator     *agent.Orchestrator
	toolRegistry     *tool.Registry
	mcpManager       *mcp.Manager
	store            *store.SQLite
	memService       *memory.Service
	services         Services
	mux              *http.ServeMux
	metrics          serverMetrics
	handlerOnce      sync.Once
	handler          http.Handler
}

// NewServer creates a new HTTP server.
func NewServer(
	cfg config.Config,
	orchestrator *agent.Orchestrator,
	registry *tool.Registry,
	mcpMgr *mcp.Manager,
	store *store.SQLite,
	memService *memory.Service,
) *Server {
	backgroundCtx, backgroundCancel := context.WithCancel(context.Background())
	s := &Server{
		backgroundCtx: backgroundCtx, backgroundCancel: backgroundCancel, backgroundRuns: map[string]context.CancelFunc{}, sockets: map[*websocket.Conn]bool{},
		cfg:          cfg,
		orchestrator: orchestrator,
		toolRegistry: registry,
		mcpManager:   mcpMgr,
		store:        store,
		memService:   memService,
		mux:          http.NewServeMux(),
	}

	s.registerRoutes()
	return s
}

// registerRoutes sets up all API routes.
func (s *Server) registerRoutes() {
	s.mux.HandleFunc("GET /ws", s.handleWebSocket)
	s.mux.HandleFunc("GET /api/server-info", s.handleServerInfo)
	// Chat endpoints
	s.mux.HandleFunc("POST /v1/chat", s.handleChatSync)
	s.mux.HandleFunc("POST /v1/chat/stream", s.handleChatStream)
	s.mux.HandleFunc("POST /v1/chat/{id}/stop", s.handleChatStop)
	s.mux.HandleFunc("POST /v1/chat/{id}/clarify", s.handleChatClarify)

	// Run management
	s.mux.HandleFunc("GET /v1/runs/{id}", s.handleGetRun)
	s.mux.HandleFunc("POST /v1/runs/{id}/stop", s.handleStopRun)
	s.mux.HandleFunc("POST /v1/runs/{id}/resume", s.handleResumeRun)
	s.mux.HandleFunc("GET /v1/runs/{id}/events", s.handleRunEvents)
	s.mux.HandleFunc("GET /v1/runs/{id}/events/stream", s.handleRunEventStream)
	s.mux.HandleFunc("GET /v1/approvals", s.handlePendingApprovals)
	s.mux.HandleFunc("POST /v1/approvals/{id}", s.handleRespondApproval)

	// Conversation management
	s.mux.HandleFunc("GET /v1/conversations", s.handleListConversations)
	s.mux.HandleFunc("GET /v1/conversations/{id}", s.handleGetConversation)
	s.mux.HandleFunc("GET /v1/conversations/{id}/logs", s.handleGetConversationLogs)
	s.mux.HandleFunc("PUT /v1/conversations/{id}", s.handleUpdateConversation)
	s.mux.HandleFunc("DELETE /v1/conversations/{id}", s.handleDeleteConversation)

	// Memory management
	s.mux.HandleFunc("GET /v1/memories", s.handleListMemories)
	s.mux.HandleFunc("POST /v1/memories", s.handleCreateMemory)
	s.mux.HandleFunc("POST /v1/memories/consolidate", s.handleConsolidateMemories)
	s.mux.HandleFunc("PUT /v1/memories/{id}", s.handleUpdateMemory)
	s.mux.HandleFunc("DELETE /v1/memories/{id}", s.handleDeleteMemory)

	// Tool management
	s.mux.HandleFunc("GET /v1/tools", s.handleListTools)
	s.mux.HandleFunc("POST /v1/tools/register", s.handleRegisterTool)
	s.mux.HandleFunc("DELETE /v1/tools/{name}", s.handleUnregisterTool)

	// MCP management
	s.mux.HandleFunc("GET /v1/mcp", s.handleListMCP)
	s.mux.HandleFunc("POST /v1/mcp", s.handleConnectMCP)
	s.mux.HandleFunc("DELETE /v1/mcp/{name}", s.handleDisconnectMCP)

	// Extension, workspace and execution control plane
	s.registerExtensionRoutes()

	// Health and diagnostics
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("GET /ready", s.handleReady)
	s.mux.HandleFunc("GET /v1/metrics", s.handleMetrics)
}

// Handler returns the HTTP handler with optional auth middleware.
func (s *Server) Handler() http.Handler {
	s.handlerOnce.Do(func() {
		var handler http.Handler = s.featureMiddleware(s.mux)
		handler = s.bodyLimitMiddleware(handler)
		if s.cfg.Server.AuthToken != "" {
			handler = s.authMiddleware(handler)
		} else {
			log.Printf("[server] WARNING: AUTH_TOKEN is not set; this is safe only on loopback (current %s)", s.cfg.Server.Host)
		}
		handler = s.rateLimitMiddleware(handler)
		handler = s.corsMiddleware(handler)
		s.handler = s.requestMiddleware(handler)
	})
	return s.handler
}

// authMiddleware validates the Authorization header.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip auth for health check
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}

		auth := r.Header.Get("Authorization")
		if r.URL.Path == "/ws" && auth == "" {
			next.ServeHTTP(w, r)
			return
		}
		if auth == "" {
			respondError(w, http.StatusUnauthorized, "missing authorization header")
			return
		}

		if !strings.HasPrefix(auth, "Bearer ") {
			respondError(w, http.StatusUnauthorized, "authorization must use Bearer scheme")
			return
		}
		token := strings.TrimPrefix(auth, "Bearer ")
		if subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.Server.AuthToken)) != 1 {
			respondError(w, http.StatusUnauthorized, "invalid token")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// --- Chat Handlers ---

func (s *Server) handleChatSync(w http.ResponseWriter, r *http.Request) {
	var req model.ChatRequest
	if err := decodeBody(r, &req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Message == "" {
		respondError(w, http.StatusBadRequest, "message is required")
		return
	}

	resolved, local, err := s.resolveChat(r.Context(), req)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	if local != nil {
		respondJSON(w, http.StatusOK, local)
		return
	}
	resp, err := s.orchestrator.ChatSync(r.Context(), resolved)
	if err != nil {
		respondRunError(w, err)
		return
	}

	respondJSON(w, http.StatusOK, resp)
}

func (s *Server) handleChatStream(w http.ResponseWriter, r *http.Request) {
	var req model.ChatRequest
	if err := decodeBody(r, &req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Message == "" {
		respondError(w, http.StatusBadRequest, "message is required")
		return
	}

	resolved, local, err := s.resolveChat(r.Context(), req)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	if local != nil {
		events := make(chan model.StreamEvent, 2)
		data, _ := json.Marshal(local)
		events <- model.StreamEvent{Type: "command.result", Content: string(data), Timestamp: time.Now()}
		events <- model.StreamEvent{Type: "done", Reply: local.Reply, Timestamp: time.Now()}
		close(events)
		s.serveEventStream(w, r, events)
		return
	}
	accepted, err := s.acceptRun(resolved, r.Header.Get("X-Command-ID"))
	if err != nil {
		respondRunError(w, err)
		return
	}
	w.Header().Set("X-Run-ID", accepted.RunID)
	events := make(chan model.StreamEvent, 128)
	go s.streamRunEvents(r, accepted.RunID, 0, events)
	s.serveEventStream(w, r, events)
}

func (s *Server) handleChatStop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stopped := s.orchestrator.Stop(id)
	respondJSON(w, http.StatusOK, map[string]bool{"stopped": stopped})
}

func (s *Server) handleChatClarify(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Response string `json:"response"`
	}
	if err := decodeBody(r, &body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	delivered := s.orchestrator.RespondClarify(id, body.Response)
	respondJSON(w, http.StatusOK, map[string]bool{"delivered": delivered})
}

// --- Conversation Handlers ---

func (s *Server) handleListConversations(w http.ResponseWriter, r *http.Request) {
	source := r.URL.Query().Get("source")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	conversations, err := s.store.ListConversations(source, limit, offset)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if conversations == nil {
		conversations = []*model.Conversation{}
	}
	respondJSON(w, http.StatusOK, conversations)
}

func (s *Server) handleGetConversation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	conv, err := s.store.GetConversation(id)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if conv == nil {
		respondError(w, http.StatusNotFound, "conversation not found")
		return
	}
	respondJSON(w, http.StatusOK, conv)
}

func (s *Server) handleGetConversationLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var logs []*model.ConversationLog
	var err error
	if query := r.URL.Query().Get("q"); query != "" {
		logs, err = s.store.SearchLogs(id, query, limit)
	} else if before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64); before > 0 {
		logs, err = s.store.GetLogsBefore(id, before, limit)
	} else {
		logs, err = s.store.GetLogs(id, limit)
	}
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load conversation logs")
		return
	}
	if logs == nil {
		logs = []*model.ConversationLog{}
	}
	if len(logs) == limit && len(logs) > 0 {
		w.Header().Set("X-Next-Cursor", strconv.FormatInt(logs[0].ID, 10))
	}
	if r.URL.Query().Get("format") == "jsonl" {
		w.Header().Set("Content-Type", "application/x-ndjson")
		encoder := json.NewEncoder(w)
		for _, entry := range logs {
			if err := encoder.Encode(entry); err != nil {
				return
			}
		}
		return
	}
	respondJSON(w, http.StatusOK, logs)
}

func (s *Server) handleUpdateConversation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Title string `json:"title"`
	}
	if err := decodeBody(r, &body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.Title != "" {
		if err := s.store.UpdateConversationTitle(id, body.Title); err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	conv, err := s.store.GetConversation(id)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if conv == nil {
		respondError(w, http.StatusNotFound, "conversation not found")
		return
	}
	respondJSON(w, http.StatusOK, conv)
}

func (s *Server) handleDeleteConversation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.ArchiveConversation(id); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "archived"})
}

// --- Memory Handlers ---

func (s *Server) handleListMemories(w http.ResponseWriter, r *http.Request) {
	category := r.URL.Query().Get("category")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	var enabled *bool
	if e := r.URL.Query().Get("enabled"); e != "" {
		v := e == "true"
		enabled = &v
	}

	memories, err := s.memService.GetAll(category, enabled, limit)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if memories == nil {
		memories = []*model.Memory{}
	}
	respondJSON(w, http.StatusOK, memories)
}

func (s *Server) handleCreateMemory(w http.ResponseWriter, r *http.Request) {
	var mem model.Memory
	if err := decodeBody(r, &mem); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if mem.Content == "" {
		respondError(w, http.StatusBadRequest, "content is required")
		return
	}

	if mem.Category == "" {
		mem.Category = "fact"
	}
	if mem.Source == "" {
		mem.Source = "user_input"
	}
	if mem.Importance == 0 {
		mem.Importance = 0.5
	}
	mem.Enabled = true

	if err := s.memService.Add(r.Context(), &mem); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, http.StatusCreated, mem)
}

func (s *Server) handleUpdateMemory(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid memory id")
		return
	}

	var mem model.Memory
	if err := decodeBody(r, &mem); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validation (mirror create endpoint)
	if mem.Content == "" {
		respondError(w, http.StatusBadRequest, "content is required")
		return
	}
	if mem.Importance < 0 || mem.Importance > 1 {
		respondError(w, http.StatusBadRequest, "importance must be between 0 and 1")
		return
	}
	if mem.Category == "" {
		mem.Category = "fact"
	}

	// Check existence
	existing, err := s.store.GetMemory(id)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing == nil {
		respondError(w, http.StatusNotFound, "memory not found")
		return
	}

	mem.ID = id
	if err := s.memService.Update(r.Context(), &mem); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, mem)
}

func (s *Server) handleDeleteMemory(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid memory id")
		return
	}

	if err := s.memService.Delete(r.Context(), id); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// --- Tool Handlers ---

func (s *Server) handleListTools(w http.ResponseWriter, r *http.Request) {
	type toolInfo struct {
		Name         string            `json:"name"`
		Description  string            `json:"description"`
		Source       string            `json:"source"`
		Origin       string            `json:"origin"`
		Capabilities tool.Capabilities `json:"capabilities"`
	}

	tools := s.toolRegistry.List()
	result := make([]toolInfo, len(tools))
	for i, t := range tools {
		result[i] = toolInfo{
			Name:         t.Provider.Name(),
			Description:  t.Provider.Description(),
			Source:       string(t.Source),
			Origin:       t.Origin,
			Capabilities: t.Capabilities,
		}
	}

	respondJSON(w, http.StatusOK, result)
}

func (s *Server) handleRegisterTool(w http.ResponseWriter, r *http.Request) {
	var reg tool.HostToolRegistration
	if err := decodeBody(r, &reg); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !validToolName(reg.Name) || reg.CallbackURL == "" || len(reg.CallbackURL) > 2048 {
		respondError(w, http.StatusBadRequest, "valid name and callback_url are required")
		return
	}
	if reg.Secret == "" {
		respondError(w, http.StatusBadRequest, "secret is required for callback authentication")
		return
	}
	var urlErr error
	if reg.AllowPrivate {
		urlErr = security.CheckHTTPURL(reg.CallbackURL)
	} else {
		urlErr = security.CheckURL(reg.CallbackURL)
	}
	if urlErr != nil {
		respondError(w, http.StatusBadRequest, "callback_url blocked")
		return
	}
	provider := tool.NewHostCallbackWithOptions(reg)
	if err := s.toolRegistry.Register(provider, tool.SourceHostCallback, "host"); err != nil {
		respondError(w, http.StatusConflict, err.Error())
		return
	}
	schema, _ := json.Marshal(reg.Schema)
	if err := s.store.SaveHostCallback(&model.HostCallback{Name: reg.Name, Description: reg.Description,
		Schema: schema, CallbackURL: reg.CallbackURL, Secret: reg.Secret, AllowPrivate: reg.AllowPrivate, Enabled: true}); err != nil {
		_ = s.toolRegistry.Unregister(reg.Name)
		respondError(w, http.StatusInternalServerError, "failed to persist host callback")
		return
	}
	log.Printf("[server] registered host tool %s", reg.Name)
	respondJSON(w, http.StatusCreated, map[string]string{"status": "registered", "name": reg.Name})
}

func (s *Server) handleUnregisterTool(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.store.DeleteHostCallback(name); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to delete host callback configuration")
		return
	}
	if err := s.toolRegistry.Unregister(name); err != nil {
		respondError(w, http.StatusConflict, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "unregistered", "name": name})
}

// --- MCP Handlers ---

func (s *Server) handleListMCP(w http.ResponseWriter, r *http.Request) {
	servers := s.mcpManager.ListServers()
	respondJSON(w, http.StatusOK, servers)
}

func (s *Server) handleConnectMCP(w http.ResponseWriter, r *http.Request) {
	var cfg mcp.ServerConfig
	if err := decodeBody(r, &cfg); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if cfg.Name == "" || cfg.Transport == "" {
		respondError(w, http.StatusBadRequest, "name and transport are required")
		return
	}

	// Connect
	timeoutCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	if err := s.mcpManager.Connect(timeoutCtx, cfg); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Persist only the transport-specific config, not a nested ServerConfig envelope.
	if err := s.store.SaveMCPServer(&model.MCPServer{Name: cfg.Name, Transport: cfg.Transport,
		Config: string(cfg.Config), Enabled: true}); err != nil {
		_ = s.mcpManager.Disconnect(cfg.Name)
		respondError(w, http.StatusInternalServerError, "failed to persist MCP configuration")
		return
	}

	respondJSON(w, http.StatusCreated, map[string]string{"status": "connected", "name": cfg.Name})
}

func (s *Server) handleDisconnectMCP(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	if err := s.mcpManager.Disconnect(name); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if err := s.store.DeleteMCPServer(name); err != nil {
		respondError(w, http.StatusInternalServerError, "disconnected but failed to delete persisted MCP configuration")
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "disconnected", "name": name})
}

// --- Health ---

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"tools":  s.toolRegistry.Count(),
		"mcp":    len(s.mcpManager.ListServers()),
	})
}
