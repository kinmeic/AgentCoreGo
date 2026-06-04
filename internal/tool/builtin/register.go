package builtin

import (
	"agent-core/internal/tool"
)

// RegisterAll registers all builtin tools into the given registry.
func RegisterAll(registry *tool.Registry, workDir string) {
	registry.Register(NewTimeTool(), tool.SourceBuiltin, "builtin")
	registry.Register(NewTerminalTool(workDir), tool.SourceBuiltin, "builtin")
	registry.Register(NewFileReadTool(workDir), tool.SourceBuiltin, "builtin")
	registry.Register(NewFileWriteTool(workDir), tool.SourceBuiltin, "builtin")
	registry.Register(NewFileListTool(workDir), tool.SourceBuiltin, "builtin")
	registry.Register(NewWebFetchTool(), tool.SourceBuiltin, "builtin")
}
