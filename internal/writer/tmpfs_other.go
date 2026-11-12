//go:build !linux

package writer

import (
	"errors"
	"os"
)

// isTmpfs always errors: the output dir is only ever checked on Linux.
func isTmpfs(*os.File) (bool, error) {
	return false, errors.New("the tmpfs check is supported on Linux only")
}
