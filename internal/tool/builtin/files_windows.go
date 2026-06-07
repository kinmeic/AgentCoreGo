//go:build windows

package builtin

// openFlagsNoFollow is 0 on Windows because O_NOFOLLOW is not available.
// Windows handles symlinks differently and the TOCTOU window is less critical
// due to different filesystem semantics.
const openFlagsNoFollow = 0
