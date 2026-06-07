package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ValidatePath validates that the requested path is within the workspace directory.
// It resolves relative paths, cleans the result, and checks for symlink escapes.
// For write operations where the target file may not exist, pass forWrite=true.
func ValidatePath(requestedPath, workspaceDir string, forWrite bool) (string, error) {
	// Resolve the path
	var resolved string
	if filepath.IsAbs(requestedPath) {
		resolved = filepath.Clean(requestedPath)
	} else {
		resolved = filepath.Clean(filepath.Join(workspaceDir, requestedPath))
	}

	// Ensure workspace itself is resolved
	evalWorkspace, err := filepath.EvalSymlinks(workspaceDir)
	if err != nil {
		evalWorkspace = workspaceDir
	}

	// For write operations, the target file may not exist yet.
	// Validate the parent directory and check if the target is a symlink.
	if forWrite {
		parent := filepath.Dir(resolved)
		evaluatedParent, err := filepath.EvalSymlinks(parent)
		if err != nil {
			// Parent doesn't exist - will be created, validate what we can
			evaluatedParent = parent
		}

		// Check if the target file already exists and is a symlink
		// This prevents writing through symlinks that point outside the workspace
		targetPath := filepath.Join(evaluatedParent, filepath.Base(resolved))
		if info, err := os.Lstat(targetPath); err == nil {
			// File exists — check if it's a symlink
			if info.Mode()&os.ModeSymlink != 0 {
				// It's a symlink — resolve it and validate the target
				symlinkTarget, err := filepath.EvalSymlinks(targetPath)
				if err != nil {
					return "", fmt.Errorf("failed to resolve symlink %q: %w", requestedPath, err)
				}
				// Check if the symlink target is within workspace
				if !isInsideWorkspace(symlinkTarget, evalWorkspace) {
					return "", fmt.Errorf("path %q is a symlink pointing outside the workspace directory", requestedPath)
				}
				// Symlink points inside workspace — use the resolved target
				resolved = symlinkTarget
			} else {
				resolved = targetPath
			}
		} else {
			resolved = targetPath
		}
	} else {
		// For read/list, the file should exist - resolve symlinks
		evaluated, err := filepath.EvalSymlinks(resolved)
		if err == nil {
			resolved = evaluated
		}
		// If EvalSymlinks fails (file doesn't exist), use the cleaned path
	}

	// Check that resolved path is within workspace
	if !isInsideWorkspace(resolved, evalWorkspace) {
		return "", fmt.Errorf("path %q is outside the workspace directory", requestedPath)
	}

	return resolved, nil
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
	path := filepath.Join(workspaceDir, soulFileName)
	if _, err := os.Stat(path); err == nil {
		return nil // Already exists
	}

	return os.WriteFile(path, []byte(defaultSoulContent), 0o644)
}

// ReadSoulFile reads the SOUL.md file content.
// Returns empty string if file doesn't exist (graceful degradation).
// Content is truncated to maxSoulSize characters.
func ReadSoulFile(workspaceDir, soulFileName string) (string, error) {
	path := filepath.Join(workspaceDir, soulFileName)
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
