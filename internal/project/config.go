package project

import (
	"agent-core/internal/config"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

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
	Workspace    string           `json:"workspace"`
	Layers       []string         `json:"layers"`
	Declarations []Declaration    `json:"declarations"`
	BundleDigest string           `json:"bundle_digest"`
	MCP          config.MCPConfig `json:"-"`
}

type fileConfig struct {
	MCP     config.MCPConfig                `yaml:"mcp"`
	Skill   map[string]config.EntryOverride `yaml:"skill"`
	Command map[string]config.EntryOverride `yaml:"command"`
}

// LoadAndApply merges only the allowed project subset and computes trust evidence.
func LoadAndApply(cfg *config.Config, workspace string) (Report, error) {
	workspace, err := canonicalWorkspace(workspace)
	if err != nil {
		return Report{}, err
	}
	report := Report{Workspace: workspace, MCP: config.MCPConfig{Servers: map[string]config.MCPServerConfig{}}}
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
		var document yaml.Node
		if err := yaml.Unmarshal(data, &document); err != nil {
			return report, err
		}
		if len(document.Content) > 0 {
			root := document.Content[0]
			if root.Kind != yaml.MappingNode {
				return report, fmt.Errorf("project config must be an object")
			}
			var allowed []*yaml.Node
			for i := 0; i+1 < len(root.Content); i += 2 {
				switch root.Content[i].Value {
				case "mcp", "skill", "command":
					allowed = append(allowed, root.Content[i], root.Content[i+1])
				}
			}
			root.Content = allowed
		}
		filtered, err := yaml.Marshal(&document)
		if err != nil {
			return report, err
		}
		decoder := yaml.NewDecoder(bytes.NewReader(filtered))
		decoder.KnownFields(true)
		if err := decoder.Decode(&layer); err != nil {
			return report, fmt.Errorf("parse project config %s: %w", path, err)
		}
		if err := config.ValidateMCP(layer.MCP); err != nil {
			return report, fmt.Errorf("project MCP: %w", err)
		}
		applyLayer(cfg, layer, workspace)
		for name, server := range layer.MCP.Servers {
			if _, userDeclared := cfg.MCP.Servers[name]; !userDeclared {
				report.MCP.Servers[name] = server
			}
		}
		report.Layers = append(report.Layers, path)
		digest := sha256.Sum256(data)
		report.Declarations = append(report.Declarations, Declaration{Kind: "project_config", Path: path, Digest: hex.EncodeToString(digest[:])})
	}
	report.BundleDigest = BundleDigest(report.Declarations)
	return report, nil
}

// Project layers cannot override user model, permissions, network, feature flags or roots.
// MCP declarations remain inert until the exact project bundle has been trusted.
func applyLayer(cfg *config.Config, layer fileConfig, workspace string) {
	for key, value := range layer.Skill {
		if filepath.IsAbs(key) {
			if cfg.SkillOverrides == nil {
				cfg.SkillOverrides = map[string]config.EntryOverride{}
			}
			cfg.SkillOverrides[filepath.Clean(key)] = value
		}
	}
	for key, value := range layer.Command {
		if filepath.IsAbs(key) {
			if cfg.CommandOverrides == nil {
				cfg.CommandOverrides = map[string]config.EntryOverride{}
			}
			cfg.CommandOverrides[filepath.Clean(key)] = value
		}
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
