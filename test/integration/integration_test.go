//go:build integration

package integration

import (
	"strings"
	"testing"

	"github.com/shreyaspurohit/sops-sidecar/internal/sopstest"
)

const (
	postgresExample   = "postgres"
	genericAppExample = "generic-app"

	extraKey  = "extra_key"
	extraPath = "/run/secrets/extra_key"

	// mapWithExtra lists a second key so rotation can remove it again.
	mapWithExtra = `files:
  - key: postgres_password
    uid: 70
    gid: 70
    mode: "0400"
  - key: extra_key
    uid: 70
    gid: 70
    mode: "0400"
`
	mapPasswordOnly = `files:
  - key: postgres_password
    uid: 70
    gid: 70
    mode: "0400"
`

	// plainVolumeOverride mounts a plain named volume where the sidecar writes.
	plainVolumeOverride = `services:
  secrets:
    volumes:
      - plain:/secrets
volumes:
  plain: {}
`
)

// 1. The Postgres example comes up healthy and enforces the decrypted password.
func TestPostgresLogin(t *testing.T) {
	s, recipient := newStack(t, postgresExample)
	password := s.newSecret()
	s.writeSOPS(recipient, passwordKey, password)
	s.up()

	s.waitSecretsHealthy()
	s.waitLogin(password)
	if err := s.login(s.newSecret()); err == nil {
		t.Error("login with a wrong password succeeded")
	}
	if got, err := s.exec(dbService, "stat", "-c", "%u:%g %a", passwordPath); err != nil || strings.TrimSpace(got) != "70:70 400" {
		t.Errorf("owner and mode of the secret file: got %q, err %v; want 70:70 400", strings.TrimSpace(got), err)
	}
	s.assertNoLeaks()
}

// 2. A wrong age key leaves the sidecar unhealthy and db never starts.
func TestWrongAgeKey(t *testing.T) {
	s, recipient := newStack(t, postgresExample)
	s.writeSOPS(recipient, passwordKey, s.newSecret())
	wrongKey, _ := sopstest.NewIdentity(t)
	s.writeAgeKey(wrongKey)

	// `up` waits for a healthy sidecar, so it fails or is cut off by the timeout.
	_, _ = s.compose(failUpTimeout, "up", "-d")

	s.assertFailsClosed()
	s.assertNoLeaks()
}

// 3. Recreating only db keeps the secret available to the new container.
func TestRecreateSafety(t *testing.T) {
	s, recipient := newStack(t, postgresExample)
	password := s.newSecret()
	s.writeSOPS(recipient, passwordKey, password)
	s.up()
	s.waitSecretsHealthy()
	s.waitLogin(password)
	oldDB, sidecar := s.containerID(dbService), s.containerID(secretsService)

	s.mustCompose(upTimeout, "up", "-d", "--force-recreate", "--no-deps", dbService)

	if got := s.containerID(dbService); got == oldDB {
		t.Fatal("db container was not recreated")
	}
	if got := s.containerID(secretsService); got != sidecar {
		t.Error("secrets container was recreated")
	}
	if got := s.fileSHA256(dbService, passwordPath); got != sha256Hex(password) {
		t.Error("secret file in the new db container is missing or has the wrong content")
	}
	s.waitLogin(password)
	s.assertNoLeaks()
}

// 4. A sidecar whose output dir is not tmpfs refuses to start.
func TestNonTmpfsRefused(t *testing.T) {
	s, recipient := newStack(t, postgresExample)
	s.writeSOPS(recipient, passwordKey, s.newSecret())
	s.writeInPlace("override.yaml", []byte(plainVolumeOverride))
	s.overrides = append(s.overrides, "override.yaml")

	_, _ = s.compose(failUpTimeout, "up", "-d")

	s.assertFailsClosed()
	logs := s.mustCompose(cmdTimeout, "logs", "--no-color", secretsService)
	if !strings.Contains(strings.ToLower(logs), "tmpfs") {
		t.Errorf("sidecar logs do not mention tmpfs:\n%s", s.redact(logs))
	}
	s.assertNoLeaks()
}

// 5. SIGHUP replaces changed values and removes files of keys that disappeared.
func TestSIGHUPRotation(t *testing.T) {
	s, recipient := newStack(t, postgresExample)
	oldPassword, extraValue, newPassword := s.newSecret(), s.newSecret(), s.newSecret()
	s.writeInPlace("secrets.map.yaml", []byte(mapWithExtra))
	s.writeSOPS(recipient, passwordKey, oldPassword, extraKey, extraValue)
	s.up()
	s.waitSecretsHealthy()
	s.waitLogin(oldPassword)
	if !s.fileExists(dbService, extraPath) {
		t.Fatal("extra key has no file before rotation")
	}

	// Rewrite both inputs in place: single-file bind mounts follow the inode.
	s.writeInPlace("secrets.map.yaml", []byte(mapPasswordOnly))
	s.writeSOPS(recipient, passwordKey, newPassword)
	s.mustCompose(cmdTimeout, "kill", "-s", "HUP", secretsService)

	waitFor(t, "rotated secret and removal of the dropped key", func() bool {
		_, healthErr := s.exec(secretsService, "/sops-sidecar", "health")
		return healthErr == nil &&
			s.fileSHA256(dbService, passwordPath) == sha256Hex(newPassword) &&
			!s.fileExists(dbService, extraPath)
	})
	s.assertNoLeaks()
}

// 6. Neither docker inspect nor the logs ever contain a secret value, across a
// full run including a rotation, and not the age key either.
func TestNoSecretsInInspectOrLogs(t *testing.T) {
	s, recipient := newStack(t, postgresExample)
	oldPassword, newPassword := s.newSecret(), s.newSecret()
	s.writeSOPS(recipient, passwordKey, oldPassword)
	s.up()
	s.waitSecretsHealthy()
	s.waitLogin(oldPassword)

	s.writeSOPS(recipient, passwordKey, newPassword)
	s.mustCompose(cmdTimeout, "kill", "-s", "HUP", secretsService)
	waitFor(t, "rotated secret", func() bool {
		return s.fileSHA256(dbService, passwordPath) == sha256Hex(newPassword)
	})

	s.assertNoLeaks()
}

// The generic-app example works with a non-root sidecar and no capabilities.
func TestGenericApp(t *testing.T) {
	s, recipient := newStack(t, genericAppExample)
	token := s.newSecret()
	s.writeSOPS(recipient, "api_token", token)
	s.up()
	s.waitSecretsHealthy()

	waitFor(t, "app to report the token loaded", func() bool {
		logs, err := s.compose(cmdTimeout, "logs", "--no-color", "app")
		return err == nil && strings.Contains(logs, "token loaded")
	})
	if got := s.fileSHA256("app", "/run/secrets/api_token"); got != sha256Hex(token) {
		t.Error("token file in the app container is missing or has the wrong content")
	}
	if got, err := s.exec("app", "stat", "-c", "%u:%g %a", "/run/secrets/api_token"); err != nil || strings.TrimSpace(got) != "65532:65532 400" {
		t.Errorf("owner and mode of the token file: got %q, err %v; want 65532:65532 400", strings.TrimSpace(got), err)
	}
	s.assertNoLeaks()
}
