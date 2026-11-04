package commands

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Loader discovers and expands custom commands without executing shell syntax.
type Loader struct{ options Options }

func NewLoader(options Options) *Loader {
	options.Roots = append([]string(nil), options.Roots...)
	overrides := make(map[string]bool, len(options.Overrides))
	for path, enabled := range options.Overrides {
		overrides[filepath.Clean(path)] = enabled
	}
	options.Overrides = overrides
	return &Loader{options: options}
}

type commandRoot struct {
	path   string
	scope  Scope
	source Source
}

func (l *Loader) roots() []commandRoot {
	var roots []commandRoot
	for _, root := range l.options.Roots {
		if !filepath.IsAbs(root) {
			root = filepath.Join(l.options.ProjectDir, root)
		}
		roots = append(roots, commandRoot{path: filepath.Clean(root), scope: ScopeRoot, source: SourceRoot})
	}
	if l.options.ProjectDir != "" {
		roots = appendStandardRoots(roots, l.options.ProjectDir, ScopeProject)
	}
	if l.options.UserDir != "" {
		roots = appendStandardRoots(roots, l.options.UserDir, ScopeUser)
	}
	return roots
}

func appendStandardRoots(roots []commandRoot, base string, scope Scope) []commandRoot {
	for _, item := range []struct {
		dir    string
		source Source
	}{{".zcode", SourceZCode}, {".agents", SourceAgents}, {".claude", SourceClaude}} {
		roots = append(roots, commandRoot{path: filepath.Join(base, item.dir, "commands"), scope: scope, source: item.source})
	}
	return roots
}

func (l *Loader) Discover() Catalog {
	var commands []Command
	var diagnostics []Diagnostic
	for _, root := range l.roots() {
		_ = filepath.WalkDir(root.path, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				diagnostics = append(diagnostics, Diagnostic{Code: "command_scan_failed", Severity: "warning", Path: path, Message: err.Error()})
				return nil
			}
			if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
				return nil
			}
			if enabled, exists := l.options.Overrides[filepath.Clean(path)]; exists && !enabled {
				return nil
			}
			command, ok := readCommand(path, root, &diagnostics)
			if ok {
				commands = append(commands, command)
			}
			return nil
		})
	}
	seen := make(map[string]string)
	for i := range commands {
		name := commands[i].Name
		if _, builtin := Builtin(name); builtin {
			commands[i].ShadowedBy = "builtin"
			diagnostics = append(diagnostics, Diagnostic{Code: "command_builtin_shadow", Severity: "warning", Path: commands[i].Path, CommandName: name, Message: "custom command cannot shadow a builtin"})
			continue
		}
		if previous, exists := seen[name]; exists {
			commands[i].ShadowedBy = previous
		} else {
			seen[name] = commands[i].Path
		}
	}
	sort.SliceStable(commands, func(i, j int) bool { return commands[i].Name < commands[j].Name })
	return Catalog{Commands: commands, Diagnostics: diagnostics, TotalDiscovered: len(commands)}
}

func readCommand(path string, root commandRoot, diagnostics *[]Diagnostic) (Command, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		*diagnostics = append(*diagnostics, Diagnostic{Code: "command_read_failed", Severity: "warning", Path: path, Message: err.Error()})
		return Command{}, false
	}
	if len(data) > 100*1024 {
		*diagnostics = append(*diagnostics, Diagnostic{Code: "command_too_large", Severity: "error", Path: path, Message: "command exceeds 100 KiB"})
		return Command{}, false
	}
	relative, _ := filepath.Rel(root.path, path)
	name := strings.TrimSuffix(filepath.ToSlash(relative), filepath.Ext(relative))
	name = strings.ReplaceAll(strings.ToLower(name), "/", ":")
	if !validCommandName(name) {
		*diagnostics = append(*diagnostics, Diagnostic{Code: "command_invalid_name", Severity: "error", Path: path, CommandName: name, Message: "invalid command name"})
		return Command{}, false
	}
	frontmatter, body, err := splitCommandDocument(data)
	if err != nil {
		*diagnostics = append(*diagnostics, Diagnostic{Code: "command_invalid_frontmatter", Severity: "error", Path: path, CommandName: name, Message: err.Error()})
		return Command{}, false
	}
	var metadata map[string]any
	keys := make([]string, 0)
	if len(bytes.TrimSpace(frontmatter)) > 0 {
		if err := yaml.Unmarshal(frontmatter, &metadata); err != nil {
			*diagnostics = append(*diagnostics, Diagnostic{Code: "command_invalid_frontmatter", Severity: "error", Path: path, CommandName: name, Message: err.Error()})
			return Command{}, false
		}
		for key := range metadata {
			keys = append(keys, key)
		}
		sort.Strings(keys)
	}
	description := stringMetadata(metadata, "description")
	if description == "" {
		for _, line := range strings.Split(string(body), "\n") {
			if strings.TrimSpace(line) != "" {
				description = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#"))
				break
			}
		}
	}
	if utf8.RuneCountInString(description) > MaxDescriptionRunes {
		description = string([]rune(description)[:MaxDescriptionRunes])
	}
	digest := sha256.Sum256(data)
	command := Command{Name: name, Description: description, ArgumentHint: stringMetadata(metadata, "argument-hint"),
		Model: stringMetadata(metadata, "model"), Path: path, RootPath: root.path, Scope: root.scope, Source: root.source,
		Digest: hex.EncodeToString(digest[:]), FrontmatterKeys: keys}
	command.AllowedTools = stringListMetadata(metadata, "allowed-tools")
	command.Skills = stringListMetadata(metadata, "skills")
	if value, ok := metadata["disable-noninteractive"].(bool); ok {
		command.DisableNonInteractive = value
	}
	return command, true
}

func validCommandName(name string) bool {
	if name == "" || len(name) > 65 {
		return false
	}
	for i, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (r == '-' && i > 0) || (r == ':' && i > 0) {
			continue
		}
		return false
	}
	return true
}

func splitCommandDocument(data []byte) ([]byte, []byte, error) {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if !bytes.HasPrefix(data, []byte("---\n")) && !bytes.HasPrefix(data, []byte("---\r\n")) {
		return nil, data, nil
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" || lines[i] == "..." {
			return []byte(strings.Join(lines[1:i], "\n")), []byte(strings.Join(lines[i+1:], "\n")), nil
		}
	}
	return nil, nil, fmt.Errorf("unterminated YAML frontmatter")
}

func stringMetadata(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func stringListMetadata(metadata map[string]any, key string) []string {
	value := metadata[key]
	switch typed := value.(type) {
	case string:
		return strings.FieldsFunc(typed, func(r rune) bool { return r == ',' || r == ' ' })
	case []any:
		var result []string
		for _, item := range typed {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result
	case []string:
		return typed
	default:
		return nil
	}
}

// Load returns a custom command by normalized name.
func (l *Loader) Load(name string) (Command, string, error) {
	name = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(name)), "/")
	if _, builtin := Builtin(name); builtin {
		return Command{}, "", &NotExpandableError{Name: name}
	}
	catalog := l.Discover()
	for _, command := range catalog.Commands {
		if command.Name == name && command.ShadowedBy == "" {
			data, err := os.ReadFile(command.Path)
			if err != nil {
				return Command{}, "", err
			}
			_, body, err := splitCommandDocument(data)
			return command, string(body), err
		}
	}
	return Command{}, "", &NotFoundError{Name: name}
}
