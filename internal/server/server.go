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
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Server is the HTTP server for Agent Core.
type Server struct {
	cfg           config.Config
	orchestrator  *agent.Orchestrator
	toolRegistry  *tool.Registry
	mcpManager    *mcp.Manager
	store         *store.SQLite
	memService    *memory.Service
	mux           *http.ServeMux
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
	s := &Server{
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
	// Chat endpoints
	s.mux.HandleFunc("POST /v1/chat", s.handleChatSync)
	s.mux.HandleFunc("POST /v1/chat/stream", s.handleChatStream)
	s.mux.HandleFunc("POST /v1/chat/{id}/stop", s.handleChatStop)
	s.mux.HandleFunc("POST /v1/chat/{id}/clarify", s.handleChatClarify)

	// Conversation management
	s.mux.HandleFunc("GET /v1/conversations", s.handleListConversations)
	s.mux.HandleFunc("GET /v1/conversations/{id}", s.handleGetConversation)
	s.mux.HandleFunc("GET /v1/conversations/{id}/logs", s.handleGetConversationLogs)
	s.mux.HandleFunc("PUT /v1/conversations/{id}", s.handleUpdateConversation)
	s.mux.HandleFunc("DELETE /v1/conversations/{id}", s.handleDeleteConversation)

	// Memory management
	s.mux.HandleFunc("GET /v1/memories", s.handleListMemories)
	s.mux.HandleFunc("POST /v1/memories", s.handleCreateMemory)
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

	// Health check
	s.mux.HandleFunc("GET /health", s.handleHealth)
}

// Handler returns the HTTP handler with optional auth middleware.
func (s *Server) Handler() http.Handler {
	if s.cfg.Server.AuthToken != "" {
		return s.authMiddleware(s.mux)
	}
	// No auth token configured — log a warning
	log.Printf("[server] WARNING: AUTH_TOKEN is not set, API is running without authentication. "+
		"This is only safe when bound to 127.0.0.1 (current: %s)", s.cfg.Server.Host)
	return s.mux
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
		if auth == "" {
			respondError(w, http.StatusUnauthorized, "missing authorization header")
			return
		}

		token := strings.TrimPrefix(auth, "Bearer ")
		if token != s.cfg.Server.AuthToken {
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

	resp, err := s.orchestrator.ChatSync(r.Context(), req)
	if err != nil {
		log.Printf("[server] Chat error: %v", err)
		respondError(w, http.StatusInternalServerError, err.Error())
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

	sse, err := NewSSEWriter(w)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	events := make(chan model.StreamEvent, 32)
	go s.orchestrator.ChatStream(r.Context(), req, events)

	// Forward events to SSE
	for event := range events {
		if err := sse.WriteEvent(event); err != nil {
			log.Printf("[server] SSE write error: %v", err)
			// Drain remaining events to prevent producer goroutine from blocking
			go func() {
				for range events {
				}
			}()
			return
		}
	}
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

	logs, err := s.store.GetLogs(id, limit)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if logs == nil {
		logs = []*model.ConversationLog{}
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
		Name        string `json:"name"`
		Description string `json:"description"`
		Source      string `json:"source"`
		Origin      string `json:"origin"`
	}

	tools := s.toolRegistry.List()
	result := make([]toolInfo, len(tools))
	for i, t := range tools {
		result[i] = toolInfo{
			Name:        t.Provider.Name(),
			Description: t.Provider.Description(),
			Source:      string(t.Source),
			Origin:      t.Origin,
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

	if reg.Name == "" || reg.CallbackURL == "" {
		respondError(w, http.StatusBadRequest, "name and callback_url are required")
		return
	}

	// Validate callback URL against SSRF rules
	if err := security.CheckURL(reg.CallbackURL); err != nil {
		respondError(w, http.StatusBadRequest, "callback_url blocked: "+err.Error())
		return
	}

	provider := tool.NewHostCallback(reg.Name, reg.Description, reg.Schema, reg.CallbackURL)
	s.toolRegistry.Register(provider, tool.SourceHostCallback, "host")

	log.Printf("[server] Registered host tool: %s -> %s", reg.Name, reg.CallbackURL)
	respondJSON(w, http.StatusCreated, map[string]string{"status": "registered", "name": reg.Name})
}

func (s *Server) handleUnregisterTool(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.toolRegistry.Unregister(name)
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

	// Save to DB
	configJSON, _ := json.Marshal(cfg)
	s.store.SaveMCPServer(&model.MCPServer{
		Name:      cfg.Name,
		Transport: cfg.Transport,
		Config:    string(configJSON),
		Enabled:   true,
	})

	respondJSON(w, http.StatusCreated, map[string]string{"status": "connected", "name": cfg.Name})
}

func (s *Server) handleDisconnectMCP(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	if err := s.mcpManager.Disconnect(name); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.store.DeleteMCPServer(name)
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
