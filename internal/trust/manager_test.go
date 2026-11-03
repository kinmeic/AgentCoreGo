package trust

import (
	"agent-core/internal/project"
	"agent-core/internal/store"
	"path/filepath"
	"testing"
)

func TestTrustGrantReviewRevoke(t *testing.T) {
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "trust.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	manager := NewManager(db)
	workspace := t.TempDir()
	declarations := []project.Declaration{{Kind: "workflow", Path: "workflow.yaml", Digest: "abc"}}
	status, err := manager.Review(workspace, declarations)
	if err != nil || status.Trusted {
		t.Fatalf("unexpected initial trust: %#v %v", status, err)
	}
	if _, err := manager.Grant(workspace, "wrong", declarations); err == nil {
		t.Fatal("granted mismatched digest")
	}
	status, err = manager.Grant(workspace, status.BundleDigest, declarations)
	if err != nil || !status.Trusted {
		t.Fatalf("grant failed: %#v %v", status, err)
	}
	status, err = manager.Review(workspace, declarations)
	if err != nil || !status.Trusted {
		t.Fatalf("trust not persisted: %#v %v", status, err)
	}
	if err := manager.Revoke(workspace, status.BundleDigest); err != nil {
		t.Fatal(err)
	}
	status, err = manager.Review(workspace, declarations)
	if err != nil || status.Trusted {
		t.Fatalf("trust not revoked: %#v %v", status, err)
	}
}
