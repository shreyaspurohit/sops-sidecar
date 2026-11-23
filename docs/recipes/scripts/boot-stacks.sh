#!/bin/sh
# Starts sops-sidecar stacks at boot, in dependency order. Run it as root from
# the host's boot scheduler, after Docker has started.
#
# Docker restarts containers at boot in any order and ignores depends_on.
# `docker compose up -d` applies depends_on again for anything not yet
# running. Services that share another container's network
# (network_mode: service:<name>, for example behind a VPN client such as
# gluetun) also need a restart once that container is healthy, because a
# restart of the network container leaves them on its old network namespace.
#
# Replace the paths, the container name and the service names below.
DOCKER=/usr/local/bin/docker

# Wait up to 5 minutes for Docker.
i=0
until $DOCKER info >/dev/null 2>&1; do
  i=$((i + 1)); [ $i -gt 60 ] && { echo "docker not ready after 5 minutes"; exit 1; }
  sleep 5
done

# One line per stack. Each project's name comes from `name:` in its compose
# file or COMPOSE_PROJECT_NAME in the .env next to it.
$DOCKER compose -f /path/to/stack-a/compose.yaml up -d
$DOCKER compose -f /path/to/stack-b/compose.yaml up -d
$DOCKER compose -f /path/to/vpn-stack/compose.yaml up -d

# Wait up to 5 minutes for the network container to report healthy.
i=0
until [ "$($DOCKER inspect -f '{{.State.Health.Status}}' gluetun 2>/dev/null)" = healthy ]; do
  i=$((i + 1)); [ $i -gt 60 ] && { echo "gluetun not healthy after 5 minutes"; exit 1; }
  sleep 5
done

# Restart the services that use network_mode: service:gluetun.
$DOCKER compose -f /path/to/vpn-stack/compose.yaml restart \
  service-one service-two
