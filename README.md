# sops-sidecar

[![CI](https://github.com/shreyaspurohit/sops-sidecar/actions/workflows/ci.yml/badge.svg)](https://github.com/shreyaspurohit/sops-sidecar/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go Reference](https://pkg.go.dev/badge/github.com/shreyaspurohit/sops-sidecar.svg)](https://pkg.go.dev/github.com/shreyaspurohit/sops-sidecar)

**`sops-sidecar` is a tiny container that decrypts one SOPS file and writes each secret as its own file into a memory-only (tmpfs) volume that neighboring containers mount read-only.**

Why it is different:

1. **Zero host install.** Only Docker and Compose are installed on the host, with no sops binary, plugin or systemd unit.
2. **Works on old Compose.** Uses only plain Compose features, down to Compose v2.20.0.
3. **Recreate-safe.** A long-running sidecar rather than a run-once init container, so the tmpfs volume stays populated when another container is recreated (for example by Watchtower or `docker compose up -d`).
4. **Secrets as files.** Pairs with the `*_FILE` convention (`POSTGRES_PASSWORD_FILE`, ...).
5. **Nothing secret in `docker inspect`.** Decrypted values exist only as files in the tmpfs volume.
6. **Per-file owner and mode.** Each secret file can be chowned and chmodded for the consuming container's user.
7. **Refuses bad input.** On a wrong key, a bad file, a SOPS file with no keys or a non-tmpfs output dir, it refuses, stays unhealthy and dependent services do not start.

Tested on Compose v2.20.0 and the latest release. It is meant for appliance builds that often ship older Compose. The postgres example has been run on one Synology NAS (DSM 7.4.1, x86_64, Container Manager's Docker 24.0.2 and Compose v2.20.1). The [recipe](docs/recipes/synology.md) lists what was covered.

See [docs/comparison.md](docs/comparison.md) for how this relates to other tools.

## Quick start: Postgres in five minutes

The quick start needs Docker with Compose, plus `age` 1.3 or newer and `sops`. Both are used only to create the encrypted file, and the sidecar needs neither on the host. The steps below were run with age 1.3.2 and sops 3.13.3.

```sh
cp -r examples/postgres my-stack && cd my-stack

# 1. Generate one age key and keep two copies of it. Keep both out of git.
#    - ~/.config/sops/age/keys.txt is the workstation copy: sops reads it by default to
#      encrypt and edit.
#    - age.key is the sidecar's: it must belong to the sidecar's uid (0 here;
#      65532 for examples/generic-app), because the sidecar drops every
#      capability and even root then cannot read a key file it does not own.
#      After the chown the workstation user cannot read age.key, which is why every
#      later command uses keys.txt. Both files hold the same key.
#    -pq makes a post-quantum hybrid key (ML-KEM-768 with X25519). SOPS files
#    tend to live in git for years, so protect them against "store now,
#    decrypt later". Classic keys (age-keygen without -pq) work too.
#    sops looks in ~/.config/sops/age/keys.txt by default on Linux but not on
#    macOS, so point it there explicitly.
export SOPS_AGE_KEY_FILE=~/.config/sops/age/keys.txt
mkdir -p ~/.config/sops/age
[ -e "$SOPS_AGE_KEY_FILE" ] || age-keygen -pq -o "$SOPS_AGE_KEY_FILE"
cp "$SOPS_AGE_KEY_FILE" age.key
sudo chown 0:0 age.key && sudo chmod 0400 age.key

# 2. Write the plaintext, encrypt it to the key's public half (the "age1..."
#    recipient, the same for both copies), delete the plaintext.
printf 'postgres_password: change-me\n' > plaintext.yaml
sops encrypt --age "$(age-keygen -y "$SOPS_AGE_KEY_FILE")" \
  --input-type yaml --output-type yaml plaintext.yaml > secrets.sops.yaml
rm plaintext.yaml
# The SOPS file and map file hold no plaintext. The sidecar must be able to read them.
chmod 0644 secrets.sops.yaml secrets.map.yaml

# 3. Start. The db container waits until the sidecar is healthy.
docker compose up -d

# 4. Check the login with the password from the secret file. Connect to the
#    container's own address, because the postgres image trusts loopback connections.
docker compose exec db sh -c 'PGPASSWORD="$(cat /run/secrets/postgres_password)" psql -h "$(hostname -i)" -U postgres -c "select 1"'
```

`compose.yaml` and `secrets.map.yaml` come from [examples/postgres](examples/postgres). A second example, [examples/generic-app](examples/generic-app), runs the sidecar as non-root with no capabilities for any image that reads a `*_FILE` variable.

### Change, add or remove a secret

The `sops` commands below read the workstation copy of the key, `$SOPS_AGE_KEY_FILE` (see step 1). The workstation user cannot read the root-owned `age.key`. They all rewrite the SOPS file in place, which keeps the single-file bind mount working (see "Single-file bind mounts follow the inode" in [Notes](#notes-1)). Signal the sidecar after each change.

```sh
# Open the decrypted file in $EDITOR. Change a value, add a line such as
# `api_token: ...`, or delete a line. sops re-encrypts on save.
sops edit secrets.sops.yaml

# Or, from a script, use --value-stdin. It reads a JSON string that jq builds
# from the raw value, so the value never shows up in the process list or history.
printf '%s' "$NEW_TOKEN" | jq -Rs . | sops set --value-stdin secrets.sops.yaml '["api_token"]'

docker compose kill -s HUP secrets
```

After adding `api_token`, the file looks like this. Every value is encrypted, and only the key names are readable:

```yaml
postgres_password: ENC[AES256_GCM,data:...,iv:...,tag:...,type:str]
api_token: ENC[AES256_GCM,data:...,iv:...,tag:...,type:str]
sops:
    age:
        - recipient: age1pq1...   # the key's public half
          enc: |
            -----BEGIN AGE ENCRYPTED FILE-----
            ...
            -----END AGE ENCRYPTED FILE-----
    lastmodified: "..."
    mac: ENC[...]   # covers all the values; the sidecar refuses a file that fails it
    version: 3.13.3
```

**A new secret needs two more things before an app can use it:**
1. **The map file.** With a map file, list the new key there. An unlisted key is ignored and counted in the log's `ignored` count. Without a map file, every top-level key is written with the defaults.
2. **The app.** Point it at the new file, for example `API_TOKEN_FILE: /run/secrets/api_token` in its `environment`. That changes the compose file, so run `docker compose up -d` afterwards. Compose recreates only the services whose configuration changed.

A removed key's file is deleted on the next reload.

### Add or replace an age key

Use this to give a second machine its own key, move from a classic key to a post-quantum one, or replace a key that is no longer trusted. A SOPS file can list several recipients, and any one of their keys decrypts it.

```sh
# 1. Create the new key next to the old one.
age-keygen -pq -o ~/.config/sops/age/new.txt

# 2. Add it as a recipient. Both keys now decrypt the file.
sops rotate -i --add-age "$(age-keygen -y ~/.config/sops/age/new.txt)" secrets.sops.yaml

# 3. Give the sidecar the new key, then reload. cp onto the existing file keeps
#    its inode, owner and mode, so the bind mount and the 0:0 0400 stay as they are.
sudo cp ~/.config/sops/age/new.txt age.key
docker compose kill -s HUP secrets

# 4. Once the sidecar is healthy, remove the old recipient and make the new key the workstation copy.
sops rotate -i --rm-age "$(age-keygen -y "$SOPS_AGE_KEY_FILE")" secrets.sops.yaml
mv ~/.config/sops/age/new.txt "$SOPS_AGE_KEY_FILE"
docker compose kill -s HUP secrets
```

`sops rotate` also generates a new data key, so the current file can't be opened with the removed key. **Older copies of the file**, in git history or backups, still can. If the old key may have leaked, change the secret values themselves too.

Each file is replaced atomically, so a reader never sees a partial file. The set of files is replaced file by file. The sidecar removes the ready marker when a reload starts and rewrites it when the reload finishes, so the marker reports only whether the last load completed at the moment it is checked. Files for keys that disappeared are removed.

### Notes

- The SOPS file and the map file must be readable by the sidecar's uid (`0644` is fine because neither holds plaintext).
- `age.key` must be owned by the sidecar's uid and mode `0400` (with `cap_drop: [ALL]` not even root can read a bind-mounted file it does not own). Keep a separate, readable copy of the key for `sops edit`. The sidecar reads `age.key` again on every reload, so a replaced key takes effect on the next `SIGHUP`.
- The output dir belongs to the sidecar. At startup it adopts the regular files already there, so the files of keys removed while it was stopped are deleted by the first load. Do not put anything else in that volume.
- The examples mount the tmpfs with `nosuid,nodev,noexec` in addition to the size and mode options. On one NAS (kernel 4.4, Docker 24.0.2) the app's read-only (`:ro`) mount of the volume showed none of these three flags, and the cause wasn't investigated. The sidecar's own writable mount keeps them. Secret file modes are capped at `0777`, so no setuid file can be written.

## Which example do I start from?

It depends on which user the app reads the secret file as. With the default mode `0400`, only the file's owner can read it.

1. **Find the app's uid.**
   - Check the image docs for "runs as user ...", `PUID`/`PGID`, or a note about `*_FILE` variables.
   - Run `docker image inspect -f '{{.Config.User}}' <image>`. An empty result means the image starts as root.
   - Some entrypoints start as root and switch to a service user before reading `*_FILE`. Postgres starts as root, then reads the password as uid 70. The image docs or the entrypoint script say which applies.
   - When unsure, start with generic-app. A "Permission denied" error on the secret file in the app's logs means one of the other setups is needed.
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

**Root for `chown`.** Giving a file to another user needs `CAP_CHOWN`, and Docker grants capabilities added with `cap_add` only to processes running as root. In every setup, `age.key` must belong to the uid the sidecar runs as, with mode `0400`, because with all capabilities dropped even root can read only files it owns.

## Configuration

Every default lives in this table. Paths must be absolute. An empty value is the same as unset.

| Variable | Default | Meaning |
|---|---|---|
| `SOPS_FILE` | `/config/secrets.sops.yaml` | The SOPS file (YAML, or JSON if the name ends in `.json`). Top-level scalar keys only. |
| `SOPS_AGE_KEY_FILE` | `/config/age.key` | The age key file. |
| `SECRETS_MAP` | `/config/secrets.map.yaml` (optional) | The map file. If this variable is set explicitly, the file must exist. |
| `OUTPUT_DIR` | `/secrets` | Where secret files are written. Must be tmpfs. |
| `ALLOW_NON_TMPFS` | `false` | Skip the tmpfs check. For tests only. Logs a warning. |
| `WIPE_ON_EXIT` | `false` | Remove written files and the ready marker on `SIGTERM`/`SIGINT`. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. Logs go to stderr as JSON, one object per line. Values are never logged at any level. |
| `SOPS_AGE_KEY` | not supported | Refused at startup because environment variables leak through `docker inspect`. Mount the key as a file. |
| *(file default)* mode | `0400` | Mode of each secret file when the map file sets none (map `defaults` or per-file `mode` override it). |
| *(file default)* owner | unchanged | Each secret file keeps the sidecar's uid and gid when the map file sets none (map `uid`/`gid` override it). |
| *(ready marker)* | `OUTPUT_DIR/.sops-sidecar-ready`, mode `0444` | Exists only while the last load succeeded. |

Without a map file, every top-level key becomes `OUTPUT_DIR/<key>` with the file defaults from the table. A SOPS file with no top-level keys is refused. On reload, the files from the last good load stay in place and the sidecar reports not ready.

### Map file

With a map file, **only listed keys** are written. A listed key missing from the SOPS file is an error. Unlisted keys are ignored, and only their count is logged.

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
| `sops-sidecar run` (default) | Decrypt, write, set the ready marker, then block. `SIGHUP` reloads. `SIGTERM`/`SIGINT` stop. |
| `sops-sidecar health` | Exit 0 only if the ready marker exists. Used by the Compose healthcheck. |
| `sops-sidecar check` | Decrypt and validate the inputs, print key names only, write nothing. |
| `sops-sidecar version` | Print the version. |

Exit codes: `0` success (`health`: ready), `1` failure (config, inputs, decrypt, write, not ready), `2` usage error.

## Notes

- **Secrets as files.** Point the app at the file (`POSTGRES_PASSWORD_FILE=/run/secrets/postgres_password`) and mount the secrets volume read-only.
- **Privileges.** The image runs as a non-root user (uid 65532) by default. It needs no capabilities while files stay owned by the sidecar's uid. Give the tmpfs volume `uid=65532,gid=65532` in its `o:` options so that user can write to it (see [examples/generic-app](examples/generic-app/compose.yaml)). When the map sets owners other than the sidecar's uid, run it as `user: "0:0"` with `cap_drop: [ALL]` and `cap_add: [CHOWN, FOWNER]` only. Docker gives added capabilities to root processes only, which is why root is needed there. See [examples/postgres/compose.yaml](examples/postgres/compose.yaml).
- **Numbers and booleans** are written in SOPS's string form (`True`/`False`). To get other text, quote the value in the YAML.
- **Single-file bind mounts follow the inode.** Rewrite the SOPS file in place (`sops edit`, `sops encrypt -i`) instead of replacing it with a new file. If the host path of a single-file bind mount does not exist, Docker creates a directory there.
- **Reboots.** Docker ignores `depends_on` when restarting containers after a host reboot. See [docs/reboot-ordering.md](docs/reboot-ordering.md).

## Documentation

- [Security model](docs/security-model.md) and [SECURITY.md](SECURITY.md)
- [Comparison with other tools](docs/comparison.md)
- [Reboot ordering](docs/reboot-ordering.md)
- [Architecture](ARCHITECTURE.md) and [decisions](DECISIONS.md)
- [Contributing](CONTRIBUTING.md) and [changelog](CHANGELOG.md)

## License

[MIT](LICENSE)
