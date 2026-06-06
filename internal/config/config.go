package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration for Agent Core.
type Config struct {
	Server      ServerConfig      `yaml:"server"`
	LLM         LLMConfig         `yaml:"llm"`
	Embedding   EmbeddingConfig   `yaml:"embedding"`
	Store       StoreConfig       `yaml:"store"`
	Workspace   WorkspaceConfig   `yaml:"workspace"`
	Compression CompressionConfig `yaml:"compression"`
	Tools       ToolsConfig       `yaml:"tools"`
	Memory      MemoryConfig      `yaml:"memory"`
}

type WorkspaceConfig struct {
	Path     string `yaml:"path"`
	SoulFile string `yaml:"soul_file"`
}

type ServerConfig struct {
	Host      string `yaml:"host"`
	Port      int    `yaml:"port"`
	AuthToken string `yaml:"auth_token"`
}

type LLMConfig struct {
	BaseURL          string  `yaml:"base_url"`
	APIKey           string  `yaml:"api_key"`
	Model            string  `yaml:"model"`
	ContextWindow    int     `yaml:"context_window"`
	MaxToolIterations int    `yaml:"max_tool_iterations"`
	MaxRetries       int     `yaml:"max_retries"`
	Temperature      float64 `yaml:"temperature"`

	// Failover providers (optional). If set, base_url/api_key/model are the primary.
	Fallbacks []FallbackProvider `yaml:"fallbacks"`
}

// FallbackProvider defines a fallback LLM provider.
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
	WorkDir      string `yaml:"work_dir"`
	ApprovalMode string `yaml:"approval_mode"`
}

type MemoryConfig struct {
	CapacityLimit              int  `yaml:"capacity_limit"`
	ConsolidationThreshold     int  `yaml:"consolidation_threshold"`
	AutoNudge                  bool `yaml:"auto_nudge"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Server: ServerConfig{
			Host: "0.0.0.0",
			Port: 8900,
		},
		LLM: LLMConfig{
			BaseURL:          "https://api.openai.com/v1",
			Model:            "gpt-4o",
			ContextWindow:    128000,
			MaxToolIterations: 15,
			MaxRetries:       3,
			Temperature:      0.7,
		},
		Embedding: EmbeddingConfig{
			BaseURL:    "https://api.openai.com/v1",
			Model:      "text-embedding-3-small",
			Dimensions: 1536,
		},
		Store: StoreConfig{
			DBPath:        "./data/agent.db",
			VectorBackend: "sqlite-vec",
		},
		Workspace: WorkspaceConfig{
			Path:     "./workspace",
			SoulFile: "SOUL.md",
		},
		Compression: CompressionConfig{
			ThresholdPercent: 0.50,
			ProtectFirstN:    3,
			ProtectLastN:     20,
			TargetRatio:      0.20,
		},
		Tools: ToolsConfig{
			WorkDir:      "./workspace",
			ApprovalMode: "smart",
		},
		Memory: MemoryConfig{
			CapacityLimit:          200,
			ConsolidationThreshold: 160,
			AutoNudge:              true,
		},
	}
}

// Load reads configuration from a YAML file and applies environment variable overrides.
func Load(path string) (Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			applyEnvOverrides(&cfg)
			return cfg, nil
		}
		return cfg, fmt.Errorf("read config file: %w", err)
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config file: %w", err)
	}

	applyEnvOverrides(&cfg)
	return cfg, nil
}

// expandEnv replaces ${VAR} references with environment variable values.
func expandEnv(s string) string {
	if s == "" {
		return s
	}
	return os.ExpandEnv(s)
}

// applyEnvOverrides applies environment variable overrides to the config.
func applyEnvOverrides(cfg *Config) {
	cfg.Server.AuthToken = expandEnv(cfg.Server.AuthToken)
	cfg.LLM.BaseURL = expandEnv(cfg.LLM.BaseURL)
	cfg.LLM.APIKey = expandEnv(cfg.LLM.APIKey)
	cfg.Embedding.BaseURL = expandEnv(cfg.Embedding.BaseURL)
	cfg.Embedding.APIKey = expandEnv(cfg.Embedding.APIKey)

	// Direct environment variable overrides
	if v := os.Getenv("AGENT_CORE_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			cfg.Server.Port = port
		}
	}
	if v := os.Getenv("AGENT_CORE_HOST"); v != "" {
		cfg.Server.Host = v
	}
	if v := os.Getenv("LLM_API_KEY"); v != "" {
		cfg.LLM.APIKey = v
	}
	if v := os.Getenv("LLM_BASE_URL"); v != "" {
		cfg.LLM.BaseURL = v
	}
	if v := os.Getenv("LLM_MODEL"); v != "" {
		cfg.LLM.Model = v
	}
	if v := os.Getenv("EMBEDDING_API_KEY"); v != "" {
		cfg.Embedding.APIKey = v
	}
	if v := os.Getenv("DB_PATH"); v != "" {
		cfg.Store.DBPath = v
	}
	if v := os.Getenv("VECTOR_BACKEND"); v != "" {
		cfg.Store.VectorBackend = v
	}
	if v := os.Getenv("WORK_DIR"); v != "" {
		cfg.Tools.WorkDir = v
	}
	if v := os.Getenv("AGENT_CORE_WORKSPACE"); v != "" {
		cfg.Workspace.Path = v
	}

	// Backward compatibility: if workspace.path is empty but tools.work_dir is set, use it
	if cfg.Workspace.Path == "" && cfg.Tools.WorkDir != "" {
		cfg.Workspace.Path = cfg.Tools.WorkDir
	}
	// Sync workspace path back to tools.work_dir for builtin tools
	if cfg.Tools.WorkDir == "" && cfg.Workspace.Path != "" {
		cfg.Tools.WorkDir = cfg.Workspace.Path
	}

	// Resolve workspace path to absolute if relative
	if cfg.Workspace.Path != "" && !strings.HasPrefix(cfg.Workspace.Path, "/") {
		if wd, err := os.Getwd(); err == nil {
			cfg.Workspace.Path = filepath.Join(wd, cfg.Workspace.Path)
		}
	}
	// Also resolve tools.work_dir for backward compat
	if cfg.Tools.WorkDir != "" && !strings.HasPrefix(cfg.Tools.WorkDir, "/") {
		if wd, err := os.Getwd(); err == nil {
			cfg.Tools.WorkDir = filepath.Join(wd, cfg.Tools.WorkDir)
		}
	}
}

// Addr returns the listen address for the HTTP server.
func (c *Config) Addr() string {
	return fmt.Sprintf("%s:%d", c.Server.Host, c.Server.Port)
}
