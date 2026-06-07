//go:build !windows

package builtin

import (
	"errors"
	"syscall"
)

// isSymlinkError returns true if the error is ELOOP, which indicates
// that O_NOFOLLOW rejected a symlink at the kernel level.
func isSymlinkError(err error) bool {
	return errors.Is(err, syscall.ELOOP)
}
