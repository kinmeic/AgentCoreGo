package project

import (
	"agent-core/internal/config"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Declaration binds a project-controlled file to its exact reviewed content.
type Declaration struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

// Report describes the effective project layer and its trust bundle.
type Report struct {
	Workspace    string        `json:"workspace"`
	Layers       []string      `json:"layers"`
	Declarations []Declaration `json:"declarations"`
	BundleDigest string        `json:"bundle_digest"`
}

type fileConfig struct {
	Model    string `yaml:"model" json:"model"`
	Effort   string `yaml:"effort" json:"effort"`
	Mode     string `yaml:"mode" json:"mode"`
	Features struct {
		Skills    *bool `yaml:"skills" json:"skills"`
		Commands  *bool `yaml:"commands" json:"commands"`
		Subagents *bool `yaml:"subagents" json:"subagents"`
		Workflow  *bool `yaml:"workflow" json:"workflow"`
		ModelIO   *bool `yaml:"model_io" json:"model_io"`
	} `yaml:"features" json:"features"`
	UI struct {
		Locale string `yaml:"locale" json:"locale"`
	} `yaml:"ui" json:"ui"`
	Skills struct {
		Roots          []string `yaml:"roots" json:"roots"`
		MetadataBudget int      `yaml:"metadata_budget" json:"metadata_budget"`
	} `yaml:"skills" json:"skills"`
	Commands struct {
		Roots           []string `yaml:"roots" json:"roots"`
		MetadataBudget  int      `yaml:"metadata_budget" json:"metadata_budget"`
		MaxCommandBytes int      `yaml:"max_command_bytes" json:"max_command_bytes"`
	} `yaml:"commands" json:"commands"`
	Workflow struct {
		Enabled        *bool    `yaml:"enabled" json:"enabled"`
		Roots          []string `yaml:"roots" json:"roots"`
		MaxNodes       int      `yaml:"max_nodes" json:"max_nodes"`
		MaxConcurrency int      `yaml:"max_concurrency" json:"max_concurrency"`
		MaxAgentCalls  int      `yaml:"max_agent_calls" json:"max_agent_calls"`
		TimeoutSeconds int      `yaml:"timeout_seconds" json:"timeout_seconds"`
	} `yaml:"workflow" json:"workflow"`
}

// LoadAndApply merges only the allowed project subset and computes trust evidence.
func LoadAndApply(cfg *config.Config, workspace string) (Report, error) {
	workspace, err := canonicalWorkspace(workspace)
	if err != nil {
		return Report{}, err
	}
	report := Report{Workspace: workspace}
	for _, name := range []string{".agent-core/config.yaml", ".zcode/config.json", "zcode.json"} {
		path := filepath.Join(workspace, name)
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return report, err
		}
		if len(data) > 1024*1024 {
			return report, fmt.Errorf("project config %s exceeds 1 MiB", path)
		}
		var layer fileConfig
		if err := yaml.Unmarshal(data, &layer); err != nil {
			return report, fmt.Errorf("parse project config %s: %w", path, err)
		}
		applyLayer(cfg, layer, workspace)
		report.Layers = append(report.Layers, path)
		digest := sha256.Sum256(data)
		report.Declarations = append(report.Declarations, Declaration{Kind: "project_config", Path: path, Digest: hex.EncodeToString(digest[:])})
	}
	report.BundleDigest = BundleDigest(report.Declarations)
	return report, nil
}

func applyLayer(cfg *config.Config, layer fileConfig, workspace string) {
	if layer.Model != "" {
		cfg.LLM.Model = layer.Model
	}
	if layer.Effort != "" {
		cfg.LLM.ReasoningEffort = layer.Effort
	}
	if layer.Mode != "" && permissionRank(layer.Mode) <= permissionRank(cfg.Tools.ApprovalMode) {
		cfg.Tools.ApprovalMode = layer.Mode
	}
	features := []struct {
		value  *bool
		target *bool
	}{
		{layer.Features.Skills, &cfg.Features.Skills}, {layer.Features.Commands, &cfg.Features.Commands},
		{layer.Features.Subagents, &cfg.Features.Subagents}, {layer.Features.Workflow, &cfg.Features.Workflow},
		{layer.Features.ModelIO, &cfg.Features.ModelIO},
	}
	for _, feature := range features {
		if feature.value != nil {
			*feature.target = *feature.value
		}
	}
	if layer.UI.Locale != "" {
		cfg.UI.Locale = layer.UI.Locale
	}
	cfg.Skills.Roots = append(cfg.Skills.Roots, resolveRoots(workspace, layer.Skills.Roots)...)
	if layer.Skills.MetadataBudget > 0 {
		cfg.Skills.MetadataBudget = layer.Skills.MetadataBudget
	}
	cfg.Commands.Roots = append(cfg.Commands.Roots, resolveRoots(workspace, layer.Commands.Roots)...)
	if layer.Commands.MetadataBudget > 0 {
		cfg.Commands.MetadataBudget = layer.Commands.MetadataBudget
	}
	if layer.Commands.MaxCommandBytes > 0 {
		cfg.Commands.MaxCommandBytes = layer.Commands.MaxCommandBytes
	}
	if layer.Workflow.Enabled != nil {
		cfg.Workflow.Enabled = *layer.Workflow.Enabled
	}
	cfg.Workflow.Roots = append(cfg.Workflow.Roots, resolveRoots(workspace, layer.Workflow.Roots)...)
	if layer.Workflow.MaxNodes > 0 {
		cfg.Workflow.MaxNodes = layer.Workflow.MaxNodes
	}
	if layer.Workflow.MaxConcurrency > 0 {
		cfg.Workflow.MaxConcurrency = layer.Workflow.MaxConcurrency
	}
	if layer.Workflow.MaxAgentCalls > 0 {
		cfg.Workflow.MaxAgentCalls = layer.Workflow.MaxAgentCalls
	}
	if layer.Workflow.TimeoutSeconds > 0 {
		cfg.Workflow.TimeoutSeconds = layer.Workflow.TimeoutSeconds
	}
}

func resolveRoots(workspace string, roots []string) []string {
	result := make([]string, 0, len(roots))
	for _, root := range roots {
		if !filepath.IsAbs(root) {
			root = filepath.Join(workspace, root)
		}
		result = append(result, filepath.Clean(root))
	}
	return result
}

func permissionRank(mode string) int {
	switch strings.ToLower(mode) {
	case "plan":
		return 0
	case "build", "smart":
		return 1
	case "edit":
		return 2
	case "guarded":
		return 3
	case "yolo":
		return 4
	default:
		return -1
	}
}

// BundleDigest is stable across declaration ordering.
func BundleDigest(declarations []Declaration) string {
	digests := make([]string, 0, len(declarations))
	for _, declaration := range declarations {
		digests = append(digests, declaration.Digest)
	}
	sort.Strings(digests)
	encoded, _ := json.Marshal(digests)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func canonicalWorkspace(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		absolute = resolved
	}
	return filepath.Clean(absolute), nil
}
