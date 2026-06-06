package builtin

import (
	"agent-core/internal/tool"
)

// RegisterAll registers all builtin tools into the given registry.
func RegisterAll(registry *tool.Registry, workDir string, processRegistry *tool.ProcessRegistry) {
	// Core tools
	registry.Register(NewTimeTool(), tool.SourceBuiltin, "builtin")
	registry.Register(NewTerminalTool(workDir, processRegistry), tool.SourceBuiltin, "builtin")
	registry.Register(NewFileReadTool(workDir), tool.SourceBuiltin, "builtin")
	registry.Register(NewFileWriteTool(workDir), tool.SourceBuiltin, "builtin")
	registry.Register(NewFileListTool(workDir), tool.SourceBuiltin, "builtin")

	// Web tools
	registry.Register(NewWebFetchTool(), tool.SourceBuiltin, "builtin")

	// Clarify tool (agent can ask user for help)
	registry.Register(NewClarifyTool(), tool.SourceBuiltin, "builtin")

	// Bridge tools for progressive tool discovery
	registry.Register(tool.NewToolSearchTool(registry), tool.SourceBuiltin, "builtin")
	registry.Register(tool.NewToolDescribeTool(registry), tool.SourceBuiltin, "builtin")
}
