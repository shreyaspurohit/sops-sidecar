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
6. **Fails closed.** Wrong key, bad file, a SOPS file with no keys, or a non-tmpfs output dir: it refuses, stays unhealthy, and dependent services do not start.

Tested on Compose v2.20.0 and the latest release. It is meant for appliance builds that often ship older Compose. The postgres example has been run on one Synology NAS (DSM 7.4.1, x86_64, Container Manager's Docker 24.0.2 and Compose v2.20.1; see the [recipe](docs/recipes/synology.md) for what was and wasn't covered). Portainer is untested.

See [docs/comparison.md](docs/comparison.md) for how this relates to other tools.

## Quick start: Postgres in five minutes

You need Docker with Compose, plus `age` and `sops` (only to create the encrypted file; the sidecar itself needs neither on the host).

```sh
cp -r examples/postgres my-stack && cd my-stack

# 1. Generate one age key and keep two copies of it. Keep both out of git.
#    - ~/.config/sops/age/keys.txt is yours: sops reads it by default to
#      encrypt and edit.
#    - age.key is the sidecar's: it must belong to the sidecar's uid (0 here;
#      65532 for examples/generic-app), because the sidecar drops every
#      capability and even root then cannot read a key file it does not own.
#      After the chown you can't read age.key yourself, which is why every
#      later command uses keys.txt. Both files hold the same key.
mkdir -p ~/.config/sops/age
[ -e ~/.config/sops/age/keys.txt ] || age-keygen -o ~/.config/sops/age/keys.txt
cp ~/.config/sops/age/keys.txt age.key
sudo chown 0:0 age.key && sudo chmod 0400 age.key

# 2. Write the plaintext, encrypt it to the key's public half (the "age1..."
#    recipient, the same for both copies), delete the plaintext.
printf 'postgres_password: change-me\n' > plaintext.yaml
sops encrypt --age "$(age-keygen -y ~/.config/sops/age/keys.txt)" \
  --input-type yaml --output-type yaml plaintext.yaml > secrets.sops.yaml
rm plaintext.yaml
# The SOPS file and map file hold no plaintext; the sidecar must be able to read them.
chmod 0644 secrets.sops.yaml secrets.map.yaml

# 3. Start. The db container waits until the sidecar is healthy.
docker compose up -d

# 4. Check the login with the password from the secret file. Connect to the
#    container's own address: the postgres image trusts loopback connections.
docker compose exec db sh -c 'PGPASSWORD="$(cat /run/secrets/postgres_password)" psql -h "$(hostname -i)" -U postgres -c "select 1"'
```

`compose.yaml` and `secrets.map.yaml` are the files you copied from [examples/postgres](examples/postgres). A second example, [examples/generic-app](examples/generic-app), runs the sidecar as non-root with no capabilities for any image that reads a `*_FILE` variable.

### Reload

Edit the SOPS file in place, then signal the sidecar:

```sh
sops edit secrets.sops.yaml   # uses ~/.config/sops/age/keys.txt; age.key is root-owned and unreadable to you
docker compose kill -s HUP secrets
```

Each file is replaced atomically, so a reader never sees a partial file. The set of files is not replaced as a unit: the ready marker is removed when a reload starts and rewritten when it finishes, so it reports only that the last load completed at the moment it is checked. Files for keys that disappeared are removed.

### Notes

- The SOPS file and the map file must be readable by the sidecar's uid (`0644` is fine: neither holds plaintext).
- `age.key` must be owned by the sidecar's uid and mode `0400` (with `cap_drop: [ALL]` not even root can read a bind-mounted file it does not own). Keep a separate, readable copy of the key for `sops edit`.
- The output dir belongs to the sidecar. At startup it adopts the regular files already there, so the files of keys removed while it was stopped are deleted by the first load. Do not put anything else in that volume.
- The examples mount the tmpfs with `nosuid,nodev,noexec` in addition to the size and mode options. On one NAS (kernel 4.4, Docker 24.0.2) the app's read-only (`:ro`) mount of the volume showed none of these three flags; the cause wasn't investigated. The sidecar's own writable mount keeps them, and secret file modes are capped at `0777`, so no setuid file can be written.

