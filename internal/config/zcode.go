package config

import (
	"agent-core/internal/security"
	"crypto/x509"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
)

// Defaults are pinned to ZCode v3.17.2 / a9fe401e, contracts/src/config.
const (
	DefaultModelStreamIdleTimeoutMs = 600_000
	DefaultNetworkTimeoutMs         = 180_000
	DefaultSkillMetadataBudget      = 20_000
	DefaultToolConcurrency          = 10
	DefaultModelRetries             = 10
	DefaultContextWindow            = 200_000
	DefaultOutputTokens             = 32_000
)

type ModelStreamConfig struct {
	IdleTimeoutMs int `yaml:"idleTimeoutMs" json:"idleTimeoutMs"`
}
type PermissionConfig struct {
	Mode                  string   `yaml:"mode" json:"mode"`
	AllowedTools          []string `yaml:"allowedTools" json:"allowedTools"`
	DisallowedTools       []string `yaml:"disallowedTools" json:"disallowedTools"`
	AutoApproveHighRisk   bool     `yaml:"autoApproveHighRisk" json:"autoApproveHighRisk"`
	AllowMediumRiskInAuto bool     `yaml:"allowMediumRiskInAuto" json:"allowMediumRiskInAuto"`
}
type StorageConfig struct {
	Dir           string `yaml:"dir" json:"dir"`
	SessionDBPath string `yaml:"sessionDbPath" json:"sessionDbPath"`
}
type NetworkConfig struct {
	HTTPProxy  string `yaml:"httpProxy,omitempty" json:"httpProxy,omitempty"`
	NoProxy    string `yaml:"noProxy,omitempty" json:"noProxy,omitempty"`
	CACertFile string `yaml:"caCertFile,omitempty" json:"caCertFile,omitempty"`
	Timeout    int    `yaml:"timeout" json:"timeout"`
}
type LoggingConfig struct {
	Level  string `yaml:"level" json:"level"`
	Format string `yaml:"format" json:"format"`
}
type ToolConcurrencyConfig struct {
	MaxConcurrency int `yaml:"maxConcurrency" json:"maxConcurrency"`
}
type ModelAnomalyGuardConfig struct {
	ToolCallWarningThreshold         *int `yaml:"toolCallWarningThreshold,omitempty" json:"toolCallWarningThreshold,omitempty"`
	RepeatedToolCallWarningThreshold int  `yaml:"repeatedToolCallWarningThreshold" json:"repeatedToolCallWarningThreshold"`
	MaxBudgetWarningsPerTurn         int  `yaml:"maxBudgetWarningsPerTurn" json:"maxBudgetWarningsPerTurn"`
}
type EntryOverride struct {
	Enable *bool `yaml:"enable,omitempty" json:"enable,omitempty"`
}

