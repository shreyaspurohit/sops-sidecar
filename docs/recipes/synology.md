# Recipe: Synology Container Manager

This recipe runs a sops-sidecar stack on a Synology NAS with Container Manager. Compose there may be v2.20.x, the minimum version this project is tested against. Open an issue if a step differs on another DSM version.

## What has been tested

The postgres example was run once on a real NAS: DSM 7.4.1-90080 Update 1, x86_64, kernel 4.4, with Container Manager's Docker 24.0.2 and Compose v2.20.1. The stack was started with `docker compose` over SSH. The image was built from source, because no release was published yet.

| Check | Result |
|---|---|
| The sidecar goes healthy before postgres starts (`depends_on: condition: service_healthy`) | Works |
| `age.key` owned by `0:0` with mode `0400`, read by the sidecar with every capability but `CHOWN` and `FOWNER` dropped | Works |
| The secret file in the db container | Owned `70:70`, mode `0400`, on tmpfs |
| Postgres over the network | Accepts the password from the secret file and refuses a wrong one |
| Editing the SOPS file in place, then `docker compose kill -s HUP secrets` | Reloads the new value |
| Postgres started before the sidecar (what a reboot does) | Restarts until the secret file exists, then comes up |
| Secret values in either container's logs | None appear |
| Who else can read the root-owned `0400` `age.key` | An SSH login to an administrator account cannot read it without `sudo`. The same kind of account **downloaded it through File Station**, which follows the shared folder's permissions, not the file's owner and mode. |

**One difference from other Docker hosts.** On this kernel, the app's read-only mount of the secrets volume loses `nosuid,nodev,noexec`, and `cat /proc/mounts` in the app container shows only `ro,relatime,…`. The sidecar's writable mount keeps the flags, and secret file modes are capped at `0777`, so no setuid file can be written.

**A key in its own shared folder.** A later run used the released v0.2.0 image with the sidecar as uid 65532 and every capability dropped. Its `age.key` (`65532:65532`, mode `0400`) sat in a separate shared folder with **No access** for every user and group. The sidecar read the key and decrypted the SOPS file. An SSH login to an administrator account could not list the key.

**Bind-mount paths must exist.** This Docker refuses a bind mount whose source path is missing ("Bind mount failed: ... does not exist") and does not create it. Create `age.key`, the SOPS file and the map file before starting the stack.

## Project setup

1. Put the stack in a project folder, for example inside a `docker` shared folder, containing `compose.yaml`, `secrets.sops.yaml` and `secrets.map.yaml` (see [examples/postgres](../../examples/postgres)).
2. In Container Manager, open **Project**, choose **Create**, set the path to that folder and use the existing `compose.yaml`.

**Set the project name in `compose.yaml`.** Container Manager gives the project the name typed when creating it. `docker compose` run from a shell takes the folder's name instead. When the two differ, `docker compose up -d` over SSH starts a second project and fails with `The container name "/<name>" is already in use`. Add a top-level `name:` that matches the Container Manager project, so both use the same one:

```yaml
name: <project-name>
services:
  ...
```

Check the existing name with `docker inspect -f '{{index .Config.Labels "com.docker.compose.project"}}' <container>`. If a failed run already created containers or a volume under the folder's name, remove only those with `docker compose -p <folder-name> down -v`.

## Keep `age.key` outside the project folder

The age key decrypts everything. Do not keep it in the project folder, which may be backed up or shared.

**Protect the key with the shared folder's permissions.** The `chown` and `chmod 0400` below let the sidecar read the key but do not hide it from people on the NAS. DSM's file services, such as File Station, decide access by the shared folder's permissions, not the file's owner and mode. In testing, an administrator account downloaded a root-owned `0400` key through File Station.

Create a separate shared folder for the key:
- Set **No access** for every user and group that doesn't need it.
- Keep it out of any file services, sharing links and backups that do not need it.
- Put `age.key` there and mount it by absolute path in the compose file:

```yaml
    volumes:
      - /volume1/<secrets-share>/age.key:/config/age.key:ro
```

Anyone with DSM administrator rights can still reach the key, because they can change those permissions or become root with `sudo`. Keep administrator accounts few.

### Who must own `age.key`

`age.key` must be owned by the user the sidecar process runs as, with mode `0400`.

**Ownership.** The examples run the sidecar with `cap_drop: [ALL]`. That removes the capabilities that normally let root read any file regardless of permissions (`CAP_DAC_OVERRIDE` and `CAP_DAC_READ_SEARCH`). So inside the container even root reads a file only through its normal owner, group and mode bits. A `0400` file is readable only by its owner, so the owner has to be the sidecar's own uid. The key is bind-mounted, so the container sees the uid that the file has on the NAS.

**The sidecar's uid.** Look at the `user:` line of the `secrets` service in the project's `compose.yaml`:

| Compose file | Sidecar runs as | Command |
|---|---|---|
| `user: "0:0"` (examples/postgres) | root, uid 0 | `sudo chown 0:0 age.key` |
| `user: "65532:65532"` (examples/generic-app) | uid 65532 | `sudo chown 65532:65532 age.key` |
| no `user:` line | the image default, uid 65532 | `sudo chown 65532:65532 age.key` |
| any other `user: "U:G"` | uid U | `sudo chown U:G age.key` |

In every case, finish with `sudo chmod 0400 age.key`. Check with `ls -ln age.key`. The third and fourth columns are the numeric uid and gid, and the mode should read `-r--------`.

**The postgres example runs as root.** The postgres image reads its password as its own user, uid 70, so the map file asks the sidecar to `chown` the secret file to `70:70`. Changing a file's owner needs `CAP_CHOWN`, and Docker gives added capabilities (`cap_add`) only to processes running as root. So that example runs the sidecar as `user: "0:0"` with `CHOWN` and `FOWNER` added back, and `age.key` must be owned by uid 0. The generic-app example needs no `chown`, so it keeps the image's non-root uid 65532 with no capabilities at all, and its key belongs to 65532.

**On the NAS.** A DSM administrator account is not uid 0, so the commands use `sudo`. Once the key belongs to root or 65532, that account cannot read it from an SSH shell without `sudo`. File Station can still serve it to anyone the shared folder's permissions allow (see above). Keep the personal copy of the key, which `sops edit` uses, somewhere else, for example `~/.config/sops/age/keys.txt` on the workstation.

## Start the stack at boot

Docker restarts containers after a reboot but ignores `depends_on` (see [reboot ordering](../reboot-ordering.md)). To start the stack at boot, add a boot task:

1. Open **Control Panel > Task Scheduler**.
2. Choose **Create > Triggered Task > User-defined script**.
3. User: `root`. Event: `Boot-up`.
4. In the task settings, set the user-defined script to:

```sh
docker compose -f /volume1/<path>/compose.yaml up -d
```

**Several stacks, or services behind a VPN container.** [boot-stacks.sh](boot-stacks.sh) waits for Docker, starts each stack with `docker compose up -d`, waits for the VPN container to be healthy, and restarts the services that share its network (see [reboot ordering](../reboot-ordering.md#services-that-share-another-containers-network)). Copy it to a root-only location, for example a project folder, edit the paths and names, and run `sudo chown 0:0 boot-stacks.sh && sudo chmod 0700 boot-stacks.sh`. The task's user-defined script is then `sh /volume1/<path>/boot-stacks.sh`.

The script was tested with the task's **Run** action. The joined services restarted and rejoined the network container's network.
