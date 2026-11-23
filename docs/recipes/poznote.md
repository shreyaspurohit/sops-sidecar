# Recipe: poznote with its OIDC credentials from sops-sidecar

[poznote](https://github.com/timothepoznanski/poznote) is a note-taking app. From version 6.157.0, every setting it reads through its configuration helper also accepts a `<NAME>_FILE` variable that points at a file. This recipe gives poznote its OIDC client ID and client secret as files from sops-sidecar, so `docker inspect` shows only the paths.

Versions before 6.157.0 read these settings only from environment variables. The [env-wrapper recipe](env-wrapper.md) covers that case for any image.

## What has been tested

One run on a Linux x86_64 host (kernel 4.4) with Docker 24.0.2 and Compose v2.20.1, using the released sops-sidecar v0.2.0 image and poznote 6.158.0.

| Check | Result |
|---|---|
| The secret files in the poznote container | Owned `82:82`, mode `0400`, on a read-only tmpfs mount |
| `POZNOTE_OIDC_*` in `docker inspect` of poznote | Only the two `_FILE` paths |
| Signing in to poznote through an OIDC provider (authentik) | Works |
| poznote's PHP code, through nginx and php-fpm, reading both values (run with dummy values) | Reads them, including a value with a `$` |
| Editing the SOPS file, then `docker compose kill -s HUP secrets` (run with dummy values) | The next web request reads the new value, with no restart |
| poznote started before its sidecar, with the secrets volume empty (what a reboot does) | poznote refuses to start, Docker restarts it, and it runs once the sidecar has written the files |
| A secret file that `www-data` cannot read (owned `65532`, mode `0400`) | poznote refuses to start and names the variable and the path |
| `POZNOTE_OIDC_CLIENT_SECRET` and `POZNOTE_OIDC_CLIENT_SECRET_FILE` both set | poznote refuses to start |

## How it fits together

- **poznote reads the files as `www-data`, uid 82.** PHP runs as that user, and poznote's startup script checks each `_FILE` path as that user before starting. If a file is missing or unreadable, it logs `Poznote does not start with an empty secret` and exits.
- **The sidecar runs as root with only `CHOWN` and `FOWNER`.** It writes each file owned by uid 82, and changing a file's owner needs `CAP_CHOWN`, which Docker grants only to root. This is the same setup as the [authentik recipe](authentik.md), so `age.key` is owned by `0:0` with mode `0400`.
- **The `secrets` volume** is tmpfs with mode `0711`. poznote can open a file by name but cannot list the folder. The [authentik recipe](authentik.md#where-the-numbers-come-from) explains each volume option.
- **poznote reads a file on each web request**, so a reloaded value applies to the next request.

To check the uid in the image:

```sh
docker run --rm --entrypoint id ghcr.io/timothepoznanski/poznote:6.158.0 www-data
# uid=82(www-data) gid=82(www-data) groups=82(www-data),82(www-data)
```

poznote's error message names uid 1000 for its rootless image. With that image, use 1000 in the map file.

## Map file

`secrets.map.yaml`:

```yaml
# uid/gid 82 is www-data in the poznote image, the user PHP runs as.
files:
  - key: oidc_client_id
    uid: 82
    gid: 82
    mode: "0400"
  - key: oidc_client_secret
    uid: 82
    gid: 82
    mode: "0400"
```

## Compose changes

These are the changes to poznote's standard Compose file: a new `secrets` service, additions to `webserver`, and a new `secrets` volume. The service keeps its other settings.

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

  webserver:
    environment:
      POZNOTE_OIDC_CLIENT_ID_FILE: /run/secrets/oidc_client_id
      POZNOTE_OIDC_CLIENT_SECRET_FILE: /run/secrets/oidc_client_secret
    volumes:
      - secrets:/run/secrets:ro
    depends_on:
      secrets:
        condition: service_healthy

volumes:
  secrets:
    driver: local
    driver_opts:
      type: tmpfs
      device: tmpfs
      o: "size=1m,mode=0711,nosuid,nodev,noexec"
```

Then remove `POZNOTE_OIDC_CLIENT_ID` and `POZNOTE_OIDC_CLIENT_SECRET` from `.env`. poznote refuses to start when a setting and its `_FILE` form are both set.

## Create the SOPS file

From an install that has the values in `.env`, take them from the running container, as in the [authentik recipe](authentik.md#create-the-sops-file-from-a-running-install), and store them under the keys `oidc_client_id` and `oidc_client_secret`:

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

1. Copy `stack-age.key` to the path the compose file mounts as `age.key`, then run `sudo chown 0:0 age.key && sudo chmod 0400 age.key` there.
2. Put `secrets.sops.yaml` and `secrets.map.yaml` in the project folder.
3. Check that the sidecar can decrypt and plan both entries:
   ```sh
   docker run --rm --user 0:0 --read-only --network none --cap-drop ALL --cap-add CHOWN --cap-add FOWNER \
     -v /path/outside/project/age.key:/config/age.key:ro \
     -v "$PWD/secrets.sops.yaml:/config/secrets.sops.yaml:ro" \
     -v "$PWD/secrets.map.yaml:/config/secrets.map.yaml:ro" \
     ghcr.io/shreyaspurohit/sops-sidecar:0.2.0 check
   ```
   It lists `oidc_client_id` and `oidc_client_secret`.
4. Apply the compose and `.env` changes, then run `docker compose up -d`.
5. Sign in to poznote through the OIDC provider.
6. Delete the workstation copy of `stack-age.key`.

**Moving from the env-wrapper setup.** The wrapper setup runs the sidecar as uid 65532 and creates the volume with `uid=65532,gid=65532`. Compose keeps an existing volume's options, so remove the volume before starting the new setup: `docker compose down`, then `docker volume rm <project>_secrets`, then `docker compose up -d`. The volume holds only the tmpfs copies, which the sidecar writes again. Change `age.key` to `0:0` as in step 1.

## Change a value

1. Run `sops edit secrets.sops.yaml` on the workstation.
2. Copy the file onto the existing one on the host with `cat new > secrets.sops.yaml`, which rewrites it in place.
3. Run `docker compose kill -s HUP secrets`. The sidecar logs `secrets written`, and the next web request uses the new value.

## Reboots

Docker restarts containers after a host reboot without waiting for `depends_on`. poznote refuses to start until the sidecar has written the files, and its restart policy brings it up once they exist. See [reboot ordering](../reboot-ordering.md) for starting stacks in order at boot.
