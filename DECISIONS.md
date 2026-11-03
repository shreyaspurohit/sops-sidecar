# Decisions

One line each: decision — reason. Newest sections at the bottom.

## Name check (2026-11-03)

- Name `sops-sidecar` is used — no real conflict found:
  - Web search for `"sops-sidecar"` returned no SOPS-related results.
  - GitHub repository search (`https://api.github.com/search/repositories?q=sops-sidecar+in:name`) found only <https://github.com/JonasKop/argocd-sops-sidecar> — an Argo CD plugin sidecar config that merely uses the phrase (incidental).
  - GitHub user/org `sops-sidecar`: <https://github.com/sops-sidecar> → 404 (free).
  - <https://github.com/shreyaspurohit/sops-sidecar> → 404 (free, to be created by the maintainer).
  - Docker Hub search (`https://hub.docker.com/v2/search/repositories/?query=sops-sidecar`) → no repository named `sops-sidecar` (only unrelated `*-sidecar` and `stagex/sops`).
  - GHCR: image lives under the maintainer's namespace `ghcr.io/shreyaspurohit/sops-sidecar`, so no global collision is possible.
  - pkg.go.dev search (<https://pkg.go.dev/search?q=sops-sidecar>) → no module with that name.

## Language and tooling

- Go (1.27.3, current stable per <https://go.dev/dl/?mode=json>) — sops and age are Go libraries (in-process decrypt), CGO_ENABLED=0 gives a static binary for a distroless image, and it is the ecosystem's language.
- All CLI tools pinned in `.tool-versions` and installed with asdf 0.20.2 — single source of truth for laptop and CI.
- `zizmor` has no asdf plugin → fallback 3: `uvx zizmor@<version>` with `uv` installed by asdf; version lives once in `scripts/zizmor.sh`, tracked by a Renovate `customManagers` regex.
- `deadcode`, `govulncheck`, `staticcheck` → `tool` directives in `go.mod`, run with `go tool` — project-local and version-pinned.
- `nodejs` in `.tool-versions` (26.13.0, current LTS) — needed only to run `renovate` and `renovate-config-validator` via `npx` for local verification.
- Trivy chosen over Grype for image scanning — single tool, asdf plugin exists, Renovate asdf manager supports it.

## Design

- Decrypt drives sops' store/tree API directly instead of `decrypt.Data` — `decrypt.Data` resolves age identities from `SOPS_AGE_KEY*` env vars and `~/.config`, and sops' local keyservice rebuilds age keys without injected identities; we parse the age key file ourselves and call `age.MasterKey.Decrypt` on each age key, so no KMS client is ever contacted (no network).
- Only a single SOPS key group is supported; Shamir/multi-group files are refused — simplest correct behavior; age is the only supported key type.
- MAC is always verified — integrity check from sops' own `DataWithFormat`; never skipped.
- Numbers and bools are written with `sops.ToBytes` (bools as `True`/`False`, floats in shortest `'f'` form) — that is literally SOPS's string form for those types; quote values in YAML to get any other text.
- SOPS file format is chosen by extension: `.json` → JSON, anything else → YAML — mirrors sops' own rule; no format env var (YAGNI).
- Map `path` is a single flat file name (no `/`), optional, defaulting to the key — removes subdirectory creation, ownership, and most traversal/symlink surface.
- Names starting with `.sops-sidecar` are reserved for the sidecar's own files (marker, temp files) and refused as secret paths.
- Readiness state lives only in the ready marker file — `health` runs as a separate process (`docker exec`), so in-memory state would be invisible to it.
- A failed reload leaves previous secret files in place but the marker removed (unhealthy) — fail closed; the next successful SIGHUP restores readiness.
- A failed initial load exits non-zero — fail fast; dependent services never see a healthy sidecar.
- Reload removes only files written by the previous successful load that are absent from the new plan; the output dir is never swept — avoids deleting anything the sidecar did not create.
- `LOG_LEVEL` env var (`debug|info|warn|error`, default `info`) — §3 says key names are never logged "at default level", which implies a non-default level; at `debug` key names (never values) are logged.
- Version comes from `runtime/debug.ReadBuildInfo` (VCS-stamped module version) — avoids a package-level `var version` set by `-ldflags`.
- Image runs as non-root (distroless `static:nonroot`, uid 65532) by default; examples that chown files run the sidecar as `user: "0:0"` with `cap_drop: [ALL]` + `cap_add: [CHOWN, FOWNER]` — Docker does not grant added capabilities to non-root processes, and the canonical tmpfs (`mode=0711`) is root-owned.
- Postgres example uses the `postgres:*-alpine` image — uid/gid 70 in the map matches the alpine image's postgres user.
- File writes go through `os.Root` — kernel-enforced confinement to the output dir, so symlinks and `..` can't escape even under TOCTOU races.
- Built-in defaults are mode `0400` and owner "unchanged" (the sidecar's own uid/gid, no chown) — §3's `defaults: {uid: 0, gid: 0, …}` is map-file syntax; a built-in uid 0 would force a non-root sidecar to need `CHOWN` it can't have. A map file may set `defaults` explicitly.

## Explicitly not doing

- Templating, env-var injection, Vault/KMS/PGP backends, web UI, file-watch auto-reload, multiple input files, nested keys, subdirectories in output paths, dotenv/ini/binary SOPS formats, Shamir key groups.

## Review findings not adopted

(filled in during review rounds)
