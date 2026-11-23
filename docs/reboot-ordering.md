# Reboot ordering

## The limitation

After a host reboot, the Docker daemon restarts containers that have a restart policy but **ignores `depends_on`**. An app container can therefore start before the sidecar is healthy and fail to find its secret. `depends_on: condition: service_healthy` applies only to `docker compose up`.

The tmpfs volume is empty after a reboot, so the sidecar must write the secrets again before the app can read them. Use one of these mitigations, or combine them.

## 1. `restart: unless-stopped` on the app

This is the simplest option. The app exits when its secret file is missing, Docker restarts it, and it succeeds once the sidecar has written the file.

```yaml
services:
  app:
    restart: unless-stopped
```

## 2. Wait for the ready marker in the entrypoint

Block until `/run/secrets/.sops-sidecar-ready` exists, with a timeout, then start the app:

```sh
#!/bin/sh
i=0
until [ -e /run/secrets/.sops-sidecar-ready ]; do
  i=$((i + 1)); [ "$i" -gt 60 ] && { echo "secrets not ready" >&2; exit 1; }
  sleep 1
done
exec "$@"
```

Use it as the container entrypoint, passing the original command as arguments.

## 3. A boot hook that runs `docker compose up -d`

Running `docker compose up -d` after Docker starts applies `depends_on` again. A generic systemd unit:

```ini
[Unit]
Description=Start the compose stack
After=docker.service
Requires=docker.service

[Service]
Type=oneshot
RemainAfterExit=yes
WorkingDirectory=/path/to/project
ExecStart=/usr/bin/docker compose up -d

[Install]
WantedBy=multi-user.target
```

Save it as `/etc/systemd/system/compose-stack.service`, set `WorkingDirectory=` to the project folder, and run `systemctl enable --now compose-stack.service`.

## Services that share another container's network

A service with `network_mode: service:<name>` joins that container's network namespace when it starts. When the network container restarts, it gets a new namespace, and the joined services stay in the old one with no network interface. A VPN client such as gluetun is the common case.

With sops-sidecar, this happens at boot: the network container starts before its sidecar, exits because its secret file is missing, and Docker restarts it. In a test with gluetun, gluetun recovered once the sidecar had written its file, while a service joined to it kept running with no `eth0`. `docker compose up -d` left that service as it was, and `docker compose restart <service>` brought its network back.

A boot hook therefore restarts the joined services after the network container is healthy. The [boot script](recipes/boot-stacks.sh) does this.

On a NAS without systemd access, use its scheduler. The [Synology recipe](recipes/synology.md) describes the Task Scheduler equivalent.
