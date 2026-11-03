package health_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shreyaspurohit/sops-sidecar/internal/health"
	"github.com/shreyaspurohit/sops-sidecar/internal/writer"
)

func newMarker(t *testing.T) (health.Marker, string) {
	t.Helper()
	dir := t.TempDir()
	d, err := writer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	m, err := health.NewMarker(d)
	if err != nil {
		t.Fatal(err)
	}
	return m, dir
}

func TestMarkerLifecycle(t *testing.T) {
	m, dir := newMarker(t)
	if err := health.Check(dir); err == nil {
		t.Fatal("Check before Set succeeded")
	}
	if err := m.Set(); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := health.Check(dir); err != nil {
		t.Fatalf("Check after Set: %v", err)
	}
	if err := m.Set(); err != nil {
		t.Fatalf("second Set: %v", err)
	}
	if err := m.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if err := health.Check(dir); err == nil {
		t.Fatal("Check after Clear succeeded")
	}
	if err := m.Clear(); err != nil {
		t.Fatalf("Clear of absent marker: %v", err)
	}
}

func TestSetWritesEmptyReadableFile(t *testing.T) {
	m, dir := newMarker(t)
	if err := m.Set(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, health.MarkerName)
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Size() != 0 {
		t.Errorf("marker mode %v size %d, want empty regular file", info.Mode(), info.Size())
	}
	if got := info.Mode().Perm(); got != os.FileMode(health.MarkerMode) {
		t.Errorf("marker perm = %v, want %v", got, os.FileMode(health.MarkerMode))
	}
	if health.MarkerName != ".sops-sidecar-ready" {
		t.Errorf("MarkerName = %q", health.MarkerName)
	}
}

func TestSetFailsAfterDirClosed(t *testing.T) {
	dir := t.TempDir()
	d, err := writer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := health.NewMarker(d)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := m.Set(); err == nil {
		t.Error("Set on a closed dir succeeded")
	}
	if err := m.Clear(); err == nil {
		t.Error("Clear on a closed dir succeeded")
	}
}

func TestCheckRejectsNonRegularMarker(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{"symlink to regular file", func(t *testing.T, path string) {
			target := filepath.Join(filepath.Dir(path), "target")
			if err := os.WriteFile(target, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
		{"dangling symlink", func(t *testing.T, path string) {
			if err := os.Symlink("/nonexistent-target", path); err != nil {
				t.Fatal(err)
			}
		}},
		{"directory", func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.setup(t, filepath.Join(dir, health.MarkerName))
			if err := health.Check(dir); err == nil {
				t.Error("Check succeeded, want error")
			}
		})
	}
}

func TestCheckMissingDir(t *testing.T) {
	if err := health.Check(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("Check of a missing dir succeeded")
	}
}

func TestClearRefusesNonRegularMarker(t *testing.T) {
	m, dir := newMarker(t)
	if err := os.Mkdir(filepath.Join(dir, health.MarkerName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := m.Clear(); err == nil {
		t.Error("Clear removed a directory")
	}
}
