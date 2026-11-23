# Changelog

All notable changes to this project are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Recipe for running gluetun with its WireGuard config file from sops-sidecar.
- README note on values that contain `$`: Compose needs `$$` in `.env`, and the SOPS file holds the single `$`.
- Recipe for poznote, an image that reads secrets only from environment variables, using an entrypoint wrapper that exports the secret files.
- Recipe for running authentik with its database password, secret key and email password from sops-sidecar, including one key written to two files.
- The NAS recipe sets the Compose project name, so `docker compose` run from a shell updates a project created in the NAS's container UI.

## [0.2.0] - 2026-11-19

### Added

- The map file accepts the same key in several entries, each with its own `path`, owner and mode, so one secret can be written to several files for consumers that run as different users. Duplicate file names are still refused.

## [0.1.0] - 2026-11-18

### Added

- `sops-sidecar run` decrypts one SOPS file (YAML or JSON, top-level scalar keys) in-process with an age key and writes each secret as its own file, byte-exact and atomically, into a tmpfs output dir.
- Optional map file that chooses which keys are written, their file names, and per-file owner and mode. The built-in defaults are mode `0400` and owner unchanged.
- Ready marker `.sops-sidecar-ready` and `sops-sidecar health` for the Compose healthcheck.
- `SIGHUP` reload that replaces each file atomically and removes files for keys that disappeared. `SIGTERM` and `SIGINT` shut the sidecar down cleanly, and `WIPE_ON_EXIT` is optional.
- `sops-sidecar check` validates inputs and prints key names only. `sops-sidecar version` prints the version.
- Startup refuses a non-tmpfs output dir, `SOPS_AGE_KEY` in the environment, nested values, unsafe paths, a wrong key, and a SOPS file with no keys (a reload of one keeps the previous files and reports not ready).
- Secret values are never logged or included in errors, verified by canary tests.
- Post-quantum hybrid age keys (`age-keygen -pq`, `age1pq1...` recipients) work and are what the docs now use. Key files may mix them with classic keys. Covered by tests.
- README sections on adding, changing and removing a secret, and on adding or replacing an age key with `sops rotate`.
- Structured JSON logs on stderr (one object per line), level set by `LOG_LEVEL`.
- The tmpfs check asks the kernel (`fstatfs`) for the output dir's file system instead of parsing `/proc/mounts`.
- Refuses SOPS files encrypted with `mac_only_encrypted`, whose unencrypted values would be unauthenticated.
- Adopts secret files left in the output dir by an earlier run, so keys removed while the sidecar was stopped are deleted at the next start.
- Example tmpfs volumes are mounted `nosuid,nodev,noexec`.
- Static, multi-arch (`linux/amd64`, `linux/arm64`) non-root image, with examples for Postgres and a generic `*_FILE` app.
- Integration tests on Compose v2.20.0 and latest, plus CI, release (signed images, SBOM, provenance) and Renovate configuration.
- Documentation: security model, tool comparison, reboot ordering, platform recipes.

[Unreleased]: https://github.com/shreyaspurohit/sops-sidecar/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/shreyaspurohit/sops-sidecar/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/shreyaspurohit/sops-sidecar/releases/tag/v0.1.0
