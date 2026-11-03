package workspace

import (
	"context"
	"os"
	"path/filepath"
)

// WriteOptions requires the current revision when replacing an existing file.
type WriteOptions struct {
	ExpectedRevision string
	CreateSnapshot   bool
}

// WriteFile atomically replaces a UTF-8 file after optimistic revision validation.
func (s *Service) WriteFile(ctx context.Context, requestedPath string, content []byte, options WriteOptions) (FilePage, error) {
	const op = "workspace.write"
	if options.CreateSnapshot {
		if _, err := s.CreateSnapshot(ctx); err != nil {
			return FilePage{}, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	resolved, relative, err := s.resolve(op, requestedPath, true)
	if err != nil {
		return FilePage{}, err
	}
	if int64(len(content)) > s.maxReadFileBytes {
		return FilePage{}, newError(op, CodeTooLarge, relative, "content exceeds write budget")
	}
	info, err := os.Lstat(resolved)
	exists := err == nil
	if exists {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return FilePage{}, newError(op, CodeNotRegularFile, relative, "target is not a regular file")
		}
		current, err := s.readFile(ctx, relative, ReadOptions{Limit: 1})
		if err != nil {
			return FilePage{}, err
		}
		if options.ExpectedRevision == "" {
			return FilePage{}, newError(op, CodeConflict, relative, "If-Match revision is required")
		}
		if current.Revision != options.ExpectedRevision {
			return FilePage{}, &Error{Code: CodeConflict, Op: op, Path: relative,
				Message: "file revision does not match", ExpectedRevision: options.ExpectedRevision, CurrentRevision: current.Revision}
		}
	} else if !os.IsNotExist(err) {
		return FilePage{}, filesystemError(op, relative, err)
	} else if options.ExpectedRevision != "" {
		return FilePage{}, newError(op, CodeConflict, relative, "file was created after the read")
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return FilePage{}, err
	}
	temp, err := os.CreateTemp(filepath.Dir(resolved), ".agentcore-write-*")
	if err != nil {
		return FilePage{}, err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	mode := os.FileMode(0o644)
	if exists {
		mode = info.Mode().Perm()
	}
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return FilePage{}, err
	}
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return FilePage{}, err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return FilePage{}, err
	}
	if err := temp.Close(); err != nil {
		return FilePage{}, err
	}
	if err := os.Rename(tempPath, resolved); err != nil {
		return FilePage{}, err
	}
	return s.readFile(ctx, relative, ReadOptions{Limit: 1})
}
