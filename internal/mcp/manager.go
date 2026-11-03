package mcp

import (
	"agent-core/internal/security"
	"agent-core/internal/tool"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Manager manages live MCP sessions and their namespaced tools.
type Manager struct {
	mu         sync.RWMutex
	clients    map[string]*clientConn
	connecting map[string]bool
	registry   *tool.Registry
}

type clientConn struct {
	name      string
	transport string
	config    json.RawMessage
	session   *sdk.ClientSession
	command   *exec.Cmd
	cancel    context.CancelFunc
	tools     []string
}

type ServerConfig struct {
	Name      string          `json:"name"`
	Transport string          `json:"transport"`
	Config    json.RawMessage `json:"config"`
}

type StdioConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Dir     string            `json:"dir,omitempty"`
}
type SSEConfig struct {
	URL          string            `json:"url"`
	Headers      map[string]string `json:"headers,omitempty"`
	AllowPrivate bool              `json:"allow_private,omitempty"`
}
type HTTPConfig struct {
	URL          string            `json:"url"`
	Headers      map[string]string `json:"headers,omitempty"`
	AllowPrivate bool              `json:"allow_private,omitempty"`
}

// RestoreConfig accepts both the current raw transport config and the legacy nested envelope.
func RestoreConfig(name, transport string, data []byte) ServerConfig {
	var legacy ServerConfig
	if err := json.Unmarshal(data, &legacy); err == nil && len(legacy.Config) > 0 {
		if legacy.Name == "" {
			legacy.Name = name
		}
		if legacy.Transport == "" {
			legacy.Transport = transport
		}
		return legacy
	}
	return ServerConfig{Name: name, Transport: transport, Config: json.RawMessage(data)}
}

func NewManager(registry *tool.Registry) *Manager {
	return &Manager{clients: make(map[string]*clientConn), connecting: make(map[string]bool), registry: registry}
}

func (m *Manager) Connect(ctx context.Context, cfg ServerConfig) error {
	if cfg.Name == "" || strings.ContainsAny(cfg.Name, "\x00\r\n") {
		return fmt.Errorf("valid MCP server name is required")
	}
	m.mu.Lock()
	if m.clients[cfg.Name] != nil || m.connecting[cfg.Name] {
		m.mu.Unlock()
		return fmt.Errorf("MCP server %q already connected or connecting", cfg.Name)
	}
	m.connecting[cfg.Name] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.connecting, cfg.Name)
		m.mu.Unlock()
	}()

	connCtx, cancel := context.WithCancel(context.Background())
	handshakeCtx, handshakeCancel := context.WithCancel(connCtx)
	conn := &clientConn{name: cfg.Name, transport: cfg.Transport, config: cfg.Config,
		cancel: func() { handshakeCancel(); cancel() }}
	client := sdk.NewClient(&sdk.Implementation{Name: "agent-core", Version: "1.0.0"},
		&sdk.ClientOptions{Capabilities: &sdk.ClientCapabilities{}, KeepAlive: 30 * time.Second})
	transport, command, err := m.transport(connCtx, cfg)
	if err != nil {
		conn.cancel()
		return err
	}
	conn.command = command
	handshakeDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			handshakeCancel()
		case <-handshakeDone:
		}
	}()
	session, err := client.Connect(handshakeCtx, transport, nil)
	close(handshakeDone)
	if err != nil {
		conn.cancel()
		if command != nil && command.Process != nil {
			_ = tool.KillProcess(command)
		}
		return fmt.Errorf("connect MCP server %q: %w", cfg.Name, err)
	}
	conn.session = session
	providers, err := m.discoverTools(ctx, conn)
	if err != nil {
		_ = closeConnection(conn)
		return err
	}
	registered := make([]string, 0, len(providers))
	for _, provider := range providers {
		if err := m.registry.Register(provider, tool.SourceMCP, cfg.Name); err != nil {
			m.registry.UnregisterByOrigin(tool.SourceMCP, cfg.Name)
			_ = session.Close()
			cancel()
			return fmt.Errorf("register MCP tool %s: %w", provider.Name(), err)
		}
		registered = append(registered, provider.Name())
	}
	conn.tools = registered
	m.mu.Lock()
	m.clients[cfg.Name] = conn
	m.mu.Unlock()
	go m.watch(conn)
	log.Printf("[mcp] connected %s transport=%s tools=%d", cfg.Name, cfg.Transport, len(registered))
	return nil
}

