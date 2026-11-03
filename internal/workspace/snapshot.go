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
	"strings"
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
			digest, err := s.storeBlob(path)
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

func (s *Service) storeBlob(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	blob := filepath.Join(s.stateDir, "blobs", digest[:2], digest)
	if _, err := os.Stat(blob); err == nil {
		return digest, nil
	}
	if err := os.MkdirAll(filepath.Dir(blob), 0o700); err != nil {
		return "", err
	}
	if _, err := os.OpenFile(blob, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); err != nil {
		if os.IsExist(err) {
			return digest, nil
		}
		return "", err
	}
	file.Seek(0, io.SeekStart)
	destination, err := os.OpenFile(blob, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(destination, file)
	syncErr := destination.Sync()
	closeErr := destination.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if syncErr != nil {
		return "", syncErr
	}
	return digest, closeErr
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
	if id == "" || strings.ContainsAny(id, "/\\\x00") {
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
	if manifest.Workspace != s.root || manifest.Digest != manifestDigest(manifest) {
		return SnapshotManifest{}, newError("workspace.snapshot.load", CodeConflict, id, "snapshot manifest is invalid or belongs to another workspace")
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
	compensation, err := s.createSnapshotLocked(ctx)
	if err != nil {
		return SnapshotManifest{}, err
	}
	if err := s.applySnapshot(ctx, manifest); err != nil {
		return SnapshotManifest{}, err
	}
	return compensation, nil
}

func (s *Service) checkRestoreConflicts(manifest SnapshotManifest, options RestoreOptions) error {
	if options.AllowChanges {
		return nil
	}
	current, err := s.currentRevisionMap()
	if err != nil {
		return err
	}
	desired := make(map[string]SnapshotFile, len(manifest.Files))
	for _, item := range manifest.Files {
		desired[item.Path] = item
	}
	for path, item := range desired {
		if current[path] != item.SHA256 && options.ExpectedRevisions[path] != current[path] {
			return newError("workspace.snapshot.restore", CodeConflict, path, "file changed after snapshot; explicit allow_changes is required")
		}
	}
	for path := range current {
		if _, ok := desired[path]; !ok && options.ExpectedRevisions[path] != current[path] {
			return newError("workspace.snapshot.restore", CodeConflict, path, "new file would be removed; explicit allow_changes is required")
		}
	}
	return nil
}

func (s *Service) currentRevisionMap() (map[string]string, error) {
	result := make(map[string]string)
	err := filepath.WalkDir(s.root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(s.root, path)
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
		if entry.Type().IsRegular() {
			hash := sha256.New()
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(hash, file)
			file.Close()
			if copyErr != nil {
				return copyErr
			}
			result[relative] = hex.EncodeToString(hash.Sum(nil))
		}
		return nil
	})
	return result, err
}

func (s *Service) createSnapshotLocked(ctx context.Context) (SnapshotManifest, error) {
	return s.createSnapshot(ctx)
}

func (s *Service) applySnapshot(ctx context.Context, manifest SnapshotManifest) error {
	desired := make(map[string]bool, len(manifest.Files))
	for _, item := range manifest.Files {
		desired[item.Path] = true
	}
	if err := filepath.WalkDir(s.root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(s.root, path)
		relative = filepath.ToSlash(relative)
		if relative == "." {
			return nil
		}
		if isGitMetadata(relative) || s.isStatePath(relative) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !desired[relative] && !entry.IsDir() {
			return os.Remove(path)
		}
		return nil
	}); err != nil {
		return err
	}
	for _, item := range manifest.Files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		path := filepath.Join(s.root, filepath.FromSlash(item.Path))
		switch item.Type {
		case EntryDirectory:
			if err := os.MkdirAll(path, modeFromUnix(item.Mode)); err != nil {
				return err
			}
		case EntryFile:
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			blob := filepath.Join(s.stateDir, "blobs", item.SHA256[:2], item.SHA256)
			data, err := os.ReadFile(blob)
			if err != nil {
				return err
			}
			if err := os.WriteFile(path, data, modeFromUnix(item.Mode)); err != nil {
				return err
			}
		case EntrySymlink:
			_ = os.Remove(path)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(item.LinkTarget, path); err != nil {
				return err
			}
		}
	}
	return nil
}
