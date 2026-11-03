#!/usr/bin/env bash
# Runs unit tests with the race detector and enforces the coverage gates:
# every internal/ package >= PKG_MIN percent, the whole module >= TOTAL_MIN.
set -euo pipefail

readonly PKG_MIN=90
readonly TOTAL_MIN=80
readonly PROFILE="${COVERAGE_PROFILE:-coverage.out}"
readonly MODULE="github.com/shreyaspurohit/sops-sidecar"
# Test-support packages have no behavior of their own and are excluded.
readonly EXCLUDE="${MODULE}/internal/sopstest/"

cd "$(dirname "$0")/.."
go test -race -covermode=atomic -coverprofile="${PROFILE}" ./...

awk -v pkg_min="${PKG_MIN}" -v total_min="${TOTAL_MIN}" \
    -v module="${MODULE}/" -v exclude="${EXCLUDE}" '
  NR == 1 { next }                       # "mode: atomic" header
  index($1, exclude) == 1 { next }
  {
    split($1, loc, ":"); file = loc[1]
    pkg = file; sub(/\/[^\/]*$/, "", pkg)
    stmts = $2; hit = ($3 > 0) ? stmts : 0
    key = $1                             # file:range; dedupes blocks repeated across test binaries
    if (key in seen) { if (hit && !covered[key]) { pkg_hit[pkg] += stmts; total_hit += stmts; covered[key] = 1 }; next }
    seen[key] = 1; covered[key] = (hit > 0)
    pkg_all[pkg] += stmts; pkg_hit[pkg] += hit
    total_all += stmts; total_hit += hit
  }
  END {
    fail = 0
    for (p in pkg_all) {
      pct = 100 * pkg_hit[p] / pkg_all[p]
      name = p; sub(module, "", name)
      gate = (index(name, "internal/") == 1) ? pkg_min : 0
      status = (pct + 1e-9 >= gate) ? "ok  " : "FAIL"
      if (status == "FAIL") fail = 1
      printf "%s %-28s %6.1f%% (min %d%%)\n", status, name, pct, gate
    }
    total = (total_all > 0) ? 100 * total_hit / total_all : 0
    status = (total + 1e-9 >= total_min) ? "ok  " : "FAIL"
    if (status == "FAIL") fail = 1
    printf "%s %-28s %6.1f%% (min %d%%)\n", status, "total", total, total_min
    exit fail
  }' "${PROFILE}"
