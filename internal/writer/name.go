// Package writer owns the output dir: file names, modes, owners, atomic
// writes, removal, and the tmpfs check.
package writer

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ReservedPrefix starts the name of every file the sidecar creates for its
// own bookkeeping. Secret files can never use it, so the two cannot collide.
const ReservedPrefix = ".sops-sidecar"

// DefaultMode is the mode of a file whose map entry and defaults set none.
const DefaultMode Mode = 0o400

// KeepID as a UID or GID leaves that id unchanged (the sidecar's own id).
const KeepID = -1

// MaxID is the largest uid or gid a file can be given. Linux ids are 32-bit
// and 2^32-1 means "unchanged" to chown, so it is excluded.
const MaxID = 1<<32 - 2

// maxNameLen is the longest file name common file systems accept.
const maxNameLen = 255

// Name is a validated single-element file name. The zero value is invalid.
type Name struct{ s string }

// ParseName validates a secret's file name: a single path element that does
// not start with ReservedPrefix.
func ParseName(s string) (Name, error) {
	n, err := parseName(s)
	if err != nil {
		return Name{}, err
	}
	if strings.HasPrefix(s, ReservedPrefix) {
		return Name{}, fmt.Errorf("file name %q: prefix %q is reserved", s, ReservedPrefix)
	}
	return n, nil
}

// ParseReservedName validates a name for a file the sidecar creates itself:
// the same rules as ParseName, but the name must start with ReservedPrefix.
func ParseReservedName(s string) (Name, error) {
	n, err := parseName(s)
	if err != nil {
		return Name{}, err
	}
	if !strings.HasPrefix(s, ReservedPrefix) {
		return Name{}, fmt.Errorf("file name %q: must start with %q", s, ReservedPrefix)
	}
	return n, nil
}

func parseName(s string) (Name, error) {
	switch {
	case s == "":
		return Name{}, errors.New("file name is empty")
	case s == "." || s == "..":
		return Name{}, fmt.Errorf("file name %q is not allowed", s)
	case len(s) > maxNameLen:
		return Name{}, fmt.Errorf("file name is longer than %d bytes", maxNameLen)
	case strings.ContainsAny(s, "/\\\x00"):
		return Name{}, fmt.Errorf("file name %q must be a single path element without '/', '\\' or NUL", s)
	}
	return Name{s: s}, nil
}

// String returns the file name.
func (n Name) String() string { return n.s }

// Mode holds permission bits only (at most 0o777).
type Mode uint32

// ParseMode parses octal permission text of three digits with an optional
// leading zero, such as "0400" or "400".
func ParseMode(s string) (Mode, error) {
	digits := s
	if len(s) == 4 && s[0] == '0' {
		digits = s[1:]
	}
	if len(digits) != 3 || strings.Trim(digits, "01234567") != "" {
		return 0, fmt.Errorf("mode %q: want three octal digits such as 0400", s)
	}
	v, err := strconv.ParseUint(digits, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("mode %q: want three octal digits such as 0400", s)
	}
	return Mode(v), nil
}

// String formats the mode as four octal digits, such as "0400".
func (m Mode) String() string { return fmt.Sprintf("%04o", uint32(m)) }

// Owner is the uid and gid a file is chowned to. KeepID leaves an id unchanged.
type Owner struct {
	// UID is the owning user id, or KeepID.
	UID int
	// GID is the owning group id, or KeepID.
	GID int
}

// KeepOwner returns the owner that leaves both ids unchanged: the built-in
// default, which needs no CHOWN capability.
func KeepOwner() Owner { return Owner{UID: KeepID, GID: KeepID} }

// File is one file to write into the output dir.
type File struct {
	// Name is the file's name in the output dir.
	Name Name
	// Data is the file's content, written byte-exact.
	Data []byte
	// Mode is the file's permission bits.
	Mode Mode
	// Owner is the uid and gid the file is chowned to.
	Owner Owner
}

// Stale returns the names in prev that are absent from next, in prev's order.
func Stale(prev, next []Name) []Name {
	keep := make(map[Name]struct{}, len(next))
	for _, n := range next {
		keep[n] = struct{}{}
	}
	var stale []Name
	for _, n := range prev {
		if _, ok := keep[n]; !ok {
			stale = append(stale, n)
		}
	}
	return stale
}
