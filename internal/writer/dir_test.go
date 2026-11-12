package writer_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/shreyaspurohit/sops-sidecar/internal/writer"
)

// canary stands in for secret data; it must never appear in an error.
const canary = "CANARY-7f3a9c-SECRET"

func mustName(t *testing.T, s string) writer.Name {
	t.Helper()
	n, err := writer.ParseName(s)
	if err != nil {
		t.Fatalf("ParseName(%q): %v", s, err)
	}
	return n
}

func openDir(t *testing.T) (d *writer.Dir, path string) {
	t.Helper()
	path = t.TempDir()
	d, err := writer.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d, path
}

func TestWriteByteExact(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"no trailing newline", []byte("abc")},
		{"trailing newline", []byte("abc\n")},
		{"unicode", []byte("pässwörd ✓")},
		{"nul and high bytes", []byte{0, 1, 0xff, 0xfe, 0x80, '\n', 0}},
		{"large", bytes.Repeat([]byte("x"), 1<<20)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, path := openDir(t)
			f := writer.File{Name: mustName(t, "secret"), Data: tt.data, Mode: 0o400, Owner: writer.KeepOwner()}
			if err := d.Write(f); err != nil {
				t.Fatalf("Write: %v", err)
			}
			got, err := os.ReadFile(filepath.Join(path, "secret"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, tt.data) {
				t.Errorf("content mismatch: got %d bytes, want %d", len(got), len(tt.data))
			}
		})
	}
}

func TestWriteModeAndOwner(t *testing.T) {
	tests := []struct {
		name  string
		mode  writer.Mode
		owner writer.Owner
	}{
		{"mode 0400 keep owner", 0o400, writer.KeepOwner()},
		{"mode 0444 keep owner", 0o444, writer.KeepOwner()},
		{"mode 0640 own ids", 0o640, writer.Owner{UID: os.Getuid(), GID: os.Getgid()}},
		{"mode 0000 own uid keep gid", 0o000, writer.Owner{UID: os.Getuid(), GID: writer.KeepID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, path := openDir(t)
			f := writer.File{Name: mustName(t, "s"), Data: []byte("v"), Mode: tt.mode, Owner: tt.owner}
			if err := d.Write(f); err != nil {
				t.Fatalf("Write: %v", err)
			}
			info, err := os.Lstat(filepath.Join(path, "s"))
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != os.FileMode(tt.mode) {
				t.Errorf("mode = %v, want %v", got, os.FileMode(tt.mode))
			}
			if !info.Mode().IsRegular() {
				t.Errorf("not a regular file: %v", info.Mode())
			}
		})
	}
}

func TestWriteReplacesExistingFile(t *testing.T) {
	d, path := openDir(t)
	target := filepath.Join(path, "s")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := writer.File{Name: mustName(t, "s"), Data: []byte("new"), Mode: 0o400, Owner: writer.KeepOwner()}
	if err := d.Write(f); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "new" {
		t.Errorf("content = %q, want new", got)
	}
}

func TestWriteLeavesNoTempFiles(t *testing.T) {
	d, path := openDir(t)
	for _, s := range []string{"a", "b", "a"} {
		f := writer.File{Name: mustName(t, s), Data: []byte(s), Mode: 0o400, Owner: writer.KeepOwner()}
		if err := d.Write(f); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "a,b" {
		t.Errorf("directory entries = %v, want [a b]", names)
	}
}

func TestWriteRefusesNonRegularTarget(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, dir string)
	}{
		{"symlink to file", func(t *testing.T, dir string) {
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("orig"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(dir, "s")); err != nil {
				t.Fatal(err)
			}
		}},
		{"dangling symlink", func(t *testing.T, dir string) {
			if err := os.Symlink("/nonexistent-target", filepath.Join(dir, "s")); err != nil {
				t.Fatal(err)
			}
		}},
		{"directory", func(t *testing.T, dir string) {
			if err := os.Mkdir(filepath.Join(dir, "s"), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, path := openDir(t)
			tt.setup(t, path)
			f := writer.File{Name: mustName(t, "s"), Data: []byte(canary), Mode: 0o400, Owner: writer.KeepOwner()}
			err := d.Write(f)
			if err == nil {
				t.Fatal("Write succeeded, want error")
			}
			if strings.Contains(err.Error(), canary) {
				t.Errorf("error leaks data: %v", err)
			}
			entries, _ := os.ReadDir(path)
			if len(entries) != 1 {
				t.Errorf("temp file left behind: %v", entries)
			}
		})
	}
}

