package writer

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ProcMounts is the kernel's mount table.
const ProcMounts = "/proc/mounts"

// tmpfsType is the file system type the output dir must have.
const tmpfsType = "tmpfs"

// MountType returns the file system type of the mount whose mount point is
// the longest path-prefix of dir, judged on whole path elements. dir must be
// absolute and clean. When several mounts share a mount point the last one
// listed wins, as it is the one that shadows the others.
func MountType(mounts []byte, dir string) (string, error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return "", fmt.Errorf("directory %q must be absolute and clean", dir)
	}
	best, fsType := -1, ""
	for i, line := range strings.Split(string(mounts), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 3 {
			return "", fmt.Errorf("mount table line %d is malformed", i+1)
		}
		point := unescapeMount(fields[1])
		if len(point) >= best && containsPath(point, dir) {
			best, fsType = len(point), fields[2]
		}
	}
	if best < 0 {
		return "", errors.New("no mount found for directory")
	}
	return fsType, nil
}

// containsPath reports whether dir is point or lies below it.
func containsPath(point, dir string) bool {
	if point == "/" || point == dir {
		return true
	}
	return strings.HasPrefix(dir, point+"/")
}

// unescapeMount decodes the octal escapes (\040, \011, \012, \134) the kernel
// uses for whitespace and backslash in mount points.
func unescapeMount(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) && isOctalEscape(s[i+1:i+4]) {
			b.WriteByte((s[i+1]-'0')<<6 | (s[i+2]-'0')<<3 | (s[i+3] - '0'))
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// isOctalEscape reports whether s is three octal digits that fit in a byte.
func isOctalEscape(s string) bool {
	return strings.Trim(s, "01234567") == "" && s[0] <= '3'
}
