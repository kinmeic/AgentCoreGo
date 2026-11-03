// Package workspace provides confined file inspection, content-addressed
// snapshots and rollback, and read-only Git queries for one workspace tree.
package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"agent-core/internal/sandbox"
)

const (
	// DefaultStateDirectory is reserved when Config.StateDir is empty.
	DefaultStateDirectory = ".agentcore-workspace"

	defaultReadFileBytes     int64 = 8 << 20
	defaultSnapshotFileBytes int64 = 32 << 20
	defaultSnapshotTotalByte int64 = 512 << 20
	defaultSnapshotEntries         = 100_000
	defaultGitOutputBytes    int64 = 4 << 20
	defaultReadLines               = 500
	maxReadLines                   = 2_000
	defaultListEntries             = 200
	maxListEntries                 = 1_000
	defaultSnapshotLimit           = 100
	maxSnapshotLimit               = 1_000
)

// Config controls a Service. Zero limits use the documented defaults. Ignore
// patterns apply only to snapshots and revision calculation: patterns without
// a slash match any path component; patterns with a slash use path.Match
// against the slash-separated workspace-relative path. A trailing slash limits
// a pattern to directories. There is no negation syntax.
type Config struct {
	Root                  string
	StateDir              string
	Ignore                []string
	MaxReadFileBytes      int64
	MaxSnapshotFileBytes  int64
	MaxSnapshotTotalBytes int64
	MaxSnapshotEntries    int
	MaxGitOutputBytes     int64
	GitExecutable         string
}

// Service serializes mutations made through the service. It does not
// coordinate with other Service instances or processes modifying the tree.
type Service struct {
	root     string
	stateDir string
	stateRel string
	gitPath  string
	ignore   ignoreMatcher
	mu       sync.RWMutex

	maxReadFileBytes      int64
	maxSnapshotFileBytes  int64
	maxSnapshotTotalBytes int64
	maxSnapshotEntries    int
	maxGitOutputBytes     int64
}

