# Decisions

Each entry states the decision and its reason. Newest sections are at the bottom.

## Name check (2026-11-03)

- The name `sops-sidecar` is used. No real conflict was found:
  - Web search for `"sops-sidecar"` returned no SOPS-related results.
  - GitHub repository search (`https://api.github.com/search/repositories?q=sops-sidecar+in:name`) found only <https://github.com/JonasKop/argocd-sops-sidecar>, an Argo CD plugin sidecar config that uses the phrase incidentally.
  - GitHub user/org `sops-sidecar`: <https://github.com/sops-sidecar> returns 404 (free).
  - <https://github.com/shreyaspurohit/sops-sidecar> returned 404 at the time and is now this repository.
  - Docker Hub search (`https://hub.docker.com/v2/search/repositories/?query=sops-sidecar`) found no repository named `sops-sidecar` (only unrelated `*-sidecar` and `stagex/sops`).
  - GHCR: image lives under the maintainer's namespace `ghcr.io/shreyaspurohit/sops-sidecar`, so no global collision is possible.
  - pkg.go.dev search (<https://pkg.go.dev/search?q=sops-sidecar>) found no module with that name.

## Language and tooling

- Go (1.27.4, current stable per <https://go.dev/dl/?mode=json>). sops and age are Go libraries (in-process decrypt), CGO_ENABLED=0 gives a static binary for a distroless image, and Go is the ecosystem's language.
- All CLI tools pinned in `.tool-versions` and installed with asdf 0.20.2, so laptop and CI share one source of versions.
- `zizmor` has no asdf plugin, so it falls back to `uvx zizmor@<version>` with `uv` installed by asdf. The version is set once in `scripts/zizmor.sh` and tracked by a Renovate `customManagers` regex.
- `deadcode`, `govulncheck` and `staticcheck` are `tool` directives in `go.mod`, run with `go tool`, so they are project-local and version-pinned.
- `nodejs` in `.tool-versions` (24.x LTS, because Renovate's engine range is `^24.11`). It is needed only to run `renovate` and `renovate-config-validator` via `npx` for local verification. It stays on 24.x through a Renovate `allowedVersions` rule: with Renovate 44.239.1 on Node 26.13.1, `renovate-config-validator` warns "RE2 not usable, falling back to RegExp: regex validation may be inaccurate", and on Node 24 it loads RE2. Node 24 is supported until April 2028. Lift the rule once the validator loads RE2 on Node 26.
- Trivy is chosen over Grype for image scanning: one tool, an asdf plugin exists, and the Renovate asdf manager supports it.

## Design

- Decrypt drives sops' store/tree API directly instead of `decrypt.Data`. `decrypt.Data` resolves age identities from `SOPS_AGE_KEY*` env vars and `~/.config`, and sops' local keyservice rebuilds age keys without injected identities. The sidecar parses the age key file itself and calls `age.MasterKey.Decrypt` on each age key, so no KMS client is ever contacted (no network).
- Only a single SOPS key group is supported. Shamir and multi-group files are refused. This is the simplest correct behavior, and age is the only supported key type.
- The MAC is always verified, using the integrity check from sops' own `DataWithFormat`.
- Numbers and bools are written with `sops.ToBytes` (bools as `True`/`False`, floats in shortest `'f'` form), which is SOPS's own string form for those types. Quote values in YAML to get any other text.
- SOPS file format is chosen by extension: `.json` is JSON and anything else is YAML. This mirrors sops' own rule, and there is no format env var (YAGNI).
- Map `path` is a single flat file name (no `/`), optional, and defaults to the key. This removes subdirectory creation, ownership handling, and most path traversal and symlink risks.
- Names starting with `.sops-sidecar` are reserved for the sidecar's own files (marker, temp files) and refused as secret paths.
- Readiness state lives only in the ready marker file, because `health` runs as a separate process (`docker exec`) and could not see in-memory state.
- A failed reload leaves previous secret files in place but the marker removed (unhealthy), so it fails closed. The next successful SIGHUP restores readiness.
- A failed initial load exits non-zero, so dependent services never see a healthy sidecar.
- Reload removes files the sidecar manages (written or adopted at startup) that are absent from the new plan. Directories, symlinks, and reserved names are never touched.
- `LOG_LEVEL` env var (`debug|info|warn|error`, default `info`). §3 says key names are never logged "at default level", which implies a non-default level. At `debug` key names (never values) are logged.
- The version comes from `runtime/debug.ReadBuildInfo` (VCS-stamped module version), which avoids a package-level `var version` set by `-ldflags`.
- Image runs as non-root (distroless `static:nonroot`, uid 65532) by default. Examples that chown files run the sidecar as `user: "0:0"` with `cap_drop: [ALL]` and `cap_add: [CHOWN, FOWNER]`, because Docker does not grant added capabilities to non-root processes and the default tmpfs (`mode=0711`) is root-owned.
- The Postgres example uses the `postgres:*-alpine` image, whose postgres user is the uid/gid 70 in the map.
- File writes go through `os.Root`, which confines them to the output dir in the kernel, so symlinks and `..` cannot escape even under TOCTOU races.
- Built-in defaults are mode `0400` and owner "unchanged" (the sidecar's own uid/gid, no chown). §3's `defaults: {uid: 0, gid: 0, …}` is map-file syntax, and a built-in uid 0 would force a non-root sidecar to need `CHOWN`, which it cannot have. A map file may set `defaults` explicitly.

## Packaging and CI

- `asdf-vm/actions/install@v4.0.1` input `asdf_version: 0.20.2` (no `v` prefix). The action builds `releases/tags/v<ver>` and downloads the Go-based release binary, so asdf v0.16+ works. `asdf_branch` clones the legacy bash asdf and is not used.
- `ASDF_VERSION` is declared once per workflow file (env), each with a `# renovate:` annotation, because workflows cannot share variables.
- `syft` is in `.tool-versions` because GoReleaser needs it on PATH for SBOMs and runs it with a scrubbed env.
- The base image is distroless `static-debian13:nonroot`, the current distroless generation.
- Dockerfile has a `prebuilt` target used by GoReleaser and a default `runtime` target that compiles from source, so one Dockerfile shares the base pins.
- `.dockerignore` excludes `.git`, so `docker build .` images report version `(devel)`. Release images built by GoReleaser carry the tag version.
- Integration job names use roles (`minimum`/`latest`) instead of versions, so required-check names stay stable across Renovate bumps.
- An extra `workflow-lint` CI job (actionlint, shellcheck, zizmor) validates workflows on every PR.
- Renovate re-enables only the three `go tool` modules among indirect gomod deps, which avoids a PR per transitive sops dependency.
- Release attests both `dist/checksums.txt` and the pushed image digest with `actions/attest-build-provenance`.
- `renovate-config.yml` pins the renovate npm version once with a `# renovate:` annotation, and the validator runs without a filename (repo mode).
- Renovate `ignorePaths` is overridden to only `node_modules`/`vendor`, because `config:best-practices` ignores `**/examples/**`, which would freeze the examples' digest-pinned images. The examples' reference to this project's own image (major tag) is excluded on purpose.
- The integration tests read the busybox image from `examples/generic-app/compose.yaml`, so there is one pin in one place, tracked by Renovate.

## Code review (lead)

- `deadcode` exception: the six redaction methods (`decrypt.Value.{String,GoString,Format,MarshalText,LogValue}`, `decrypt.Identities.Format`) are unreachable by design. They exist so a value or age identity can never be printed, logged, or JSON-encoded by mistake. `scripts/deadcode.sh` allows exactly these and fails on anything else.
- The `shutting down` log has no `signal` attribute, because `signal.NotifyContext` does not expose which signal fired and capturing it would need an extra goroutine.
- sops' own logrus loggers are discarded at startup (`decrypt.DisableSOPSLogs`). They write outside the sidecar's log level and can echo file content (for example an unencrypted comment), and all failures are reported through the sidecar's own errors.
- A panic inside sops' tree decryption (crafted `type:` metadata) is recovered and reported as invalid ciphertext. The pinned sops cipher panics before the MAC check, and recovering at that single boundary keeps "return errors, don't panic".
- Leftover `.sops-sidecar-tmp-*` files are removed at startup, because a killed write can leave a plaintext temp file and names under the reserved prefix belong only to the sidecar. The security review supersedes this: regular files already in the output dir are adopted at startup (`writer.Dir.Names`), so the first load removes files of keys that were dropped while the sidecar was stopped. Without this, a revoked secret would stay served and reported ready after a restart. The output dir belongs to the sidecar (documented).
- uid/gid are limited to `0..4294967294` (`writer.MaxID`), because larger values would truncate or hit chown's "unchanged" sentinel.
- Errors are wrapped once, by the package that knows the context (`writer`, `health`). `cmd` passes them through without re-prefixing.

## Security review

- Accepted:
  - adopt existing output files at startup (stale secrets after a restart);
  - refuse `mac_only_encrypted` SOPS files (unencrypted values would be unauthenticated);
  - chown before chmod;
  - examples mount the tmpfs `nosuid,nodev,noexec` (beyond the options in §3, which remain a valid minimum).

## Explicitly not doing

- Templating, env-var injection, Vault/KMS/PGP backends, web UI, file-watch auto-reload, multiple input files, nested keys, subdirectories in output paths, dotenv/ini/binary SOPS formats, Shamir key groups.

## Review findings not adopted

- Round 1, Codex #7 (sops `audit` package `init` reads `/etc/sops/audit.yaml` and may connect to PostgreSQL). The package is linked transitively by sops and cannot be excluded without forking. In the published image that file cannot exist unless an operator deliberately mounts it, and the examples run with `network_mode: none`. Documented in docs/security-model.md.
- Round 1, Claude #10 (Lstat-then-Remove race in `writer.Dir.Remove`). All operations are confined by `os.Root`. The worst case is removing an empty directory someone swapped in inside the output dir, which needs write access to the tmpfs and so already implies full control of the secrets.
- Round 1, Claude #1 (`docker_signs.artifacts: ""` never signs). Adopted at first, then reverted: the GoReleaser docs (customization/docker_sign) state that `''` is the default and means "images built by dockers_v2", which is exactly what this config builds. A comment in `.goreleaser.yaml` records this.
- Round 3, Claude #3 (no test pins chown-before-chmod). The order is only observable as root with a foreign gid, and a seam in the writer for it would cost more than it proves. The order is fixed in `writeAll` with a comment stating why.
- Security S4 (release job runs asdf-installed tools from unpinned plugin repos with signing rights). The spec mandates installing CI tools from `.tool-versions` via `asdf-vm/actions/install`. The action itself is SHA-pinned and tool versions are exact. Residual risk documented in docs/security-model.md.
- Security S5 (Renovate automerges non-major updates, including sops/age and base digests). The spec requires automerge for patch/minor/digest/pin after required checks pass, and the 3-day minimum release age and the CI gates apply.
- Security S6 (any `v*` tag push publishes). This is a repository setting, and the maintainer steps include a tag protection rule for `v*`.
- Security S7 (examples use the floating `:0` sidecar tag). The tag is floating so users receive fixes. docs/security-model.md advises digest pinning and shows `cosign verify`.
- Security S9–S12 (informational: partial set after ENOSPC is reported not ready; no memory zeroing/core-dump limit; sops audit init; compose checksum from the same release; zizmor not hash-pinned). These are accepted as documented limitations.
- Round 1, Codex #6 (part). Non-regular input files are now refused, but input reads are not made cancellable via context, because with only regular files accepted a read cannot block indefinitely.

## Release attestations skipped on private repos

The two `actions/attest-build-provenance` steps in `release.yml` run only when the repository is public (`!github.event.repository.private`). GitHub offers artifact attestations for private repositories only on Enterprise Cloud, so a release from the private pre-launch repo would otherwise fail after the release and image were already published. cosign keyless signing still runs either way.

## Scheduled image scan skipped on private repos

The weekly `trivy (latest published image)` job in `scheduled.yml` runs only when the repository is public. Before launch there is no published image, and a private GHCR package can't be pulled with the workflow's token, so the job failed every week with an access error (Claude round 1 #11, previously accepted as expected). It starts running when the repo goes public, and `v0.1.0` is tagged right after that. Until then the `docker` job in CI still scans every freshly built image.

## Library review (November 2026)

Every non-test Go file was reviewed for code that a maintained, open-source library or the standard library already provides.

- **Replaced:** the `/proc/mounts` parser (octal unescaping and longest-prefix mount matching) with `unix.Fstatfs` from `golang.org/x/sys` (BSD-3-Clause, maintained by the Go team, already in the module graph through sops). The kernel reports the file system of the opened output dir directly, which also removes any path-matching ambiguity.
- **Replaced:** the hand-written octal check in `writer.ParseMode` with `strconv.ParseUint(s, 8, 32)`, which already rejects non-octal digits, signs, prefixes and underscores.
- **Atomic writes in `writer.Dir`: kept for now, to be re-evaluated.** The original reasons were partly wrong. `google/renameio/v2` v2.0.2 (Apache-2.0) does support `WithRoot(*os.Root)`, and its `PendingFile` exposes `Chown` before `CloseAtomicallyReplace`. What it still doesn't provide here: temp files under the reserved `.sops-sidecar-tmp-` prefix (the startup sweep relies on it to delete leftovers that may hold secrets, and renameio names temp files after the target), and refusing to replace a non-regular target (this could be checked before the rename). Switching is a maintainer decision.
- **Kept: `sops/decrypt.Data` is not used** (see Design above).
- **Kept: the environment config reader.** `caarlos0/env` (MIT) would replace about 40 lines, but the absolute-path checks, "map file set explicitly" and the `SOPS_AGE_KEY` refusal would still be custom.
- **Kept: the `LOG_LEVEL` parsing.** `slog.Level.UnmarshalText` also accepts offsets such as `info+1`, which the config contract deliberately refuses.
- **Kept: the CLI dispatch.** Four subcommands with no flags do not justify cobra or urfave/cli.

## Empty plans are refused

A load that yields no files (a SOPS file with no top-level keys) is an error, `config.ErrNoSecrets`, not an empty plan. Applying an empty plan would remove every secret file as stale and then set the ready marker, so consumers would start healthy with no secrets. `Map.Plan` refuses it, and `apply` checks again just before writing, so that no future change to `load` can reintroduce that failure. Deliberately empty SOPS files are not a supported use case. The value-type `Plan` is kept (zero value plus error, Go convention). The guard sits where the damage would happen instead of in a `*Plan`/`nil` return.

## Appliance testing claim corrected (November 2026)

Round 1, Claude #12 flagged that the README named NAS and Portainer appliance builds as tested. The finding was first rejected because the wording came from the spec. It was later adopted. The README states only what was tested: Compose v2.20.0 and latest, plus the one NAS run recorded in the recipe.

## Post-quantum age keys recommended

The docs generate keys with `age-keygen -pq`, a hybrid of ML-KEM-768 and X25519. SOPS files are meant to be committed and copied, which is the "store now, decrypt later" case a hybrid key protects against. `decrypt.ParseIdentities` already uses `age.ParseIdentities`, which reads hybrid identities in age v1.3+, and sops v3.13.3 encrypts to `age1pq1` recipients. `TestDecryptHybridIdentities` covers this, including key files that mix both kinds. Costs:
- users need age 1.3+ to create the key;
- the recipient is about 2,000 characters.

Classic keys keep working.

## Docs set `SOPS_AGE_KEY_FILE` explicitly

On macOS, sops looks for `keys.txt` under `~/Library/Application Support/sops/age/` unless `XDG_CONFIG_HOME` is set (`getUserConfigDir` in sops v3.13.3), so the earlier "sops reads ~/.config/sops/age/keys.txt by default" failed there. The README and examples now export `SOPS_AGE_KEY_FILE` before using sops on the workstation. This is the host-side sops variable. The sidecar's own `SOPS_AGE_KEY_FILE` is set inside the container, and `SOPS_AGE_KEY` stays refused.

## CI skips docs-only changes

A `changes` job in `ci.yml` uses `dorny/paths-filter` (v4.0.3, MIT, pinned by SHA) to find out whether anything changed outside Markdown, `docs/`, the `*.example` walkthroughs and `LICENSE`. When nothing did, the other jobs are skipped through their `if:`, and GitHub counts a skipped job as passing a required check.
- **Why not `paths-ignore`:** it skips the whole workflow, which leaves the required checks waiting and blocks the merge.
- **The integration matrix is gated per step:** a matrix job skipped at job level reports a single check with the unexpanded name (`integration (compose ${{ matrix.role }})`), so the two required integration checks would never report.
- **If the `changes` job fails,** every gated job runs.
- **What it runs on:** the scheduled workflow and the release workflow are ungated.

## cosign is installed by sigstore/cosign-installer, not asdf

The first `v0.1.0` release run failed at signing with `exec: "cosign": executable file not found`. The asdf cosign plugin verifies the downloaded binary with the older `.sig` and public-key scheme. That check fails for cosign 3.x, and the plugin then deletes the binary and still exits 0. CI never signs, so only the release job hit it.
- **Fix:** `release.yml` installs cosign with `sigstore/cosign-installer`, pinned by SHA. The action checks the binary against SHA-256 hashes stored in the action itself. The cosign version is pinned in the step and tracked by Renovate.
- **cosign left `.tool-versions`,** because the plugin cannot install it. It is needed locally only to verify a release.
- **Nothing was published** by the failed run: no release, image, signature or transparency-log entry.

## CI reuses the pull request's run on main, and caches Go builds

The `main` ruleset requires an up-to-date branch and squash merges, so a merged commit has the same tree as the pull request head that CI already tested.
- **Reuse:** on a push to `main`, the `changes` job finds the merged pull request through the API and compares the two trees. When they match and the pull request's CI run concluded `success`, the other jobs are skipped and `main` still gets a passing run on its exact commit. A missing pull request, a different tree, an unfinished or failed run, or an API error runs every job.
- **Go cache:** `actions/cache` (v6.1.0, MIT, pinned by SHA) holds `GOMODCACHE` and `GOCACHE`, keyed by `.tool-versions` and `go.sum`. Only the `go-cache` job on `main` saves it, after compiling what the other jobs compile. Pull requests can restore caches saved on `main` but not each other's, so `main` keeps them current even when its other jobs are skipped, and no pull request writes a cache. The release workflow uses no cache.
- **Integration image:** the integration jobs compile the binary on the runner, using the cache, and package it with the Dockerfile's `prebuilt` stage, which is how release images are built. The `docker` job still builds the full Dockerfile for both architectures.
- **Local actions use `./`,** and zizmor's `self-repository` audit is off in `.github/zizmor.yml`. That audit asks for GitHub's `$/` syntax (July 2026), which actionlint 1.7.12 rejects. Revisit when actionlint supports it.

