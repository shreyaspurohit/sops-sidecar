//go:build linux

package writer

import (
	"os"

	"golang.org/x/sys/unix"
)

// isTmpfs reports whether the open file f lies on a tmpfs, asking the kernel
// for the file system of f itself rather than matching paths.
func isTmpfs(f *os.File) (bool, error) {
	var st unix.Statfs_t
	if err := unix.Fstatfs(int(f.Fd()), &st); err != nil {
		return false, err
	}
	return int64(st.Type) == unix.TMPFS_MAGIC, nil //nolint:unconvert // Statfs_t.Type is not int64 on every Linux architecture
}
