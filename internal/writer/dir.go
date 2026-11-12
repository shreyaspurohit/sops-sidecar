package writer

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// tempFileMode is the mode of a file while its content is being written,
// before the final mode applies.
const tempFileMode fs.FileMode = 0o600

// tempPrefix starts the name of every temp file.
const tempPrefix = ReservedPrefix + "-tmp-"

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

// CheckTmpfs errors unless the open output dir itself lies on a tmpfs, as
// reported by the kernel (fstatfs).
func (d *Dir) CheckTmpfs() error {
	f, err := d.root.Open(".")
	if err != nil {
		return fmt.Errorf("open output dir: %w", err)
	}
	ok, err := isTmpfs(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("output dir %q: file system type: %w", d.path, err)
	}
	if !ok {
		return fmt.Errorf("output dir %q is not on tmpfs", d.path)
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

// writeTemp leaves either a complete temp file with its final mode and owner,
// or no temp file at all.
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

// fill guarantees h is closed, and on success that its content is durable and
// its mode and owner are final.
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
	// Chown before chmod: the final mode's group bits must apply only once
	// the final group owns the file.
	if err := h.Chown(f.Owner.UID, f.Owner.GID); err != nil {
		return err
	}
	return h.Chmod(fs.FileMode(f.Mode))
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

// RemoveTempFiles deletes temp files a previous run left behind when it was
// killed mid-write. They may hold secret values and no later write would
// replace them; names under ReservedPrefix belong only to the sidecar.
func (d *Dir) RemoveTempFiles() error {
	names, err := d.entries()
	if err != nil {
		return err
	}
	for _, n := range names {
		if !strings.HasPrefix(n, tempPrefix) {
			continue
		}
		if err := d.root.Remove(n); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove leftover temp file: %w", err)
		}
	}
	return nil
}

// Names returns the regular files in the output dir whose names ParseName
// accepts: the secret files any run may have written. Reserved files,
// directories, symlinks, and other entries are left out.
func (d *Dir) Names() ([]Name, error) {
	entries, err := d.entries()
	if err != nil {
		return nil, err
	}
	var names []Name
	for _, e := range entries {
		n, err := ParseName(e)
		if err != nil {
			continue
		}
		info, err := d.root.Lstat(e)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", n, err)
		}
		if info.Mode().IsRegular() {
			names = append(names, n)
		}
	}
	return names, nil
}

// entries returns the names of all entries in the output dir.
func (d *Dir) entries() ([]string, error) {
	dir, err := d.root.Open(".")
	if err != nil {
		return nil, fmt.Errorf("list output dir: %w", err)
	}
	names, err := dir.Readdirnames(-1)
	if cerr := dir.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, fmt.Errorf("list output dir: %w", err)
	}
	return names, nil
}

// tempName returns an unpredictable name reserved for the sidecar's temp files.
func tempName() (string, error) {
	b := make([]byte, tempRandomBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random temp name: %w", err)
	}
	return tempPrefix + hex.EncodeToString(b), nil
}
