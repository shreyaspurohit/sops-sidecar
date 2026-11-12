package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/shreyaspurohit/sops-sidecar/internal/config"
	"github.com/shreyaspurohit/sops-sidecar/internal/health"
	"github.com/shreyaspurohit/sops-sidecar/internal/sopstest"
)

// child is the test binary re-executed as the real sops-sidecar process.
type child struct {
	cmd    *exec.Cmd
	stdout syncBuffer
	stderr syncBuffer
}

// spawn starts the real main() in a subprocess with only the fixture's
// environment, so sops' own output is captured too.
func (f *fixture) spawn(args ...string) *child {
	f.t.Helper()
	c := &child{cmd: exec.Command(os.Args[0], args...)}
	// A coverage-instrumented test binary warns on stderr when GOCOVERDIR is
	// unset; give the child a scratch dir so stderr holds only the logs.
	c.cmd.Env = []string{testMainEnv + "=1", "GOCOVERDIR=" + f.t.TempDir()}
	for k, v := range f.env {
		c.cmd.Env = append(c.cmd.Env, k+"="+v)
	}
	c.cmd.Stdout = &c.stdout
	c.cmd.Stderr = &c.stderr
	if err := c.cmd.Start(); err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = c.cmd.Process.Kill() })
	return c
}

// wait returns the child's exit code and fails the test if the canary leaked.
func (c *child) wait(t *testing.T) int {
	t.Helper()
	err := c.cmd.Wait()
	noCanary(t, c.stdout.String(), c.stderr.String())
	if err == nil {
		return exitOK
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatal(err)
	}
	return exitErr.ExitCode()
}

func TestSubprocessSubcommands(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		mutate   func(t *testing.T, f *fixture)
		wantCode int
		wantOut  string
	}{
		{"version", []string{"version"}, nil, exitOK, "sops-sidecar "},
		{"check", []string{"check"}, nil, exitOK, "api_token\ndb_password\n"},
		{"health not ready", []string{"health"}, nil, exitFailure, ""},
		{"usage", []string{"bogus"}, nil, exitUsage, ""},
		{"wrong key", []string{"run"}, func(t *testing.T, f *fixture) {
			other, _ := sopstest.NewIdentity(t)
			f.writeFile("age.key", other)
		}, exitFailure, ""},
		{"garbage SOPS file at debug", []string{"run"}, func(_ *testing.T, f *fixture) {
			f.env[config.EnvLogLevel] = "debug"
			f.writeFile("secrets.sops.yaml", []byte("sops: {}\nkey: "+canary+"\n"))
		}, exitFailure, ""},
		{"unencrypted comment holding the canary", []string{"check"}, func(t *testing.T, f *fixture) {
			data, err := os.ReadFile(f.path("secrets.sops.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			f.writeFile("secrets.sops.yaml", append([]byte("# "+canary+"\n"), data...))
		}, exitOK, "api_token\ndb_password\n"},
		{"SOPS_AGE_KEY present", []string{"run"}, func(_ *testing.T, f *fixture) { f.env[config.EnvAgeKey] = canary }, exitFailure, ""},
		{"tmpfs refused", []string{"run"}, func(_ *testing.T, f *fixture) { delete(f.env, config.EnvAllowNonTmpfs) }, exitFailure, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			if tt.mutate != nil {
				tt.mutate(t, f)
			}
			c := f.spawn(tt.args...)
			if code := c.wait(t); code != tt.wantCode {
				t.Errorf("exit = %d, want %d; stderr = %s", code, tt.wantCode, c.stderr.String())
			}
			if !strings.HasPrefix(c.stdout.String(), tt.wantOut) {
				t.Errorf("stdout = %q, want prefix %q", c.stdout.String(), tt.wantOut)
			}
		})
	}
}

func TestSubprocessRunSignals(t *testing.T) {
	f := newFixture(t)
	f.env[config.EnvLogLevel] = "debug"
	c := f.spawn("run")
	waitFor(t, "ready marker", func() bool { return health.Check(f.out) == nil })

	// Rotate the value, ask for a reload, and wait for it to finish.
	f.encrypt(item("db_password", canary+"-rotated"))
	if err := c.cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "reload", func() bool { return strings.Count(c.stderr.String(), "secrets written") >= 2 })
	if got := readFile(t, filepath.Join(f.out, "db_password")); got != canary+"-rotated" {
		t.Errorf("db_password = %q", got)
	}
	if _, err := os.Stat(filepath.Join(f.out, "api_token")); !os.IsNotExist(err) {
		t.Errorf("removed key's file still present: %v", err)
	}

	// The health subcommand sees the marker from another process.
	h := f.spawn("health")
	if code := h.wait(t); code != exitOK {
		t.Errorf("health exit = %d, stderr = %s", code, h.stderr.String())
	}

	if err := c.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := c.wait(t); code != exitOK {
		t.Errorf("exit = %d, stderr = %s", code, c.stderr.String())
	}
	assertJSONLines(t, c.stderr.String())
	if !strings.Contains(c.stderr.String(), "shutting down") {
		t.Errorf("stderr = %s", c.stderr.String())
	}
}

func TestSubprocessSIGINTWipes(t *testing.T) {
	f := newFixture(t)
	f.env[config.EnvWipeOnExit] = "true"
	c := f.spawn("run")
	waitFor(t, "ready marker", func() bool { return health.Check(f.out) == nil })
	if err := c.cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	if code := c.wait(t); code != exitOK {
		t.Errorf("exit = %d, stderr = %s", code, c.stderr.String())
	}
	if entries, err := os.ReadDir(f.out); err != nil || len(entries) != 0 {
		t.Errorf("output dir not wiped: %v, %v", entries, err)
	}
}
