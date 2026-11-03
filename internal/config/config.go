package config

import (
	"agent-core/internal/security"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server      ServerConfig      `yaml:"server"`
	LLM         LLMConfig         `yaml:"llm"`
	Embedding   EmbeddingConfig   `yaml:"embedding"`
	Store       StoreConfig       `yaml:"store"`
	Workspace   WorkspaceConfig   `yaml:"workspace"`
	Compression CompressionConfig `yaml:"compression"`
	Tools       ToolsConfig       `yaml:"tools"`
	Memory      MemoryConfig      `yaml:"memory"`
	Security    SecurityConfig    `yaml:"security"`
}

type WorkspaceConfig struct {
	Path     string `yaml:"path"`
	SoulFile string `yaml:"soul_file"`
}
type ServerConfig struct {
	Host               string   `yaml:"host"`
	Port               int      `yaml:"port"`
	AuthToken          string   `yaml:"auth_token"`
	MaxRequestBytes    int64    `yaml:"max_request_bytes"`
	CORSOrigins        []string `yaml:"cors_origins"`
	RateLimitPerMinute int      `yaml:"rate_limit_per_minute"`
	MaxConcurrentRuns  int      `yaml:"max_concurrent_runs"`
	LogFormat          string   `yaml:"log_format"`
}
type LLMConfig struct {
	BaseURL           string             `yaml:"base_url"`
	APIKey            string             `yaml:"api_key"`
	Model             string             `yaml:"model"`
	ContextWindow     int                `yaml:"context_window"`
	MaxToolIterations int                `yaml:"max_tool_iterations"`
	MaxRetries        int                `yaml:"max_retries"`
	Temperature       float64            `yaml:"temperature"`
	MaxOutputTokens   int                `yaml:"max_output_tokens"`
	MaxRunTokens      int                `yaml:"max_run_tokens"`
	RunTimeoutSeconds int                `yaml:"run_timeout_seconds"`
	ReasoningEffort   string             `yaml:"reasoning_effort"`
	Fallbacks         []FallbackProvider `yaml:"fallbacks"`
}
type FallbackProvider struct {
	Name          string  `yaml:"name"`
	BaseURL       string  `yaml:"base_url"`
	APIKey        string  `yaml:"api_key"`
	Model         string  `yaml:"model"`
	ContextWindow int     `yaml:"context_window"`
	Temperature   float64 `yaml:"temperature"`
}
type EmbeddingConfig struct {
	BaseURL    string `yaml:"base_url"`
	APIKey     string `yaml:"api_key"`
	Model      string `yaml:"model"`
	Dimensions int    `yaml:"dimensions"`
}
type StoreConfig struct {
	DBPath        string `yaml:"db_path"`
	VectorBackend string `yaml:"vector_backend"`
}
type CompressionConfig struct {
	ThresholdPercent float64 `yaml:"threshold_percent"`
	ProtectFirstN    int     `yaml:"protect_first_n"`
	ProtectLastN     int     `yaml:"protect_last_n"`
	TargetRatio      float64 `yaml:"target_ratio"`
}
type ToolsConfig struct {
	WorkDir             string   `yaml:"work_dir"`
	ApprovalMode        string   `yaml:"approval_mode"`
	TerminalEnabled     bool     `yaml:"terminal_enabled"`
	ProgressiveTools    bool     `yaml:"progressive_tools"`
	ToolSearchThreshold int      `yaml:"tool_search_threshold"`
	EnabledToolsets     []string `yaml:"enabled_toolsets"`
}
type MemoryConfig struct {
	CapacityLimit          int  `yaml:"capacity_limit"`
	ConsolidationThreshold int  `yaml:"consolidation_threshold"`
	AutoNudge              bool `yaml:"auto_nudge"`
	AutoConsolidate        bool `yaml:"auto_consolidate"`
}
type SecurityConfig struct {
	Policies []security.Policy `yaml:"policies"`
}

func DefaultConfig() Config {
	return Config{
		Server: ServerConfig{Host: "127.0.0.1", Port: 8900, MaxRequestBytes: 1024 * 1024,
			RateLimitPerMinute: 300, MaxConcurrentRuns: 10, LogFormat: "text"},
		LLM: LLMConfig{BaseURL: "https://api.openai.com/v1", Model: "gpt-4o", ContextWindow: 128000,
			MaxToolIterations: 15, MaxRetries: 3, Temperature: 0.7, MaxOutputTokens: 4096,
			MaxRunTokens: 100000, RunTimeoutSeconds: 1800},
		Embedding:   EmbeddingConfig{BaseURL: "https://api.openai.com/v1", Model: "text-embedding-3-small", Dimensions: 1536},
		Store:       StoreConfig{DBPath: "./data/agent.db", VectorBackend: "sqlite"},
		Workspace:   WorkspaceConfig{Path: "./workspace", SoulFile: "SOUL.md"},
		Compression: CompressionConfig{ThresholdPercent: 0.50, ProtectFirstN: 3, ProtectLastN: 20, TargetRatio: 0.20},
		Tools:       ToolsConfig{ApprovalMode: "smart", TerminalEnabled: true, ProgressiveTools: true, ToolSearchThreshold: 10},
		Memory:      MemoryConfig{CapacityLimit: 200, ConsolidationThreshold: 160, AutoNudge: true},
	}
}

