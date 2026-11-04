package config

import (
	"agent-core/internal/security"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ModelStream       ModelStreamConfig        `yaml:"modelStream" json:"modelStream"`
	Permission        PermissionConfig         `yaml:"permission" json:"permission"`
	Storage           StorageConfig            `yaml:"storage" json:"storage"`
	Network           NetworkConfig            `yaml:"network" json:"network"`
	Logging           LoggingConfig            `yaml:"logging" json:"logging"`
	ToolConcurrency   ToolConcurrencyConfig    `yaml:"toolConcurrency" json:"toolConcurrency"`
	ModelAnomalyGuard ModelAnomalyGuardConfig  `yaml:"modelAnomalyGuard" json:"modelAnomalyGuard"`
	MCP               MCPConfig                `yaml:"mcp" json:"mcp"`
	SkillOverrides    map[string]EntryOverride `yaml:"skill" json:"skill"`
	CommandOverrides  map[string]EntryOverride `yaml:"command" json:"command"`
	Server            ServerConfig             `yaml:"server" json:"server"`
	LLM               LLMConfig                `yaml:"llm" json:"llm"`
	Store             StoreConfig              `yaml:"-" json:"-"`
	Workspace         WorkspaceConfig          `yaml:"workspace" json:"workspace"`
	Compression       CompressionConfig        `yaml:"-" json:"-"`
	Tools             ToolsConfig              `yaml:"-" json:"-"`
	Memory            MemoryConfig             `yaml:"memory" json:"memory"`
	Security          SecurityConfig           `yaml:"-" json:"-"`
	Features          FeatureConfig            `yaml:"features" json:"features"`
	UI                UIConfig                 `yaml:"ui" json:"ui"`
	Skills            SkillsConfig             `yaml:"skills" json:"skills"`
	Commands          CommandsConfig           `yaml:"-" json:"-"`
	Workflow          WorkflowConfig           `yaml:"-" json:"-"`
	Telemetry         TelemetryConfig          `yaml:"-" json:"-"`
}

