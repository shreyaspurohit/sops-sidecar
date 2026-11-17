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
WorkingDirectory=/path/to/your/project
ExecStart=/usr/bin/docker compose up -d

[Install]
WantedBy=multi-user.target
```

Save it as `/etc/systemd/system/compose-stack.service`, set `WorkingDirectory=` to your project, and run `systemctl enable --now compose-stack.service`.

On a NAS without systemd access, use its scheduler. The [Synology recipe](recipes/synology.md) describes the Task Scheduler equivalent.
