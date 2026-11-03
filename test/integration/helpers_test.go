//go:build integration

// Package integration drives the example Compose projects against a built
// sidecar image. It needs SIDECAR_IMAGE (the image under test) and COMPOSE_BIN
// (the Compose binary, see scripts/fetch-compose.sh).
package integration

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/getsops/sops/v3"

	"github.com/shreyaspurohit/sops-sidecar/internal/sopstest"
)

const (
	envImage   = "SIDECAR_IMAGE"
	envCompose = "COMPOSE_BIN"

	// envImageOverride is the variable the examples read to pick the sidecar image.
	envImageOverride = "SOPS_SIDECAR_IMAGE"

	// upTimeout covers pulling the postgres image on a cold machine.
	upTimeout = 5 * time.Minute
	// cmdTimeout bounds every other docker or compose invocation.
	cmdTimeout = time.Minute
	// waitTimeout bounds each poll for an expected state.
	waitTimeout = 90 * time.Second
	// failUpTimeout bounds `up` in scenarios where it is expected to fail or hang.
	failUpTimeout = 45 * time.Second
	// neverWindow is how long a state that must not happen is watched for.
	neverWindow  = 10 * time.Second
	pollInterval = 500 * time.Millisecond

	secretsService = "secrets"
	dbService      = "db"

	passwordKey  = "postgres_password"
	passwordPath = "/run/secrets/postgres_password"

	healthyStatus = "healthy"

	// chownImagePrefix starts the image line of the busybox service in
	// examples/generic-app/compose.yaml, the single place that image is pinned.
	chownImagePrefix = "image: busybox:"

	// zeroTime is the StartedAt of a container that was created but never started.
	zeroTime = "0001-01-01T00:00:00Z"
)

// keyOwner returns the uid:gid the example's sidecar runs as.
func keyOwner(example string) string {
	if example == genericAppExample {
		return "65532:65532"
	}
	return "0:0"
}

// stack is one Compose project started from a copy of an example directory.
type stack struct {
	t          *testing.T
	composeBin string
	image      string
	dir        string
	project    string
	overrides  []string
	canaries   []string
	// keyOwner is the uid:gid the example's sidecar runs as.
	keyOwner string
}

// newStack copies examples/<example> into a temp dir, generates an age
// identity, writes it as age.key, and returns the stack and the recipient.
// The project is torn down (volumes included) when the test ends.
func newStack(t *testing.T, example string) (s *stack, recipient string) {
	t.Helper()
	image, bin := os.Getenv(envImage), os.Getenv(envCompose)
	if image == "" || bin == "" {
		t.Fatalf("%s and %s must be set (see .github/workflows/ci.yml)", envImage, envCompose)
	}
	s = &stack{
		t:          t,
		composeBin: bin,
		image:      image,
		dir:        t.TempDir(),
		project:    "sstest-" + randomHex(t, 4),
		keyOwner:   keyOwner(example),
	}
	copyExample(t, example, s.dir)
	t.Cleanup(s.down)

	keyFile, recipient := sopstest.NewIdentity(t)
	for line := range strings.SplitSeq(string(keyFile), "\n") {
		if strings.HasPrefix(line, "AGE-SECRET-KEY-") {
			s.canaries = append(s.canaries, line)
		}
	}
	s.writeAgeKey(keyFile)
	return s, recipient
}

// writeAgeKey replaces age.key with data, mode 0400 and owned by the sidecar's
// uid: with every capability dropped, not even root can read a key it does not
// own. The chown runs in a throwaway container, so no sudo is needed. It must
// be called before the stack starts.
func (s *stack) writeAgeKey(data []byte) {
	s.t.Helper()
	path := filepath.Join(s.dir, "age.key")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		s.t.Fatalf("remove age.key: %v", err)
	}
	if err := os.WriteFile(path, data, 0o400); err != nil {
		s.t.Fatalf("write age.key: %v", err)
	}
	s.chownAgeKey(s.keyOwner)
}

// chownAgeKey chowns age.key to owner (uid:gid) as root in a throwaway
// container that mounts the project dir.
func (s *stack) chownAgeKey(owner string) {
	s.t.Helper()
	out, err := s.docker("run", "--rm", "--user", "0:0", "-v", s.dir+":/d", chownImage(s.t), "chown", owner, "/d/age.key")
	if err != nil && runtime.GOOS == "darwin" {
		// Docker Desktop's file sharing refuses chown from a container; it
		// presents bind mounts to containers regardless of host ownership.
		s.t.Logf("chown age.key to %s skipped on macOS: %v", owner, err)
		return
	}
	if err != nil {
		s.t.Fatalf("chown age.key to %s: %v\n%s", owner, err, s.redact(out))
	}
}