type MCPConfig struct {
	Servers map[string]MCPServerConfig `yaml:"servers" json:"servers"`
}
type MCPServerConfig struct {
	Type            string            `yaml:"type" json:"type"`
	Command         string            `yaml:"command,omitempty" json:"command,omitempty"`
	Args            []string          `yaml:"args,omitempty" json:"args,omitempty"`
	Cwd             string            `yaml:"cwd,omitempty" json:"cwd,omitempty"`
	Env             map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	URL             string            `yaml:"url,omitempty" json:"url,omitempty"`
	Headers         map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	Enabled         *bool             `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	AlwaysLoad      bool              `yaml:"alwaysLoad,omitempty" json:"alwaysLoad,omitempty"`
	Isolation       string            `yaml:"isolation,omitempty" json:"isolation,omitempty"`
	ProtocolVersion string            `yaml:"protocolVersion,omitempty" json:"protocolVersion,omitempty"`
	TimeoutMs       int               `yaml:"timeoutMs,omitempty" json:"timeoutMs,omitempty"`
}

// Derived implementation settings are not a second public configuration surface.
// 独立服务不能默认打开 ZCode Desktop 的 SQLite；只对齐字段语义，隔离存储路径。
func (c *Config) applyRuntimeSettings() {
	c.Tools.ApprovalMode = c.Permission.Mode
	c.Tools.TerminalEnabled = c.Server.TerminalEnabled
	c.Server.LogFormat = c.Logging.Format
	c.Store.DBPath = expandHome(c.Storage.SessionDBPath)
	c.Storage.Dir = expandHome(c.Storage.Dir)
	c.Storage.SessionDBPath = c.Store.DBPath
	c.LLM.HTTPProxy, c.LLM.NoProxy, c.LLM.CACertFile = c.Network.HTTPProxy, c.Network.NoProxy, expandHome(c.Network.CACertFile)
	c.LLM.RequestTimeoutMs = c.Network.Timeout
	c.LLM.StreamIdleTimeoutMs = c.ModelStream.IdleTimeoutMs
	c.Compression.MaxOutputTokens = c.LLM.MaxOutputTokens
	c.Workflow.MaxConcurrency = c.ToolConcurrency.MaxConcurrency
	c.Features.Skills = c.Features.Skill && c.Skills.Enabled
	c.Features.Subagents = c.Features.Subagent
	if c.Workspace.Path == "" {
		c.Workspace.Path = "."
	}
	c.Telemetry.Directory = filepath.Join(c.Storage.Dir, "model-io")
}

func expandHome(path string) string {
	if path == "~" || len(path) > 1 && path[0] == '~' && (path[1] == '/' || path[1] == '\\') {
		if home, err := os.UserHomeDir(); err == nil {
			if len(path) == 1 {
				return home
			}
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func (c Config) validateRuntimeSettings() error {
	if c.ModelStream.IdleTimeoutMs <= 0 || c.Network.Timeout <= 0 || c.ToolConcurrency.MaxConcurrency <= 0 {
		return fmt.Errorf("modelStream.idleTimeoutMs, network.timeout and toolConcurrency.maxConcurrency must be positive")
	}
	if c.ModelAnomalyGuard.RepeatedToolCallWarningThreshold <= 0 || c.ModelAnomalyGuard.MaxBudgetWarningsPerTurn < 0 {
		return fmt.Errorf("invalid modelAnomalyGuard warning settings")
	}
	if v := c.ModelAnomalyGuard.ToolCallWarningThreshold; v != nil && *v <= 0 {
		return fmt.Errorf("modelAnomalyGuard.toolCallWarningThreshold must be positive when set")
	}
	if c.LLM.RetryBaseDelayMs < 0 || c.LLM.RetryMaxDelayMs < 0 || c.LLM.RetryBackoffFactor <= 0 || math.IsNaN(c.LLM.RetryBackoffFactor) || math.IsInf(c.LLM.RetryBackoffFactor, 0) {
		return fmt.Errorf("invalid ZCODE_MODEL_RETRY settings")
	}
	if c.UI.Theme != "auto" && c.UI.Theme != "light" && c.UI.Theme != "dark" {
		return fmt.Errorf("invalid ui.theme")
	}
	switch c.Logging.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("invalid logging.level")
	}
	if c.Network.HTTPProxy != "" {
		u, err := url.Parse(c.Network.HTTPProxy)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" && u.Scheme != "socks5h") {
			return fmt.Errorf("network.httpProxy is invalid")
		}
	}
	if c.Network.CACertFile != "" {
		pem, err := os.ReadFile(expandHome(c.Network.CACertFile))
		if err != nil {
			return fmt.Errorf("network.caCertFile: %w", err)
		}
		if !x509.NewCertPool().AppendCertsFromPEM(pem) {
			return fmt.Errorf("network.caCertFile contains no CA certificate")
		}
	}
	for path := range c.SkillOverrides {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("skill override requires an absolute file path: %s", path)
		}
	}
	for path := range c.CommandOverrides {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("command override requires an absolute file path: %s", path)
		}
	}
	return ValidateMCP(c.MCP)
}

func ValidateMCP(cfg MCPConfig) error {
	for name, server := range cfg.Servers {
		if name == "" || server.TimeoutMs < 0 {
			return fmt.Errorf("invalid mcp server %q", name)
		}
		switch server.Type {
		case "stdio":
			if server.Command == "" {
				return fmt.Errorf("mcp server %s requires command", name)
			}
		case "http", "sse":
			if server.URL == "" || security.CheckHTTPURL(server.URL) != nil {
				return fmt.Errorf("mcp server %s requires url", name)
			}
		default:
			return fmt.Errorf("mcp server %s type must be stdio, http or sse", name)
		}
		if server.Isolation != "" && server.Isolation != "workspace" {
			return fmt.Errorf("MCP server %s: session isolation is not implemented; use workspace or omit isolation", name)
		}
		if server.ProtocolVersion != "" && server.ProtocolVersion != "auto" {
			return fmt.Errorf("MCP server %s: only protocolVersion auto negotiation is supported", name)
		}
	}
	return nil
}

func maskValues(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	masked := make(map[string]string, len(values))
	for key := range values {
		masked[key] = "***"
	}
	return masked
}

func EnabledOverrides(entries map[string]EntryOverride) map[string]bool {
	result := map[string]bool{}
	for path, entry := range entries {
		if entry.Enable != nil {
			result[path] = *entry.Enable
		}
	}
	return result
}
