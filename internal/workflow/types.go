package workflow

import (
	"agent-core/internal/store"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Definition is a versioned declarative workflow, not executable script.
type Definition struct {
	SchemaVersion int    `json:"schema_version" yaml:"schema_version"`
	Name          string `json:"name" yaml:"name"`
	Description   string `json:"description,omitempty" yaml:"description"`
	Nodes         []Node `json:"nodes" yaml:"nodes"`
}

// Node is one agent or tool activity. Items provides static fan-out.
type Node struct {
	ID             string         `json:"id" yaml:"id"`
	Type           string         `json:"type" yaml:"type"`
	DependsOn      []string       `json:"depends_on,omitempty" yaml:"depends_on"`
	Condition      string         `json:"condition,omitempty" yaml:"condition"`
	Prompt         string         `json:"prompt,omitempty" yaml:"prompt"`
	Tool           string         `json:"tool,omitempty" yaml:"tool"`
	Args           map[string]any `json:"args,omitempty" yaml:"args"`
	Items          []any          `json:"items,omitempty" yaml:"items"`
	Retry          int            `json:"retry,omitempty" yaml:"retry"`
	TimeoutSeconds int            `json:"timeout_seconds,omitempty" yaml:"timeout_seconds"`
}

// RunView joins immutable definition, scheduler state and node attempts.
type RunView struct {
	Record     *store.WorkflowDefinitionRecord `json:"record"`
	Definition Definition                      `json:"definition"`
	Nodes      []*store.WorkflowNodeRecord     `json:"nodes"`
}

func ParseDefinition(data []byte) (Definition, error) {
	var definition Definition
	if err := yaml.Unmarshal(data, &definition); err != nil {
		return definition, err
	}
	if err := definition.Validate(100); err != nil {
		return definition, err
	}
	return definition, nil
}

func (d Definition) Validate(maxNodes int) error {
	if d.SchemaVersion != 1 || d.Name == "" {
		return fmt.Errorf("workflow schema_version and name are required")
	}
	if len(d.Nodes) == 0 || len(d.Nodes) > maxNodes {
		return fmt.Errorf("workflow must contain 1-%d nodes", maxNodes)
	}
	seen := make(map[string]bool)
	expandedNodes := 0
	for _, node := range d.Nodes {
		if strings.TrimSpace(node.ID) == "" || strings.ContainsAny(node.ID, "[]") || seen[node.ID] {
			return fmt.Errorf("duplicate or empty node ID %q", node.ID)
		}
		expansion := max(1, len(node.Items))
		if expansion > maxNodes-expandedNodes {
			return fmt.Errorf("workflow fan-out exceeds %d tasks", maxNodes)
		}
		expandedNodes += expansion
		if node.TimeoutSeconds < 0 || int64(node.TimeoutSeconds) > (1<<63-1)/1_000_000_000 {
			return fmt.Errorf("node %s has invalid timeout_seconds", node.ID)
		}
		seen[node.ID] = true
		if node.Type != "agent" && node.Type != "tool" {
			return fmt.Errorf("node %s has unsupported type %q", node.ID, node.Type)
		}
		if node.Type == "agent" && strings.TrimSpace(node.Prompt) == "" {
			return fmt.Errorf("agent node %s needs prompt", node.ID)
		}
		if node.Type == "tool" && node.Tool == "" {
			return fmt.Errorf("tool node %s needs tool", node.ID)
		}
		switch node.Condition {
		case "", "always", "on_success", "on_failure":
		default:
			return fmt.Errorf("node %s has invalid condition %q", node.ID, node.Condition)
		}
		if node.Retry < 0 || node.Retry > 5 {
			return fmt.Errorf("node %s retry must be 0-5", node.ID)
		}
		for _, dependency := range node.DependsOn {
			if dependency == node.ID {
				return fmt.Errorf("node %s depends on itself", node.ID)
			}
		}
	}
	for _, node := range d.Nodes {
		for _, dependency := range node.DependsOn {
			if !seen[dependency] {
				return fmt.Errorf("node %s depends on unknown node %s", node.ID, dependency)
			}
		}
	}
	visit := make(map[string]int)
	var dfs func(string) error
	dfs = func(id string) error {
		if visit[id] == 1 {
			return fmt.Errorf("workflow cycle detected at %s", id)
		}
		if visit[id] == 2 {
			return nil
		}
		visit[id] = 1
		for _, node := range d.Nodes {
			if node.ID == id {
				for _, dependency := range node.DependsOn {
					if err := dfs(dependency); err != nil {
						return err
					}
				}
			}
		}
		visit[id] = 2
		return nil
	}
	for id := range seen {
		if err := dfs(id); err != nil {
			return err
		}
	}
	return nil
}

func (d Definition) Digest() string {
	data, _ := json.Marshal(d)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (d Definition) SortedNodeIDs() []string {
	ids := make([]string, 0, len(d.Nodes))
	for _, node := range d.Nodes {
		ids = append(ids, node.ID)
	}
	sort.Strings(ids)
	return ids
}
