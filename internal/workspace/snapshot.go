package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/google/uuid"
)

// SnapshotFile is one manifest entry; symlink targets are recorded, never followed.
type SnapshotFile struct {
	Path       string    `json:"path"`
	Type       EntryType `json:"type"`
	Mode       uint32    `json:"mode"`
	Size       int64     `json:"size,omitempty"`
	SHA256     string    `json:"sha256,omitempty"`
	LinkTarget string    `json:"link_target,omitempty"`
}

// SnapshotManifest binds a complete tree to one workspace and content digest.
type SnapshotManifest struct {
	SchemaVersion int            `json:"schema_version"`
	ID            string         `json:"id"`
	Workspace     string         `json:"workspace"`
	Digest        string         `json:"digest"`
	CreatedAt     time.Time      `json:"created_at"`
	Files         []SnapshotFile `json:"files"`
}

// SnapshotSummary is the list representation without the full manifest.
type SnapshotSummary struct {
	ID        string    `json:"id"`
	Digest    string    `json:"digest"`
	FileCount int       `json:"file_count"`
	CreatedAt time.Time `json:"created_at"`
}

// RestoreOptions controls explicit approval of changes made after the snapshot.
type RestoreOptions struct {
	AllowChanges      bool
	ExpectedRevisions map[string]string
}

func (s *Service) CreateSnapshot(ctx context.Context) (SnapshotManifest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.createSnapshot(ctx)
}

