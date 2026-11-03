package writer

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// tempFileMode is the mode of a file while its content is being written,
// before the final mode applies.
const tempFileMode fs.FileMode = 0o600

// tempRandomBytes is the number of random bytes in a temp file name.
const tempRandomBytes = 8

// Dir is an open output dir. Every operation is confined to it by the
// kernel, so symlinks and ".." cannot lead out of it.
type Dir struct {
	root *os.Root
	path string
}

// Open opens the output dir at path, which must be absolute. The final path
// element must be a real directory, not a symlink.
func Open(path string) (*Dir, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("output dir %q must be an absolute path", path)
	}
	clean := filepath.Clean(path)
	info, err := os.Lstat(clean)
	if err != nil {
		return nil, fmt.Errorf("stat output dir: %w", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("output dir %q is a symlink", path)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("output dir %q is not a directory", path)
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return nil, fmt.Errorf("resolve output dir: %w", err)
	}
	root, err := os.OpenRoot(resolved)
	if err != nil {
		return nil, fmt.Errorf("open output dir: %w", err)
	}
	return &Dir{root: root, path: resolved}, nil
}

// Close releases the output dir.
func (d *Dir) Close() error { return d.root.Close() }

// CheckTmpfs errors unless the output dir lies on a tmpfs according to the
// mount table in mountsFile.
func (d *Dir) CheckTmpfs(mountsFile string) error {
	mounts, err := os.ReadFile(mountsFile) //nolint:gosec // the mount table path is a trusted setting, not request input
	if err != nil {
		return fmt.Errorf("read mount table: %w", err)
	}
	fsType, err := MountType(mounts, d.path)
	if err != nil {
		return fmt.Errorf("output dir %q: %w", d.path, err)
	}
	if fsType != "tmpfs" {
		return fmt.Errorf("output dir %q is on %s, not tmpfs", d.path, fsType)
	}
	return nil
}

// Write atomically replaces the file f.Name with f. Readers see either the
// previous content or the complete new content, never a partial file. On
// error the previous file, if any, is untouched and no temp file remains.
func (d *Dir) Write(f File) error {
	if f.Name == (Name{}) {
		return errors.New("write: file name is invalid")
	}
	tmp, err := tempName()
	if err != nil {
		return fmt.Errorf("write %s: %w", f.Name, err)
	}
	if err := d.writeTemp(tmp, f); err != nil {
		return fmt.Errorf("write %s: %w", f.Name, err)
	}
	if err := d.refuseNonRegular(f.Name); err != nil {
		return d.discard(tmp, fmt.Errorf("write: %w", err))
	}
	if err := d.root.Rename(tmp, f.Name.s); err != nil {
		return d.discard(tmp, fmt.Errorf("write %s: rename: %w", f.Name, err))
	}
	return nil
}

// writeTemp creates the temp file with exclusive access, fills it, and sets
// its final mode and owner, removing it again on any failure.
func (d *Dir) writeTemp(tmp string, f File) error {
	h, err := d.root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, tempFileMode)
	if err != nil {
		return err
	}
	if err := fill(h, f); err != nil {
		return d.discard(tmp, err)
	}
	return nil
}

// fill writes, syncs, sets mode and owner, and closes h. It closes h even on error.
func fill(h *os.File, f File) error {
	err := writeAll(h, f)
	if cerr := h.Close(); err == nil {
		err = cerr
	}
	return err
}

func writeAll(h *os.File, f File) error {
	if _, err := h.Write(f.Data); err != nil {
		return err
	}
	if err := h.Sync(); err != nil {
		return err
	}
	if err := h.Chmod(fs.FileMode(f.Mode)); err != nil {
		return err
	}
	return h.Chown(f.Owner.UID, f.Owner.GID)
}

// discard removes the temp file and returns cause.
func (d *Dir) discard(tmp string, cause error) error {
	if err := d.root.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errors.Join(cause, fmt.Errorf("remove temp file: %w", err))
	}
	return cause
}

// refuseNonRegular errors when name exists and is not a regular file.
func (d *Dir) refuseNonRegular(n Name) error {
	info, err := d.root.Lstat(n.s)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", n, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s exists and is not a regular file", n)
	}
	return nil
}

// Remove deletes the file n. It succeeds when n is already absent and refuses
// to remove anything that is not a regular file.
func (d *Dir) Remove(n Name) error {
	if n == (Name{}) {
		return errors.New("remove: file name is invalid")
	}
	if err := d.refuseNonRegular(n); err != nil {
		return fmt.Errorf("remove: %w", err)
	}
	if err := d.root.Remove(n.s); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", n, err)
	}
	return nil
}

// tempName returns an unpredictable name reserved for the sidecar's temp files.
func tempName() (string, error) {
	b := make([]byte, tempRandomBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random temp name: %w", err)
	}
	return ReservedPrefix + "-tmp-" + hex.EncodeToString(b), nil
}
