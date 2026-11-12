# Recipe: Synology Container Manager

How to run a sops-sidecar stack on a Synology NAS with Container Manager. Compose there may be v2.20.x, the minimum version this project is tested against. This recipe has not yet been tried on a real NAS; please open an issue if a step does not match what you see.

## Project setup

1. Put the stack in a project folder, for example inside a `docker` shared folder, containing `compose.yaml`, `secrets.sops.yaml` and `secrets.map.yaml` (see [examples/postgres](../../examples/postgres)).
2. In Container Manager, open **Project**, choose **Create**, set the path to that folder and use the existing `compose.yaml`.

## Keep `age.key` outside the project folder

The age key decrypts everything. Do not keep it in the project folder, which may be backed up or shared. Create a separate shared folder with tight permissions (only an administrator or the account that runs the stack), put `age.key` there, and mount it by absolute path in the compose file:

```yaml
    volumes:
      - /volume1/<secrets-share>/age.key:/config/age.key:ro
```

Set the file's owner to the sidecar's uid and its mode to 0400 (over SSH: `sudo chown 0:0 age.key && sudo chmod 0400 age.key` for the postgres example, `65532:65532` for generic-app). With all capabilities dropped the sidecar cannot read a key it does not own. Exclude that shared folder from backups and from sharing.

## Start the stack at boot

Docker restarts containers after a reboot but ignores `depends_on` (see [reboot ordering](../reboot-ordering.md)). Add a boot task:

1. Open **Control Panel > Task Scheduler**.
2. Choose **Create > Triggered Task > User-defined script**.
3. User: `root`. Event: `Boot-up`.
4. In the task settings, set the user-defined script to:

```sh
docker compose -f /volume1/<path>/compose.yaml up -d
```