func TestWriteSymlinkTargetOutsideUntouched(t *testing.T) {
	d, path := openDir(t)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("orig"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(path, "s")); err != nil {
		t.Fatal(err)
	}
	_ = d.Write(writer.File{Name: mustName(t, "s"), Data: []byte("new"), Mode: 0o400, Owner: writer.KeepOwner()})
	got, _ := os.ReadFile(outside)
	if string(got) != "orig" {
		t.Errorf("file behind symlink changed to %q", got)
	}
}

func TestWriteRejectsZeroName(t *testing.T) {
	d, _ := openDir(t)
	if err := d.Write(writer.File{Data: []byte(canary)}); err == nil {
		t.Fatal("Write with zero Name succeeded")
	}
}

func TestWriteAfterClose(t *testing.T) {
	d, _ := openDir(t)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	f := writer.File{Name: mustName(t, "s"), Data: []byte(canary), Mode: 0o400, Owner: writer.KeepOwner()}
	err := d.Write(f)
	if err == nil {
		t.Fatal("Write on closed dir succeeded")
	}
	if strings.Contains(err.Error(), canary) {
		t.Errorf("error leaks data: %v", err)
	}
}

func TestWriteBadOwnerCleansUp(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root may chown to any id")
	}
	d, path := openDir(t)
	f := writer.File{Name: mustName(t, "s"), Data: []byte(canary), Mode: 0o400, Owner: writer.Owner{UID: os.Getuid() + 1, GID: writer.KeepID}}
	err := d.Write(f)
	if err == nil {
		t.Fatal("Write succeeded with a uid the process may not chown to")
	}
	if strings.Contains(err.Error(), canary) {
		t.Errorf("error leaks data: %v", err)
	}
	if entries, _ := os.ReadDir(path); len(entries) != 0 {
		t.Errorf("files left behind: %v", entries)
	}
}

func TestRemove(t *testing.T) {
	t.Run("existing file", func(t *testing.T) {
		d, path := openDir(t)
		if err := os.WriteFile(filepath.Join(path, "s"), []byte("v"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := d.Remove(mustName(t, "s")); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(path, "s")); !os.IsNotExist(err) {
			t.Errorf("file still present: %v", err)
		}
	})
	t.Run("absent is ok", func(t *testing.T) {
		d, _ := openDir(t)
		if err := d.Remove(mustName(t, "nope")); err != nil {
			t.Fatalf("Remove absent: %v", err)
		}
	})
	t.Run("directory refused", func(t *testing.T) {
		d, path := openDir(t)
		if err := os.Mkdir(filepath.Join(path, "s"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := d.Remove(mustName(t, "s")); err == nil {
			t.Fatal("Remove of directory succeeded")
		}
	})
	t.Run("symlink refused", func(t *testing.T) {
		d, path := openDir(t)
		if err := os.Symlink("/nonexistent-target", filepath.Join(path, "s")); err != nil {
			t.Fatal(err)
		}
		if err := d.Remove(mustName(t, "s")); err == nil {
			t.Fatal("Remove of symlink succeeded")
		}
	})
	t.Run("zero name refused", func(t *testing.T) {
		d, _ := openDir(t)
		if err := d.Remove(writer.Name{}); err == nil {
			t.Fatal("Remove of zero Name succeeded")
		}
	})
}

func TestOpen(t *testing.T) {
	t.Run("relative path refused", func(t *testing.T) {
		if _, err := writer.Open("relative/dir"); err == nil {
			t.Fatal("Open of relative path succeeded")
		}
	})
	t.Run("missing refused", func(t *testing.T) {
		if _, err := writer.Open(filepath.Join(t.TempDir(), "missing")); err == nil {
			t.Fatal("Open of missing path succeeded")
		}
	})
	t.Run("regular file refused", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "f")
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Open(p); err == nil {
			t.Fatal("Open of regular file succeeded")
		}
	})
	t.Run("symlink refused", func(t *testing.T) {
		base := t.TempDir()
		realDir := filepath.Join(base, "real")
		link := filepath.Join(base, "link")
		if err := os.Mkdir(realDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(realDir, link); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Open(link); err == nil {
			t.Fatal("Open of symlinked dir succeeded")
		}
	})
	t.Run("unclean path accepted", func(t *testing.T) {
		base := t.TempDir()
		d, err := writer.Open(base + "/./")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		_ = d.Close()
	})
}

func TestCheckTmpfs(t *testing.T) {
	t.Run("disk dir refused", func(t *testing.T) {
		// A dir inside the module tree is on the developer's or runner's disk,
		// unlike t.TempDir, which some systems put on tmpfs.
		path, err := os.MkdirTemp(".", "not-tmpfs-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(path) })
		abs, err := filepath.Abs(path)
		if err != nil {
			t.Fatal(err)
		}
		d, err := writer.Open(abs)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.Close() })
		if err := d.CheckTmpfs(); err == nil {
			t.Fatal("CheckTmpfs accepted a directory on disk")
		}
	})
	t.Run("tmpfs accepted", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("tmpfs exists on Linux only")
		}
		d, err := writer.Open("/dev/shm")
		if err != nil {
			t.Skipf("no /dev/shm: %v", err)
		}
		t.Cleanup(func() { _ = d.Close() })
		if err := d.CheckTmpfs(); err != nil {
			t.Fatalf("CheckTmpfs on /dev/shm: %v", err)
		}
	})
}