// New creates a workspace service and its snapshot state directory.
func New(config Config) (*Service, error) {
	const op = "workspace.new"
	if config.Root == "" {
		return nil, newError(op, CodeInvalidArgument, "", "root is required")
	}
	for name, value := range map[string]int64{
		"max read bytes": config.MaxReadFileBytes, "max snapshot file bytes": config.MaxSnapshotFileBytes,
		"max snapshot total bytes": config.MaxSnapshotTotalBytes, "max git output bytes": config.MaxGitOutputBytes,
	} {
		if value < 0 {
			return nil, newError(op, CodeInvalidArgument, "", "%s cannot be negative", name)
		}
	}
	if config.MaxSnapshotEntries < 0 {
		return nil, newError(op, CodeInvalidArgument, "", "max snapshot entries cannot be negative")
	}

	root, err := filepath.Abs(config.Root)
	if err != nil {
		return nil, wrapError(op, CodeInvalidArgument, "", err, "resolve root: %v", err)
	}
	if err := sandbox.EnsureWorkspace(root); err != nil {
		return nil, wrapError(op, CodeInternal, "", err, "create root: %v", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, wrapError(op, CodeInternal, "", err, "resolve root: %v", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, wrapError(op, CodeInternal, "", err, "stat root: %v", err)
	}
	if !info.IsDir() {
		return nil, newError(op, CodeInvalidArgument, "", "root is not a directory")
	}

	stateDir := config.StateDir
	if stateDir == "" {
		stateDir = filepath.Join(root, DefaultStateDirectory)
	} else if !filepath.IsAbs(stateDir) {
		stateDir = filepath.Join(root, stateDir)
	}
	stateDir, err = filepath.Abs(stateDir)
	if err != nil {
		return nil, wrapError(op, CodeInvalidArgument, "", err, "resolve state directory: %v", err)
	}
	stateDir = filepath.Clean(stateDir)
	if stateDir == root {
		return nil, newError(op, CodeInvalidArgument, "", "state directory cannot be the workspace root")
	}
	if rel, relErr := filepath.Rel(root, stateDir); relErr == nil && !pathEscapes(rel) {
		if strings.HasPrefix(filepath.ToSlash(rel), ".git/") || filepath.ToSlash(rel) == ".git" {
			return nil, newError(op, CodeInvalidArgument, "", "state directory cannot be inside .git")
		}
		if linkInfo, lstatErr := os.Lstat(stateDir); lstatErr == nil && linkInfo.Mode()&os.ModeSymlink != 0 {
			return nil, newError(op, CodeInvalidArgument, "", "internal state directory cannot be a symlink")
		}
		stateDir, err = sandbox.ValidatePath(rel, root, true)
		if err != nil {
			return nil, wrapError(op, CodeOutsideWorkspace, "", err, "invalid state directory: %v", err)
		}
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, wrapError(op, CodeInternal, "", err, "create state directory: %v", err)
	}
	stateDir, err = filepath.EvalSymlinks(stateDir)
	if err != nil {
		return nil, wrapError(op, CodeInternal, "", err, "resolve state directory: %v", err)
	}

	matcher, err := newIgnoreMatcher(config.Ignore)
	if err != nil {
		return nil, wrapError(op, CodeInvalidArgument, "", err, "invalid ignore pattern: %v", err)
	}
	service := &Service{
		root: root, stateDir: stateDir, gitPath: config.GitExecutable, ignore: matcher,
		maxReadFileBytes: config.MaxReadFileBytes, maxSnapshotFileBytes: config.MaxSnapshotFileBytes,
		maxSnapshotTotalBytes: config.MaxSnapshotTotalBytes, maxSnapshotEntries: config.MaxSnapshotEntries,
		maxGitOutputBytes: config.MaxGitOutputBytes,
	}
	if service.gitPath == "" {
		service.gitPath = "git"
	}
	if service.maxReadFileBytes == 0 {
		service.maxReadFileBytes = defaultReadFileBytes
	}
	if service.maxSnapshotFileBytes == 0 {
		service.maxSnapshotFileBytes = defaultSnapshotFileBytes
	}
	if service.maxSnapshotTotalBytes == 0 {
		service.maxSnapshotTotalBytes = defaultSnapshotTotalByte
	}
	if service.maxSnapshotEntries == 0 {
		service.maxSnapshotEntries = defaultSnapshotEntries
	}
	if service.maxGitOutputBytes == 0 {
		service.maxGitOutputBytes = defaultGitOutputBytes
	}
	if rel, relErr := filepath.Rel(root, stateDir); relErr == nil && !pathEscapes(rel) {
		service.stateRel = filepath.ToSlash(rel)
	}
	return service, nil
}

// Root returns the resolved workspace root.
func (s *Service) Root() string { return s.root }

// StateDir returns the resolved snapshot state directory.
func (s *Service) StateDir() string { return s.stateDir }

// EntryType identifies a filesystem entry without following symlinks.
type EntryType string

const (
	EntryFile      EntryType = "file"
	EntryDirectory EntryType = "directory"
	EntrySymlink   EntryType = "symlink"
	EntryOther     EntryType = "other"
)

// DirectoryEntry describes one immediate child of a directory.
type DirectoryEntry struct {
	Name string    `json:"name"`
	Path string    `json:"path"`
	Type EntryType `json:"type"`
	Mode uint32    `json:"mode"`
	Size int64     `json:"size"`
}

// ListOptions uses a zero-based entry offset. Zero Limit selects 200 entries;
// the maximum is 1000.
type ListOptions struct {
	Offset int
	Limit  int
}

// DirectoryListing is a deterministic, name-sorted directory page.
type DirectoryListing struct {
	Path       string           `json:"path"`
	Entries    []DirectoryEntry `json:"entries"`
	Offset     int              `json:"offset"`
	Limit      int              `json:"limit"`
	Total      int              `json:"total"`
	HasMore    bool             `json:"has_more"`
	NextOffset int              `json:"next_offset,omitempty"`
}

// ListDirectory lists immediate children without following child symlinks.
func (s *Service) ListDirectory(ctx context.Context, requestedPath string, options ListOptions) (DirectoryListing, error) {
	const op = "workspace.list"
	s.mu.RLock()
	defer s.mu.RUnlock()
	if options.Offset < 0 || options.Limit < 0 || options.Limit > maxListEntries {
		return DirectoryListing{}, newError(op, CodeInvalidArgument, requestedPath, "offset must be non-negative and limit must be between 0 and %d", maxListEntries)
	}
	if options.Limit == 0 {
		options.Limit = defaultListEntries
	}
	resolved, relative, err := s.resolve(op, requestedPath, false)
	if err != nil {
		return DirectoryListing{}, err
	}
	if err := contextErr(op, ctx); err != nil {
		return DirectoryListing{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return DirectoryListing{}, filesystemError(op, relative, err)
	}
	if !info.IsDir() {
		return DirectoryListing{}, newError(op, CodeNotDirectory, relative, "not a directory")
	}
	children, err := os.ReadDir(resolved)
	if err != nil {
		return DirectoryListing{}, filesystemError(op, relative, err)
	}
	entries := make([]DirectoryEntry, 0, len(children))
	for _, child := range children {
		childRelative := child.Name()
		if relative != "." {
			childRelative = relative + "/" + child.Name()
		}
		if s.isStatePath(childRelative) {
			continue
		}
		childInfo, infoErr := child.Info()
		if infoErr != nil {
			return DirectoryListing{}, filesystemError(op, childRelative, infoErr)
		}
		entries = append(entries, DirectoryEntry{
			Name: child.Name(), Path: childRelative, Type: entryType(childInfo.Mode()),
			Mode: modeToUnix(childInfo.Mode()), Size: childInfo.Size(),
		})
	}
	total := len(entries)
	start := min(options.Offset, total)
	end := min(start+options.Limit, total)
	page := DirectoryListing{Path: relative, Offset: options.Offset, Limit: options.Limit, Total: total}
	page.Entries = append([]DirectoryEntry(nil), entries[start:end]...)
	if page.Entries == nil {
		page.Entries = []DirectoryEntry{}
	}
	page.HasMore = end < total
	if page.HasMore {
		page.NextOffset = end
	}
	return page, contextErr(op, ctx)
}

// ReadOptions uses a one-based line Offset. Zero values select line 1 and 500
// lines; the maximum page size is 2000 lines.
type ReadOptions struct {
	Offset int
	Limit  int
}

// FilePage contains UTF-8 file content and its SHA256 content revision.
type FilePage struct {
	Path       string `json:"path"`
	Revision   string `json:"revision"`
	Size       int64  `json:"size"`
	Mode       uint32 `json:"mode"`
	Offset     int    `json:"offset"`
	Limit      int    `json:"limit"`
	LineCount  int    `json:"line_count"`
	TotalLines int    `json:"total_lines"`
	Content    string `json:"content"`
	HasMore    bool   `json:"has_more"`
	NextOffset int    `json:"next_offset,omitempty"`
}

// ReadFile reads a page of a UTF-8 regular file. The revision covers the whole
// file, not just the returned page.
func (s *Service) ReadFile(ctx context.Context, requestedPath string, options ReadOptions) (FilePage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readFile(ctx, requestedPath, options)
}

func (s *Service) readFile(ctx context.Context, requestedPath string, options ReadOptions) (FilePage, error) {
	const op = "workspace.read"
	if options.Offset < 0 || options.Limit < 0 || options.Limit > maxReadLines {
		return FilePage{}, newError(op, CodeInvalidArgument, requestedPath, "offset must be non-negative and limit must be between 0 and %d", maxReadLines)
	}
	if options.Offset == 0 {
		options.Offset = 1
	}
	if options.Limit == 0 {
		options.Limit = defaultReadLines
	}
	resolved, relative, err := s.resolve(op, requestedPath, false)
	if err != nil {
		return FilePage{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return FilePage{}, filesystemError(op, relative, err)
	}
	if !info.Mode().IsRegular() {
		return FilePage{}, newError(op, CodeNotRegularFile, relative, "not a regular file")
	}
	if info.Size() > s.maxReadFileBytes {
		return FilePage{}, newError(op, CodeTooLarge, relative, "file size %d exceeds read budget %d", info.Size(), s.maxReadFileBytes)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return FilePage{}, filesystemError(op, relative, err)
	}
	if int64(len(data)) > s.maxReadFileBytes {
		return FilePage{}, newError(op, CodeTooLarge, relative, "file exceeds read budget %d", s.maxReadFileBytes)
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return FilePage{}, newError(op, CodeBinaryFile, relative, "file is not UTF-8 text")
	}
	lines := strings.Split(string(data), "\n")
	start := min(options.Offset-1, len(lines))
	end := min(start+options.Limit, len(lines))
	content := strings.Join(lines[start:end], "\n")
	digest := sha256.Sum256(data)
	page := FilePage{
		Path: relative, Revision: hex.EncodeToString(digest[:]), Size: int64(len(data)), Mode: modeToUnix(info.Mode()),
		Offset: options.Offset, Limit: options.Limit, LineCount: end - start, TotalLines: len(lines), Content: content,
		HasMore: end < len(lines),
	}
	if page.HasMore {
		page.NextOffset = end + 1
	}
	return page, contextErr(op, ctx)
}

func (s *Service) resolve(op, requestedPath string, forWrite bool) (string, string, error) {
	if requestedPath == "" {
		requestedPath = "."
	}
	resolved, err := sandbox.ValidatePath(requestedPath, s.root, forWrite)
	if err != nil {
		code := CodeInvalidArgument
		if strings.Contains(err.Error(), "outside the workspace") {
			code = CodeOutsideWorkspace
		}
		return "", "", wrapError(op, code, filepath.ToSlash(requestedPath), err, "%v", err)
	}
	relative, err := filepath.Rel(s.root, resolved)
	if err != nil || pathEscapes(relative) {
		return "", "", newError(op, CodeOutsideWorkspace, filepath.ToSlash(requestedPath), "path is outside the workspace")
	}
	relative = filepath.ToSlash(relative)
	if s.isStatePath(relative) {
		return "", "", newError(op, CodeInvalidArgument, relative, "snapshot state is private to the workspace service")
	}
	return resolved, relative, nil
}

func (s *Service) isStatePath(relative string) bool {
	return s.stateRel != "" && (relative == s.stateRel || strings.HasPrefix(relative, s.stateRel+"/"))
}

func filesystemError(op, relative string, err error) error {
	code := CodeInternal
	switch {
	case os.IsNotExist(err):
		code = CodeNotFound
	case os.IsPermission(err):
		code = CodeUnsupported
	}
	return wrapError(op, code, relative, err, "%v", err)
}

func contextErr(op string, ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return wrapError(op, CodeCanceled, "", err, "%v", err)
	}
	return nil
}

func entryType(mode fs.FileMode) EntryType {
	switch {
	case mode.IsRegular():
		return EntryFile
	case mode.IsDir():
		return EntryDirectory
	case mode&os.ModeSymlink != 0:
		return EntrySymlink
	default:
		return EntryOther
	}
}

func modeToUnix(mode fs.FileMode) uint32 {
	result := uint32(mode.Perm())
	if mode&os.ModeSetuid != 0 {
		result |= 0o4000
	}
	if mode&os.ModeSetgid != 0 {
		result |= 0o2000
	}
	if mode&os.ModeSticky != 0 {
		result |= 0o1000
	}
	return result
}

func modeFromUnix(mode uint32) fs.FileMode {
	result := fs.FileMode(mode & 0o777)
	if mode&0o4000 != 0 {
		result |= os.ModeSetuid
	}
	if mode&0o2000 != 0 {
		result |= os.ModeSetgid
	}
	if mode&0o1000 != 0 {
		result |= os.ModeSticky
	}
	return result
}

func pathEscapes(relative string) bool {
	return relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative)
}

func pathDepth(relative string) int {
	if relative == "." || relative == "" {
		return 0
	}
	return strings.Count(filepath.ToSlash(relative), "/") + 1
}

func (s *Service) String() string {
	return fmt.Sprintf("Workspace(%s)", s.root)
}
