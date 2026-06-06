package tool

// DefaultToolsets returns the predefined toolset definitions.
// These can be overridden via configuration.
func DefaultToolsets() map[string]*Toolset {
	return map[string]*Toolset{
		"core": {
			Name:        "core",
			Description: "Core file and terminal tools (always available)",
			Tools: []string{
				"read_file",
				"write_file",
				"list_dir",
				"run_terminal",
				"get_current_time",
			},
			Enabled: true,
		},
		"web": {
			Name:        "web",
			Description: "Web access tools",
			Tools: []string{
				"web_fetch",
			},
			Enabled: true,
		},
		"memory": {
			Name:        "memory",
			Description: "Long-term memory tools",
			Tools: []string{
				"save_memory",
				"search_memory",
			},
			Enabled: true,
		},
		"mcp": {
			Name:        "mcp",
			Description: "Tools from connected MCP servers",
			Tools:       []string{}, // Dynamically populated
			Enabled:     true,
		},
		"host_callback": {
			Name:        "host_callback",
			Description: "Tools registered via host callback",
			Tools:       []string{}, // Dynamically populated
			Enabled:     true,
		},
	}
}

// CoreToolNames returns the tool names that are always exposed directly
// (never hidden behind tool search).
func CoreToolNames() []string {
	return []string{
		"read_file",
		"write_file",
		"list_dir",
		"run_terminal",
		"get_current_time",
		// Bridge tools are also always exposed
		"tool_search",
		"tool_describe",
	}
}

// IsCoreTool returns true if the tool should always be visible to the LLM.
func IsCoreTool(name string) bool {
	for _, n := range CoreToolNames() {
		if n == name {
			return true
		}
	}
	return false
}
