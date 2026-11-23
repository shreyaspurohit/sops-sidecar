# Recipe: gluetun with its WireGuard config from sops-sidecar

[gluetun](https://github.com/qdm12/gluetun) is a VPN client container. With its custom provider it reads a whole WireGuard config file from the path in `WIREGUARD_CONF_SECRETFILE`. This recipe keeps that file encrypted on disk with SOPS and gives gluetun a decrypted copy on a tmpfs volume.

## What has been tested

One run on a Linux x86_64 host (kernel 4.4) with Docker 24.0.2 and Compose v2.20.1, using the released sops-sidecar v0.2.0 image and the gluetun `latest` image built on 2025-12-29 (revision `9b9b723`).

| Check | Result |
|---|---|
| The sidecar goes healthy before gluetun starts (`depends_on: condition: service_healthy`) | Works |
| The secret file in the gluetun container | Owned `65532:65532`, mode `0400`, on a read-only tmpfs mount |
| gluetun with `VPN_SERVICE_PROVIDER=custom`, `VPN_TYPE=wireguard` and `WIREGUARD_CONF_SECRETFILE` | Reads the file and connects to a real WireGuard server |
| `WIREGUARD_*` variables in `docker inspect` of gluetun | Only the image's empty defaults |
| Editing the SOPS file in place, `docker compose kill -s HUP secrets`, then `docker compose restart gluetun` | gluetun uses the new endpoint (run with a dummy config) |
| Creating the SOPS file from an existing `wg0.conf` with the pipeline below, for two age recipients | Each key decrypts content identical to the original |

## How it fits together

- **The sidecar** runs as the image's default user, uid 65532 (the distroless `nonroot` user), with every capability dropped and no network. It decrypts the SOPS file and writes `wg0.conf` into the `secrets` volume, owned by itself with mode `0400`.
- **The `secrets` volume** is tmpfs, so the decrypted config exists only in memory. It is created owned by uid and gid 65532 (`uid=65532,gid=65532` in its options), so the sidecar can write to it without root.
- **gluetun** runs as root with Docker's default capabilities, which include `CAP_DAC_OVERRIDE`. That lets it read a `0400` file owned by another uid, so no map file and no `chown` are needed. It mounts the volume read-only.

## Compose file

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

  gluetun:
    image: qmcgaw/gluetun:latest
    cap_add: [NET_ADMIN]
    devices:
      - /dev/net/tun:/dev/net/tun
    environment:
      - VPN_SERVICE_PROVIDER=custom
      - VPN_TYPE=wireguard
      - WIREGUARD_CONF_SECRETFILE=/run/secrets/wg0.conf
    volumes:
      - secrets:/run/secrets:ro
    security_opt:
      - no-new-privileges:true
    restart: unless-stopped
    depends_on:
      secrets:
        condition: service_healthy

volumes:
  secrets:
    driver: local
    driver_opts:
      type: tmpfs
      device: tmpfs
      o: "size=1m,mode=0711,uid=65532,gid=65532,nosuid,nodev,noexec"
```

The sidecar image is pinned by tag and digest. The tag says which release it is, and the digest makes sure that exact image runs. If an image updater such as Watchtower runs on the host, exclude the sidecar from it, so a new image arrives only by changing the pin.

Services that share gluetun's network (`network_mode: service:gluetun`) need no changes. They already wait for gluetun.

## Keys

Two age keys are recipients of the SOPS file:

- **A personal key** on a workstation, used to edit the file. It can decrypt every SOPS file it is a recipient of, so keep a copy offline, for example in a password manager.
- **A key for this stack**, mounted only into this stack's sidecar. It is owned by uid 65532 with mode `0400`, because the sidecar runs as 65532 with every capability dropped and reads the key through its normal owner bits.

Keep the stack key outside the project folder, in a place with no file sharing and no backups that do not need it.

## Create the SOPS file

These commands use the age and sops versions pinned in this repository's `.tool-versions` (age 1.3.2 for `-pq`, sops 3.13.3) and `jq`. The pipeline reads an existing `wg0.conf` and stores it as one multi-line value under the key `wg0.conf`, so the plain text is never written anywhere else.

```sh
age-keygen -pq -o ~/.config/sops/age/keys.txt   # personal key, once for every stack
chmod 600 ~/.config/sops/age/keys.txt
age-keygen -pq -o stack-age.key                 # this stack's key

ME=$(age-keygen -y ~/.config/sops/age/keys.txt)
STACK=$(age-keygen -y stack-age.key)
jq -Rs '{"wg0.conf": .}' < wg0.conf \
  | sops encrypt --age "$ME,$STACK" --input-type json --output-type yaml \
      --filename-override secrets.sops.yaml /dev/stdin > secrets.sops.yaml

SOPS_AGE_KEY_FILE=~/.config/sops/age/keys.txt sops decrypt --extract '["wg0.conf"]' secrets.sops.yaml \
  | cmp -s - wg0.conf && echo identical
```

Then, on the Docker host:

1. Put `secrets.sops.yaml` in the project folder.
2. Copy `stack-age.key` to the path the compose file mounts as `age.key`, then run `sudo chown 65532:65532 age.key && sudo chmod 0400 age.key` there.
3. Run `docker compose up -d` and check that gluetun connects, for example with `docker logs gluetun 2>&1 | grep -i "public ip"`.
4. Delete the plain-text `wg0.conf` and the workstation copy of `stack-age.key`. From then on, `secrets.sops.yaml` and the personal key are what the config is edited with.

## Change the config

When the VPN provider changes an address or a key:

1. Run `sops edit secrets.sops.yaml` on the workstation, using the personal key.
2. Copy the file onto the existing one on the host with `cat new > secrets.sops.yaml`, which rewrites it in place. A single-file bind mount follows the original file, so replacing it with a new file (`mv`, or an editor that saves a new file) leaves the sidecar reading the old one.
3. Run `docker compose kill -s HUP secrets`. The sidecar logs `secrets written`.
4. Run `docker compose restart gluetun`, so gluetun reads the new file.

## Reboots

Docker restarts containers after a host reboot without waiting for `depends_on`. In a reboot test, gluetun exited while its secret file was missing (`endpoint IP is not set`), and `restart: always` brought it back once the sidecar had written the file.

Services with `network_mode: service:gluetun` need one more step. A gluetun restart leaves them on its old network namespace with no network, and they need a restart after gluetun is healthy. See [services that share another container's network](../reboot-ordering.md#services-that-share-another-containers-network) and the [boot script](boot-stacks.sh).