func ExampleParseMode() {
	m, err := writer.ParseMode("640")
	if err != nil {
		panic(err)
	}
	fmt.Println(m)
	// Output: 0640
}

func TestRemoveTempFiles(t *testing.T) {
	d, path := openDir(t)
	files := map[string]bool{ // name → expected to survive
		writer.ReservedPrefix + "-tmp-0123456789abcdef": false,
		writer.ReservedPrefix + "-tmp-":                 false,
		writer.ReservedPrefix + "-ready":                true,
		"secret":                                        true,
	}
	for name := range files {
		if err := os.WriteFile(filepath.Join(path, name), []byte(canary), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.RemoveTempFiles(); err != nil {
		t.Fatalf("RemoveTempFiles: %v", err)
	}
	for name, survives := range files {
		_, err := os.Lstat(filepath.Join(path, name))
		if exists := err == nil; exists != survives {
			t.Errorf("%s: exists = %v, want %v", name, exists, survives)
		}
	}
}

func TestRemoveTempFilesErrors(t *testing.T) {
	t.Run("closed dir", func(t *testing.T) {
		d, err := writer.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
		if err := d.RemoveTempFiles(); err == nil {
			t.Fatal("RemoveTempFiles on a closed dir succeeded")
		}
	})
	t.Run("temp name held by a non-empty directory", func(t *testing.T) {
		d, path := openDir(t)
		blocked := filepath.Join(path, writer.ReservedPrefix+"-tmp-dir")
		if err := os.MkdirAll(filepath.Join(blocked, "inner"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := d.RemoveTempFiles(); err == nil {
			t.Fatal("RemoveTempFiles succeeded on a non-empty directory")
		}
	})
}

func TestNames(t *testing.T) {
	d, path := openDir(t)
	for _, name := range []string{"a", "b", writer.ReservedPrefix + "-ready", writer.ReservedPrefix + "-tmp-x"} {
		if err := os.WriteFile(filepath.Join(path, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(path, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a", filepath.Join(path, "link")); err != nil {
		t.Fatal(err)
	}
	names, err := d.Names()
	if err != nil {
		t.Fatalf("Names: %v", err)
	}
	var got []string
	for _, n := range names {
		got = append(got, n.String())
	}
	slices.Sort(got)
	if want := []string{"a", "b"}; !slices.Equal(got, want) {
		t.Errorf("Names = %v, want %v", got, want)
	}
}

func TestNamesClosedDir(t *testing.T) {
	d, err := writer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Names(); err == nil {
		t.Fatal("Names on a closed dir succeeded")
	}
}