// copyExample copies the regular files of examples/<name> into dst.
func copyExample(t *testing.T, name, dst string) {
	t.Helper()
	src := filepath.Join("..", "..", "examples", name)
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("read example %s: %v", name, err)
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatalf("read example file %s: %v", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o644); err != nil {
			t.Fatalf("copy example file %s: %v", e.Name(), err)
		}
	}
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("random: %v", err)
	}
	return hex.EncodeToString(b)
}

// newSecret returns a random value and registers it as a canary: it must never
// appear in any failure message, inspect output or log.
func (s *stack) newSecret() string {
	s.t.Helper()
	v := randomHex(s.t, 16)
	s.canaries = append(s.canaries, v)
	return v
}

func sha256Hex(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

// redact removes every canary from text before it reaches a test message.
func (s *stack) redact(text string) string {
	for _, c := range s.canaries {
		text = strings.ReplaceAll(text, c, "[REDACTED]")
	}
	return text
}

// writeInPlace writes data into the file with the same inode (truncate, no
// rename), as `sops edit` does, which is what single-file bind mounts need.
func (s *stack) writeInPlace(name string, data []byte) {
	s.t.Helper()
	f, err := os.OpenFile(filepath.Join(s.dir, name), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		s.t.Fatalf("open %s: %v", name, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		s.t.Fatalf("write %s: %v", name, err)
	}
	if err := f.Close(); err != nil {
		s.t.Fatalf("close %s: %v", name, err)
	}
}

// writeSOPS encrypts the key/value pairs for recipient into secrets.sops.yaml.
func (s *stack) writeSOPS(recipient string, kv ...string) {
	s.t.Helper()
	var branch sops.TreeBranch
	for i := 0; i+1 < len(kv); i += 2 {
		branch = append(branch, sops.TreeItem{Key: kv[i], Value: kv[i+1]})
	}
	s.writeInPlace("secrets.sops.yaml", sopstest.EncryptYAML(s.t, recipient, branch))
}

// run executes bin with args in the project dir and returns combined output.
func (s *stack) run(timeout time.Duration, bin string, args ...string) (string, error) {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = s.dir
	cmd.Env = append(os.Environ(), envImageOverride+"="+s.image)
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (s *stack) composeArgs(args []string) []string {
	full := []string{"-p", s.project, "-f", "compose.yaml"}
	for _, o := range s.overrides {
		full = append(full, "-f", o)
	}
	return append(full, args...)
}

// compose runs a Compose command and returns its output.
func (s *stack) compose(timeout time.Duration, args ...string) (string, error) {
	s.t.Helper()
	return s.run(timeout, s.composeBin, s.composeArgs(args)...)
}

// mustCompose is compose that fails the test on error, with redacted output.
func (s *stack) mustCompose(timeout time.Duration, args ...string) string {
	s.t.Helper()
	out, err := s.compose(timeout, args...)
	if err != nil {
		s.t.Fatalf("compose %s: %v\n%s", args[0], err, s.redact(out))
	}
	return out
}

func (s *stack) down() {
	out, err := s.compose(upTimeout, "down", "-v", "--remove-orphans")
	if err != nil {
		s.t.Logf("compose down: %v\n%s", err, s.redact(out))
	}
}

// docker runs the docker CLI.
func (s *stack) docker(args ...string) (string, error) {
	s.t.Helper()
	return s.run(cmdTimeout, "docker", args...)
}

// containerID returns the ID of the service's container (any state), or "".
func (s *stack) containerID(service string) string {
	s.t.Helper()
	out, err := s.compose(cmdTimeout, "ps", "-a", "-q", service)
	if err != nil {
		s.t.Fatalf("compose ps %s: %v\n%s", service, err, s.redact(out))
	}
	return strings.TrimSpace(out)
}

// inspect returns a docker inspect format result for the service's container,
// or "" if it has no container.
func (s *stack) inspect(service, format string) string {
	s.t.Helper()
	id := s.containerID(service)
	if id == "" {
		return ""
	}
	out, err := s.docker("inspect", "-f", format, id)
	if err != nil {
		s.t.Fatalf("docker inspect %s: %v\n%s", service, err, s.redact(out))
	}
	return strings.TrimSpace(out)
}

// health returns the health status of the service's container ("" if none).
func (s *stack) health(service string) string {
	s.t.Helper()
	return s.inspect(service, "{{if .State.Health}}{{.State.Health.Status}}{{end}}")
}

// exec runs a command inside the service's running container.
func (s *stack) exec(service string, args ...string) (string, error) {
	s.t.Helper()
	return s.compose(cmdTimeout, append([]string{"exec", "-T", service}, args...)...)
}

// fileExists reports whether path exists in the service's container.
func (s *stack) fileExists(service, path string) bool {
	s.t.Helper()
	out, err := s.exec(service, "sh", "-c", "if test -e '"+path+"'; then echo yes; else echo no; fi")
	if err != nil {
		s.t.Fatalf("exec in %s: %v\n%s", service, err, s.redact(out))
	}
	return strings.TrimSpace(out) == "yes"
}

// fileSHA256 returns the SHA-256 of a file in the service's container, or "".
func (s *stack) fileSHA256(service, path string) string {
	s.t.Helper()
	out, err := s.exec(service, "sh", "-c", "sha256sum < '"+path+"'")
	if err != nil {
		return ""
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// login tries a TCP psql login to the db service with password. It connects to
// the container's own non-loopback address: the official image trusts
// loopback connections, so only other addresses check the password.
func (s *stack) login(password string) error {
	s.t.Helper()
	_, err := s.exec(dbService, "env", "PGPASSWORD="+password, "sh", "-c",
		`psql -h "$(hostname -i)" -U postgres -tAc "select 1"`)
	return err
}

// waitFor polls cond until it is true or waitTimeout passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", waitTimeout, what)
		}
		time.Sleep(pollInterval)
	}
}

// waitSecretsHealthy waits for the sidecar's healthcheck to pass.
func (s *stack) waitSecretsHealthy() {
	s.t.Helper()
	waitFor(s.t, "secrets to become healthy", func() bool {
		return s.health(secretsService) == healthyStatus
	})
}

// waitLogin waits until the db accepts password over TCP.
func (s *stack) waitLogin(password string) {
	s.t.Helper()
	waitFor(s.t, "db login to succeed", func() bool {
		return s.login(password) == nil
	})
}

// up starts the whole project and fails the test if it does not come up.
func (s *stack) up() {
	s.t.Helper()
	s.mustCompose(upTimeout, "up", "-d")
}

// assertFailsClosed checks that the sidecar failed and the db never started:
// the sidecar exits non-zero, is never healthy during neverWindow, and db has
// no container or one that never started.
func (s *stack) assertFailsClosed() {
	s.t.Helper()
	waitFor(s.t, "secrets to exit non-zero", func() bool {
		code := s.inspect(secretsService, "{{.State.ExitCode}}")
		return code != "" && code != "0"
	})
	deadline := time.Now().Add(neverWindow)
	for time.Now().Before(deadline) {
		if got := s.health(secretsService); got == healthyStatus {
			s.t.Fatal("secrets became healthy")
		}
		time.Sleep(pollInterval)
	}
	s.assertDBNeverStarted()
}

// assertDBNeverStarted fails unless db has no container, or one that was
// created but never started and never restarted.
func (s *stack) assertDBNeverStarted() {
	s.t.Helper()
	if s.containerID(dbService) == "" {
		return
	}
	if started := s.inspect(dbService, "{{.State.StartedAt}}"); started != zeroTime {
		s.t.Fatalf("db was started at %s", started)
	}
	if restarts := s.inspect(dbService, "{{.RestartCount}}"); restarts != "0" {
		s.t.Fatalf("db restart count is %s", restarts)
	}
}

// assertNoLeaks fails if any canary appears in docker inspect output of the
// project's containers or in the full Compose logs.
func (s *stack) assertNoLeaks() {
	s.t.Helper()
	ids := strings.Fields(s.mustCompose(cmdTimeout, "ps", "-a", "-q"))
	if len(ids) == 0 {
		s.t.Fatal("no containers to inspect")
	}
	inspected, err := s.docker(append([]string{"inspect"}, ids...)...)
	if err != nil {
		s.t.Fatalf("docker inspect: %v", err)
	}
	logs := s.mustCompose(cmdTimeout, "logs", "--no-color")
	for i, c := range s.canaries {
		if strings.Contains(inspected, c) {
			s.t.Errorf("canary %d found in docker inspect output", i)
		}
		if strings.Contains(logs, c) {
			s.t.Errorf("canary %d found in compose logs", i)
		}
	}
}

// chownImage returns the busybox image reference pinned (tag and digest) in
// examples/generic-app/compose.yaml.
func chownImage(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", genericAppExample, "compose.yaml"))
	if err != nil {
		t.Fatalf("read generic-app compose file: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if ref, ok := strings.CutPrefix(strings.TrimSpace(line), chownImagePrefix); ok {
			return "busybox:" + ref
		}
	}
	t.Fatal("no busybox image in the generic-app compose file")
	return ""
}
