#!/usr/bin/env bash
# Runs zizmor (GitHub Actions security linter), which has no asdf plugin, via
# uvx at an exact version. uv itself comes from .tool-versions.
# Usage: scripts/zizmor.sh .github/workflows
set -euo pipefail

# renovate: datasource=pypi depName=zizmor
readonly ZIZMOR_VERSION="1.32.0"

cd "$(dirname "$0")/.."
exec uvx "zizmor@${ZIZMOR_VERSION}" "$@"
