#!/usr/bin/env bash
#
# Compile the fork-owned packages for the operating systems they claim to
# support.
#
# The writer lock has a POSIX implementation and a Windows one, and this host can
# only ever execute the POSIX one. A cross-compile is not a test of the Windows
# lock's *behaviour* and is not claimed to be; it is the check that the Windows
# path still compiles, which is the failure a Linux-only run would otherwise ship
# without noticing.
#
# Scope note. The first unattended run declared this check as `go build ./...`
# and it failed — correctly, and for a reason that had nothing to do with this
# work. The gascity tree has never been Windows-buildable: `internal/processgroup`
# has no Windows files at all, and `internal/pidutil`, `internal/runtime` and
# `internal/events` call syscall.Kill and syscall.Flock directly. That is an
# upstream condition, not a regression, and demanding the whole tree cross-compile
# would have meant a permanently red check that everyone learns to ignore. The
# check is therefore scoped to what the fork actually owns and actually claims.
#
# Usage: cross-build.sh [goos ...]   (default: windows darwin)

set -euo pipefail

TARGETS=("$@")
if [ ${#TARGETS[@]} -eq 0 ]; then
  TARGETS=(windows darwin)
fi

# The packages this fork owns and claims cross-platform support for.
PACKAGES=(
  ./internal/unattended/
  ./internal/projector/
  ./corsolv/unattended-run/
  ./corsolv/projector-gen/
)

# Never point the build cache at /tmp. It is a size-capped RAM-backed tmpfs
# shared by the whole host, and one cold cache built there has filled it before.
# The default GOCACHE is the shared on-disk one and is exactly what is wanted.
out="$(mktemp -d -p /var/tmp cross-build-XXXXXX)"
trap 'rm -rf "$out"' EXIT

for goos in "${TARGETS[@]}"; do
  echo "=== GOOS=${goos} ==="
  # vet rather than build: it compiles the test files too, so a Windows-only
  # test helper that stopped compiling is caught here rather than in CI.
  GOOS="$goos" go vet "${PACKAGES[@]}"
  GOOS="$goos" go build -o "$out/" "${PACKAGES[@]}"
  echo "GOOS=${goos}: the fork-owned packages compile and vet clean"
done
