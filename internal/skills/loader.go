package skills

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Loader performs uncached discovery and on-demand body loads. It is safe for
// concurrent use because Options is copied by NewLoader and never mutated.
type Loader struct {
	options Options
}

// NewLoader constructs a skill loader.
func NewLoader(options Options) *Loader {
	options.Roots = append([]string(nil), options.Roots...)
	return &Loader{options: options}
}

// Discover scans all configured roots. Filesystem and frontmatter failures are
// returned as diagnostics so one bad skill does not hide the remaining skills.
func (l *Loader) Discover() Catalog {
	roots, diagnostics := l.roots()
	var discovered []Skill
	seenPaths := make(map[string]bool)
	for _, root := range roots {
		paths := skillFiles(root, &diagnostics)
		for _, path := range paths {
			path = filepath.Clean(path)
			if seenPaths[path] {
				continue
			}
			seenPaths[path] = true
			skill, ok := readSkillMetadata(path, root, &diagnostics)
			if ok {
				discovered = append(discovered, skill)
			}
		}
	}

	winners := make(map[string]string)
	for i := range discovered {
		if winner, exists := winners[discovered[i].Name]; exists {
			discovered[i].ShadowedBy = winner
		} else {
			winners[discovered[i].Name] = discovered[i].Path
		}
	}
	sort.SliceStable(discovered, func(i, j int) bool {
		return discovered[i].Name < discovered[j].Name
	})
	return Catalog{Skills: discovered, Diagnostics: diagnostics, TotalDiscovered: len(discovered)}
}

// Load selects the highest-priority skill with name and reads its body. Names
// are case-sensitive; surrounding whitespace and one leading slash are ignored.
func (l *Loader) Load(name string) (LoadedSkill, error) {
	name = strings.TrimPrefix(strings.TrimSpace(name), "/")
	catalog := l.Discover()
	var selected *Skill
	for i := range catalog.Skills {
		if catalog.Skills[i].Name == name {
			selected = &catalog.Skills[i]
			break
		}
	}
	if selected == nil {
		return LoadedSkill{}, &NotFoundError{Name: name}
	}

	data, err := os.ReadFile(selected.Path)
	if err != nil {
		return LoadedSkill{}, fmt.Errorf("read skill %q: %w", name, err)
	}
	frontmatter, err := splitFrontmatter(data)
	if err != nil {
		return LoadedSkill{}, fmt.Errorf("load skill %q: %w", name, err)
	}
	body := strings.TrimSpace(string(frontmatter.body))
	size := len(body)
	truncated := size > MaxBodyBytes
	if truncated {
		end := MaxBodyBytes
		for end > 0 && !utf8.ValidString(body[:end]) {
			end--
		}
		body = body[:end]
	}
	return LoadedSkill{
		Skill:         *selected,
		Content:       body,
		BaseDirectory: selected.Directory,
		BytesRead:     int64(len(body)),
		SizeBytes:     int64(size),
		Truncated:     truncated,
	}, nil
}

type skillRoot struct {
	path   string
	scope  Scope
	source Source
}

func (l *Loader) roots() ([]skillRoot, []Diagnostic) {
	var roots []skillRoot
	var diagnostics []Diagnostic
	base := l.options.ProjectDir
	if base == "" {
		base = "."
	}
	for _, configured := range l.options.Roots {
		path, err := resolveConfiguredPath(configured, base, l.options.UserDir)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{
				Code: "skill_invalid_root", Severity: "error", Path: configured, Message: err.Error(),
			})
			continue
		}
		roots = append(roots, skillRoot{path: path, scope: ScopeRoot, source: SourceRoot})
	}
	if l.options.ProjectDir != "" {
		project, err := filepath.Abs(l.options.ProjectDir)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{
				Code: "skill_invalid_root", Severity: "error", Path: l.options.ProjectDir, Message: err.Error(),
			})
		} else {
			for _, directory := range projectDirectories(project) {
				roots = appendStandardRoots(roots, directory, ScopeProject)
			}
		}
	}
	if l.options.UserDir != "" {
		if user, err := filepath.Abs(l.options.UserDir); err == nil {
			roots = appendStandardRoots(roots, user, ScopeUser)
		} else {
			diagnostics = append(diagnostics, Diagnostic{
				Code: "skill_invalid_root", Severity: "error", Path: l.options.UserDir, Message: err.Error(),
			})
		}
	}

	seen := make(map[string]bool)
	result := roots[:0]
	for _, root := range roots {
		if !seen[root.path] {
			seen[root.path] = true
			result = append(result, root)
		}
	}
	return result, diagnostics
}