type WorkspaceConfig struct {
	Path     string `yaml:"path" json:"path"`
	SoulFile string `yaml:"-" json:"-"`
}
type ServerConfig struct {
	Host               string   `yaml:"host" json:"host"`
	Port               int      `yaml:"port" json:"port"`
	AuthToken          string   `yaml:"auth_token" json:"auth_token"`
	MaxRequestBytes    int64    `yaml:"max_request_bytes" json:"max_request_bytes"`
	CORSOrigins        []string `yaml:"cors_origins" json:"cors_origins"`
	RateLimitPerMinute int      `yaml:"rate_limit_per_minute" json:"rate_limit_per_minute"`
	MaxConcurrentRuns  int      `yaml:"max_concurrent_runs" json:"max_concurrent_runs"`
	LogFormat          string   `yaml:"-" json:"-"`
	TerminalEnabled    bool     `yaml:"terminalEnabled" json:"terminalEnabled"`
}
type LLMConfig struct {
	RequestTimeoutMs    int                `yaml:"-" json:"-"`
	StreamIdleTimeoutMs int                `yaml:"-" json:"-"`
	RetryBaseDelayMs    int                `yaml:"-" json:"-"`
	RetryMaxDelayMs     int                `yaml:"-" json:"-"`
	RetryBackoffFactor  float64            `yaml:"-" json:"-"`
	HTTPProxy           string             `yaml:"-" json:"-"`
	NoProxy             string             `yaml:"-" json:"-"`
	CACertFile          string             `yaml:"-" json:"-"`
	RetryJitter         bool               `yaml:"-" json:"-"`
	APIFormat           string             `yaml:"api_format" json:"api_format"`
	BaseURL             string             `yaml:"base_url" json:"base_url"`
	APIKey              string             `yaml:"api_key" json:"api_key"`
	Model               string             `yaml:"model" json:"model"`
	ContextWindow       int                `yaml:"context_window" json:"context_window"`
	MaxToolIterations   int                `yaml:"-" json:"-"`
	MaxRetries          int                `yaml:"-" json:"-"`
	Temperature         float64            `yaml:"-" json:"-"`
	MaxOutputTokens     int                `yaml:"max_output_tokens" json:"max_output_tokens"`
	MaxRunTokens        int                `yaml:"-" json:"-"`
	RunTimeoutSeconds   int                `yaml:"-" json:"-"`
	ReasoningEffort     string             `yaml:"reasoning_effort" json:"reasoning_effort"`
	ExposeReasoning     bool               `yaml:"expose_reasoning" json:"expose_reasoning"`
	ThinkingType        string             `yaml:"thinking_type" json:"thinking_type"`
	ThinkingBudgetTokens int               `yaml:"thinking_budget_tokens" json:"thinking_budget_tokens"`
	Fallbacks           []FallbackProvider `yaml:"fallbacks" json:"fallbacks"`
}
type FallbackProvider struct {
	Name          string  `yaml:"name" json:"name"`
	APIFormat     string  `yaml:"api_format" json:"api_format"`
	BaseURL       string  `yaml:"base_url" json:"base_url"`
	APIKey        string  `yaml:"api_key" json:"api_key"`
	Model         string  `yaml:"model" json:"model"`
	ContextWindow int     `yaml:"context_window" json:"context_window"`
	Temperature   float64 `yaml:"-" json:"-"`
}
type StoreConfig struct {
	DBPath string `yaml:"db_path" json:"db_path"`
}
type CompressionConfig struct {
	MaxOutputTokens  int     `yaml:"-" json:"-"`
	ThresholdPercent float64 `yaml:"threshold_percent" json:"threshold_percent"`
	ProtectFirstN    int     `yaml:"protect_first_n" json:"protect_first_n"`
	ProtectLastN     int     `yaml:"protect_last_n" json:"protect_last_n"`
	TargetRatio      float64 `yaml:"target_ratio" json:"target_ratio"`
}
type ToolsConfig struct {
	WorkDir             string   `yaml:"work_dir" json:"work_dir"`
	ApprovalMode        string   `yaml:"approval_mode" json:"approval_mode"`
	TerminalEnabled     bool     `yaml:"terminal_enabled" json:"terminal_enabled"`
	ProgressiveTools    bool     `yaml:"progressive_tools" json:"progressive_tools"`
	ToolSearchThreshold int      `yaml:"tool_search_threshold" json:"tool_search_threshold"`
	EnabledToolsets     []string `yaml:"enabled_toolsets" json:"enabled_toolsets"`
}
type MemoryConfig struct {
	Use                    bool `yaml:"use" json:"use"`
	CapacityLimit          int  `yaml:"-" json:"-"`
	ConsolidationThreshold int  `yaml:"-" json:"-"`
	AutoNudge              bool `yaml:"-" json:"-"`
	AutoConsolidate        bool `yaml:"-" json:"-"`
}
type SecurityConfig struct {
	Policies []security.Policy `yaml:"policies" json:"policies"`
}

type FeatureConfig struct {
	Compact   bool `yaml:"compact" json:"compact"`
	Rewind    bool `yaml:"rewind" json:"rewind"`
	Subagent  bool `yaml:"subagent" json:"subagent"`
	Memory    bool `yaml:"memory" json:"memory"`
	Skill     bool `yaml:"skill" json:"skill"`
	MCP       bool `yaml:"mcp" json:"mcp"`
	Skills    bool `yaml:"-" json:"-"`
	Subagents bool `yaml:"-" json:"-"`
	Commands  bool `yaml:"-" json:"-"`
	Workflow  bool `yaml:"-" json:"-"`
	ModelIO   bool `yaml:"-" json:"-"`
}
type UIConfig struct {
	Locale string `yaml:"locale" json:"locale"`
	Theme  string `yaml:"theme" json:"theme"`
}
type SkillsConfig struct {
	Enabled             bool     `yaml:"enabled" json:"enabled"`
	IncludeInstructions bool     `yaml:"includeInstructions" json:"includeInstructions"`
	Roots               []string `yaml:"roots" json:"roots"`
	MetadataBudget      int      `yaml:"metadataBudget" json:"metadataBudget"`
}
type CommandsConfig struct {
	Roots           []string `yaml:"roots" json:"roots"`
	MetadataBudget  int      `yaml:"metadata_budget" json:"metadata_budget"`
	MaxCommandBytes int      `yaml:"max_command_bytes" json:"max_command_bytes"`
}
type WorkflowConfig struct {
	Enabled        bool     `yaml:"enabled" json:"enabled"`
	Roots          []string `yaml:"roots" json:"roots"`
	MaxNodes       int      `yaml:"max_nodes" json:"max_nodes"`
	MaxConcurrency int      `yaml:"max_concurrency" json:"max_concurrency"`
	MaxAgentCalls  int      `yaml:"max_agent_calls" json:"max_agent_calls"`
	TimeoutSeconds int      `yaml:"timeout_seconds" json:"timeout_seconds"`
}
type TelemetryConfig struct {
	Mode             string `yaml:"mode" json:"mode"`
	Directory        string `yaml:"directory" json:"directory"`
	QueueSize        int    `yaml:"queue_size" json:"queue_size"`
	MaxArtifactBytes int64  `yaml:"max_artifact_bytes" json:"max_artifact_bytes"`
	RetentionFiles   int    `yaml:"retention_files" json:"retention_files"`
}

