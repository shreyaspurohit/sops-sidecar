package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/getsops/sops/v3"

	"github.com/shreyaspurohit/sops-sidecar/internal/config"
	"github.com/shreyaspurohit/sops-sidecar/internal/health"
	"github.com/shreyaspurohit/sops-sidecar/internal/sopstest"
)

func TestUsage(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout bool
		wantStderr bool
	}{
		{"unknown subcommand", []string{"bogus"}, exitUsage, false, true},
		{"extra args after run", []string{"run", "x"}, exitUsage, false, true},
		{"extra args after version", []string{"version", "--json"}, exitUsage, false, true},
		{"help", []string{"help"}, exitOK, true, false},
		{"-h", []string{"-h"}, exitOK, true, false},
		{"--help", []string{"--help"}, exitOK, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			code, stdout, stderr := f.runCLI(tt.args...)
			if code != tt.wantCode {
				t.Errorf("exit = %d, want %d", code, tt.wantCode)
			}
			if got := strings.Contains(stdout, "usage:"); got != tt.wantStdout {
				t.Errorf("usage on stdout = %v, want %v", got, tt.wantStdout)
			}
			if got := strings.Contains(stderr, "usage:"); got != tt.wantStderr {
				t.Errorf("usage on stderr = %v, want %v", got, tt.wantStderr)
			}
		})
	}
}