func (s *Service) createSnapshot(ctx context.Context) (SnapshotManifest, error) {
	const op = "workspace.snapshot.create"
	manifest := SnapshotManifest{SchemaVersion: 1, ID: uuid.NewString(), Workspace: s.root, CreatedAt: time.Now().UTC()}
	var total int64
	err := filepath.WalkDir(s.root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		relative, relErr := filepath.Rel(s.root, path)
		if relErr != nil {
			return relErr
		}
		relative = filepath.ToSlash(relative)
		if relative == "." {
			return nil
		}
		if isGitMetadata(relative) || s.isStatePath(relative) || s.ignore.Match(relative, entry.IsDir()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if len(manifest.Files) >= s.maxSnapshotEntries {
			return newError(op, CodeTooLarge, relative, "too many snapshot entries")
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		item := SnapshotFile{Path: relative, Type: entryType(info.Mode()), Mode: modeToUnix(info.Mode()), Size: info.Size()}
		switch {
		case info.Mode().IsRegular():
			total += info.Size()
			if info.Size() > s.maxSnapshotFileBytes || total > s.maxSnapshotTotalBytes {
				return newError(op, CodeTooLarge, relative, "snapshot byte budget exceeded")
			}
			digest, err := s.storeBlob(ctx, path, info.Size())
			if err != nil {
				return err
			}
			item.SHA256 = digest
		case info.IsDir():
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			item.LinkTarget = target
		default:
			return newError(op, CodeUnsupported, relative, "unsupported filesystem entry")
		}
		manifest.Files = append(manifest.Files, item)
		return nil
	})
	if err != nil {
		return SnapshotManifest{}, normalizeError(op, err)
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	manifest.Digest = manifestDigest(manifest)
	if err := s.writeManifest(manifest); err != nil {
		return SnapshotManifest{}, err
	}
	return manifest, nil
}

func manifestDigest(manifest SnapshotManifest) string {
	clone := manifest
	clone.Digest = ""
	data, _ := json.Marshal(clone)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (s *Service) storeBlob(ctx context.Context, path string, expectedSize int64) (string, error) {
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return "", err
	}
	defer root.Close()
	relative, err := filepath.Rel(s.root, path)
	if err != nil {
		return "", err
	}
	file, err := root.Open(relative)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", newError("workspace.snapshot.create", CodeNotRegularFile, relative, "not a regular file")
	}
	directory := filepath.Join(s.stateDir, "blobs")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	destination, err := os.CreateTemp(directory, ".blob-*")
	if err != nil {
		return "", err
	}
	defer destination.Close()
	defer os.Remove(destination.Name())
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(destination, hash), io.LimitReader(file, s.maxSnapshotFileBytes+1))
	if err != nil {
		return "", err
	}
	if size > s.maxSnapshotFileBytes {
		return "", newError("workspace.snapshot.create", CodeTooLarge, relative, "snapshot file budget exceeded")
	}
	if size != expectedSize {
		return "", newError("workspace.snapshot.create", CodeConflict, relative, "file changed while snapshotting")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := destination.Sync(); err != nil {
		return "", err
	}
	if err := destination.Close(); err != nil {
		return "", err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	blob := filepath.Join(directory, digest[:2], digest)
	if err := os.MkdirAll(filepath.Dir(blob), 0o700); err != nil {
		return "", err
	}
	// Publish only complete blobs. Hash and stored bytes come from one read.
	return digest, os.Rename(destination.Name(), blob)
}

func (s *Service) writeManifest(manifest SnapshotManifest) error {
	directory := filepath.Join(s.stateDir, "snapshots", manifest.ID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	path := filepath.Join(directory, "manifest.json")
	return os.WriteFile(path, data, 0o600)
}

func (s *Service) Snapshot(id string) (SnapshotManifest, error) {
	if _, err := uuid.Parse(id); err != nil {
		return SnapshotManifest{}, newError("workspace.snapshot.load", CodeInvalidArgument, id, "invalid snapshot ID")
	}
	data, err := os.ReadFile(filepath.Join(s.stateDir, "snapshots", id, "manifest.json"))
	if err != nil {
		return SnapshotManifest{}, filesystemError("workspace.snapshot.load", id, err)
	}
	var manifest SnapshotManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return SnapshotManifest{}, err
	}
	if manifest.ID != id || manifest.SchemaVersion != 1 || manifest.Workspace != s.root || manifest.Digest != manifestDigest(manifest) {
		return SnapshotManifest{}, newError("workspace.snapshot.load", CodeConflict, id, "snapshot manifest is invalid or belongs to another workspace")
	}
	if err := s.validateManifest(manifest); err != nil {
		return SnapshotManifest{}, err
	}
	return manifest, nil
}

func (s *Service) ListSnapshots(_ context.Context, limit int) ([]SnapshotSummary, error) {
	if limit <= 0 {
		limit = defaultSnapshotLimit
	}
	if limit > maxSnapshotLimit {
		limit = maxSnapshotLimit
	}
	entries, err := os.ReadDir(filepath.Join(s.stateDir, "snapshots"))
	if os.IsNotExist(err) {
		return []SnapshotSummary{}, nil
	}
	if err != nil {
		return nil, err
	}
	var result []SnapshotSummary
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifest, err := s.Snapshot(entry.Name())
		if err != nil {
			continue
		}
		result = append(result, SnapshotSummary{ID: manifest.ID, Digest: manifest.Digest, FileCount: len(manifest.Files), CreatedAt: manifest.CreatedAt})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

// RestoreSnapshot creates a compensating snapshot before applying the requested tree.
func (s *Service) RestoreSnapshot(ctx context.Context, id string, options RestoreOptions) (SnapshotManifest, error) {
	const op = "workspace.snapshot.restore"
	s.mu.Lock()
	defer s.mu.Unlock()
	manifest, err := s.Snapshot(id)
	if err != nil {
		return SnapshotManifest{}, err
	}
	if err := s.checkRestoreConflicts(manifest, options); err != nil {
		return SnapshotManifest{}, err
	}
	if err := s.verifySnapshotBlobs(ctx, manifest); err != nil {
		return SnapshotManifest{}, err
	}
	compensation, err := s.createSnapshotLocked(ctx)
	if err != nil {
		return SnapshotManifest{}, err
	}
	if err := s.applySnapshot(ctx, manifest); err != nil {
		return SnapshotManifest{}, err
	}
	return compensation, nil
}

func (s *Service) createSnapshotLocked(ctx context.Context) (SnapshotManifest, error) {
	return s.createSnapshot(ctx)
}
