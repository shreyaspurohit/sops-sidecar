package writer_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
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

func openDir(t *testing.T) (*writer.Dir, string) {
	t.Helper()
	path := t.TempDir()
	d, err := writer.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d, path
}

func keep() writer.Owner { return writer.Owner{UID: writer.KeepID, GID: writer.KeepID} }

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
			f := writer.File{Name: mustName(t, "secret"), Data: tt.data, Mode: 0o400, Owner: keep()}
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
		{"mode 0400 keep owner", 0o400, keep()},
		{"mode 0444 keep owner", 0o444, keep()},
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
	f := writer.File{Name: mustName(t, "s"), Data: []byte("new"), Mode: 0o400, Owner: keep()}
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
		f := writer.File{Name: mustName(t, s), Data: []byte(s), Mode: 0o400, Owner: keep()}
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
			f := writer.File{Name: mustName(t, "s"), Data: []byte(canary), Mode: 0o400, Owner: keep()}
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
	_ = d.Write(writer.File{Name: mustName(t, "s"), Data: []byte("new"), Mode: 0o400, Owner: keep()})
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
	f := writer.File{Name: mustName(t, "s"), Data: []byte(canary), Mode: 0o400, Owner: keep()}
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
		real := filepath.Join(base, "real")
		link := filepath.Join(base, "link")
		if err := os.Mkdir(real, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, link); err != nil {
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
	d, path := openDir(t)
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	write := func(t *testing.T, content string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "mounts")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	tests := []struct {
		name    string
		mounts  func(t *testing.T) string
		wantErr bool
	}{
		{"on tmpfs", func(t *testing.T) string { return write(t, "tmpfs "+resolved+" tmpfs rw 0 0\n") }, false},
		{"on overlay root", func(t *testing.T) string { return write(t, "overlay / overlay rw 0 0\n") }, true},
		{"tmpfs ancestor overridden by ext4", func(t *testing.T) string {
			return write(t, "tmpfs / tmpfs rw 0 0\next4 "+resolved+" ext4 rw 0 0\n")
		}, true},
		{"no matching line", func(t *testing.T) string { return write(t, "tmpfs /elsewhere tmpfs rw 0 0\n") }, true},
		{"unreadable mounts file", func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent") }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := d.CheckTmpfs(tt.mounts(t))
			if (err != nil) != tt.wantErr {
				t.Fatalf("CheckTmpfs error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func ExampleParseMode() {
	m, err := writer.ParseMode("640")
	if err != nil {
		panic(err)
	}
	fmt.Println(m)
	// Output: 0640
}
