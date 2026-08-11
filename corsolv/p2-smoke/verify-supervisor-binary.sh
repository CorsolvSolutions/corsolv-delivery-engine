#!/usr/bin/env bash
#
# Prove the running supervisor is executing an exact binary, by fingerprinting
# what the kernel has open rather than trusting PATH or a version string.
#
# /proc/<pid>/exe is the authority: it is the inode the process is running, so
# it stays correct even if the file on PATH was replaced afterwards -- which is
# exactly the failure this guards. A supervisor started from an older build
# keeps materializing that build's launch commands, and an acceptance run
# against a freshly installed gc silently proved the previous build.
#
# Usage: verify-supervisor-binary.sh <expected-sha256>
# Exit:  0 match, 66 mismatch, 65 no supervisor running.

set -uo pipefail
export PATH="$HOME/.local/bin:/usr/local/go/bin:$PATH"

EXPECTED="${1:?expected sha256 required}"

PID="$(pgrep -f 'gc supervisor run' | head -1)"
if [ -z "$PID" ]; then
  echo 'No running supervisor found.'
  exit 65
fi

EXE="$(readlink -f "/proc/$PID/exe" 2>/dev/null)"
if [ -z "$EXE" ]; then
  echo "Cannot read /proc/$PID/exe"
  exit 65
fi

ACTUAL="$(sha256sum "/proc/$PID/exe" | awk '{print $1}')"

echo '============================================================'
echo 'SUPERVISOR BINARY FINGERPRINT'
echo '============================================================'
echo "supervisor pid: $PID"
echo "running image:  $EXE"
echo "expected:       $EXPECTED"
echo "actual:         $ACTUAL"
echo

if [ "$ACTUAL" = "$EXPECTED" ]; then
  echo 'SUPERVISOR BINARY: PASS (running the fingerprinted build)'
  exit 0
fi
echo 'SUPERVISOR BINARY: FAIL (running a different build)'
exit 66
