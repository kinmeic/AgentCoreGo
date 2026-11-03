package telemetry

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ArtifactRef identifies the bytes actually stored by their SHA-256 digest.
// Size is the original stream size and StoredSize is the retained size; they
// differ when Truncated is true.
type ArtifactRef struct {
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	StoredSize  int64  `json:"stored_size"`
	ContentType string `json:"content_type"`
	Truncated   bool   `json:"truncated"`
}

// StoragePath returns the portable path relative to an artifact store root.
func (r ArtifactRef) StoragePath() (string, error) {
	if err := validateDigest(r.SHA256); err != nil {
		return "", err
	}
	return filepath.ToSlash(filepath.Join("sha256", r.SHA256[:2], r.SHA256[2:4], r.SHA256)), nil
}

func validateDigest(digest string) error {
	if len(digest) != sha256.Size*2 {
		return fmt.Errorf("telemetry: invalid SHA-256 digest length")
	}
	if strings.ToLower(digest) != digest {
		return fmt.Errorf("telemetry: SHA-256 digest must be lowercase")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return fmt.Errorf("telemetry: invalid SHA-256 digest: %w", err)
	}
	return nil
}

// ArtifactStore is a private SHA-256 content-addressed store. All directories
// are forced to 0700 and files to 0600, including existing store directories.
type ArtifactStore struct {
	root     string
	maxBytes int64
}

// NewArtifactStore opens or creates a store at root. maxBytes limits each new
// artifact; zero means unlimited and negative values are invalid.
func NewArtifactStore(root string, maxBytes int64) (*ArtifactStore, error) {
	if root == "" {
		return nil, fmt.Errorf("telemetry: artifact store root is required")
	}
	if maxBytes < 0 {
		return nil, fmt.Errorf("telemetry: artifact byte limit cannot be negative")
	}
	if err := secureMkdirAll(root); err != nil {
		return nil, err
	}
	if err := secureMkdirAll(filepath.Join(root, "tmp")); err != nil {
		return nil, err
	}
	return &ArtifactStore{root: root, maxBytes: maxBytes}, nil
}