func TestVersion(t *testing.T) {
	f := newFixture(t)
	code, stdout, _ := f.runCLI(cmdVersion)
	if code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	if !strings.HasPrefix(stdout, "sops-sidecar ") || !strings.HasSuffix(stdout, "\n") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestCheck(t *testing.T) {
	f := newFixture(t)
	code, stdout, stderr := f.runCLI(cmdCheck)
	if code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if want := "api_token\ndb_password\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	entries, err := os.ReadDir(f.out)
	if err != nil || len(entries) != 0 {
		t.Errorf("check wrote to the output dir: %v, %v", entries, err)
	}
}

func TestCheckJSONAndMap(t *testing.T) {
	f := newFixture(t)
	f.env[config.EnvSOPSFile] = f.writeFile("secrets.sops.json",
		sopstest.EncryptJSON(t, f.recipient, sops.TreeBranch{item("a", canary), item("b", "x"), item("c", "y")}))
	f.env[config.EnvMapFile] = f.writeFile("map.yaml", []byte("files:\n  - key: c\n  - key: a\n    path: renamed\n"))
	code, stdout, stderr := f.runCLI(cmdCheck)
	if code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if want := "a\nc\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestHealth(t *testing.T) {
	f := newFixture(t)
	if code, stdout, stderr := f.runCLI(cmdHealth); code != exitFailure || stdout != "" || stderr == "" {
		t.Errorf("not ready: exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if err := os.WriteFile(filepath.Join(f.out, health.MarkerName), nil, 0o444); err != nil {
		t.Fatal(err)
	}
	if code, stdout, stderr := f.runCLI(cmdHealth); code != exitOK || stdout != "" || stderr != "" {
		t.Errorf("ready: exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
}

// failureCase breaks a valid fixture in one way.
type failureCase struct {
	name string
	// checkFails is false for problems check does not look at (output dir, tmpfs).
	checkFails bool
	mutate     func(t *testing.T, f *fixture)
	want       string
}

func failureCases() []failureCase {
	return []failureCase{
		{"SOPS_AGE_KEY present", true, func(_ *testing.T, f *fixture) { f.env[config.EnvAgeKey] = canary }, "SOPS_AGE_KEY"},
		{"invalid log level", true, func(_ *testing.T, f *fixture) { f.env[config.EnvLogLevel] = "loud" }, "config failed"},
		{"missing SOPS file", true, func(t *testing.T, f *fixture) {
			if err := os.Remove(f.path("secrets.sops.yaml")); err != nil {
				t.Fatal(err)
			}
		}, "read SOPS file"},
		{"missing age key file", true, func(t *testing.T, f *fixture) {
			if err := os.Remove(f.path("age.key")); err != nil {
				t.Fatal(err)
			}
		}, "read age key file"},
		{"SOPS file is a FIFO", true, func(t *testing.T, f *fixture) {
			fifo := f.path("fifo")
			if err := syscall.Mkfifo(fifo, 0o600); err != nil {
				t.Fatal(err)
			}
			f.env[config.EnvSOPSFile] = fifo
		}, "not a regular file"},
		{"age key file is a directory", true, func(t *testing.T, f *fixture) {
			dir := f.path("keydir")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			f.env[config.EnvAgeKeyFile] = dir
		}, "not a regular file"},
		{"oversize SOPS file", true, func(_ *testing.T, f *fixture) {
			f.writeFile("secrets.sops.yaml", bytes.Repeat([]byte("a"), maxInputBytes+1))
		}, "larger than"},
		{"oversize age key file", true, func(_ *testing.T, f *fixture) {
			f.writeFile("age.key", bytes.Repeat([]byte("a"), maxInputBytes+1))
		}, "larger than"},
		{"garbage age key file", true, func(_ *testing.T, f *fixture) { f.writeFile("age.key", []byte("not a key")) }, "age key file"},
		{"wrong age key", true, func(t *testing.T, f *fixture) {
			other, _ := sopstest.NewIdentity(t)
			f.writeFile("age.key", other)
		}, "decrypt SOPS file"},
		{"garbage SOPS file", true, func(_ *testing.T, f *fixture) { f.writeFile("secrets.sops.yaml", []byte("{{{")) }, "decrypt SOPS file"},
		{"nested key", true, func(_ *testing.T, f *fixture) {
			f.encrypt(item("db", sops.TreeBranch{item("password", canary)}))
		}, "decrypt SOPS file"},
		{"required map file missing", true, func(_ *testing.T, f *fixture) {
			f.env[config.EnvMapFile] = f.path("missing.yaml")
		}, "read map file"},
		{"invalid map file", true, func(_ *testing.T, f *fixture) {
			f.env[config.EnvMapFile] = f.writeFile("map.yaml", []byte("files: []\n"))
		}, "map file"},
		{"oversize map file", true, func(_ *testing.T, f *fixture) {
			f.env[config.EnvMapFile] = f.writeFile("map.yaml", bytes.Repeat([]byte("#"), maxInputBytes+1))
		}, "larger than"},
		{"map lists absent key", true, func(_ *testing.T, f *fixture) {
			f.env[config.EnvMapFile] = f.writeFile("map.yaml", []byte("files:\n  - key: nope\n"))
		}, "nope"},
		{"tmpfs refused", false, func(_ *testing.T, f *fixture) { delete(f.env, config.EnvAllowNonTmpfs) }, "startup failed"},
		{"output dir missing", false, func(_ *testing.T, f *fixture) { f.env[config.EnvOutputDir] = f.path("absent") }, "startup failed"},
	}
}

func TestRunFailures(t *testing.T) {
	for _, tt := range failureCases() {
		for _, level := range []string{"info", "debug"} {
			t.Run(tt.name+"/"+level, func(t *testing.T) {
				f := newFixture(t)
				f.env[config.EnvLogLevel] = level
				tt.mutate(t, f)
				code, stdout, stderr := f.runCLI(cmdRun)
				if code != exitFailure {
					t.Fatalf("exit = %d, want %d; stderr = %s", code, exitFailure, stderr)
				}
				if stdout != "" {
					t.Errorf("stdout = %q", stdout)
				}
				if !strings.Contains(stderr, tt.want) {
					t.Errorf("stderr = %q, want it to contain %q", stderr, tt.want)
				}
				if _, err := os.Stat(filepath.Join(f.out, health.MarkerName)); err == nil {
					t.Error("ready marker exists after a failed run")
				}
			})
		}
	}
}

func TestCheckFailures(t *testing.T) {
	for _, tt := range failureCases() {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			tt.mutate(t, f)
			code, stdout, stderr := f.runCLI(cmdCheck)
			if !tt.checkFails {
				if code != exitOK {
					t.Fatalf("exit = %d, stderr = %s", code, stderr)
				}
				return
			}
			if code != exitFailure || stdout != "" {
				t.Fatalf("exit = %d, stdout = %q, stderr = %s", code, stdout, stderr)
			}
			if !strings.Contains(stderr, tt.want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, tt.want)
			}
		})
	}
}

func TestHealthConfigError(t *testing.T) {
	f := newFixture(t)
	f.env[config.EnvAgeKey] = canary
	code, _, stderr := f.runCLI(cmdHealth)
	if code != exitFailure || !strings.Contains(stderr, "config failed") {
		t.Errorf("exit = %d, stderr = %s", code, stderr)
	}
}
