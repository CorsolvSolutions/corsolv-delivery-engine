#!/usr/bin/env bash
#
# Compile the tree for another operating system.
#
# The writer lock has a POSIX implementation and a Windows one, and this host can
# only ever execute the POSIX one. A cross-compile is not a test of the Windows
# lock's behaviour and is not claimed to be; it is the check that the Windows
# path still compiles, which is the failure a Linux-only run would otherwise ship
# without noticing.
#
# Usage: cross-build.sh <goos>

set -euo pipefail

GOOS_TARGET="${1:?usage: cross-build.sh <goos>}"

# Never point the build cache at /tmp. It is a size-capped RAM-backed tmpfs
# shared by the whole host, and one cold cache built there has filled it before.
# The default GOCACHE is the shared on-disk one and is exactly what is wanted.
out="$(mktemp -d -p /var/tmp cross-build-XXXXXX)"
trap 'rm -rf "$out"' EXIT

echo "cross-compiling for GOOS=${GOOS_TARGET}"
GOOS="$GOOS_TARGET" go build -o "$out/" ./... 2>&1

echo "GOOS=${GOOS_TARGET}: the whole tree compiles"
