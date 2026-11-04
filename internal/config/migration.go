package config

import (
	"fmt"
	"gopkg.in/yaml.v3"
)

var removedConfigFields = map[string]string{
	"embedding":                      "embedding/vector retrieval was removed; use memory.use",
	"store":                          "use storage.dir and storage.sessionDbPath; vector_backend was removed",
	"tools":                          "use permission.mode, toolConcurrency.maxConcurrency and server.terminalEnabled; workspace.path replaces work_dir",
	"compression":                    "use features.compact; compaction follows the model context/output window",
	"security":                       "use permission.allowedTools and permission.disallowedTools",
	"commands":                       "use command.<absolute file path>.enable",
	"workflow":                       "workflow implementation limits are no longer public runtime settings",
	"telemetry":                      "capture implementation settings are no longer public runtime settings",
	"llm.max_tool_iterations":        "main agent has no tool iteration hard stop",
	"llm.max_retries":                "use ZCODE_MODEL_RETRY_MAX_RETRIES (default 10)",
	"llm.temperature":                "sampling parameters follow model capabilities",
	"llm.max_run_tokens":             "cumulative token hard stop was removed",
	"llm.run_timeout_seconds":        "use modelStream.idleTimeoutMs; total run duration has no hard stop",
	"workspace.soul_file":            "workspace instructions are read from AGENTS.md",
	"server.log_format":              "use logging.format",
	"skills.metadata_budget":         "use skills.metadataBudget (default 20000)",
	"features.skills":                "use features.skill and skills.enabled",
	"features.subagents":             "use features.subagent",
	"features.commands":              "use per-file command overrides",
	"features.workflow":              "not a ZCode feature flag",
	"features.model_io":              "not a ZCode feature flag",
	"memory.capacity_limit":          "removed; use memory.use",
	"memory.consolidation_threshold": "removed; use memory.use",
	"memory.auto_nudge":              "removed; use memory.use",
	"memory.auto_consolidate":        "removed; use memory.use",
}

func rejectRemovedConfigFields(data []byte) error {
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return err
	}
	var walk func(*yaml.Node, string) error
	walk = func(n *yaml.Node, prefix string) error {
		if n.Kind == yaml.DocumentNode {
			for _, child := range n.Content {
				if err := walk(child, prefix); err != nil {
					return err
				}
			}
		}
		if n.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(n.Content); i += 2 {
				name := n.Content[i].Value
				if prefix != "" {
					name = prefix + "." + name
				}
				if hint, ok := removedConfigFields[name]; ok {
					return fmt.Errorf("removed configuration %s: %s (see docs/zcode-alignment.md)", name, hint)
				}
				if err := walk(n.Content[i+1], name); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(&node, "")
}
