package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ValidatePath validates paths and resolves every existing ancestor, including
// ancestors of directories that still need to be created.
func ValidatePath(requestedPath, workspaceDir string, forWrite bool) (string, error) {
	resolved := requestedPath
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(workspaceDir, resolved)
	}
	resolved = filepath.Clean(resolved)
	evalWorkspace, err := filepath.EvalSymlinks(workspaceDir)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("resolve workspace: %w", err)
		}
		evalWorkspace = filepath.Clean(workspaceDir)
	}

	if forWrite {
		parent, err := resolveExistingAncestor(filepath.Dir(resolved))
		if err != nil {
			return "", fmt.Errorf("resolve parent: %w", err)
		}
		resolved = filepath.Join(parent, filepath.Base(resolved))
		if info, err := os.Lstat(resolved); err == nil && info.Mode()&os.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(resolved)
			if err != nil {
				return "", fmt.Errorf("resolve target symlink: %w", err)
			}
			resolved = target
		}
	} else {
		resolved, err = resolveExistingAncestor(resolved)
		if err != nil {
			return "", fmt.Errorf("resolve path: %w", err)
		}
	}
	if !isInsideWorkspace(resolved, evalWorkspace) {
		return "", fmt.Errorf("path %q is outside the workspace directory", requestedPath)
	}
	return resolved, nil
}

func resolveExistingAncestor(path string) (string, error) {
	current := filepath.Clean(path)
	var missing []string
	for {
		evaluated, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				evaluated = filepath.Join(evaluated, missing[i])
			}
			return evaluated, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

// ValidateWorkingDir validates that the requested working directory is within the workspace.
func ValidateWorkingDir(requestedDir, workspaceDir string) (string, error) {
	resolved, err := ValidatePath(requestedDir, workspaceDir, false)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("working directory does not exist: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path is not a directory: %s", resolved)
	}

	return resolved, nil
}

// isInsideWorkspace checks if the resolved path is within the workspace directory.
func isInsideWorkspace(resolvedPath, workspaceDir string) bool {
	// Exact match - the path IS the workspace
	if resolvedPath == workspaceDir {
		return true
	}
	// Must be under workspace (with separator to prevent /workspace-evil matching /workspace)
	return strings.HasPrefix(resolvedPath, workspaceDir+string(filepath.Separator))
}

// EnsureWorkspace creates the workspace directory if it doesn't exist.
func EnsureWorkspace(dir string) error {
	return os.MkdirAll(dir, 0o755)
}

// EnsureSoulFile creates a default SOUL.md if it doesn't exist.
func EnsureSoulFile(workspaceDir, soulFileName string) error {
	path, err := ValidatePath(soulFileName, workspaceDir, true)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return nil // Already exists
	}

	return os.WriteFile(path, []byte(defaultSoulContent), 0o644)
}

// ReadSoulFile reads the SOUL.md file content.
// Returns empty string if file doesn't exist (graceful degradation).
// Content is truncated to maxSoulSize characters.
func ReadSoulFile(workspaceDir, soulFileName string) (string, error) {
	path, err := ValidatePath(soulFileName, workspaceDir, false)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}

	content := string(data)
	if len(content) > maxSoulSize {
		content = content[:maxSoulSize] + "\n... (truncated)"
	}

	return content, nil
}

const maxSoulSize = 10000

const defaultSoulContent = `# Agent Guidelines

## Identity
You are a helpful AI assistant operating within a sandboxed workspace.

## Communication
- Be concise and direct
- Explain your reasoning when making decisions
- Ask for clarification when requirements are ambiguous

## Tool Usage
- Always prefer workspace-relative paths
- Explain what you're doing before executing terminal commands
- When writing files, organize them logically within the workspace

## Constraints
- All file operations are confined to the workspace directory
- Do not attempt to access paths outside the workspace
`
