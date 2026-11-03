package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"
)

func (s *Service) snapshotExcluded(relative string, directory bool) bool {
	if isGitMetadata(relative) || s.isStatePath(relative) {
		return true
	}
	parts := strings.Split(relative, "/")
	for i := range parts {
		if s.ignore.Match(strings.Join(parts[:i+1], "/"), i < len(parts)-1 || directory) {
			return true
		}
	}
	return false
}

// Validate the complete tree before consuming paths or slicing content hashes.
func (s *Service) validateManifest(manifest SnapshotManifest) error {
	const op = "workspace.snapshot.load"
	if len(manifest.Files) > s.maxSnapshotEntries {
		return newError(op, CodeTooLarge, manifest.ID, "too many snapshot entries")
	}
	entries := make(map[string]EntryType, len(manifest.Files))
	var total int64
	for _, item := range manifest.Files {
		_, duplicate := entries[item.Path]
		if item.Path == "." || !fs.ValidPath(item.Path) || !filepath.IsLocal(filepath.FromSlash(item.Path)) || strings.ContainsAny(item.Path, "\\\x00") || duplicate || item.Mode & ^uint32(0o7777) != 0 || s.snapshotExcluded(item.Path, item.Type == EntryDirectory) {
			return newError(op, CodeInvalidArgument, item.Path, "invalid or protected snapshot path")
		}
		entries[item.Path] = item.Type
		switch item.Type {
		case EntryFile:
			digest, err := hex.DecodeString(item.SHA256)
			if err != nil || len(digest) != sha256.Size || strings.ToLower(item.SHA256) != item.SHA256 || item.Size < 0 {
				return newError(op, CodeInvalidArgument, item.Path, "invalid snapshot file digest or size")
			}
			if item.Size > s.maxSnapshotFileBytes || item.Size > s.maxSnapshotTotalBytes-total {
				return newError(op, CodeTooLarge, item.Path, "snapshot byte budget exceeded")
			}
			total += item.Size
		case EntryDirectory:
		case EntrySymlink:
			if item.LinkTarget == "" || strings.ContainsRune(item.LinkTarget, 0) {
				return newError(op, CodeInvalidArgument, item.Path, "invalid symlink target")
			}
		default:
			return newError(op, CodeUnsupported, item.Path, "unsupported snapshot entry")
		}
	}
	for _, item := range manifest.Files {
		if parent := path.Dir(item.Path); parent != "." && entries[parent] != EntryDirectory {
			return newError(op, CodeInvalidArgument, item.Path, "snapshot parent must be a directory")
		}
	}
	return nil
}

func (s *Service) verifySnapshotBlobs(ctx context.Context, manifest SnapshotManifest) error {
	for _, item := range manifest.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if item.Type != EntryFile {
			continue
		}
		if err := s.copySnapshotBlob(io.Discard, item); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) copySnapshotBlob(destination io.Writer, item SnapshotFile) error {
	root, err := os.OpenRoot(filepath.Join(s.stateDir, "blobs"))
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := root.Open(filepath.Join(item.SHA256[:2], item.SHA256))
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != item.Size {
		return newError("workspace.snapshot.restore", CodeConflict, item.Path, "snapshot blob size or type is invalid")
	}
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(destination, hash), io.LimitReader(file, item.Size+1))
	if err != nil {
		return err
	}
	if size != item.Size || hex.EncodeToString(hash.Sum(nil)) != item.SHA256 {
		return newError("workspace.snapshot.restore", CodeConflict, item.Path, "snapshot blob checksum mismatch")
	}
	return nil
}

func (s *Service) checkRestoreConflicts(manifest SnapshotManifest, options RestoreOptions) error {
	if options.AllowChanges {
		return nil
	}
	current, err := s.currentSnapshotTree()
	if err != nil {
		return err
	}
	desired := make(map[string]SnapshotFile, len(manifest.Files))
	for _, item := range manifest.Files {
		desired[item.Path] = item
	}
	for name, item := range desired {
		existing, exists := current[name]
		if exists && existing.Type == item.Type && existing.Mode == item.Mode && existing.SHA256 == item.SHA256 && existing.LinkTarget == item.LinkTarget {
			continue
		}
		// Revision approvals are explicit, including an empty revision for a
		// missing path. Directory and symlink changes need allow_changes.
		expected, approved := options.ExpectedRevisions[name]
		if approved && item.Type == EntryFile && (!exists || existing.Type == EntryFile) && expected == existing.SHA256 {
			continue
		}
		return newError("workspace.snapshot.restore", CodeConflict, name, "entry changed after snapshot; explicit approval is required")
	}
	for name, item := range current {
		if _, exists := desired[name]; exists {
			continue
		}
		expected, approved := options.ExpectedRevisions[name]
		if approved && item.Type == EntryFile && expected == item.SHA256 {
			continue
		}
		return newError("workspace.snapshot.restore", CodeConflict, name, "new entry would be removed; explicit approval is required")
	}
	return nil
}

