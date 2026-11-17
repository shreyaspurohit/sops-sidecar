//go:build !linux

package writer

import (
	"errors"
	"os"
)

// isTmpfs always errors: the file system type comes from Linux fstatfs.
func isTmpfs(*os.File) (bool, error) {
	return false, errors.New("the tmpfs check is supported on Linux only")
}
