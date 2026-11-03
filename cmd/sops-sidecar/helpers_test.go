package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getsops/sops/v3"

	"github.com/shreyaspurohit/sops-sidecar/internal/config"
	"github.com/shreyaspurohit/sops-sidecar/internal/sopstest"
)

// canary is a secret value that must never appear in any output.
const canary = "CANARY-91c2-SECRET-VALUE"

// testMainEnv makes the test binary run main() instead of the tests, so
// subprocess tests can exercise the real entry point.
const testMainEnv = "SOPS_SIDECAR_TEST_MAIN"

const waitTimeout = 10 * time.Second

func TestMain(m *testing.M) {
	if os.Getenv(testMainEnv) == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

// syncBuffer is a bytes.Buffer safe for concurrent use.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func item(key string, value any) sops.TreeItem {
	return sops.TreeItem{Key: key, Value: value}
}

// fixture is a set of input files and an output dir under a temp dir, plus
// the environment that points the sidecar at them.
type fixture struct {
	t         *testing.T
	root      string
	out       string
	recipient string
	env       map[string]string
}

func (f *fixture) path(name string) string { return filepath.Join(f.root, name) }

func (f *fixture) lookup(key string) (string, bool) {
	v, ok := f.env[key]
	return v, ok
}

func (f *fixture) writeFile(name string, data []byte) string {
	f.t.Helper()
	p := f.path(name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		f.t.Fatal(err)
	}
	return p
}

// encrypt writes the SOPS file with the given top-level items.
func (f *fixture) encrypt(items ...sops.TreeItem) {
	f.t.Helper()
	f.writeFile("secrets.sops.yaml", sopstest.EncryptYAML(f.t, f.recipient, sops.TreeBranch(items)))
}

// newFixture creates a valid setup with a canary secret and a plain one.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{t: t, root: root, out: filepath.Join(root, "out")}
	if err := os.Mkdir(f.out, 0o700); err != nil {
		t.Fatal(err)
	}
	keyFile, recipient := sopstest.NewIdentity(t)
	f.recipient = recipient
	f.env = map[string]string{
		config.EnvSOPSFile:      f.path("secrets.sops.yaml"),
		config.EnvAgeKeyFile:    f.path("age.key"),
		config.EnvOutputDir:     f.out,
		config.EnvAllowNonTmpfs: "true",
	}
	f.writeFile("age.key", keyFile)
	f.encrypt(item("db_password", canary), item("api_token", "token\n"))
	return f
}

// runCLI runs cli and fails the test if the canary leaks into its output.
func (f *fixture) runCLI(args ...string) (code int, stdout, stderr string) {
	f.t.Helper()
	var out, errOut bytes.Buffer
	code = cli(args, f.lookup, &out, &errOut)
	noCanary(f.t, out.String(), errOut.String())
	return code, out.String(), errOut.String()
}

func noCanary(t *testing.T, outputs ...string) {
	t.Helper()
	for _, o := range outputs {
		if strings.Contains(o, canary) {
			t.Fatalf("canary value leaked into output: %q", o)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