func secureMkdirAll(path string) error {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("telemetry: refusing symlink directory %q", path)
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("telemetry: inspect directory %q: %w", path, err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("telemetry: create private directory %q: %w", path, err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("telemetry: secure directory %q: %w", path, err)
	}
	return nil
}

// Root returns the store's private filesystem root.
func (s *ArtifactStore) Root() string { return s.root }

// Put stores data and returns its content address. If a limit is configured,
// the retained prefix is addressed and the truncation is explicit in the ref.
func (s *ArtifactStore) Put(contentType string, data []byte) (ArtifactRef, error) {
	writer, err := s.newWriter(contentType)
	if err != nil {
		return ArtifactRef{}, err
	}
	if _, err := writer.Write(data); err != nil {
		_ = writer.Abort()
		return ArtifactRef{}, err
	}
	return writer.Close()
}

// Open opens a retained artifact after validating its address and size.
// Callers that need tamper detection should use Verify or verify while copying.
func (s *ArtifactStore) Open(ref ArtifactRef) (*os.File, error) {
	path, err := s.path(ref)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("telemetry: open artifact %s: %w", ref.SHA256, err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("telemetry: stat artifact %s: %w", ref.SHA256, err)
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("telemetry: artifact %s is not a regular file", ref.SHA256)
	}
	if ref.StoredSize >= 0 && info.Size() != ref.StoredSize {
		file.Close()
		return nil, fmt.Errorf("telemetry: artifact %s size mismatch: got %d, want %d", ref.SHA256, info.Size(), ref.StoredSize)
	}
	return file, nil
}

// Verify checks the digest and both size fields of an artifact.
func (s *ArtifactStore) Verify(ref ArtifactRef) error {
	if ref.Size < 0 || ref.StoredSize < 0 || ref.Size < ref.StoredSize {
		return fmt.Errorf("telemetry: invalid artifact sizes for %s", ref.SHA256)
	}
	if ref.Truncated != (ref.Size > ref.StoredSize) {
		return fmt.Errorf("telemetry: inconsistent truncation marker for %s", ref.SHA256)
	}
	file, err := s.Open(ref)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	written, err := io.Copy(hash, file)
	if err != nil {
		return fmt.Errorf("telemetry: hash artifact %s: %w", ref.SHA256, err)
	}
	if written != ref.StoredSize || hex.EncodeToString(hash.Sum(nil)) != ref.SHA256 {
		return fmt.Errorf("telemetry: artifact %s failed content verification", ref.SHA256)
	}
	return nil
}

func (s *ArtifactStore) path(ref ArtifactRef) (string, error) {
	relative, err := ref.StoragePath()
	if err != nil {
		return "", err
	}
	return filepath.Join(s.root, filepath.FromSlash(relative)), nil
}

type artifactWriter struct {
	store       *ArtifactStore
	file        *os.File
	tempPath    string
	hash        hash.Hash
	contentType string
	totalSize   int64
	storedSize  int64
	truncated   bool
	closed      bool
	result      ArtifactRef
	closeErr    error
}

func (s *ArtifactStore) newWriter(contentType string) (*artifactWriter, error) {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	file, err := os.CreateTemp(filepath.Join(s.root, "tmp"), "capture-*")
	if err != nil {
		return nil, fmt.Errorf("telemetry: create artifact temporary file: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		os.Remove(file.Name())
		return nil, fmt.Errorf("telemetry: secure artifact temporary file: %w", err)
	}
	return &artifactWriter{
		store:       s,
		file:        file,
		tempPath:    file.Name(),
		hash:        sha256.New(),
		contentType: contentType,
	}, nil
}

func (w *artifactWriter) Write(data []byte) (int, error) {
	if w.closed {
		return 0, fmt.Errorf("telemetry: artifact writer is closed")
	}
	originalLength := len(data)
	w.totalSize += int64(originalLength)
	remaining := originalLength
	if w.store.maxBytes > 0 {
		remaining64 := w.store.maxBytes - w.storedSize
		if remaining64 <= 0 {
			remaining = 0
		} else if int64(remaining) > remaining64 {
			remaining = int(remaining64)
		}
	}
	if remaining != originalLength {
		w.truncated = true
	}
	if remaining > 0 {
		chunk := data[:remaining]
		n, err := w.file.Write(chunk)
		w.storedSize += int64(n)
		if n > 0 {
			_, _ = w.hash.Write(chunk[:n])
		}
		if err != nil {
			return originalLength, fmt.Errorf("telemetry: write artifact: %w", err)
		}
		if n != len(chunk) {
			return originalLength, fmt.Errorf("telemetry: write artifact: %w", io.ErrShortWrite)
		}
	}
	return originalLength, nil
}

func (w *artifactWriter) Close() (ArtifactRef, error) {
	if w.closed {
		return w.result, w.closeErr
	}
	w.closed = true
	fail := func(err error) (ArtifactRef, error) {
		w.closeErr = err
		_ = w.file.Close()
		_ = os.Remove(w.tempPath)
		return ArtifactRef{}, err
	}
	if err := w.file.Sync(); err != nil {
		return fail(fmt.Errorf("telemetry: sync artifact: %w", err))
	}
	if err := w.file.Close(); err != nil {
		_ = os.Remove(w.tempPath)
		return fail(fmt.Errorf("telemetry: close artifact: %w", err))
	}
	digest := hex.EncodeToString(w.hash.Sum(nil))
	ref := ArtifactRef{
		SHA256:      digest,
		Size:        w.totalSize,
		StoredSize:  w.storedSize,
		ContentType: w.contentType,
		Truncated:   w.truncated,
	}
	path, err := w.store.path(ref)
	if err != nil {
		_ = os.Remove(w.tempPath)
		return fail(err)
	}
	if err := secureMkdirAll(filepath.Dir(path)); err != nil {
		_ = os.Remove(w.tempPath)
		return fail(err)
	}
	if info, err := os.Stat(path); err == nil {
		if !info.Mode().IsRegular() || info.Size() != ref.StoredSize {
			_ = os.Remove(w.tempPath)
			return fail(fmt.Errorf("telemetry: existing artifact %s is invalid", digest))
		}
		if err := os.Chmod(path, 0o600); err != nil {
			_ = os.Remove(w.tempPath)
			return fail(fmt.Errorf("telemetry: secure existing artifact: %w", err))
		}
		if err := os.Remove(w.tempPath); err != nil {
			return fail(fmt.Errorf("telemetry: remove duplicate temporary artifact: %w", err))
		}
	} else if os.IsNotExist(err) {
		if err := os.Rename(w.tempPath, path); err != nil {
			_ = os.Remove(w.tempPath)
			return fail(fmt.Errorf("telemetry: commit artifact: %w", err))
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return fail(fmt.Errorf("telemetry: secure artifact: %w", err))
		}
	} else {
		_ = os.Remove(w.tempPath)
		return fail(fmt.Errorf("telemetry: inspect committed artifact: %w", err))
	}
	w.result = ref
	return ref, nil
}

// Abort removes uncommitted bytes. It is safe to call after Close.
func (w *artifactWriter) Abort() error {
	if !w.closed {
		w.closed = true
		if err := w.file.Close(); err != nil {
			_ = os.Remove(w.tempPath)
			return err
		}
	}
	if w.result.SHA256 != "" {
		return nil
	}
	if err := os.Remove(w.tempPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
