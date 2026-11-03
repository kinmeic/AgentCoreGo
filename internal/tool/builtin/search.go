package builtin

import (
	"agent-core/internal/sandbox"
	"agent-core/internal/tool"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// GlobTool finds workspace paths using *, ?, and ** patterns.
type GlobTool struct{ workDir string }

func NewGlobTool(workDir string) *GlobTool { return &GlobTool{workDir: workDir} }
func (t *GlobTool) Name() string           { return "glob_files" }
func (t *GlobTool) Capabilities() tool.Capabilities {
	return tool.Capabilities{ReadOnly: true, ConcurrentSafe: true, SideEffectScope: "workspace", RiskLevel: "low", Timeout: 30 * time.Second}
}
func (t *GlobTool) Description() string { return "Find files by glob pattern; ** crosses directories." }
func (t *GlobTool) Schema() tool.ToolSchema {
	return tool.ToolSchema{Type: "object", Required: []string{"pattern"}, Properties: map[string]any{
		"pattern": map[string]any{"type": "string"}, "path": map[string]any{"type": "string"},
		"max_results": map[string]any{"type": "integer", "minimum": 1, "maximum": 500},
	}}
}
func (t *GlobTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	root := t.workDir
	if value, ok := args["path"].(string); ok && value != "" {
		var err error
		root, err = sandbox.ValidatePath(value, t.workDir, false)
		if err != nil {
			return "", err
		}
	}
	matcher, err := compileGlob(args["pattern"].(string))
	if err != nil {
		return "", err
	}
	maximum := intArg(args, "max_results", 200)
	var matches []string
	visited := 0
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		visited++
		if visited > 10_000 {
			return fmt.Errorf("glob visited too many entries")
		}
		if path == root {
			return nil
		}
		relative, _ := filepath.Rel(root, path)
		relative = filepath.ToSlash(relative)
		if entry.IsDir() && relative == ".git" {
			return fs.SkipDir
		}
		if !entry.IsDir() && matcher.MatchString(relative) {
			matches = append(matches, relative)
			if len(matches) >= maximum {
				return fs.SkipAll
			}
		}
		return nil
	})
	if err != nil && err != fs.SkipAll {
		return "", err
	}
	return strings.Join(matches, "\n"), nil
}

func compileGlob(pattern string) (*regexp.Regexp, error) {
	pattern = filepath.ToSlash(pattern)
	var expression strings.Builder
	expression.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					expression.WriteString("(?:.*/)?")
				} else {
					expression.WriteString(".*")
				}
			} else {
				expression.WriteString("[^/]*")
			}
		case '?':
			expression.WriteString("[^/]")
		default:
			expression.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	expression.WriteString("$")
	return regexp.Compile(expression.String())
}

// GrepTool searches UTF-8 text with Go's bounded RE2 regular expressions.
type GrepTool struct{ workDir string }

func NewGrepTool(workDir string) *GrepTool { return &GrepTool{workDir: workDir} }
func (t *GrepTool) Name() string           { return "grep_files" }
func (t *GrepTool) Capabilities() tool.Capabilities {
	return tool.Capabilities{ReadOnly: true, ConcurrentSafe: true, SideEffectScope: "workspace", RiskLevel: "low", Timeout: 45 * time.Second, MaxOutputBytes: 100_000}
}
func (t *GrepTool) Description() string { return "Search text files with an RE2 regular expression." }
func (t *GrepTool) Schema() tool.ToolSchema {
	return tool.ToolSchema{Type: "object", Required: []string{"query"}, Properties: map[string]any{
		"query": map[string]any{"type": "string"}, "path": map[string]any{"type": "string"},
		"glob": map[string]any{"type": "string"}, "case_sensitive": map[string]any{"type": "boolean"},
		"max_results": map[string]any{"type": "integer", "minimum": 1, "maximum": 500},
	}}
}
func (t *GrepTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	query := args["query"].(string)
	if sensitive, ok := args["case_sensitive"].(bool); ok && !sensitive {
		query = "(?i)" + query
	}
	matcher, err := regexp.Compile(query)
	if err != nil {
		return "", err
	}
	root := t.workDir
	if value, ok := args["path"].(string); ok && value != "" {
		root, err = sandbox.ValidatePath(value, t.workDir, false)
		if err != nil {
			return "", err
		}
	}
	glob, err := compileGlob("**/*")
	if pattern, ok := args["glob"].(string); ok && pattern != "" {
		glob, err = compileGlob(pattern)
		if err != nil {
			return "", err
		}
	}
	maximum := intArg(args, "max_results", 100)
	var matches []string
	visited := 0
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		visited++
		if visited > 10_000 {
			return fmt.Errorf("grep visited too many entries")
		}
		if path == root {
			return nil
		}
		relative, _ := filepath.Rel(root, path)
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			if relative == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !glob.MatchString(relative) {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxManagedFileBytes {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil || bytes.IndexByte(data, 0) >= 0 {
			return nil
		}
		for index, line := range strings.Split(string(data), "\n") {
			if matcher.MatchString(line) {
				if len(line) > 500 {
					line = truncateUTF8(line, 500) + "..."
				}
				matches = append(matches, fmt.Sprintf("%s:%d:%s", relative, index+1, line))
				if len(matches) >= maximum {
					return fs.SkipAll
				}
			}
		}
		return nil
	})
	if err != nil && err != fs.SkipAll {
		return "", err
	}
	return strings.Join(matches, "\n"), nil
}
