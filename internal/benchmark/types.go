package benchmark

import (
	"agent-core/internal/config"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// Manifest defines an isolated, reproducible evaluation plan.
type Manifest struct {
	SchemaVersion  int    `json:"schema_version" yaml:"schema_version"`
	Name           string `json:"name" yaml:"name"`
	BaseURL        string `json:"base_url" yaml:"base_url"`
	APIFormat      string `json:"api_format,omitempty" yaml:"api_format"`
	APIKey         string `json:"api_key,omitempty" yaml:"api_key"`
	Model          string `json:"model" yaml:"model"`
	Fixture        string `json:"fixture,omitempty" yaml:"fixture"`
	Concurrency    int    `json:"concurrency,omitempty" yaml:"concurrency"`
	Repetitions    int    `json:"repetitions,omitempty" yaml:"repetitions"`
	TimeoutSeconds int    `json:"timeout_seconds" yaml:"timeout_seconds"`
	PermissionMode string `json:"permission_mode,omitempty" yaml:"permission_mode"`
	Trusted        bool   `json:"trusted,omitempty" yaml:"trusted"`
	Cases          []Case `json:"cases" yaml:"cases"`
}

// Case is one prompt and independent grading contract.
type Case struct {
	ID     string `json:"id" yaml:"id"`
	Prompt string `json:"prompt" yaml:"prompt"`
	Expect Expect `json:"expect" yaml:"expect"`
}

// Expect supports deterministic built-in graders and trusted argv graders.
type Expect struct {
	Type   string         `json:"type" yaml:"type"`
	Value  string         `json:"value,omitempty" yaml:"value"`
	Path   string         `json:"path,omitempty" yaml:"path"`
	Schema map[string]any `json:"schema,omitempty" yaml:"schema"`
	Argv   []string       `json:"argv,omitempty" yaml:"argv"`
}

// GraderResult is separate from Agent execution success.
type GraderResult struct {
	Passed bool   `json:"passed"`
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

// CaseResult contains evidence for one repetition.
type CaseResult struct {
	CaseID       string        `json:"case_id"`
	Repetition   int           `json:"repetition"`
	Status       string        `json:"status"`
	Reply        string        `json:"reply,omitempty"`
	RunID        string        `json:"run_id,omitempty"`
	Conversation string        `json:"conversation_id,omitempty"`
	DurationMS   int64         `json:"duration_ms"`
	TTFBMS       int64         `json:"ttfb_ms,omitempty"`
	PromptTokens int           `json:"prompt_tokens,omitempty"`
	OutputTokens int           `json:"output_tokens,omitempty"`
	TotalTokens  int           `json:"total_tokens,omitempty"`
	Error        string        `json:"error,omitempty"`
	Grader       *GraderResult `json:"grader,omitempty"`
	StartedAt    time.Time     `json:"started_at"`
	FinishedAt   time.Time     `json:"finished_at"`
}

// Plan is persisted before the first model request.
type Plan struct {
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	Manifest       Manifest     `json:"manifest"`
	ManifestDigest string       `json:"manifest_digest"`
	Status         string       `json:"status"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
	Results        []CaseResult `json:"results"`
}

func (m Manifest) Validate() error {
	if err := config.ValidateAPIFormat(m.APIFormat); err != nil {
		return fmt.Errorf("api_format: %w", err)
	}
	if m.SchemaVersion != 1 || m.Name == "" || m.BaseURL == "" || m.Model == "" {
		return fmt.Errorf("schema_version, name, base_url and model are required")
	}
	if len(m.Cases) == 0 {
		return fmt.Errorf("at least one case is required")
	}
	if m.TimeoutSeconds <= 0 {
		return fmt.Errorf("timeout_seconds must be positive")
	}
	seen := make(map[string]bool)
	for _, item := range m.Cases {
		if item.ID == "" || item.Prompt == "" || seen[item.ID] {
			return fmt.Errorf("case IDs must be unique and non-empty")
		}
		seen[item.ID] = true
		switch item.Expect.Type {
		case "exact", "contains", "regex", "json_schema", "file_exists", "argv":
		default:
			return fmt.Errorf("case %s has unsupported grader %q", item.ID, item.Expect.Type)
		}
		if item.Expect.Type == "argv" && !m.Trusted {
			return fmt.Errorf("case %s uses argv grader without trust", item.ID)
		}
	}
	return nil
}

func (m Manifest) Digest() string {
	clone := m
	clone.APIKey = ""
	data, _ := json.Marshal(clone)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