func DefaultConfig() Config {
	return Config{
		ModelStream:       ModelStreamConfig{IdleTimeoutMs: DefaultModelStreamIdleTimeoutMs},
		Permission:        PermissionConfig{Mode: "build", AllowedTools: []string{}, DisallowedTools: []string{}},
		Storage:           StorageConfig{Dir: "./data", SessionDBPath: "./data/agent.db"},
		Network:           NetworkConfig{Timeout: DefaultNetworkTimeoutMs},
		Logging:           LoggingConfig{Level: "info", Format: "text"},
		ToolConcurrency:   ToolConcurrencyConfig{MaxConcurrency: DefaultToolConcurrency},
		ModelAnomalyGuard: ModelAnomalyGuardConfig{RepeatedToolCallWarningThreshold: 3, MaxBudgetWarningsPerTurn: 3},
		MCP:               MCPConfig{Servers: map[string]MCPServerConfig{}},
		SkillOverrides:    map[string]EntryOverride{}, CommandOverrides: map[string]EntryOverride{},
		Server: ServerConfig{Host: "127.0.0.1", Port: 3030, MaxRequestBytes: 1024 * 1024,
			RateLimitPerMinute: 300, MaxConcurrentRuns: 10, LogFormat: "text", TerminalEnabled: true},
		LLM: LLMConfig{APIFormat: "openai", BaseURL: "https://api.openai.com/v1", Model: "", ContextWindow: DefaultContextWindow,
			MaxRetries: DefaultModelRetries, MaxOutputTokens: DefaultOutputTokens, ExposeReasoning: true,
			ThinkingType: ThinkingTypeDisabled, ThinkingBudgetTokens: 16000,
			RequestTimeoutMs: DefaultNetworkTimeoutMs, StreamIdleTimeoutMs: DefaultModelStreamIdleTimeoutMs,
			RetryBaseDelayMs: 2000, RetryBackoffFactor: 2, RetryMaxDelayMs: 60000, RetryJitter: true},
		Store:       StoreConfig{DBPath: "./data/agent.db"},
		Workspace:   WorkspaceConfig{Path: ".", SoulFile: "AGENTS.md"},
		Compression: CompressionConfig{MaxOutputTokens: DefaultOutputTokens, ThresholdPercent: 1.0, ProtectFirstN: 3, ProtectLastN: 20, TargetRatio: 0.20},
		Tools:       ToolsConfig{ApprovalMode: "build", TerminalEnabled: true, ProgressiveTools: true, ToolSearchThreshold: 10},
		Memory:      MemoryConfig{Use: true},
		Features:    FeatureConfig{Compact: true, Rewind: true, Subagent: true, Memory: true, Skill: true, MCP: true, Skills: true, Commands: true, Subagents: true, Workflow: true, ModelIO: true},
		UI:          UIConfig{Locale: "en-US", Theme: "auto"},
		Skills:      SkillsConfig{Enabled: true, IncludeInstructions: true, MetadataBudget: DefaultSkillMetadataBudget},
		Commands:    CommandsConfig{MetadataBudget: 8192, MaxCommandBytes: 100_000},
		Workflow:    WorkflowConfig{Enabled: true, MaxNodes: 100, MaxConcurrency: DefaultToolConcurrency, MaxAgentCalls: 0, TimeoutSeconds: 0},
		Telemetry:   TelemetryConfig{Mode: "redacted", Directory: "./data/model-io", QueueSize: 256, MaxArtifactBytes: 4 * 1024 * 1024, RetentionFiles: 100},
	}
}

