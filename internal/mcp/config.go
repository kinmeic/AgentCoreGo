package mcp

import (
	"agent-core/internal/config"
	"context"
	"encoding/json"
	"log"
	"os"
	"time"
)

// ConnectConfigured consumes declarations from a trusted service configuration.
// Configuration is the authority to reach private MCP addresses and spawn stdio.
func (m *Manager) ConnectConfigured(ctx context.Context, cfg config.MCPConfig, enabled bool) {
	if !enabled {
		return
	}
	for name, entry := range cfg.Servers {
		if entry.Enabled != nil && !*entry.Enabled {
			continue
		}
		timeout := entry.TimeoutMs
		if timeout == 0 {
			timeout = 30000
		}
		transport := entry.Type
		var payload any
		env := map[string]string{}
		for k, v := range entry.Env {
			env[k] = os.ExpandEnv(v)
		}
		headers := map[string]string{}
		for k, v := range entry.Headers {
			headers[k] = os.ExpandEnv(v)
		}
		if transport == "stdio" {
			payload = StdioConfig{Command: entry.Command, Args: entry.Args, Dir: entry.Cwd, Env: env}
		} else {
			payload = HTTPConfig{URL: os.ExpandEnv(entry.URL), Headers: headers, AllowPrivate: true}
			if transport == "http" {
				transport = "streamable_http"
			}
		}
		raw, _ := json.Marshal(payload)
		connectCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
		err := m.Connect(connectCtx, ServerConfig{Name: name, Transport: transport, Config: raw, Timeout: time.Duration(timeout) * time.Millisecond})
		cancel()
		if err == nil && entry.AlwaysLoad {
			m.registry.SetOriginAlwaysLoaded(name)
		}
		if err != nil {
			log.Printf("[mcp] configured server %s connection failed", name)
		}
	}
}