func appendStandardRoots(roots []skillRoot, base string, scope Scope) []skillRoot {
	conventions := []struct {
		directory string
		source    Source
	}{
		{".zcode", SourceZCode},
		{".agents", SourceAgents},
		{".claude", SourceClaude},
	}
	for _, convention := range conventions {
		roots = append(roots, skillRoot{
			path: filepath.Join(base, convention.directory, "skills"), scope: scope, source: convention.source,
		})
	}
	return roots
}

func resolveConfiguredPath(configured, project, user string) (string, error) {
	path := configured
	if path == "~" || strings.HasPrefix(path, "~/") {
		if user == "" {
			return "", fmt.Errorf("cannot resolve %q without UserDir", configured)
		}
		if path == "~" {
			path = user
		} else {
			path = filepath.Join(user, strings.TrimPrefix(path, "~/"))
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(project, path)
	}
	return filepath.Abs(path)
}

// projectDirectories returns the working directory and its parents through the
// repository root. Without a .git marker only the supplied directory is used.
func projectDirectories(project string) []string {
	var possible []string
	for current := project; ; current = filepath.Dir(current) {
		possible = append(possible, current)
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return possible
		}
		parent := filepath.Dir(current)
		if parent == current {
			return possible[:1]
		}
	}
}

func skillFiles(root skillRoot, diagnostics *[]Diagnostic) []string {
	info, err := os.Stat(root.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err == nil && !info.IsDir() {
		err = fmt.Errorf("not a directory")
	}
	if err != nil {
		*diagnostics = append(*diagnostics, Diagnostic{
			Code: "skill_scan_failed", Severity: "warning", Path: root.path, Message: err.Error(),
		})
		return nil
	}

	var paths []string
	err = filepath.WalkDir(root.path, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			*diagnostics = append(*diagnostics, Diagnostic{
				Code: "skill_scan_failed", Severity: "warning", Path: path, Message: walkErr.Error(),
			})
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			name := entry.Name()
			if path != root.path && ((strings.HasPrefix(name, ".") && name != ".system") || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() == "SKILL.md" {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		*diagnostics = append(*diagnostics, Diagnostic{
			Code: "skill_scan_failed", Severity: "warning", Path: root.path, Message: err.Error(),
		})
	}
	return paths
}

var safeFrontmatterKeys = map[string]bool{
	"name": true, "description": true, "when_to_use": true, "license": true, "metadata": true,
}

func readSkillMetadata(path string, root skillRoot, diagnostics *[]Diagnostic) (Skill, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			*diagnostics = append(*diagnostics, Diagnostic{
				Code: "skill_read_failed", Severity: "warning", Path: path, Message: err.Error(),
			})
		}
		return Skill{}, false
	}
	frontmatter, err := splitFrontmatter(data)
	if err != nil {
		*diagnostics = append(*diagnostics, Diagnostic{
			Code: "skill_invalid_frontmatter", Severity: "error", Path: path, Message: err.Error(),
		})
		return Skill{}, false
	}
	values, keys, err := parseYAML(frontmatter.values)
	if err != nil {
		*diagnostics = append(*diagnostics, Diagnostic{
			Code: "skill_invalid_frontmatter", Severity: "error", Path: path, Message: err.Error(),
		})
		return Skill{}, false
	}

	name := stringValue(values, "name")
	if !frontmatter.present && name == "" {
		name = filepath.Base(filepath.Dir(path))
	}
	if name == "" {
		*diagnostics = append(*diagnostics, Diagnostic{
			Code: "skill_missing_name", Severity: "error", Path: path,
			Message: "skill frontmatter must include a non-empty name",
		})
		return Skill{}, false
	}
	description := stringValue(values, "description")
	if description == "" && frontmatter.present {
		*diagnostics = append(*diagnostics, Diagnostic{
			Code: "skill_missing_description", Severity: "error", Path: path, SkillName: name,
			Message: "skill frontmatter must include a non-empty description",
		})
		return Skill{}, false
	}
	if utf8.RuneCountInString(description) > MaxDescriptionRunes {
		*diagnostics = append(*diagnostics, Diagnostic{
			Code: "skill_description_too_long", Severity: "error", Path: path, SkillName: name,
			Message: fmt.Sprintf("skill description exceeds %d characters", MaxDescriptionRunes),
		})
		return Skill{}, false
	}

	var metadata map[string]any
	if raw, exists := values["metadata"]; exists && raw != nil {
		if converted, ok := raw.(map[string]any); ok {
			metadata = converted
		} else {
			*diagnostics = append(*diagnostics, Diagnostic{
				Code: "skill_invalid_metadata", Severity: "warning", Path: path, SkillName: name,
				Message: "skill metadata must be a YAML mapping",
			})
		}
	}
	riskFields := make([]string, 0)
	for _, key := range keys {
		if !safeFrontmatterKeys[key] {
			riskFields = append(riskFields, key)
		}
	}
	sort.Strings(riskFields)
	digest := sha256.Sum256(data)
	return Skill{
		Name:            name,
		Description:     description,
		WhenToUse:       stringValue(values, "when_to_use"),
		License:         stringValue(values, "license"),
		Metadata:        metadata,
		Path:            path,
		Directory:       filepath.Dir(path),
		RootPath:        root.path,
		Scope:           root.scope,
		Source:          root.source,
		Digest:          hex.EncodeToString(digest[:]),
		SafeToAutoLoad:  len(riskFields) == 0,
		FrontmatterKeys: keys,
		RiskFields:      riskFields,
	}, true
}

type frontmatterData struct {
	present bool
	values  []byte
	body    []byte
}

func splitFrontmatter(data []byte) (frontmatterData, error) {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	firstEnd := bytes.IndexByte(data, '\n')
	firstLine := data
	if firstEnd >= 0 {
		firstLine = data[:firstEnd]
	}
	if strings.TrimSpace(string(firstLine)) != "---" {
		return frontmatterData{body: data}, nil
	}
	if firstEnd < 0 {
		return frontmatterData{present: true}, fmt.Errorf("unterminated YAML frontmatter")
	}
	position := firstEnd + 1
	for position <= len(data) {
		lineEndRelative := bytes.IndexByte(data[position:], '\n')
		lineEnd := len(data)
		next := len(data) + 1
		if lineEndRelative >= 0 {
			lineEnd = position + lineEndRelative
			next = lineEnd + 1
		}
		line := strings.TrimSpace(string(data[position:lineEnd]))
		if line == "---" || line == "..." {
			body := []byte(nil)
			if next <= len(data) {
				body = data[next:]
			}
			return frontmatterData{present: true, values: data[firstEnd+1 : position], body: body}, nil
		}
		if lineEndRelative < 0 {
			break
		}
		position = next
	}
	return frontmatterData{present: true}, fmt.Errorf("unterminated YAML frontmatter")
}

func parseYAML(data []byte) (map[string]any, []string, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]any{}, nil, nil
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, nil, fmt.Errorf("parse YAML frontmatter: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, nil, fmt.Errorf("multiple YAML frontmatter documents are not supported")
		}
		return nil, nil, fmt.Errorf("parse trailing YAML frontmatter: %w", err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("YAML frontmatter must be a mapping")
	}
	mapping := document.Content[0]
	values := make(map[string]any, len(mapping.Content)/2)
	keys := make([]string, 0, len(mapping.Content)/2)
	for i := 0; i < len(mapping.Content); i += 2 {
		key := mapping.Content[i].Value
		var value any
		if err := mapping.Content[i+1].Decode(&value); err != nil {
			return nil, nil, fmt.Errorf("decode frontmatter key %q: %w", key, err)
		}
		keys = append(keys, key)
		values[key] = value
	}
	return values, keys, nil
}

func stringValue(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}
