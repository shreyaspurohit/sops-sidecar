# sops-sidecar

[![CI](https://github.com/shreyaspurohit/sops-sidecar/actions/workflows/ci.yml/badge.svg)](https://github.com/shreyaspurohit/sops-sidecar/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go Reference](https://pkg.go.dev/badge/github.com/shreyaspurohit/sops-sidecar.svg)](https://pkg.go.dev/github.com/shreyaspurohit/sops-sidecar)

**`sops-sidecar` is a tiny container that decrypts one SOPS file and writes each secret as its own file into a memory-only (tmpfs) volume that neighboring containers mount read-only.**

Why it is different:

1. **Zero host install.** Only Docker and Compose. No sops binary, no plugin, no systemd unit required.
2. **Works on old Compose.** Plain Compose features only, tested on Compose v2.20.0 and latest.
3. **Recreate-safe.** A long-running sidecar, not a run-once init container, so the tmpfs volume stays populated when another container is recreated (for example by Watchtower or `docker compose up -d`).
4. **Files, not env vars.** Pairs with the `*_FILE` convention (`POSTGRES_PASSWORD_FILE`, ...). Nothing secret shows in `docker inspect`.
5. **Per-file owner and mode.** Each secret file can be chowned and chmodded for the consuming container's user.
6. **Fails closed.** Wrong key, bad file, or a non-tmpfs output dir: it refuses, stays unhealthy, and dependent services do not start.

Tested on Compose v2.20+, including appliance builds such as Synology Container Manager and Portainer.

See [docs/comparison.md](docs/comparison.md) for how this relates to other tools.

## Quick start: Postgres in five minutes

You need Docker with Compose, plus `age` and `sops` (only to create the encrypted file; the sidecar itself needs neither on the host).

```sh
cp -r examples/postgres my-stack && cd my-stack

# 1. Generate an age key. Keep it out of git.
age-keygen -o age.key && chmod 600 age.key

# 2. Write the plaintext, encrypt it to your key, delete the plaintext.
printf 'postgres_password: change-me\n' > plaintext.yaml
sops encrypt --age "$(age-keygen -y age.key)" \
  --input-type yaml --output-type yaml plaintext.yaml > secrets.sops.yaml
rm plaintext.yaml

# 3. Start. The db container waits until the sidecar is healthy.
docker compose up -d

# 4. Check the login (the password is read from the secret file).
docker compose exec db sh -c 'PGPASSWORD="$(cat /run/secrets/postgres_password)" psql -h 127.0.0.1 -U postgres -c "select 1"'
```

`compose.yaml` and `secrets.map.yaml` are the files you copied from [examples/postgres](examples/postgres). A second example, [examples/generic-app](examples/generic-app), runs the sidecar as non-root with no capabilities for any image that reads a `*_FILE` variable.

### Reload

Edit the SOPS file in place, then signal the sidecar:

```sh
SOPS_AGE_KEY_FILE=age.key sops edit secrets.sops.yaml
docker compose kill -s HUP secrets
```

Outputs are replaced atomically; files for keys that disappeared are removed.

## Configuration

Every default lives in this table. Paths must be absolute. An empty value is the same as unset.

| Variable | Default | Meaning |
|---|---|---|
| `SOPS_FILE` | `/config/secrets.sops.yaml` | The SOPS file (YAML, or JSON if the name ends in `.json`). Top-level scalar keys only. |
| `SOPS_AGE_KEY_FILE` | `/config/age.key` | The age key file. |
| `SECRETS_MAP` | `/config/secrets.map.yaml` (optional) | The map file. If you set this variable explicitly, the file must exist. |
| `OUTPUT_DIR` | `/secrets` | Where secret files are written. Must be tmpfs. |
| `ALLOW_NON_TMPFS` | `false` | Skip the tmpfs check. For tests only; logs a warning. |
| `WIPE_ON_EXIT` | `false` | Remove written files and the ready marker on `SIGTERM`/`SIGINT`. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. Values are never logged at any level. |
| `SOPS_AGE_KEY` | not supported | Refused at startup: environment variables leak through `docker inspect`. Mount the key as a file. |

Without a map file, every top-level key becomes `OUTPUT_DIR/<key>` with the built-in file defaults: **mode `0400`, owner unchanged** (the sidecar's uid and gid). The ready marker, `OUTPUT_DIR/.sops-sidecar-ready` (mode `0444`), exists only while the last load succeeded.

### Map file

With a map file, **only listed keys** are written. A listed key missing from the SOPS file is an error; unlisted keys are ignored (only their count is logged).

```yaml
defaults: {uid: 0, gid: 0, mode: "0400"}   # all optional; override the built-in defaults
files:
  - key: postgres_password   # top-level key in the SOPS file (required)
    path: postgres_password  # file name in OUTPUT_DIR; defaults to key
    uid: 70                  # optional; inherits defaults
    gid: 70
    mode: "0400"
```

## Commands and exit codes

| Command | Does |
|---|---|
| `sops-sidecar run` (default) | Decrypt, write, set the ready marker, then block. `SIGHUP` reloads; `SIGTERM`/`SIGINT` stop. |
| `sops-sidecar health` | Exit 0 only if the ready marker exists. Used by the Compose healthcheck. |
| `sops-sidecar check` | Decrypt and validate the inputs, print key names only, write nothing. |
| `sops-sidecar version` | Print the version. |

Exit codes: `0` success (`health`: ready), `1` failure (config, inputs, decrypt, write, not ready), `2` usage error.

## Notes

- **Files, not env vars.** Point the app at the file (`POSTGRES_PASSWORD_FILE=/run/secrets/postgres_password`) and mount the secrets volume read-only.
- **Privileges.** The image runs as a non-root user by default, which needs no capabilities while files stay owned by the sidecar's uid. When the map sets owners other than the sidecar's uid, run it as `user: "0:0"` with `cap_drop: [ALL]` and `cap_add: [CHOWN, FOWNER]` only. Docker gives added capabilities to root processes only, which is why root is needed there. See [examples/postgres/compose.yaml](examples/postgres/compose.yaml).
- **Numbers and booleans** are written in SOPS's string form (`True`/`False`). To get other text, quote the value in the YAML.
- **Single-file bind mounts follow the inode.** Rewrite the SOPS file in place (`sops edit`, `sops encrypt -i`); do not replace it with a new file. If the host path of a single-file bind mount does not exist, Docker creates a directory there.
- **Reboots.** Docker ignores `depends_on` when restarting containers after a host reboot; see [docs/reboot-ordering.md](docs/reboot-ordering.md).

## Documentation

- [Security model](docs/security-model.md) and [SECURITY.md](SECURITY.md)
- [Comparison with other tools](docs/comparison.md)
- [Reboot ordering](docs/reboot-ordering.md)
- [Architecture](ARCHITECTURE.md) and [decisions](DECISIONS.md)
- [Contributing](CONTRIBUTING.md) and [changelog](CHANGELOG.md)

## License

[MIT](LICENSE)