func Load(path string) (Config, error) { return loadConfigFiles(path) }

// LoadLayered applies user configuration before the explicit project/service file.
func LoadLayered(path, userPath string) (Config, error) {
	if userPath == path {
		return loadConfigFiles(path)
	}
	return loadConfigFiles(userPath, path)
}

func loadConfigFiles(paths ...string) (Config, error) {
	cfg := DefaultConfig()
	for _, path := range paths {
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return cfg, fmt.Errorf("read config file %s: %w", path, err)
		}
		if err := rejectRemovedConfigFields(data); err != nil {
			return cfg, fmt.Errorf("%s: %w", path, err)
		}
		decoder := yaml.NewDecoder(strings.NewReader(string(data)))
		decoder.KnownFields(true)
		if err := decoder.Decode(&cfg); err != nil {
			return cfg, fmt.Errorf("parse config file %s: %w", path, err)
		}
		if err := ensureSingleDocument(decoder); err != nil {
			return cfg, err
		}
	}
	applyEnvOverrides(&cfg)
	cfg.applyRuntimeSettings()
	if err := normalizePaths(&cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func ensureSingleDocument(decoder *yaml.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple YAML documents are not supported")
		}
		return fmt.Errorf("parse trailing YAML document: %w", err)
	}
	return nil
}

func expandEnv(value string) string {
	if value == "" {
		return value
	}
	return os.ExpandEnv(value)
}

func applyEnvOverrides(cfg *Config) {
	cfg.Server.AuthToken = expandEnv(cfg.Server.AuthToken)
	cfg.LLM.BaseURL = expandEnv(cfg.LLM.BaseURL)
	cfg.LLM.APIKey = expandEnv(cfg.LLM.APIKey)
	if value := os.Getenv("AUTH_TOKEN"); value != "" {
		cfg.Server.AuthToken = value
	}
	if value := os.Getenv("AGENT_CORE_PORT"); value != "" {
		if port, err := strconv.Atoi(value); err == nil {
			cfg.Server.Port = port
		}
	}
	setString := func(key string, target *string) {
		if value := os.Getenv(key); value != "" {
			*target = value
		}
	}
	setString("AGENT_CORE_HOST", &cfg.Server.Host)
	setString("LLM_API_KEY", &cfg.LLM.APIKey)
	setString("LLM_API_FORMAT", &cfg.LLM.APIFormat)
	setString("LLM_BASE_URL", &cfg.LLM.BaseURL)
	setString("LLM_MODEL", &cfg.LLM.Model)
	setString("DB_PATH", &cfg.Storage.SessionDBPath)
	setString("ZCODE_STORAGE_DIR", &cfg.Storage.Dir)
	setString("ZCODE_SESSION_DB", &cfg.Storage.SessionDBPath)
	setString("ZCODE_SESSION_DB_PATH", &cfg.Storage.SessionDBPath)
	setString("ZCODE_HTTP_PROXY", &cfg.Network.HTTPProxy)
	setString("ZCODE_NO_PROXY", &cfg.Network.NoProxy)
	setString("ZCODE_AGENT_CA_CERT", &cfg.Network.CACertFile)
	setString("ZCODE_LOG_FORMAT", &cfg.Logging.Format)
	for _, entry := range []struct {
		key    string
		target *int
	}{
		{"ZCODE_TIMEOUT", &cfg.Network.Timeout}, {"ZCODE_HTTP_TIMEOUT", &cfg.Network.Timeout},
		{"ZCODE_MAX_TOOL_CONCURRENCY", &cfg.ToolConcurrency.MaxConcurrency},
		{"ZCODE_MODEL_RETRY_MAX_RETRIES", &cfg.LLM.MaxRetries},
		{"ZCODE_MODEL_RETRY_BASE_DELAY_MS", &cfg.LLM.RetryBaseDelayMs},
		{"ZCODE_MODEL_RETRY_MAX_DELAY_MS", &cfg.LLM.RetryMaxDelayMs},
	} {
		if value := os.Getenv(entry.key); value != "" {
			n, err := strconv.Atoi(value)
			if err != nil {
				n = -1
			}
			*entry.target = n
		}
	}

	if value := os.Getenv("ZCODE_MODEL_RETRY_BACKOFF_FACTOR"); value != "" {
		if n, err := strconv.ParseFloat(value, 64); err == nil {
			cfg.LLM.RetryBackoffFactor = n
		} else {
			cfg.LLM.RetryBackoffFactor = -1
		}
	}
	setString("AGENT_CORE_WORKSPACE", &cfg.Workspace.Path)
}