func Load(path string) (Config, error) {
	cfg := DefaultConfig()
	workspaceExplicit, workDirExplicit := false, false
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return cfg, fmt.Errorf("read config file: %w", err)
	}
	if err == nil {
		decoder := yaml.NewDecoder(strings.NewReader(string(data)))
		decoder.KnownFields(true)
		if err := decoder.Decode(&cfg); err != nil {
			return cfg, fmt.Errorf("parse config file: %w", err)
		}
		if err := ensureSingleDocument(decoder); err != nil {
			return cfg, err
		}
		var explicit struct {
			Workspace struct {
				Path *string `yaml:"path"`
			} `yaml:"workspace"`
			Tools struct {
				WorkDir *string `yaml:"work_dir"`
			} `yaml:"tools"`
		}
		if err := yaml.Unmarshal(data, &explicit); err != nil {
			return cfg, err
		}
		workspaceExplicit = explicit.Workspace.Path != nil
		workDirExplicit = explicit.Tools.WorkDir != nil
	}
	applyEnvOverrides(&cfg)
	if err := normalizePaths(&cfg, workspaceExplicit, workDirExplicit); err != nil {
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
	cfg.Embedding.BaseURL = expandEnv(cfg.Embedding.BaseURL)
	cfg.Embedding.APIKey = expandEnv(cfg.Embedding.APIKey)
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
	setString("LLM_BASE_URL", &cfg.LLM.BaseURL)
	setString("LLM_MODEL", &cfg.LLM.Model)
	setString("EMBEDDING_API_KEY", &cfg.Embedding.APIKey)
	setString("DB_PATH", &cfg.Store.DBPath)
	setString("VECTOR_BACKEND", &cfg.Store.VectorBackend)
	setString("WORK_DIR", &cfg.Tools.WorkDir)
	setString("AGENT_CORE_WORKSPACE", &cfg.Workspace.Path)
}

func normalizePaths(cfg *Config, workspaceExplicit, workDirExplicit bool) error {
	workspaceEnv := os.Getenv("AGENT_CORE_WORKSPACE") != ""
	if workDirExplicit && !workspaceExplicit && !workspaceEnv {
		cfg.Workspace.Path = cfg.Tools.WorkDir
	}
	if workspaceEnv || (workspaceExplicit && !workDirExplicit) || cfg.Tools.WorkDir == "" {
		cfg.Tools.WorkDir = cfg.Workspace.Path
	}
	if cfg.Workspace.Path != cfg.Tools.WorkDir {
		workspace, err1 := filepath.Abs(cfg.Workspace.Path)
		workDir, err2 := filepath.Abs(cfg.Tools.WorkDir)
		if err1 == nil && err2 == nil && filepath.Clean(workspace) != filepath.Clean(workDir) {
			return fmt.Errorf("workspace.path and tools.work_dir differ; use workspace.path as the single source")
		}
	}
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
	if c.Server.AuthToken != "" {
		c.Server.AuthToken = "***"
	}
	if c.LLM.APIKey != "" {
		c.LLM.APIKey = "***"
	}
	for i := range c.LLM.Fallbacks {
		if c.LLM.Fallbacks[i].APIKey != "" {
			c.LLM.Fallbacks[i].APIKey = "***"
		}
	}
	if c.Embedding.APIKey != "" {
		c.Embedding.APIKey = "***"
	}
	return c
}

type ValidateMode int

const (
	ModeHTTP ValidateMode = iota
	ModeMCP
)

func (c *Config) Validate(mode ValidateMode) error {
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
	if c.LLM.Model == "" || c.LLM.ContextWindow <= 0 || c.LLM.MaxToolIterations <= 0 || c.LLM.MaxRetries < 0 {
		return fmt.Errorf("invalid LLM model, context window, iteration or retry budget")
	}
	if c.LLM.MaxOutputTokens <= 0 || c.LLM.MaxRunTokens <= 0 || c.LLM.RunTimeoutSeconds <= 0 {
		return fmt.Errorf("LLM output/run budgets must be positive")
	}
	if c.LLM.Temperature < 0 || c.LLM.Temperature > 2 {
		return fmt.Errorf("llm.temperature must be between 0 and 2")
	}
	for _, fallback := range c.LLM.Fallbacks {
		if fallback.BaseURL == "" || security.CheckHTTPURL(fallback.BaseURL) != nil || fallback.Model == "" || fallback.ContextWindow <= 0 {
			return fmt.Errorf("invalid fallback provider %q", fallback.Name)
		}
	}
	if c.Embedding.Dimensions <= 0 || security.CheckHTTPURL(c.Embedding.BaseURL) != nil {
		return fmt.Errorf("invalid embedding configuration")
	}
	if c.Store.DBPath == "" {
		return fmt.Errorf("store.db_path is required")
	}
	switch c.Store.VectorBackend {
	case "sqlite", "sqlite-vec", "none":
	default:
		return fmt.Errorf("unsupported store.vector_backend %q", c.Store.VectorBackend)
	}
	if c.Workspace.Path == "" || c.Workspace.SoulFile == "" {
		return fmt.Errorf("workspace.path and soul_file are required")
	}
	if c.Compression.ThresholdPercent <= 0 || c.Compression.ThresholdPercent >= 1 || c.Compression.TargetRatio <= 0 || c.Compression.TargetRatio > 1 {
		return fmt.Errorf("invalid compression ratios")
	}
	if c.Compression.ProtectFirstN < 0 || c.Compression.ProtectLastN < 0 {
		return fmt.Errorf("compression protection counts cannot be negative")
	}
	switch c.Tools.ApprovalMode {
	case "smart", "plan", "build", "edit", "guarded", "yolo":
	default:
		return fmt.Errorf("invalid tools.approval_mode %q", c.Tools.ApprovalMode)
	}
	if c.Tools.ToolSearchThreshold <= 0 {
		return fmt.Errorf("tools.tool_search_threshold must be positive")
	}
	if c.Memory.CapacityLimit <= 0 || c.Memory.ConsolidationThreshold <= 0 || c.Memory.ConsolidationThreshold > c.Memory.CapacityLimit {
		return fmt.Errorf("invalid memory capacity or consolidation threshold")
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
