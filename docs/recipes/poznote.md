# Recipe: poznote, an app that reads secrets only from environment variables

[poznote](https://github.com/timothepoznanski/poznote) is a note-taking app. It reads its OIDC client ID and client secret from the environment variables `POZNOTE_OIDC_CLIENT_ID` and `POZNOTE_OIDC_CLIENT_SECRET`. It has no `_FILE` variant and no `file://` prefix, so a secret file alone cannot reach it.

This recipe covers any image like that. A short entrypoint wrapper reads the secret files that sops-sidecar wrote, exports them as environment variables inside the container, and then starts the image as usual. The app sees the variables it expects, and `docker inspect` shows only the wrapper script, which holds file paths and no values.

## What has been tested

One run on a Linux x86_64 host (kernel 4.4) with Docker 24.0.2 and Compose v2.20.1, using the released sops-sidecar v0.2.0 image and poznote 6.156.2 (`ghcr.io/timothepoznanski/poznote:6`).

| Check | Result |
|---|---|
| The sidecar goes healthy before poznote starts | Works |
| The secret files in the poznote container | Owned `65532:65532`, mode `0400`, on a read-only tmpfs mount |
| The values in poznote's main process (supervisord) after the wrapper ran | Both present, with the same lengths as the files |
| `POZNOTE_OIDC_*` in `docker inspect` of poznote | None |
| Signing in to poznote through an OIDC provider (authentik) | Works |
| poznote's PHP code, through nginx and php-fpm, reading both values (run with dummy values) | Reads them, including a value with a `$` |
| A secret file missing when the container starts | The wrapper exits with status 1 before poznote starts |
| Editing the SOPS file, `docker compose kill -s HUP secrets`, then `docker compose restart webserver` (run with dummy values) | poznote uses the new value after the restart. After the HUP alone, it keeps the old one. |

## Why a wrapper is needed

There are three ways to hand a secret to a container:

| Method | Where the value ends up | Example |
|---|---|---|
| `environment:` or `env_file:` | In the container's configuration, so `docker inspect` shows it | poznote's standard setup |
| A `_FILE` variable or `file://` prefix pointing at a secret file | Only in the file. `docker inspect` shows the path. | postgres, authentik |
| A variable exported by a process inside the container | Only in that process and its children | This recipe |

poznote's configuration code reads a setting from `$_ENV`, then from `getenv()`, then falls back to a default. Only the third method keeps the value out of `docker inspect` for it. Support for `_FILE` variables is requested in [poznote#1814](https://github.com/timothepoznanski/Poznote/issues/1814). Once poznote supports them, the secret files can be passed directly and the wrapper is no longer needed.

## How the wrapper works

```yaml
    entrypoint:
      - /bin/sh
      - -c
      - |
        set -e
        POZNOTE_OIDC_CLIENT_ID=$$(cat /run/secrets/oidc_client_id)
        POZNOTE_OIDC_CLIENT_SECRET=$$(cat /run/secrets/oidc_client_secret)
        export POZNOTE_OIDC_CLIENT_ID POZNOTE_OIDC_CLIENT_SECRET
        exec docker-php-entrypoint "$$@"
      - --
    command: ["/bin/sh", "-c", "/usr/local/bin/init.sh && exec /usr/bin/supervisord -c /etc/supervisor/conf.d/supervisord.conf"]
```

Line by line:

- **`/bin/sh -c '<script>'`** runs the script with the image's own shell, so nothing is added to the image.
- **`set -e`** stops the script at the first failing command. If a secret file is missing or unreadable, `cat` fails, the container exits with status 1, and poznote never starts with an empty secret.
- **`VAR=$$(cat /run/secrets/...)`** reads each file into a variable. `$(...)` drops trailing newlines. The `$$` is Compose's escape for a literal `$`. Without it, Compose would try to fill in `$(...)` and `$@` itself when it reads the file.
- **`export`** passes the variables on to every program the script starts.
- **`exec docker-php-entrypoint "$$@"`** replaces the shell with the image's original entrypoint and hands it the container's command. `exec` keeps the process as PID 1, so Docker's stop signals reach the app directly.
- **`--`** is the first argument after the script, and `sh -c` uses it as the script's name (`$0`). The words of `command:` then become `$1`, `$2` and so on, which is what `"$@"` passes on.
- **`command:` repeats the image's start command.** Setting `entrypoint:` in Compose also clears the image's default command, so the original has to be written out again.

### Where the entrypoint and command come from

Read them from the image:

```sh
docker inspect -f '{{json .Config.Entrypoint}} {{json .Config.Cmd}}' ghcr.io/timothepoznanski/poznote:6
# ["docker-php-entrypoint"] ["/bin/sh","-c","/usr/local/bin/init.sh && exec /usr/bin/supervisord -c /etc/supervisor/conf.d/supervisord.conf"]
```

The wrapper ends with the first part and `command:` holds the second.

### Why PHP sees the variables

The exported variables pass from the wrapper to supervisord, which starts nginx, php-fpm and poznote's workers. php-fpm gives its workers an empty environment unless `clear_env = no` is set. poznote's image sets it in `docker.conf`, so the workers keep the variables:

```sh
docker run --rm --entrypoint grep ghcr.io/timothepoznanski/poznote:6 -r clear_env /usr/local/etc/php-fpm.d/
# /usr/local/etc/php-fpm.d/www.conf.default:;clear_env = no
# /usr/local/etc/php-fpm.d/docker.conf:clear_env = no
```

### Where the values remain visible

The values live in the environment of poznote's processes. Inside the container, root can read them from `/proc/<pid>/environ`. They stay out of the container's configuration, `docker inspect` and the compose file.

## Permissions

The sidecar runs as the image's default user, uid 65532 (the distroless `nonroot` user), with every capability dropped. It writes each file owned by itself with mode `0400`. poznote's image sets no `USER`, so the wrapper runs as root with Docker's default capabilities, which include `CAP_DAC_OVERRIDE`. That lets it read a file owned by another uid, so no map file and no `chown` are needed. `age.key` is owned by `65532:65532` with mode `0400` for the same reason as in the [gluetun recipe](gluetun.md).

The `secrets` volume uses `uid=65532,gid=65532` so the non-root sidecar can write to it. The [authentik recipe](authentik.md#where-the-numbers-come-from) explains the other volume options.

## Compose file

The changes to poznote's standard Compose file are the `secrets` service, the wrapper, the `secrets` volume and the `depends_on`. The service keeps its other settings.

```yaml
services:
  secrets:
    image: ghcr.io/shreyaspurohit/sops-sidecar:0.2.0@sha256:a59645a3fbfea2621d54dd79635d2535005acbc22f2d2b68b8c49568dbabaded
    read_only: true
    network_mode: none
    cap_drop: [ALL]
    security_opt:
      - no-new-privileges:true
    restart: unless-stopped
    volumes:
      - /path/outside/project/age.key:/config/age.key:ro
      - ./secrets.sops.yaml:/config/secrets.sops.yaml:ro
      - secrets:/secrets
    healthcheck:
      test: ["CMD", "/sops-sidecar", "health"]
      interval: 2s
      timeout: 3s
      retries: 15

  webserver:
    image: ghcr.io/timothepoznanski/poznote:6
    restart: unless-stopped
    entrypoint:
      - /bin/sh
      - -c
      - |
        set -e
        POZNOTE_OIDC_CLIENT_ID=$$(cat /run/secrets/oidc_client_id)
        POZNOTE_OIDC_CLIENT_SECRET=$$(cat /run/secrets/oidc_client_secret)
        export POZNOTE_OIDC_CLIENT_ID POZNOTE_OIDC_CLIENT_SECRET
        exec docker-php-entrypoint "$$@"
      - --
    command: ["/bin/sh", "-c", "/usr/local/bin/init.sh && exec /usr/bin/supervisord -c /etc/supervisor/conf.d/supervisord.conf"]
    env_file:
      - .env
    volumes:
      - ./data:/var/www/html/data
      - secrets:/run/secrets:ro
    depends_on:
      secrets:
        condition: service_healthy
    security_opt:
      - no-new-privileges:true

volumes:
  secrets:
    driver: local
    driver_opts:
      type: tmpfs
      device: tmpfs
      o: "size=1m,mode=0711,uid=65532,gid=65532,nosuid,nodev,noexec"
```

Then remove `POZNOTE_OIDC_CLIENT_ID` and `POZNOTE_OIDC_CLIENT_SECRET` from `.env`. No map file is needed. Each top-level key of the SOPS file becomes `/run/secrets/<key>`.

## Create the SOPS file from a running install

Take the values from the running container, as in the [authentik recipe](authentik.md#create-the-sops-file-from-a-running-install), and store them under the keys `oidc_client_id` and `oidc_client_secret`:

```sh
age-keygen -pq -o stack-age.key
ME=$(age-keygen -y ~/.config/sops/age/keys.txt)
STACK=$(age-keygen -y stack-age.key)

ssh <docker-host> 'docker exec <poznote-container> env -0' \
  | jq -Rs 'split("\u0000") | map(select(length > 0) | capture("^(?<k>[^=]+)=(?<v>.*)$"; "s"))
      | map({POZNOTE_OIDC_CLIENT_ID: "oidc_client_id", POZNOTE_OIDC_CLIENT_SECRET: "oidc_client_secret"}[.k] as $n
            | select($n) | {key: $n, value: .v})
      | from_entries' \
  | sops encrypt --age "$ME,$STACK" --input-type json --output-type yaml \
      --filename-override secrets.sops.yaml /dev/stdin > secrets.sops.yaml

SOPS_AGE_KEY_FILE=~/.config/sops/age/keys.txt sops decrypt --output-type json secrets.sops.yaml \
  | jq 'map_values(length)'
```

## Roll it out

1. Copy `stack-age.key` to the path the compose file mounts as `age.key`, then run `sudo chown 65532:65532 age.key && sudo chmod 0400 age.key` there.
2. Put `secrets.sops.yaml` in the project folder.
3. Check that the sidecar can decrypt it:
   ```sh
   docker run --rm --read-only --network none --cap-drop ALL \
     -v /path/outside/project/age.key:/config/age.key:ro \
     -v "$PWD/secrets.sops.yaml:/config/secrets.sops.yaml:ro" \
     ghcr.io/shreyaspurohit/sops-sidecar:0.2.0 check
   ```
   It lists `oidc_client_id` and `oidc_client_secret`.
4. Apply the compose and `.env` changes, then run `docker compose up -d`.
5. Sign in to poznote through the OIDC provider.
6. Delete the workstation copy of `stack-age.key`.

## Change a value

poznote reads its environment once, when it starts, so a change needs a restart after the reload:

1. Run `sops edit secrets.sops.yaml` on the workstation.
2. Copy the file onto the existing one on the host with `cat new > secrets.sops.yaml`, which rewrites it in place.
3. Run `docker compose kill -s HUP secrets`. The sidecar logs `secrets written`.
4. Run `docker compose restart webserver`. The wrapper runs again and reads the new files.

## After an image update

The wrapper repeats the image's start command. When the image changes, compare its command with the one in the compose file:

```sh
docker inspect -f '{{json .Config.Entrypoint}} {{json .Config.Cmd}}' ghcr.io/timothepoznanski/poznote:6
```

If either part differs, update the last line of the wrapper or `command:` to match.

## Reboots

Docker restarts containers after a host reboot without waiting for `depends_on`. If poznote starts before the sidecar has written the files, the wrapper exits with status 1. See [reboot ordering](../reboot-ordering.md) for starting the stack in order at boot.
