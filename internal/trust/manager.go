package trust

import (
	"agent-core/internal/project"
	"agent-core/internal/store"
	"encoding/json"
	"fmt"
	"path/filepath"
)

// Status is the trust decision for one exact declaration bundle.
type Status struct {
	Workspace    string                `json:"workspace"`
	BundleDigest string                `json:"bundle_digest"`
	Trusted      bool                  `json:"trusted"`
	Declarations []project.Declaration `json:"declarations"`
}

type Manager struct{ store *store.SQLite }

func NewManager(store *store.SQLite) *Manager { return &Manager{store: store} }

func (m *Manager) Review(workspace string, declarations []project.Declaration) (Status, error) {
	workspace, err := canonical(workspace)
	if err != nil {
		return Status{}, err
	}
	bundle := project.BundleDigest(declarations)
	trusted, _, err := m.store.WorkspaceTrust(workspace, bundle)
	if err != nil {
		return Status{}, err
	}
	return Status{Workspace: workspace, BundleDigest: bundle, Trusted: trusted, Declarations: declarations}, nil
}

// Grant recomputes the bundle so callers cannot grant an unrelated digest.
func (m *Manager) Grant(workspace, expectedDigest string, declarations []project.Declaration) (Status, error) {
	status, err := m.Review(workspace, declarations)
	if err != nil {
		return Status{}, err
	}
	if status.BundleDigest == "" || status.BundleDigest != expectedDigest {
		return Status{}, fmt.Errorf("trust bundle digest does not match current declarations")
	}
	encoded, _ := json.Marshal(declarations)
	if err := m.store.GrantWorkspaceTrust(status.Workspace, status.BundleDigest, encoded); err != nil {
		return Status{}, err
	}
	status.Trusted = true
	return status, nil
}

func (m *Manager) Revoke(workspace, bundleDigest string) error {
	workspace, err := canonical(workspace)
	if err != nil {
		return err
	}
	return m.store.RevokeWorkspaceTrust(workspace, bundleDigest)
}

func canonical(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		absolute = resolved
	}
	return filepath.Clean(absolute), nil
}
