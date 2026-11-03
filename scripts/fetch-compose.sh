#!/usr/bin/env bash
# Download a standalone Docker Compose release binary into the project-local
# cache, verify it against the release's published .sha256 file, and print its
# absolute path on stdout. Progress goes to stderr. The system Compose is never
# touched.
#
# Usage: scripts/fetch-compose.sh <version>     e.g. v2.20.0
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <compose-version, e.g. v2.20.0>" >&2
  exit 2
fi
version=$1

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) echo "unsupported OS: $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=x86_64 ;;
  aarch64 | arm64) arch=aarch64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

asset="docker-compose-${os}-${arch}"
base="https://github.com/docker/compose/releases/download/${version}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dir="${root}/.cache/compose/${version}"
bin="${dir}/docker-compose"

# sha256_of prints the SHA-256 of a file with whichever tool the host has.
sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

mkdir -p "${dir}"
tmp="$(mktemp -d "${dir}/fetch.XXXXXX")"
trap 'rm -rf "${tmp}"' EXIT

echo "fetching ${asset}.sha256 (${version})" >&2
curl --fail --silent --show-error --location --retry 3 \
  --output "${tmp}/expected" "${base}/${asset}.sha256"
expected="$(cut -d' ' -f1 "${tmp}/expected")"

if [ -f "${bin}" ] && [ "$(sha256_of "${bin}")" = "${expected}" ]; then
  echo "verified cached copy" >&2
  echo "${bin}"
  exit 0
fi

echo "downloading ${asset}" >&2
curl --fail --silent --show-error --location --retry 3 \
  --output "${tmp}/binary" "${base}/${asset}"
actual="$(sha256_of "${tmp}/binary")"
if [ "${actual}" != "${expected}" ]; then
  echo "checksum mismatch for ${asset}: expected ${expected}, got ${actual}" >&2
  exit 1
fi

chmod +x "${tmp}/binary"
mv "${tmp}/binary" "${bin}"
echo "${bin}"
