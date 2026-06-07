//go:build !windows

package builtin

import "syscall"

// openFlagsNoFollow adds O_NOFOLLOW to prevent symlink race conditions.
// On Unix systems, this flag causes open() to fail if the final path component
// is a symlink, completely eliminating the TOCTOU window.
const openFlagsNoFollow = syscall.O_NOFOLLOW
