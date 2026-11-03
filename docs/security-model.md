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
| Tampered input | The SOPS MAC is verified; a modified file is refused. |
| Network exposure | Decryption is in-process with an age key. No KMS or network calls; run it with `network_mode: none`. |
| Path tricks | Output goes through `os.Root`; file names are single path elements; names under the reserved `.sops-sidecar` prefix are rejected. Absolute paths, `..` and symlinks are refused. |
| Torn or partial state | Writes are atomic (temp file, fsync, chmod/chown, rename). The ready marker is removed before a reload and rewritten after. |
| Silent failure | It fails closed: any problem leaves the sidecar unhealthy, so dependents using `depends_on: condition: service_healthy` do not start. |

## What it does not protect

- **Root on the host.** Root can read any container's memory, mounts and files.
- **Anyone who can read the sidecar's `/proc` or memory, or `docker exec` into a container that mounts the volume.** Treat access to the Docker socket as access to the secrets.
- **The age key file.** It decrypts everything. Protect it with host file permissions, keep it out of git and out of backups, and do not place it in a shared or backed-up project folder.
- **Swap.** If the host swaps tmpfs pages to disk, plaintext can reach disk. Disable swap or use encrypted swap if that matters to you.
- **The consuming application.** Once a container reads a secret, what it does with it is up to that application.
- **Compromised images and the supply chain** beyond what signatures give you. Released images are signed with cosign keyless signing; verify before use:

```sh
cosign verify ghcr.io/shreyaspurohit/sops-sidecar:<tag> \
  --certificate-identity-regexp 'https://github.com/shreyaspurohit/sops-sidecar/.github/workflows/release.yml@refs/tags/v.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Pin images by digest where you can. To report a vulnerability, see [SECURITY.md](../SECURITY.md).
