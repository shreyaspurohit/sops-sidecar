package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shreyaspurohit/sops-sidecar/internal/config"
	"github.com/shreyaspurohit/sops-sidecar/internal/health"
	"github.com/shreyaspurohit/sops-sidecar/internal/writer"
)

// running is a serve call in progress with a driver for reloads and shutdown.
type running struct {
	t      *testing.T
	f      *fixture
	log    *syncBuffer
	reload chan os.Signal
	cancel context.CancelFunc
	done   chan int
}

func (f *fixture) start() *running {
	f.t.Helper()
	cfg, err := config.FromEnv(f.lookup)
	if err != nil {
		f.t.Fatal(err)
	}
	r := &running{
		t:      f.t,
		f:      f,
		log:    &syncBuffer{},
		reload: make(chan os.Signal, 1),
		done:   make(chan int, 1),
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	f.t.Cleanup(cancel)
	go func() { r.done <- serve(ctx, cfg, newLogger(r.log, cfg.LogLevel), r.reload) }()
	return r
}

// logged counts occurrences of msg in the log.
func (r *running) logged(msg string) int { return strings.Count(r.log.String(), msg) }

func (r *running) waitLogged(msg string, n int) {
	r.t.Helper()
	waitFor(r.t, msg, func() bool { return r.logged(msg) >= n })
}

// reloadAndWait requests a reload and waits for its outcome message.
func (r *running) reloadAndWait(outcome string) {
	r.t.Helper()
	before := r.logged(outcome)
	r.reload <- os.Interrupt
	r.waitLogged(outcome, before+1)
}

// exitCode waits for serve to return.
func (r *running) exitCode() int {
	r.t.Helper()
	select {
	case code := <-r.done:
		noCanary(r.t, r.log.String())
		return code
	case <-time.After(waitTimeout):
		r.t.Fatal("serve did not return")
		return -1
	}
}

// stop cancels the context and returns the exit code.
func (r *running) stop() int {
	r.t.Helper()
	r.cancel()
	return r.exitCode()
}

func (r *running) ready() bool { return health.Check(r.f.out) == nil }

func TestServeWritesFilesByteExact(t *testing.T) {
	f := newFixture(t)
	f.env[config.EnvLogLevel] = "debug"
	r := f.start()
	r.waitLogged("secrets written", 1)

	if got := readFile(t, filepath.Join(f.out, "db_password")); got != canary {
		t.Errorf("db_password = %q", got)
	}
	if got := readFile(t, filepath.Join(f.out, "api_token")); got != "token\n" {
		t.Errorf("api_token = %q", got)
	}
	info, err := os.Stat(filepath.Join(f.out, "db_password"))
	if err != nil || info.Mode().Perm() != 0o400 {
		t.Errorf("mode = %v, err = %v", info, err)
	}
	if !r.ready() {
		t.Error("not ready after load")
	}
	if code := r.stop(); code != exitOK {
		t.Errorf("exit = %d", code)
	}
	log := r.log.String()
	assertJSONLines(t, log)
	for _, want := range []string{"tmpfs check disabled", `"files":2`, `"ignored":0`, "secret written", "shutting down"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	assertKeptOnShutdown(t, r)
}

func assertKeptOnShutdown(t *testing.T, r *running) {
	t.Helper()
	if !r.ready() {
		t.Error("marker must be kept on shutdown by default")
	}
	if got := readFile(t, filepath.Join(r.f.out, "api_token")); got != "token\n" {
		t.Errorf("files must be kept on shutdown by default, api_token = %q", got)
	}
}

func TestServeInfoLogOmitsNames(t *testing.T) {
	f := newFixture(t)
	r := f.start()
	r.waitLogged("secrets written", 1)
	r.stop()
	if log := r.log.String(); strings.Contains(log, "db_password") || strings.Contains(log, "api_token") {
		t.Errorf("key names logged at info level:\n%s", log)
	}
}

func TestServeWipeOnExit(t *testing.T) {
	f := newFixture(t)
	f.env[config.EnvWipeOnExit] = "true"
	// Only regular files are adopted and wiped; other entries are left alone.
	foreign := filepath.Join(f.out, "not-ours")
	if err := os.Mkdir(foreign, 0o700); err != nil {
		t.Fatal(err)
	}
	r := f.start()
	r.waitLogged("secrets written", 1)
	if code := r.stop(); code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	entries, err := os.ReadDir(f.out)
	if err != nil || len(entries) != 1 || entries[0].Name() != "not-ours" {
		t.Errorf("only the foreign file should remain: %v, %v", entries, err)
	}
}

func TestServeReload(t *testing.T) {
	f := newFixture(t)
	f.env[config.EnvMapFile] = f.writeFile("map.yaml", []byte("files:\n  - key: db_password\n  - key: api_token\n    path: token\n"))
	r := f.start()
	r.waitLogged("secrets written", 1)

	f.encrypt(item("db_password", canary+"-rotated"), item("new_key", "n"), item("api_token", "t"))
	f.writeFile("map.yaml", []byte("files:\n  - key: db_password\n  - key: new_key\n"))
	r.reloadAndWait("secrets written")

	if got := readFile(t, filepath.Join(f.out, "db_password")); got != canary+"-rotated" {
		t.Errorf("db_password = %q", got)
	}
	if got := readFile(t, filepath.Join(f.out, "new_key")); got != "n" {
		t.Errorf("new_key = %q", got)
	}
	if _, err := os.Stat(filepath.Join(f.out, "token")); !os.IsNotExist(err) {
		t.Errorf("removed key's file still present: %v", err)
	}
	if !r.ready() {
		t.Error("not ready after reload")
	}
	if got := r.logged("reload requested"); got != 1 {
		t.Errorf("reload requested logged %d times", got)
	}
	if code := r.stop(); code != exitOK {
		t.Errorf("exit = %d", code)
	}
}

func TestServeFailedReloadThenRecovery(t *testing.T) {
	f := newFixture(t)
	r := f.start()
	r.waitLogged("secrets written", 1)

	f.writeFile("secrets.sops.yaml", []byte("garbage"))
	r.reloadAndWait("reload failed")
	if r.ready() {
		t.Error("ready after a failed reload")
	}
	if got := readFile(t, filepath.Join(f.out, "db_password")); got != canary {
		t.Errorf("previous file changed after failed reload: %q", got)
	}

	f.encrypt(item("db_password", "second"))
	r.reloadAndWait("secrets written")
	if !r.ready() {
		t.Error("not ready after recovery")
	}
	if _, err := os.Stat(filepath.Join(f.out, "api_token")); !os.IsNotExist(err) {
		t.Errorf("stale file survived recovery: %v", err)
	}
	if code := r.stop(); code != exitOK {
		t.Errorf("exit = %d", code)
	}
}

// A reload that fails after writing some files must still let a later
// reload remove the files it wrote.
func TestServePartialReloadTracksWrittenFiles(t *testing.T) {
	f := newFixture(t)
	f.encrypt(item("a", "1"))
	r := f.start()
	r.waitLogged("secrets written", 1)

	if err := os.Mkdir(filepath.Join(f.out, "z"), 0o700); err != nil {
		t.Fatal(err)
	}
	f.encrypt(item("a", "2"), item("b", "2"), item("z", "2"))
	r.reloadAndWait("reload failed")
	if r.ready() {
		t.Error("ready after a partial reload")
	}
	if got := readFile(t, filepath.Join(f.out, "b")); got != "2" {
		t.Errorf("b = %q", got)
	}

	f.encrypt(item("a", "3"))
	r.reloadAndWait("secrets written")
	if _, err := os.Stat(filepath.Join(f.out, "b")); !os.IsNotExist(err) {
		t.Errorf("file from the failed reload was never removed: %v", err)
	}
	if !r.ready() {
		t.Error("not ready after recovery")
	}
	r.stop()
}

func TestServeInitialLoadFailure(t *testing.T) {
	f := newFixture(t)
	f.writeFile("secrets.sops.yaml", []byte("garbage"))
	r := f.start()
	if code := r.exitCode(); code != exitFailure {
		t.Errorf("exit = %d", code)
	}
	if !strings.Contains(r.log.String(), "load failed") {
		t.Errorf("log = %s", r.log.String())
	}
}

func TestServeWriteErrorLeavesNotReady(t *testing.T) {
	f := newFixture(t)
	if err := os.Mkdir(filepath.Join(f.out, "db_password"), 0o700); err != nil {
		t.Fatal(err)
	}
	r := f.start()
	if code := r.exitCode(); code != exitFailure {
		t.Errorf("exit = %d", code)
	}
	if r.ready() {
		t.Error("ready after a write error")
	}
}

func TestServeRemovesLeftoverTempFiles(t *testing.T) {
	f := newFixture(t)
	leftover := filepath.Join(f.out, writer.ReservedPrefix+"-tmp-0123456789abcdef")
	if err := os.WriteFile(leftover, []byte(canary), 0o600); err != nil {
		t.Fatal(err)
	}
	r := f.start()
	r.waitLogged("secrets written", 1)
	r.stop()
	if _, err := os.Lstat(leftover); !os.IsNotExist(err) {
		t.Errorf("leftover temp file still present: %v", err)
	}
}

func TestServeStartupFailureClearsInheritedMarker(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(filepath.Join(f.out, health.MarkerName), nil, 0o444); err != nil {
		t.Fatal(err)
	}
	// A non-empty directory under a temp-file name makes the startup sweep fail.
	if err := os.MkdirAll(filepath.Join(f.out, writer.ReservedPrefix+"-tmp-dir", "inner"), 0o700); err != nil {
		t.Fatal(err)
	}
	if code := f.start().exitCode(); code != exitFailure {
		t.Fatalf("exit = %d, want %d", code, exitFailure)
	}
	if err := health.Check(f.out); err == nil {
		t.Error("health reports ready after a failed startup")
	}
}

func TestServeWipeFailureLeavesNotReady(t *testing.T) {
	f := newFixture(t)
	f.env[config.EnvWipeOnExit] = "true"
	r := f.start()
	r.waitLogged("secrets written", 1)
	// Replace one written file with a directory so its removal is refused.
	blocked := filepath.Join(f.out, "api_token")
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	if code := r.stop(); code != exitFailure {
		t.Fatalf("exit = %d, want %d", code, exitFailure)
	}
	if r.logged("wipe failed") != 1 {
		t.Errorf("wipe failure not logged:\n%s", r.log.String())
	}
	if err := health.Check(f.out); err == nil {
		t.Error("health reports ready after a failed wipe")
	}
}

func TestServeAdoptsFilesFromEarlierRun(t *testing.T) {
	f := newFixture(t)
	stale := filepath.Join(f.out, "revoked_key")
	if err := os.WriteFile(stale, []byte(canary), 0o400); err != nil {
		t.Fatal(err)
	}
	r := f.start()
	r.waitLogged("secrets written", 1)
	if _, err := os.Lstat(stale); !os.IsNotExist(err) {
		t.Errorf("file of a key absent from the SOPS file survived startup: %v", err)
	}
	if !r.ready() {
		t.Error("not ready after the first load")
	}
	r.stop()
}

// assertJSONLines fails unless every non-empty line of log is a JSON object
// with the standard slog time, level and msg keys.
func assertJSONLines(t *testing.T, log string) {
	t.Helper()
	for line := range strings.Lines(log) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Errorf("log line is not JSON: %v\n%s", err, line)
			continue
		}
		for _, k := range []string{"time", "level", "msg"} {
			if _, ok := rec[k]; !ok {
				t.Errorf("log line lacks %q: %s", k, line)
			}
		}
	}
}
