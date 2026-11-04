package builtin

import (
	"agent-core/internal/tool"
	"fmt"
)

// RegisterAll registers built-in tools and returns registration failures.
func RegisterAll(registry *tool.Registry, workDir string, processRegistry *tool.ProcessRegistry, terminalEnabled, allowOutsideWorkspace bool) error {
	providers := []tool.Provider{
		NewTimeTool(), NewFileReadTool(workDir, allowOutsideWorkspace), NewFileWriteTool(workDir, allowOutsideWorkspace), NewFileListTool(workDir, allowOutsideWorkspace),
		NewFileEditTool(workDir, allowOutsideWorkspace), NewFileDeleteTool(workDir, allowOutsideWorkspace), NewFileMoveTool(workDir, allowOutsideWorkspace),
		NewGlobTool(workDir, allowOutsideWorkspace), NewGrepTool(workDir, allowOutsideWorkspace), NewGitTool(workDir, allowOutsideWorkspace), NewWebFetchTool(),
		NewClarifyTool(), tool.NewToolSearchTool(registry), tool.NewToolDescribeTool(registry),
	}
	if terminalEnabled {
		providers = append(providers, NewTerminalTool(workDir, processRegistry, allowOutsideWorkspace))
	}
	for _, provider := range providers {
		if err := registry.Register(provider, tool.SourceBuiltin, "builtin"); err != nil {
			return fmt.Errorf("register %s: %w", provider.Name(), err)
		}
	}
	return nil
}