func (s *Service) currentSnapshotTree() (map[string]SnapshotFile, error) {
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	result := make(map[string]SnapshotFile)
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		if s.snapshotExcluded(name, entry.IsDir()) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		item := SnapshotFile{Path: name, Type: entryType(info.Mode()), Mode: modeToUnix(info.Mode())}
		switch item.Type {
		case EntryFile:
			file, err := root.Open(name)
			if err != nil {
				return err
			}
			hash := sha256.New()
			size, copyErr := io.Copy(hash, io.LimitReader(file, s.maxSnapshotFileBytes+1))
			file.Close()
			if copyErr != nil {
				return copyErr
			}
			if size > s.maxSnapshotFileBytes {
				return newError("workspace.snapshot.restore", CodeTooLarge, name, "file exceeds snapshot budget")
			}
			item.SHA256 = hex.EncodeToString(hash.Sum(nil))
		case EntrySymlink:
			item.LinkTarget, err = root.Readlink(name)
			if err != nil {
				return err
			}
		}
		result[name] = item
		return nil
	})
	return result, err
}

func (s *Service) applySnapshot(ctx context.Context, manifest SnapshotManifest) error {
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return err
	}
	defer root.Close()
	desired := make(map[string]SnapshotFile, len(manifest.Files))
	for _, item := range manifest.Files {
		desired[item.Path] = item
	}
	var existing []string
	protectedAncestors := make(map[string]bool)
	if err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		if s.snapshotExcluded(name, entry.IsDir()) {
			for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
				protectedAncestors[parent] = true
			}
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		existing = append(existing, name)
		return nil
	}); err != nil {
		return err
	}
	for name := range protectedAncestors {
		if item, wanted := desired[name]; wanted && item.Type != EntryDirectory {
			return newError("workspace.snapshot.restore", CodeConflict, name, "directory contains protected entries")
		}
	}
	// Remove children before parents, replacing links rather than following
	// them. Never RemoveAll: a directory can contain excluded/private data.
	for i := len(existing) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := existing[i]
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		item, wanted := desired[name]
		if wanted && info.Mode().IsRegular() && item.Type == EntryFile {
			continue
		}
		if info.IsDir() {
			if wanted && item.Type == EntryDirectory {
				continue
			}
			children, err := fs.ReadDir(root.FS(), name)
			if err != nil {
				return err
			}
			if len(children) != 0 {
				if wanted {
					return newError("workspace.snapshot.restore", CodeConflict, name, "directory contains protected entries")
				}
				continue
			}
		}
		if err := root.Remove(name); err != nil {
			return err
		}
	}
	files := append([]SnapshotFile(nil), manifest.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	for _, item := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := filepath.FromSlash(item.Path)
		switch item.Type {
		case EntryDirectory:
			// Defer restrictive modes until all descendants are restored.
			if err := root.MkdirAll(name, modeFromUnix(item.Mode)|0o700); err != nil {
				return err
			}
		case EntryFile:
			tempName := filepath.Join(filepath.Dir(name), ".agentcore-restore-"+uuid.NewString())
			file, err := root.OpenFile(tempName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			err = s.copySnapshotBlob(file, item)
			if err == nil {
				err = file.Chmod(modeFromUnix(item.Mode))
			}
			if err == nil {
				err = file.Sync()
			}
			closeErr := file.Close()
			if err == nil {
				err = closeErr
			}
			if err == nil {
				err = root.Rename(tempName, name)
			}
			_ = root.Remove(tempName)
			if err != nil {
				return err
			}
		case EntrySymlink:
			if err := root.Symlink(item.LinkTarget, name); err != nil {
				return err
			}
		}
	}
	for i := len(files) - 1; i >= 0; i-- {
		if files[i].Type == EntryDirectory {
			if err := root.Chmod(filepath.FromSlash(files[i].Path), modeFromUnix(files[i].Mode)); err != nil {
				return err
			}
		}
	}
	return nil
}
