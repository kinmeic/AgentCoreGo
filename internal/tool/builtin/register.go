package builtin

import (
	"agent-core/internal/tool"
	"log"
)

// RegisterAll registers all builtin tools into the given registry.
// If terminalEnabled is false, the run_terminal tool is skipped.
func RegisterAll(registry *tool.Registry, workDir string, processRegistry *tool.ProcessRegistry, terminalEnabled bool) {
	// Core tools
	registry.Register(NewTimeTool(), tool.SourceBuiltin, "builtin")
	if terminalEnabled {
		registry.Register(NewTerminalTool(workDir, processRegistry), tool.SourceBuiltin, "builtin")
	} else {
		log.Printf("[tools] Terminal tool DISABLED (tools.terminal_enabled=false)")
	}
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
