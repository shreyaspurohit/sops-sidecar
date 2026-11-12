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
# grep exits 1 when every line is allowed (fine) and 2 on a bad regex or I/O error (fail).
status=0
unexpected="$(grep -Ev -- "${ALLOWED}" <<<"${report}")" || status=$?
if (( status > 1 )); then
  echo "deadcode.sh: grep failed (status ${status}); check ALLOWED" >&2
  exit "${status}"
fi
if [[ -n "${unexpected}" ]]; then
  echo "unreachable functions:" >&2
  echo "${unexpected}" >&2
  exit 1
fi