func (m *Manager) transport(ctx context.Context, cfg ServerConfig) (sdk.Transport, *exec.Cmd, error) {
	switch cfg.Transport {
	case "stdio":
		var config StdioConfig
		if err := json.Unmarshal(cfg.Config, &config); err != nil {
			return nil, nil, fmt.Errorf("parse stdio config: %w", err)
		}
		if config.Command == "" {
			return nil, nil, fmt.Errorf("stdio command is required")
		}
		command := exec.CommandContext(ctx, config.Command, config.Args...)
		command.Dir = config.Dir
		command.Env = controlledEnvironment(config.Env)
		tool.PrepareProcess(command)
		return &sdk.CommandTransport{Command: command, TerminateDuration: 3 * time.Second}, command, nil
	case "sse":
		var config SSEConfig
		if err := json.Unmarshal(cfg.Config, &config); err != nil {
			return nil, nil, err
		}
		client, err := secureHTTPClient(config.URL, config.Headers, config.AllowPrivate)
		if err != nil {
			return nil, nil, err
		}
		return &sdk.SSEClientTransport{Endpoint: config.URL, HTTPClient: client, MaxEventSize: 1024 * 1024}, nil, nil
	case "streamable_http":
		var config HTTPConfig
		if err := json.Unmarshal(cfg.Config, &config); err != nil {
			return nil, nil, err
		}
		client, err := secureHTTPClient(config.URL, config.Headers, config.AllowPrivate)
		if err != nil {
			return nil, nil, err
		}
		return &sdk.StreamableClientTransport{Endpoint: config.URL, HTTPClient: client, MaxEventSize: 1024 * 1024}, nil, nil
	default:
		return nil, nil, fmt.Errorf("unsupported transport: %s", cfg.Transport)
	}
}

func controlledEnvironment(overrides map[string]string) []string {
	values := make(map[string]string)
	for _, key := range []string{"PATH", "HOME", "USERPROFILE", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT", "TEMP", "TMP", "TMPDIR"} {
		if value := os.Getenv(key); value != "" {
			values[key] = value
		}
	}
	for key, value := range overrides {
		if strings.ContainsAny(key, "\x00=") || strings.ContainsRune(value, '\x00') {
			continue
		}
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func (m *Manager) discoverTools(ctx context.Context, conn *clientConn) ([]tool.Provider, error) {
	var providers []tool.Provider
	for remote, err := range conn.session.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("list tools for %s: %w", conn.name, err)
		}
		providers = append(providers, newRemoteTool(conn.name, remote, conn.session))
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].Name() < providers[j].Name() })
	return providers, nil
}

func (m *Manager) watch(conn *clientConn) {
	_ = conn.session.Wait()
	m.mu.Lock()
	current := m.clients[conn.name]
	if current == conn {
		delete(m.clients, conn.name)
	}
	m.mu.Unlock()
	if current == conn {
		m.registry.UnregisterByOrigin(tool.SourceMCP, conn.name)
		if conn.command != nil && conn.command.Process != nil && conn.command.ProcessState == nil {
			_ = tool.KillProcess(conn.command)
		}
		conn.cancel()
		log.Printf("[mcp] session closed: %s", conn.name)
	}
}

func closeConnection(conn *clientConn) error {
	err := conn.session.Close()
	if conn.command != nil && conn.command.Process != nil && conn.command.ProcessState == nil {
		_ = tool.KillProcess(conn.command)
	}
	conn.cancel()
	return err
}

func (m *Manager) Disconnect(name string) error {
	m.mu.Lock()
	conn := m.clients[name]
	if conn == nil {
		m.mu.Unlock()
		return fmt.Errorf("MCP server %q not connected", name)
	}
	delete(m.clients, name)
	m.mu.Unlock()
	m.registry.UnregisterByOrigin(tool.SourceMCP, name)
	return closeConnection(conn)
}

func (m *Manager) DisconnectAll() {
	m.mu.Lock()
	connections := make([]*clientConn, 0, len(m.clients))
	for name, conn := range m.clients {
		connections = append(connections, conn)
		delete(m.clients, name)
	}
	m.mu.Unlock()
	for _, conn := range connections {
		m.registry.UnregisterByOrigin(tool.SourceMCP, conn.name)
		_ = closeConnection(conn)
	}
}

func (m *Manager) ListServers() []ServerInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]ServerInfo, 0, len(m.clients))
	for _, conn := range m.clients {
		result = append(result, ServerInfo{Name: conn.name, Transport: conn.transport, ToolCount: len(conn.tools), Tools: append([]string(nil), conn.tools...)})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

type ServerInfo struct {
	Name      string   `json:"name"`
	Transport string   `json:"transport"`
	ToolCount int      `json:"tool_count"`
	Tools     []string `json:"tools"`
}

func validateRemoteURL(raw string, allowPrivate bool) error {
	if allowPrivate {
		return security.CheckHTTPURL(raw)
	}
	return security.CheckURL(raw)
}
