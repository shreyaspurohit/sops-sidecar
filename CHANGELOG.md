# Changelog

All notable changes to this project are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `sops-sidecar run`: decrypts one SOPS file (YAML or JSON, top-level scalar keys) in-process with an age key and writes each secret as its own file, byte-exact and atomically, into a tmpfs output dir.
- Optional map file to choose which keys are written, their file names, and per-file owner and mode (built-in defaults: mode `0400`, owner unchanged).
- Ready marker `.sops-sidecar-ready` and `sops-sidecar health` for the Compose healthcheck.
- `SIGHUP` reload that atomically replaces outputs and removes files for keys that disappeared; clean shutdown on `SIGTERM`/`SIGINT`, optional `WIPE_ON_EXIT`.
- `sops-sidecar check` (validate inputs, print key names only) and `sops-sidecar version`.
- Fail-closed startup: refuses a non-tmpfs output dir, `SOPS_AGE_KEY` in the environment, nested values, unsafe paths and a wrong key.
- Secret values are never logged or included in errors, verified by canary tests.
- Static, multi-arch (`linux/amd64`, `linux/arm64`) non-root image; examples for Postgres and a generic `*_FILE` app.
- Integration tests on Compose v2.20.0 and latest; CI, release (signed images, SBOM, provenance) and Renovate configuration.
- Documentation: security model, tool comparison, reboot ordering, platform recipes.
