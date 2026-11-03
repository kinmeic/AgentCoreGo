package builtin

import (
	"agent-core/internal/tool"
	"fmt"
)

// RegisterAll registers built-in tools and returns registration failures.
func RegisterAll(registry *tool.Registry, workDir string, processRegistry *tool.ProcessRegistry, terminalEnabled bool) error {
	providers := []tool.Provider{
		NewTimeTool(), NewFileReadTool(workDir), NewFileWriteTool(workDir), NewFileListTool(workDir),
		NewFileEditTool(workDir), NewFileDeleteTool(workDir), NewFileMoveTool(workDir),
		NewGlobTool(workDir), NewGrepTool(workDir), NewGitTool(workDir), NewWebFetchTool(),
		NewClarifyTool(), tool.NewToolSearchTool(registry), tool.NewToolDescribeTool(registry),
	}
	if terminalEnabled {
		providers = append(providers, NewTerminalTool(workDir, processRegistry))
	}
	for _, provider := range providers {
		if err := registry.Register(provider, tool.SourceBuiltin, "builtin"); err != nil {
			return fmt.Errorf("register %s: %w", provider.Name(), err)
		}
	}
	return nil
}
