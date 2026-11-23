# Agent guide for sops-sidecar

Instructions for any coding agent (and human) changing this repository. `CLAUDE.md` imports this file.

`sops-sidecar` is a small Go program, shipped as a distroless container. It decrypts one SOPS file with an age key and writes each top-level secret as its own file into a tmpfs volume, which neighboring containers mount read-only. It is security software: a mistake can leak a secret or start an app without its secrets.

## Read before changing code

1. **`ARCHITECTURE.md`** is the contract: package boundaries, the exported API with signatures, data flow, which package owns each concept, and the glossary. Use its terms (SOPS file, key, value, secret, map file, output dir, plan, ready marker, load, reload) in code, logs and docs.
2. **`DECISIONS.md`** records why things are the way they are. Read "Explicitly not doing" and "Review findings not adopted" before proposing a feature or a "fix". Many apparent oddities are deliberate. Examples: decrypting without `sops/decrypt.Data`, the `LOG_LEVEL` switch, running the postgres example as root, keeping the value-type `Plan`.
3. **`CONTRIBUTING.md`** lists tool setup and every check to run.

Layout:
- `cmd/sops-sidecar`: CLI, lifecycle and signals.
- `internal/config`: env, map file, plan. Pure.
- `internal/decrypt`: SOPS and age. Pure.
- `internal/writer`: everything that touches the output dir.
- `internal/health`: ready marker.
- `internal/sopstest`: test fixtures.
- `test/integration`: real Docker Compose tests.
- `examples/`, `docs/`.

## Invariants: never break these

- **No secret value in any output.** Values never appear in logs (at any level), error messages, stdout, panics or `docker inspect`. Key names may appear in errors and debug logs only. `decrypt.Value` redacts through `String`, `GoString`, `Format`, `MarshalText` and `LogValue`. Errors from parsers that can echo input (age, sops stores, sops tree decrypt) are replaced with fixed messages, not wrapped. Every new code path that handles values needs a canary test (see `canary` in the tests).
- **Refuse and stay unhealthy on any doubt.** When in doubt, refuse, clear the ready marker and stay unhealthy. Never write a partial state and report ready. The ready marker is cleared before any change and set only after every file is written and stale files are removed. An empty plan is `config.ErrNoSecrets`, refused both in `Map.Plan` and again in `apply`. Assume users will feed in every wrong input imaginable.
- **All output-dir I/O goes through `writer.Dir`** (an `os.Root`). Never use `os.WriteFile`, `filepath.Join(outputDir, ...)` or similar for the output dir. Writes are atomic, in this order: create a temp file with `O_EXCL` and mode `0600`, write, `Sync`, `Chown`, `Chmod`, close, refuse a non-regular target, then rename.
- **Names starting with `.sops-sidecar` are reserved** for the sidecar's own files (ready marker, temp files). Secret file names are single path elements validated by `writer.ParseName`.
- **The output dir must be tmpfs**, checked with `fstatfs` on the opened dir (Linux only). `ALLOW_NON_TMPFS` exists for tests only.
- **Decryption uses only the configured age key file.** Never read `SOPS_AGE_KEY*` env vars, home-directory keys, or call KMS, Vault or keyservice. `SOPS_AGE_KEY` in the environment is refused. The MAC is always verified. `mac_only_encrypted` files and multi-document YAML are refused.
- **Pure core, I/O shell.** `config` and `decrypt` do no I/O. The environment is passed in as `config.LookupFunc`. Keep new logic pure and testable, and keep I/O thin and at the edges. No package-level mutable state, no `init()`.
- **Logs are JSON** (`slog.NewJSONHandler`) on stderr, one object per line. Tests assert every stderr line parses as JSON.
- **Examples use only Compose v2.20.0 features**: named volumes with `driver_opts` tmpfs, `healthcheck`, and `depends_on: condition: service_healthy`. No provider services and no newer syntax. v2.20.0 is the supported minimum and is never bumped.

## Rules for changes

