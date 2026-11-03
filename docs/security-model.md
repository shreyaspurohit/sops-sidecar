# Security model

`sops-sidecar` decrypts one SOPS file and writes each secret as a file into a tmpfs volume. This page says what that protects and what it does not.

## What it protects

| Asset | How |
|---|---|
| Plaintext on disk | The output dir must be tmpfs (memory-backed); the sidecar verifies it in `/proc/mounts` and refuses otherwise. |
| Plaintext in git | Secrets are committed only as a SOPS file. |
| `docker inspect` | Secrets are files, not environment variables. `SOPS_AGE_KEY` is refused, so the key cannot be passed as an env var either. |
| Logs | Values are never logged or put in errors, at any level. Tests run every code path with a canary value and assert it never appears. |
| Over-broad access | Files default to mode `0400`; the map file sets owner and mode per file. Consumers mount the volume read-only. |
| Tampered input | The SOPS MAC is verified over every value; a modified file is refused. Files encrypted with `mac_only_encrypted` are refused, because that MAC leaves unencrypted values unauthenticated. |
| Network exposure | Decryption is in-process with an age key. No KMS or network calls; run it with `network_mode: none`. |
| Path tricks | Output goes through `os.Root`; file names are single path elements; names under the reserved `.sops-sidecar` prefix are rejected. Absolute paths, `..` and symlinks are refused. |
| Stale secrets after a restart | The output dir belongs to the sidecar: at startup it adopts the regular files already there and the first load deletes those whose keys are gone. If that first load fails, the adopted files stay but the sidecar exits and is never reported ready. |
| Torn or partial state | Each file is replaced atomically (temp file, fsync, chown/chmod, rename), so a reader never sees a partial file. The set is not transactional: the ready marker is removed before a reload and rewritten after, so it reports only that the last load completed at the moment it is checked. |
| Silent failure | It fails closed: any problem leaves the sidecar unhealthy, so dependents using `depends_on: condition: service_healthy` do not start. |

## What it does not protect

- **Root on the host.** Root can read any container's memory, mounts and files.
- **Anyone who can read the sidecar's `/proc` or memory, or `docker exec` into a container that mounts the volume.** Treat access to the Docker socket as access to the secrets.
- **The age key file.** It decrypts everything. Protect it with host file permissions: own it by the sidecar's uid (0 for the postgres example, 65532 for generic-app) with mode `0400`, since with all capabilities dropped not even root can read a bind-mounted key it does not own. Keep it out of git and out of backups, and do not place it in a shared or backed-up project folder.
- **Swap.** If the host swaps tmpfs pages to disk, plaintext can reach disk. Disable swap or use encrypted swap if that matters to you.
- **The CI and release toolchain beyond exact versions.** CI installs its tools from `.tool-versions` through asdf plugins, which are not pinned to a commit; the asdf action itself is SHA-pinned and every tool version is exact.
- **Whatever you mount into `/etc/sops`.** The sops library linked into the binary reads `/etc/sops/audit.yaml` at startup and, if that file configures an audit database, connects to it. The image ships no such file and the examples use `network_mode: none`; do not mount one.
- **The consuming application.** Once a container reads a secret, what it does with it is up to that application.
- **Compromised images and the supply chain** beyond what signatures give you. Released images are signed with cosign keyless signing; verify before use:

```sh
cosign verify ghcr.io/shreyaspurohit/sops-sidecar:<tag> \
  --certificate-identity-regexp 'https://github.com/shreyaspurohit/sops-sidecar/.github/workflows/release.yml@refs/tags/v.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Pin images by digest where you can. To report a vulnerability, see [SECURITY.md](../SECURITY.md).