## Which example do I start from?

It depends on which user your app reads the secret file as. With the default mode `0400`, only the file's owner can read it.

1. **Find the app's uid.**
   - Check the image docs for "runs as user ...", `PUID`/`PGID`, or a note about `*_FILE` variables.
   - Run `docker image inspect -f '{{.Config.User}}' <image>`. An empty result means the image starts as root.
   - Some entrypoints start as root and switch to a service user before reading `*_FILE`. Postgres starts as root, then reads the password as uid 70. The docs or the entrypoint script tell you which applies.
   - When unsure, start with generic-app. A "Permission denied" error on the secret file in the app's logs means you need one of the other setups.
2. **Pick the setup.**

| The app reads secrets as | Start from | Sidecar runs as |
|---|---|---|
| root (uid 0), or uid 65532 | [examples/generic-app](examples/generic-app) | `65532:65532`, no capabilities |
| one other non-root uid, e.g. 70 or 1000 | generic-app, with the uid changed (see below) | that uid, no capabilities |
| several different uids from one sidecar | [examples/postgres](examples/postgres), with `uid`/`gid` per file in the map | `0:0` with only `CHOWN` and `FOWNER` |

Root in an ordinary container can read a `0400` file it does not own, because Docker's default capabilities include `CAP_DAC_OVERRIDE`. That is why a root app works with generic-app as it is. It stops working if the app's container drops capabilities.

**Running the sidecar as the app's uid.** When every consumer uses the same non-root uid, run the sidecar as that uid. The files are then owned by it with no `chown` and no root. Starting from generic-app, with uid 70 as an example:

- set `user: "70:70"` on the sidecar;
- put `uid=70,gid=70` in the tmpfs volume's `o:` options;
- make `age.key` owned by `70:70` with mode `0400`;
- leave `uid`/`gid` out of the map file.

This setup has been tested with the postgres image. The postgres example itself uses the root setup instead, to show per-file ownership.

**Why the root setup needs root.** Giving a file to another user needs `CAP_CHOWN`, and Docker grants capabilities added with `cap_add` only to processes running as root. In every setup, `age.key` must belong to the uid the sidecar runs as, with mode `0400`: with all capabilities dropped, even root can read only files it owns.

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
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. Logs go to stderr as JSON, one object per line. Values are never logged at any level. |
| `SOPS_AGE_KEY` | not supported | Refused at startup: environment variables leak through `docker inspect`. Mount the key as a file. |
| *(file default)* mode | `0400` | Mode of each secret file when the map file sets none (map `defaults` or per-file `mode` override it). |
| *(file default)* owner | unchanged | Each secret file keeps the sidecar's uid and gid when the map file sets none (map `uid`/`gid` override it). |
| *(ready marker)* | `OUTPUT_DIR/.sops-sidecar-ready`, mode `0444` | Exists only while the last load succeeded. |

Without a map file, every top-level key becomes `OUTPUT_DIR/<key>` with the file defaults from the table. A SOPS file with no top-level keys is refused. On reload, the files from the last good load stay in place and the sidecar reports not ready.

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
- **Privileges.** The image runs as a non-root user (uid 65532) by default, which needs no capabilities while files stay owned by the sidecar's uid; give the tmpfs volume `uid=65532,gid=65532` in its `o:` options so that user can write to it (see [examples/generic-app](examples/generic-app/compose.yaml)). When the map sets owners other than the sidecar's uid, run it as `user: "0:0"` with `cap_drop: [ALL]` and `cap_add: [CHOWN, FOWNER]` only. Docker gives added capabilities to root processes only, which is why root is needed there. See [examples/postgres/compose.yaml](examples/postgres/compose.yaml).
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