func normalizePaths(cfg *Config) error {
	workspace, err := filepath.Abs(cfg.Workspace.Path)
	if err != nil {
		return fmt.Errorf("resolve workspace: %w", err)
	}
	cfg.Workspace.Path = filepath.Clean(workspace)
	cfg.Tools.WorkDir = cfg.Workspace.Path
	return nil
}

// Redacted returns an effective configuration safe for diagnostics.
func (c Config) Redacted() Config {
	if proxy, err := url.Parse(c.Network.HTTPProxy); err == nil && proxy.User != nil {
		proxy.User = nil
		c.Network.HTTPProxy = proxy.String()
	}
	if c.Server.AuthToken != "" {
		c.Server.AuthToken = "***"
	}
	if c.LLM.APIKey != "" {
		c.LLM.APIKey = "***"
	}
	c.LLM.Fallbacks = append([]FallbackProvider(nil), c.LLM.Fallbacks...)
	servers := make(map[string]MCPServerConfig, len(c.MCP.Servers))
	for name, server := range c.MCP.Servers {
		server.Env = maskValues(server.Env)
		server.Headers = maskValues(server.Headers)
		servers[name] = server
	}
	c.MCP.Servers = servers
	for i := range c.LLM.Fallbacks {
		if c.LLM.Fallbacks[i].APIKey != "" {
			c.LLM.Fallbacks[i].APIKey = "***"
		}
	}
	return c
}

type ValidateMode int

const (
	ModeHTTP ValidateMode = iota
	ModeMCP
)

