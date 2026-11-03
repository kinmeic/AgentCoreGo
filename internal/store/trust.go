package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

func (s *SQLite) GrantWorkspaceTrust(workspaceID, bundleDigest string, declarations []byte) error {
	_, err := s.db.Exec(`INSERT INTO workspace_trust(workspace_id, bundle_digest, declarations, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT(workspace_id, bundle_digest) DO UPDATE SET declarations=excluded.declarations,
		updated_at=excluded.updated_at`, workspaceID, bundleDigest, string(declarations), time.Now(), time.Now())
	return err
}

func (s *SQLite) WorkspaceTrust(workspaceID, bundleDigest string) (bool, []byte, error) {
	var declarations string
	err := s.db.QueryRow(`SELECT declarations FROM workspace_trust WHERE workspace_id = ? AND bundle_digest = ?`,
		workspaceID, bundleDigest).Scan(&declarations)
	if err == sql.ErrNoRows {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	if !json.Valid([]byte(declarations)) {
		return false, nil, fmt.Errorf("corrupt workspace trust declarations")
	}
	return true, []byte(declarations), nil
}

func (s *SQLite) RevokeWorkspaceTrust(workspaceID, bundleDigest string) error {
	var err error
	if bundleDigest == "" {
		_, err = s.db.Exec(`DELETE FROM workspace_trust WHERE workspace_id = ?`, workspaceID)
	} else {
		_, err = s.db.Exec(`DELETE FROM workspace_trust WHERE workspace_id = ? AND bundle_digest = ?`, workspaceID, bundleDigest)
	}
	return err
}
