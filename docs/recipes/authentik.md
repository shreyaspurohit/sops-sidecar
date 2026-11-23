# Recipe: authentik with its secrets from sops-sidecar

[authentik](https://goauthentik.io) is an identity provider. Its standard Compose setup puts three secrets in `.env`:
- the PostgreSQL password, shared by the database and authentik;
- authentik's secret key;
- an SMTP password.

`env_file` passes them to the containers as environment variables, so they show up in `docker inspect`. This recipe keeps them in one SOPS file and gives each container its own copy as a file on a tmpfs volume.

It is also an example of writing one key to several files: the database password is read by two images that run as different users.

## What has been tested

One run on a Linux x86_64 host (kernel 4.4) with Docker 24.0.2 and Compose v2.20.1. The stack used the released sops-sidecar v0.2.0 image, authentik 2026.8.6 (`ghcr.io/goauthentik/server`) and `postgres:16-alpine`. An existing authentik install with an initialized database was migrated.

| Check | Result |
|---|---|
| The sidecar goes healthy before postgres, the server and the worker start | Works |
| Secret files in postgres | `pg_pass` owned `70:70`, mode `0400` |
| Secret files in the authentik server and worker | `pg_pass_authentik`, `secret_key` and `email_password` owned `1000:1000`, mode `0400` |
| authentik with `file://` paths for the database password, secret key and email password | Logging in works, and `ak test_email` sends an email |
| Secret values in `docker inspect` of every container | None. Only the `file://` paths and `POSTGRES_PASSWORD_FILE` appear. |
| Building the SOPS file from the running container's environment, with the pipeline below | Each value has the same length as in the container |

## How it fits together

- **postgres** (`postgres:*-alpine`) reads the password as its `postgres` user, uid 70, through `POSTGRES_PASSWORD_FILE`.
- **authentik's server and worker** run as the image's `authentik` user, uid 1000. They read each setting from a file through authentik's `file://` prefix, which its [configuration docs](https://docs.goauthentik.io/install-config/configuration/) support for every setting.
- **The sidecar runs as root with only `CHOWN` and `FOWNER`.** It has to give files to two different owners, and changing a file's owner needs `CAP_CHOWN`, which Docker grants only to root. This is the same setup as [examples/postgres](../../examples/postgres).
- **The map file writes `pg_pass` twice**, once for each owner (a key in several entries needs sops-sidecar v0.2.0 or later).
- **The `secrets` volume** is tmpfs, so the decrypted values exist only in memory.

## Where the numbers come from

**uid 70, the `postgres` user.** The `postgres:*-alpine` images create a `postgres` user with uid and gid 70. The container starts as root and switches to that user before reading `POSTGRES_PASSWORD_FILE`, so the file must belong to 70. To check an image:

```sh
docker run --rm --entrypoint id postgres:16-alpine postgres
# uid=70(postgres) gid=70(postgres) groups=70(postgres),70(postgres)
```

Other postgres variants, such as the Debian-based images without `-alpine`, may use another uid, so check the image in use.

**uid 1000, the `authentik` user.** The `goauthentik/server` image sets `USER 1000`, so the server and the worker run as uid and gid 1000:

```sh
docker run --rm --entrypoint id ghcr.io/goauthentik/server:2026.8.6
# uid=1000(authentik) gid=1000(authentik) groups=1000(authentik)
```

For a running stack, `docker exec <container> id` shows the same.

**`age.key` owned by `0:0`, mode `0400`.**
- **Owner 0:** the sidecar runs as uid 0 (`user: "0:0"`). With `cap_drop: [ALL]` it has no `CAP_DAC_OVERRIDE` or `CAP_DAC_READ_SEARCH`, the capabilities that let root read any file. Inside the container, root reads a file only through its normal owner, group and mode bits, so the key must belong to uid 0.
- **Mode `0400`:** read for the owner, nothing for anyone else. The sidecar only reads the key, so the owner needs no write bit. Other users on the host get no access.

**Secret files, mode `0400`.** Each file belongs to the one user that reads it (70 or 1000), and only that user can read it. Nothing writes to them except the sidecar, which replaces them as root.

**The volume options, `o: "size=1m,mode=0711,nosuid,nodev,noexec"`.**

| Option | Meaning | Why |
|---|---|---|
| `size=1m` | The tmpfs holds at most 1 MiB, in memory | Secrets are small. The limit stops a mistake from filling the host's memory. |
| `mode=0711` | The folder's owner (root, the sidecar) can read, write and list it. Everyone else can only pass through it (`x`). | postgres and authentik can open a file whose name they know, such as `/run/secrets/pg_pass`, but they cannot list the folder to find the other secrets. `ls /run/secrets` in the authentik container gives "Permission denied". |
| `nosuid` | setuid and setgid bits on files are ignored | No file there can grant extra privileges |
| `nodev` | Device files do not work | No file there can open a device |
| `noexec` | Files cannot be run as programs | The folder holds data, never code |

There is no `uid=` option, so the folder belongs to root, which is the user the sidecar runs as. The [gluetun recipe](gluetun.md) runs the sidecar as uid 65532 instead and adds `uid=65532,gid=65532`.

On the tested host, `/proc/mounts` in the authentik container showed only `ro,relatime,size=1024k,mode=711` for the read-only mount. `nosuid,nodev,noexec` did not appear there. The secret files are created with mode `0400`, so none of them is executable or setuid.

## Map file

`secrets.map.yaml`:

```yaml
# uid/gid 70 is the postgres user in the postgres:*-alpine images.
# uid/gid 1000 is the authentik user in the goauthentik/server image.
files:
  - key: pg_pass
    uid: 70
    gid: 70
    mode: "0400"
  - key: pg_pass
    path: pg_pass_authentik
    uid: 1000
    gid: 1000
    mode: "0400"
  - key: secret_key
    uid: 1000
    gid: 1000
    mode: "0400"
  - key: email_password
    uid: 1000
    gid: 1000
    mode: "0400"
```

## Compose changes

These are the changes to authentik's standard Compose file: a new `secrets` service, additions to `postgresql`, `server` and `worker`, and a new `secrets` volume. Add the lines below to the existing services. Each service keeps its other settings: image, command, ports, other volumes and other environment variables.

```yaml
services:
  secrets:
    image: ghcr.io/shreyaspurohit/sops-sidecar:0.2.0@sha256:a59645a3fbfea2621d54dd79635d2535005acbc22f2d2b68b8c49568dbabaded
    user: "0:0"
    read_only: true
    network_mode: none
    cap_drop: [ALL]
    cap_add: [CHOWN, FOWNER]
    security_opt:
      - no-new-privileges:true
    restart: unless-stopped
    volumes:
      - /path/outside/project/age.key:/config/age.key:ro
      - ./secrets.sops.yaml:/config/secrets.sops.yaml:ro
      - ./secrets.map.yaml:/config/secrets.map.yaml:ro
      - secrets:/secrets
    healthcheck:
      test: ["CMD", "/sops-sidecar", "health"]
      interval: 2s
      timeout: 3s
      retries: 15

  postgresql:
    environment:
      POSTGRES_PASSWORD_FILE: /run/secrets/pg_pass   # replaces POSTGRES_PASSWORD
    volumes:
      - secrets:/run/secrets:ro
    depends_on:
      secrets:
        condition: service_healthy

  server:
    environment:
      AUTHENTIK_POSTGRESQL__PASSWORD: file:///run/secrets/pg_pass_authentik
      AUTHENTIK_SECRET_KEY: file:///run/secrets/secret_key
      AUTHENTIK_EMAIL__PASSWORD: file:///run/secrets/email_password
    volumes:
      - secrets:/run/secrets:ro
    depends_on:
      secrets:
        condition: service_healthy
      postgresql:
        condition: service_healthy

  worker:
    environment:
      AUTHENTIK_POSTGRESQL__PASSWORD: file:///run/secrets/pg_pass_authentik
      AUTHENTIK_SECRET_KEY: file:///run/secrets/secret_key
      AUTHENTIK_EMAIL__PASSWORD: file:///run/secrets/email_password
    volumes:
      - secrets:/run/secrets:ro
    depends_on:
      secrets:
        condition: service_healthy
      postgresql:
        condition: service_healthy

volumes:
  secrets:
    driver: local
    driver_opts:
      type: tmpfs
      device: tmpfs
      o: "size=1m,mode=0711,nosuid,nodev,noexec"
```

Then remove `PG_PASS`, `AUTHENTIK_SECRET_KEY` and `AUTHENTIK_EMAIL__PASSWORD` from `.env`, so that `env_file` no longer passes them to the containers.

**Existing databases keep their password.** postgres reads `POSTGRES_PASSWORD_FILE` only when it creates a new database. An initialized database keeps the password it already has, so the value in the SOPS file has to match it.

## Create the SOPS file from a running install

Take the values from the running authentik container. Compose interpolates `.env` before the containers see it, so the container's environment is the exact value authentik uses.

**Passwords that contain `$`.** In `.env`, a literal `$` is written `$$`, and Compose turns it back into `$` for the container. A SOPS value is written to its file byte for byte, so it must hold the single `$`. The pipeline below takes values from the container, so they are already unescaped. When typing a value into `sops edit` instead, write `$`, not `$$`.

The pipeline below runs on a workstation with the age and sops versions in `.tool-versions` and `jq`. It reads the container's environment over SSH, keeps only the three values, and encrypts them for two recipients: a personal key and this stack's key. The values stay in the pipe and are not written in plain text on either machine.

```sh
age-keygen -pq -o stack-age.key
ME=$(age-keygen -y ~/.config/sops/age/keys.txt)
STACK=$(age-keygen -y stack-age.key)

ssh <docker-host> 'docker exec <authentik-server-container> env -0' \
  | jq -Rs 'split("\u0000") | map(select(length > 0) | capture("^(?<k>[^=]+)=(?<v>.*)$"; "s"))
      | map({AUTHENTIK_POSTGRESQL__PASSWORD: "pg_pass", AUTHENTIK_SECRET_KEY: "secret_key",
             AUTHENTIK_EMAIL__PASSWORD: "email_password"}[.k] as $n | select($n) | {key: $n, value: .v})
      | from_entries' \
  | sops encrypt --age "$ME,$STACK" --input-type json --output-type yaml \
      --filename-override secrets.sops.yaml /dev/stdin > secrets.sops.yaml

SOPS_AGE_KEY_FILE=~/.config/sops/age/keys.txt sops decrypt --output-type json secrets.sops.yaml \
  | jq 'map_values(length)'
```

The last command prints only the length of each value. Compare them with the lengths in the container, for example `docker exec <authentik-server-container> sh -c 'printf %s "$AUTHENTIK_SECRET_KEY" | wc -c'`.

## Roll it out

1. Copy `stack-age.key` to the path the compose file mounts as `age.key`, then run `sudo chown 0:0 age.key && sudo chmod 0400 age.key` there. The owner is 0 because the sidecar runs as root with every capability but `CHOWN` and `FOWNER` dropped, so it reads the key through its owner bits.
2. Put `secrets.sops.yaml` and `secrets.map.yaml` in the project folder.
3. Check that the sidecar can decrypt and plan every entry before changing the stack:
   ```sh
   docker run --rm --user 0:0 --read-only --network none --cap-drop ALL --cap-add CHOWN --cap-add FOWNER \
     -v /path/outside/project/age.key:/config/age.key:ro \
     -v "$PWD/secrets.sops.yaml:/config/secrets.sops.yaml:ro" \
     -v "$PWD/secrets.map.yaml:/config/secrets.map.yaml:ro" \
     ghcr.io/shreyaspurohit/sops-sidecar:0.2.0 check
   ```
   It lists `email_password`, `pg_pass` twice and `secret_key`.
4. Apply the compose and `.env` changes, then run `docker compose up -d`. postgres, the server and the worker are recreated.
5. Log in, and send a test email with `docker exec <authentik-worker-container> ak test_email <address>`.
6. Delete the workstation copy of `stack-age.key`.

## Reboots

Docker restarts containers after a host reboot without waiting for `depends_on`. See [reboot ordering](../reboot-ordering.md) for how to start the stack in order at boot.