func (c *Config) Validate(mode ValidateMode) error {
	if err := c.validateRuntimeSettings(); err != nil {
		return err
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port must be between 1 and 65535")
	}
	if c.Server.MaxRequestBytes < 1024 {
		return fmt.Errorf("server.max_request_bytes must be at least 1024")
	}
	if c.Server.RateLimitPerMinute <= 0 || c.Server.MaxConcurrentRuns <= 0 {
		return fmt.Errorf("server rate and concurrency limits must be positive")
	}
	if c.Server.LogFormat != "text" && c.Server.LogFormat != "json" {
		return fmt.Errorf("server.log_format must be text or json")
	}
	for _, origin := range c.Server.CORSOrigins {
		if origin == "*" || !regexp.MustCompile(`^https?://[^/]+$`).MatchString(origin) {
			return fmt.Errorf("invalid exact CORS origin %q", origin)
		}
	}
	if c.LLM.BaseURL == "" || security.CheckHTTPURL(c.LLM.BaseURL) != nil {
		return fmt.Errorf("llm.base_url is invalid")
	}
	if err := ValidateAPIFormat(c.LLM.APIFormat); err != nil {
		return fmt.Errorf("llm.api_format: %w", err)
	}
	if c.LLM.ContextWindow <= 0 || c.LLM.MaxToolIterations < 0 || c.LLM.MaxRetries < 0 {
		return fmt.Errorf("invalid LLM model, context window, iteration or retry budget")
	}
	if c.LLM.MaxOutputTokens <= 0 || c.LLM.MaxRunTokens < 0 || c.LLM.RunTimeoutSeconds < 0 {
		return fmt.Errorf("LLM output/run budgets must be positive")
	}
	if err := ValidateThinking(c.LLM); err != nil {
		return fmt.Errorf("llm.thinking: %w", err)
	}
	if c.LLM.Temperature < 0 || c.LLM.Temperature > 2 {
		return fmt.Errorf("llm.temperature must be between 0 and 2")
	}
	for _, fallback := range c.LLM.Fallbacks {
		if err := ValidateAPIFormat(fallback.APIFormat); err != nil {
			return fmt.Errorf("fallback %q api_format: %w", fallback.Name, err)
		}
		if fallback.BaseURL == "" || security.CheckHTTPURL(fallback.BaseURL) != nil || fallback.Model == "" || fallback.ContextWindow <= 0 {
			return fmt.Errorf("invalid fallback provider %q", fallback.Name)
		}
	}
	if c.Store.DBPath == "" {
		return fmt.Errorf("storage.sessionDbPath is required")
	}

	if c.Workspace.Path == "" || c.Workspace.SoulFile == "" {
		return fmt.Errorf("workspace.path and soul_file are required")
	}
	if c.Compression.ThresholdPercent <= 0 || c.Compression.ThresholdPercent > 1 || c.Compression.TargetRatio <= 0 || c.Compression.TargetRatio > 1 {
		return fmt.Errorf("invalid compression ratios")
	}
	if c.Compression.ProtectFirstN < 0 || c.Compression.ProtectLastN < 0 {
		return fmt.Errorf("compression protection counts cannot be negative")
	}
	switch c.Tools.ApprovalMode {
	case "smart", "plan", "build", "edit", "guarded", "yolo", "auto":
	default:
		return fmt.Errorf("invalid tools.approval_mode %q", c.Tools.ApprovalMode)
	}
	if c.Tools.ToolSearchThreshold <= 0 {
		return fmt.Errorf("tools.tool_search_threshold must be positive")
	}
	for _, policy := range c.Security.Policies {
		for _, condition := range policy.Conditions {
			if condition.Operator == "regex" {
				if len(condition.Values) == 0 {
					return fmt.Errorf("policy %s regex requires values", policy.Name)
				}
				for _, value := range condition.Values {
					if _, err := regexp.Compile(value); err != nil {
						return fmt.Errorf("policy %s regex: %w", policy.Name, err)
					}
				}
			}
		}
	}
	if c.Skills.MetadataBudget <= 0 || c.Commands.MetadataBudget <= 0 || c.Commands.MaxCommandBytes <= 0 {
		return fmt.Errorf("skill and command budgets must be positive")
	}
	if c.Workflow.MaxNodes <= 0 || c.Workflow.MaxConcurrency <= 0 || c.Workflow.MaxAgentCalls < 0 || c.Workflow.TimeoutSeconds < 0 {
		return fmt.Errorf("workflow limits must be positive")
	}
	if c.Telemetry.Mode != "redacted" && c.Telemetry.Mode != "training" {
		return fmt.Errorf("telemetry.mode must be redacted or training")
	}
	if c.Features.ModelIO && (c.Telemetry.Directory == "" || c.Telemetry.QueueSize <= 0 || c.Telemetry.MaxArtifactBytes <= 0 || c.Telemetry.RetentionFiles <= 0) {
		return fmt.Errorf("invalid model-io capture limits")
	}
	if c.UI.Locale != "auto" && c.UI.Locale != "en-US" && c.UI.Locale != "zh-CN" {
		return fmt.Errorf("ui.locale must be auto, en-US, or zh-CN")
	}
	if mode == ModeHTTP {
		host := c.Server.Host
		loopback := host == "127.0.0.1" || host == "localhost" || host == "::1" || host == ""
		if !loopback && c.Server.AuthToken == "" {
			return fmt.Errorf("security: non-loopback binding requires AUTH_TOKEN")
		}
		if !loopback && c.Tools.TerminalEnabled {
			return fmt.Errorf("security: terminal must be disabled for network-exposed deployments; it is not an OS sandbox")
		}
	}
	return nil
}

func (c *Config) Addr() string { return fmt.Sprintf("%s:%d", c.Server.Host, c.Server.Port) }
