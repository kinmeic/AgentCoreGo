//go:build windows

package builtin

// isSymlinkError returns false on Windows because O_NOFOLLOW is not available.
// Windows handles symlink errors differently and the TOCTOU window is managed
// by the pre-open and post-open Lstat checks instead.
func isSymlinkError(_ error) bool {
	return false
}