- **Library first.** If a well-maintained, OSS-licensed library or the standard library already does it, use it. Hand-written code is kept only when a library would lose a needed property, and the reason is recorded in `DECISIONS.md` ("Library review" lists the current ones). Every new dependency needs a one-line justification in `DECISIONS.md`.
- **Idiomatic Go.** Return zero values plus an error for value types, pointers for resources. Give a function signature that repeats across files a name instead of repeating it. Doc comments on every exported identifier say what is guaranteed and why. No dead code, no commented-out code.
- **Keep the docs in sync in the same change.**
  - An exported API or behavior change goes in `ARCHITECTURE.md`.
  - A default or env var change goes in the README configuration table, the single place every default is listed.
  - A user-visible change goes in `CHANGELOG.md` under Unreleased.
  - A non-obvious choice or a rejected suggestion goes in `DECISIONS.md`.
- **Honest docs.** Never write "tested", "verified" or "works on X" unless a test actually ran on X. Today that means Compose v2.20.0 and latest, on Linux amd64 (CI runners), Linux arm64 and macOS Docker Desktop, plus manual runs on the NAS model in the README compatibility line (the postgres example, and the gluetun and authentik recipes on the v0.2.0 release), each with its scope recorded in its recipe. Docs state only what was tested. Comparisons with other tools must credit their real advantages and mark inferred claims. Run a suggested setup before documenting it.
- **The NAS vendor is named in exactly three places:** the README compatibility line, the NAS paragraph of `docs/reboot-ordering.md`, and the NAS recipe in `docs/recipes/`. Other recipes describe the host generically. Don't add a fourth mention of the vendor or its OS, links included. Check with a case-insensitive `git grep` for the vendor name.
- **Explain the numbers.** When docs tell users to run something like `chown 0:0` or `uid=65532`, say where the number comes from.

## Testing

- Table-driven tests through the public API. Fuzz targets (`FuzzParseName`, `FuzzParseMap`) run their seed corpus in normal `go test`. Real fuzzing needs `-fuzz`.
- Coverage gates are at least 90% per `internal/` package and 80% overall. They are **enforced on Linux only**, which is what CI runs. On macOS `scripts/coverage.sh` prints the numbers without failing, because Linux-only code cannot run there. To get the real numbers on a Mac:

  ```sh
  docker run --rm -v "$PWD":/src -w /src golang:"$(awk '$1=="golang"{print $2}' .tool-versions)" \
    sh -c 'useradd -m -u 1001 t && su t -c "cd /src && PATH=/usr/local/go/bin:\$PATH GOTOOLCHAIN=local GOFLAGS=-buildvcs=false GOCACHE=/tmp/gc GOPATH=/tmp/gopath ./scripts/coverage.sh"'
  ```

- macOS Docker Desktop ignores host file ownership on bind mounts. Anything that depends on uid, gid or mode (the age key's owner, chown of secret files) must be verified on Linux: CI, a Linux VM, or ownership set inside a named volume.
- Integration tests use real containers and pinned Compose binaries (see `CONTRIBUTING.md`). The postgres image trusts loopback, so tests connect to the container's own IP to prove the password is checked.
- Never commit real secrets or keys. Test keys are generated at runtime.

## Tooling

- All tools are pinned exactly in `.tool-versions` and installed with asdf. Don't install tools globally. `staticcheck`, `govulncheck` and `deadcode` are `go tool` directives. `zizmor` runs through `scripts/zizmor.sh` (uvx).
- Everything is pinned: images by tag and digest, GitHub Actions by commit SHA, tool versions exactly. Renovate keeps them current. Versions in scripts and workflows carry a `# renovate: datasource=... depName=...` comment above them. Keep that format when adding a pin.
- `scripts/deadcode.sh` allows exactly the six redaction methods. Anything else it reports is real dead code.
- When bumping Go, change `.tool-versions`, the `go` line in `go.mod`, and the Dockerfile builder image (tag and digest) together.

## Git, CI and releases

- Conventional Commits (`feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `chore:`, `ci:`, `build:`).
- The required CI checks are `lint`, `workflow-lint`, `test`, `govulncheck`, `docker`, `integration (compose minimum)` and `integration (compose latest)`. Don't rename these jobs, because the branch ruleset and Renovate automerge depend on the names.
- Pushing a `v*` tag publishes a signed release (GoReleaser, cosign, GHCR). Never create, move or delete tags, and never push to `main` or publish anything, unless the maintainer asks. Released tags are immutable. Fix a bad release with a new patch version.
- `.progress/`, `review/`, `.cache/`, `dist/` and `coverage.out` are local only and must never be committed.
