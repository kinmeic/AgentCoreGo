// Package skills discovers and loads filesystem-based agent skills.
package skills

import (
	"errors"
	"fmt"
)

const (
	// MaxBodyBytes is the maximum number of markdown body bytes returned by Load.
	MaxBodyBytes = 100 * 1024
	// MaxDescriptionRunes matches the ZCode metadata limit.
	MaxDescriptionRunes = 1024
)

// Scope identifies the configuration layer that contributed a skill.
type Scope string

const (
	ScopeRoot    Scope = "root"
	ScopeProject Scope = "project"
	ScopeUser    Scope = "user"
)

// Source identifies the directory convention that contributed a skill.
type Source string

const (
	SourceRoot   Source = "root"
	SourceZCode  Source = "zcode"
	SourceAgents Source = "agents"
	SourceClaude Source = "claude"
)

// Options controls discovery. Explicit Roots have the highest priority and are
// searched in slice order. Project directories are then searched nearest-first,
// followed by user directories. Within a directory, .zcode, .agents, and
// .claude are searched in that order.
type Options struct {
	Roots      []string
	ProjectDir string
	UserDir    string
}

// Skill is the discoverable metadata for one SKILL.md. Same-named skills are
// retained; ShadowedBy identifies the higher-priority path selected by Load.
type Skill struct {
	Name            string         `json:"name"`
	Description     string         `json:"description"`
	WhenToUse       string         `json:"when_to_use,omitempty"`
	License         string         `json:"license,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	Path            string         `json:"path"`
	Directory       string         `json:"directory"`
	RootPath        string         `json:"root_path"`
	Scope           Scope          `json:"scope"`
	Source          Source         `json:"source"`
	Digest          string         `json:"digest"`
	SafeToAutoLoad  bool           `json:"safe_to_auto_load"`
	FrontmatterKeys []string       `json:"frontmatter_keys,omitempty"`
	RiskFields      []string       `json:"risk_fields,omitempty"`
	ShadowedBy      string         `json:"shadowed_by,omitempty"`
}

// LoadedSkill contains a skill body read on demand. Content never exceeds
// MaxBodyBytes. Digest on Skill covers the complete source file, including
// frontmatter and any truncated body bytes.
type LoadedSkill struct {
	Skill         Skill  `json:"skill"`
	Content       string `json:"content"`
	BaseDirectory string `json:"base_directory"`
	BytesRead     int64  `json:"bytes_read"`
	SizeBytes     int64  `json:"size_bytes"`
	Truncated     bool   `json:"truncated"`
}

// Diagnostic reports a non-fatal discovery problem.
type Diagnostic struct {
	Code      string `json:"code"`
	Severity  string `json:"severity"`
	Message   string `json:"message"`
	Path      string `json:"path,omitempty"`
	SkillName string `json:"skill_name,omitempty"`
}

// Catalog is a point-in-time discovery result. Skills are sorted by name and
// then discovery priority, so the first entry for a name is the one Load uses.
type Catalog struct {
	Skills          []Skill      `json:"skills"`
	Diagnostics     []Diagnostic `json:"diagnostics,omitempty"`
	TotalDiscovered int          `json:"total_discovered"`
}

// ErrNotFound is returned when no visible skill has the requested name.
var ErrNotFound = errors.New("skill not found")

// NotFoundError includes the requested skill name and supports errors.Is.
type NotFoundError struct {
	Name string
}

func (e *NotFoundError) Error() string { return fmt.Sprintf("skill not found: %s", e.Name) }
func (e *NotFoundError) Unwrap() error { return ErrNotFound }
