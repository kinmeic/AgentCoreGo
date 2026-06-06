package mcp

import (
	"agent-core/internal/tool"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
)

// Manager manages connections to MCP servers and registers their tools.
type Manager struct {
	mu        sync.RWMutex
	clients   map[string]*clientConn
	registry  *tool.Registry
}

// clientConn holds the state of a connected MCP server.
type clientConn struct {
	name      string
	transport string
	config    json.RawMessage
	cancel    context.CancelFunc
	tools     []string // tool names registered by this server
}

// ServerConfig represents the configuration for an MCP server.
type ServerConfig struct {
	Name      string          `json:"name"`
	Transport string          `json:"transport"` // stdio, sse, streamable_http
	Config    json.RawMessage `json:"config"`
}

// StdioConfig is the config for stdio transport.
type StdioConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// SSEConfig is the config for SSE transport.
type SSEConfig struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

// HTTPConfig is the config for streamable HTTP transport.
type HTTPConfig struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

// NewManager creates a new MCP manager.
func NewManager(registry *tool.Registry) *Manager {
	return &Manager{
		clients:  make(map[string]*clientConn),
		registry: registry,
	}
}

// Connect establishes a connection to an MCP server.
func (m *Manager) Connect(ctx context.Context, cfg ServerConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.clients[cfg.Name]; exists {
		return fmt.Errorf("MCP server %q already connected", cfg.Name)
	}

	// Parse transport-specific config
	var transportConfig any
	switch cfg.Transport {
	case "stdio":
		var sc StdioConfig
		if err := json.Unmarshal(cfg.Config, &sc); err != nil {
			return fmt.Errorf("parse stdio config: %w", err)
		}
		transportConfig = sc
	case "sse":
		var sc SSEConfig
		if err := json.Unmarshal(cfg.Config, &sc); err != nil {
			return fmt.Errorf("parse sse config: %w", err)
		}
		transportConfig = sc
	case "streamable_http":
		var hc HTTPConfig
		if err := json.Unmarshal(cfg.Config, &hc); err != nil {
			return fmt.Errorf("parse http config: %w", err)
		}
		transportConfig = hc
	default:
		return fmt.Errorf("unsupported transport: %s", cfg.Transport)
	}

	// Create context for this connection — decouple from caller's context
	// so the connection outlives the initial Connect() call
	connCtx, cancel := context.WithCancel(context.Background())

	// Connect and discover tools based on transport
	tools, err := m.connectTransport(connCtx, cfg.Name, cfg.Transport, transportConfig)
	if err != nil {
		cancel()
		return fmt.Errorf("connect to MCP server %q: %w", cfg.Name, err)
	}

	m.clients[cfg.Name] = &clientConn{
		name:      cfg.Name,
		transport: cfg.Transport,
		config:    cfg.Config,
		cancel:    cancel,
		tools:     tools,
	}

	log.Printf("[mcp] Connected to %s (%s), %d tools available", cfg.Name, cfg.Transport, len(tools))
	return nil
}

// connectTransport establishes the actual transport connection and discovers tools.
func (m *Manager) connectTransport(ctx context.Context, name, transport string, config any) ([]string, error) {
	switch transport {
	case "stdio":
		return m.connectStdio(ctx, name, config.(StdioConfig))
	case "sse":
		return m.connectSSE(ctx, name, config.(SSEConfig))
	case "streamable_http":
		return m.connectHTTP(ctx, name, config.(HTTPConfig))
	default:
		return nil, fmt.Errorf("unsupported transport: %s", transport)
	}
}

// connectStdio connects via stdio transport.
func (m *Manager) connectStdio(ctx context.Context, name string, cfg StdioConfig) ([]string, error) {
	// TODO: Implement full MCP stdio protocol
	// For now, return a placeholder that logs the intent
	log.Printf("[mcp] stdio connection to %s: command=%s args=%v", name, cfg.Command, cfg.Args)
	return nil, fmt.Errorf("stdio transport not yet implemented - use mark3labs/mcp-go for production")
}

// connectSSE connects via SSE transport.
func (m *Manager) connectSSE(ctx context.Context, name string, cfg SSEConfig) ([]string, error) {
	// TODO: Implement full MCP SSE protocol
	log.Printf("[mcp] SSE connection to %s: url=%s", name, cfg.URL)
	return nil, fmt.Errorf("SSE transport not yet implemented - use mark3labs/mcp-go for production")
}

// connectHTTP connects via streamable HTTP transport.
func (m *Manager) connectHTTP(ctx context.Context, name string, cfg HTTPConfig) ([]string, error) {
	// TODO: Implement full MCP streamable HTTP protocol
	log.Printf("[mcp] HTTP connection to %s: url=%s", name, cfg.URL)
	return nil, fmt.Errorf("HTTP transport not yet implemented - use mark3labs/mcp-go for production")
}

// Disconnect closes a connection to an MCP server.
func (m *Manager) Disconnect(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	conn, ok := m.clients[name]
	if !ok {
		return fmt.Errorf("MCP server %q not connected", name)
	}

	// Cancel the connection context
	conn.cancel()

	// Unregister all tools from this server
	m.registry.UnregisterByOrigin(tool.SourceMCP, name)

	delete(m.clients, name)
	log.Printf("[mcp] Disconnected from %s", name)
	return nil
}

// DisconnectAll closes all MCP server connections.
func (m *Manager) DisconnectAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for name, conn := range m.clients {
		conn.cancel()
		m.registry.UnregisterByOrigin(tool.SourceMCP, name)
		log.Printf("[mcp] Disconnected from %s", name)
	}
	m.clients = make(map[string]*clientConn)
}

// ListServers returns information about connected servers.
func (m *Manager) ListServers() []ServerInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]ServerInfo, 0, len(m.clients))
	for _, conn := range m.clients {
		result = append(result, ServerInfo{
			Name:      conn.name,
			Transport: conn.transport,
			ToolCount: len(conn.tools),
			Tools:     conn.tools,
		})
	}
	return result
}

// ServerInfo contains information about a connected MCP server.
type ServerInfo struct {
	Name      string   `json:"name"`
	Transport string   `json:"transport"`
	ToolCount int      `json:"tool_count"`
	Tools     []string `json:"tools"`
}
