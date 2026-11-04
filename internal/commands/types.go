// Package commands discovers custom slash commands and expands their static
// markdown templates. It never executes dynamic shell blocks.
package commands

import (
	"errors"
	"fmt"
)

const MaxDescriptionRunes = 1024

// Scope identifies the configuration layer that contributed a command.
type Scope string

const (
	ScopeRoot    Scope = "root"
	ScopeProject Scope = "project"
	ScopeUser    Scope = "user"
	ScopeSystem  Scope = "system"
)

// Source identifies the directory convention that contributed a command.
type Source string

const (
	SourceRoot    Source = "root"
	SourceZCode   Source = "zcode"
	SourceAgents  Source = "agents"
	SourceClaude  Source = "claude"
	SourceBuiltin Source = "builtin"
)

// Options controls custom command discovery. Explicit Roots have the highest
// priority and are searched in slice order. Project directories are then
// searched nearest-first, followed by user directories. Within a directory,
// .zcode, .agents, and .claude are searched in that order.
type Options struct {
	Overrides  map[string]bool
	Roots      []string
	ProjectDir string
	UserDir    string
}

// Command describes either a built-in command or a custom markdown command.
// Digest is the SHA-256 of the complete custom source file; it is empty for
// built-ins, whose executable behavior belongs to the embedding application.
type Command struct {
	Name                  string   `json:"name"`
	Aliases               []string `json:"aliases,omitempty"`
	Description           string   `json:"description"`
	ArgumentHint          string   `json:"argument_hint,omitempty"`
	AllowedTools          []string `json:"allowed_tools,omitempty"`
	Model                 string   `json:"model,omitempty"`
	Skills                []string `json:"skills,omitempty"`
	DisableNonInteractive bool     `json:"disable_non_interactive,omitempty"`
	Path                  string   `json:"path"`
	RootPath              string   `json:"root_path"`
	Scope                 Scope    `json:"scope"`
	Source                Source   `json:"source"`
	Digest                string   `json:"digest,omitempty"`
	FrontmatterKeys       []string `json:"frontmatter_keys,omitempty"`
	Builtin               bool     `json:"builtin,omitempty"`
	Summary               string   `json:"summary,omitempty"`
	Details               []string `json:"details,omitempty"`
	Usage                 string   `json:"usage,omitempty"`
	ShadowedBy            string   `json:"shadowed_by,omitempty"`
}

// Expansion contains both the unmodified markdown body and the result of
// argument substitution. RawDigest hashes Raw; Digest hashes Expanded.
type Expansion struct {
	Command                  Command  `json:"command"`
	Raw                      string   `json:"raw"`
	Expanded                 string   `json:"expanded"`
	RawDigest                string   `json:"raw_digest"`
	Digest                   string   `json:"digest"`
	Arguments                []string `json:"arguments"`
	UsedArgumentsPlaceholder bool     `json:"used_arguments_placeholder"`
}

// Diagnostic reports a non-fatal custom command discovery problem.
type Diagnostic struct {
	Code        string `json:"code"`
	Severity    string `json:"severity"`
	Message     string `json:"message"`
	Path        string `json:"path,omitempty"`
	CommandName string `json:"command_name,omitempty"`
}

// Catalog is a point-in-time custom command discovery result.
type Catalog struct {
	Commands        []Command    `json:"commands"`
	Diagnostics     []Diagnostic `json:"diagnostics,omitempty"`
	TotalDiscovered int          `json:"total_discovered"`
}

var (
	ErrNotFound      = errors.New("command not found")
	ErrDynamicShell  = errors.New("dynamic shell is not allowed in custom commands")
	ErrNotExpandable = errors.New("built-in commands do not have markdown expansions")
)

// NotFoundError includes the requested command name and supports errors.Is.
type NotFoundError struct {
	Name string
}

func (e *NotFoundError) Error() string { return fmt.Sprintf("command not found: %s", e.Name) }
func (e *NotFoundError) Unwrap() error { return ErrNotFound }

// NotExpandableError identifies a built-in command passed to Load.
type NotExpandableError struct {
	Name string
}

func (e *NotExpandableError) Error() string {
	return fmt.Sprintf("built-in command /%s has no markdown expansion", e.Name)
}
func (e *NotExpandableError) Unwrap() error { return ErrNotExpandable }
