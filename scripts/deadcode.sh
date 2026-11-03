#!/usr/bin/env bash
# Fails if deadcode reports any unreachable function in the sops-sidecar
# binary, except the redaction methods listed below. Those exist only so that
# a secret value or age identity can never be printed, logged, or encoded by
# mistake; the binary never formats them on purpose, so they are unreachable
# by design (see DECISIONS.md).
set -euo pipefail

readonly ALLOWED='^internal/decrypt/(identities\.go:[0-9:]+: unreachable func: Identities\.Format|value\.go:[0-9:]+: unreachable func: Value\.(String|GoString|Format|MarshalText|LogValue))$'

cd "$(dirname "$0")/.."
report="$(go tool deadcode -test=false ./cmd/sops-sidecar)"
unexpected="$(grep -Ev "${ALLOWED}" <<<"${report}" || true)"
if [[ -n "${unexpected}" ]]; then
  echo "unreachable functions:" >&2
  echo "${unexpected}" >&2
  exit 1
fi
