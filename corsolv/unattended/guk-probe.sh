#!/usr/bin/env bash
#
# Read-only readiness probe of the GUK BPM pilot target.
#
# It reads and never writes. GUK BPM is the intended first production pilot and
# is explicitly not to be mutated by this programme, so this probe answers "what
# would a pilot run there find" without becoming the thing that finds out by
# changing it. Every command below is a read.
#
# Usage: guk-probe.sh [--deploy-dry-run]
#
# --deploy-dry-run is the mode that needs a deployment credential this host does
# not hold. It exists so the plan can declare a task that is genuinely behind a
# human boundary rather than a contrived one.

set -uo pipefail

TARGET="${GUK_BPM_PATH:-/mnt/d/Development/guk-bpm-platform}"
MODE="${1:-probe}"

if [ "$MODE" = "--deploy-dry-run" ]; then
  if [ -z "${GUK_BPM_DEPLOY_TOKEN:-}" ]; then
    echo "GUK_BPM_DEPLOY_TOKEN is not set; a deployment dry run cannot be attempted" >&2
    exit 78 # EX_CONFIG
  fi
  echo "deployment dry run is not implemented; the credential exists but the pilot is not yet approved" >&2
  exit 78
fi

echo "=== GUK BPM pilot readiness (read-only) ==="
echo "target: $TARGET"

if [ ! -d "$TARGET" ]; then
  echo "state: ABSENT — the pilot target is not present on this host"
  echo
  echo "A pilot cannot be scheduled against a directory that is not here. This is"
  echo "reported rather than treated as a failure of this run: the readiness"
  echo "answer for the pilot is 'not on this machine', which is a real answer."
  exit 0
fi

echo "state: PRESENT"
echo
echo "--- repository ---"
if git -C "$TARGET" rev-parse --show-toplevel >/dev/null 2>&1; then
  echo "worktree:  $(git -C "$TARGET" rev-parse --show-toplevel)"
  echo "branch:    $(git -C "$TARGET" rev-parse --abbrev-ref HEAD)"
  echo "head:      $(git -C "$TARGET" rev-parse HEAD)"
  echo "origin:    $(git -C "$TARGET" remote get-url origin 2>/dev/null || echo 'none')"
  dirty="$(git -C "$TARGET" status --porcelain | wc -l)"
  echo "dirty:     $dirty path(s)"
  echo "worktrees:"
  git -C "$TARGET" worktree list 2>/dev/null | sed 's/^/  /'
else
  echo "not a git worktree — a pilot would have no ref to fence"
fi

echo
echo "--- project shape ---"
for marker in package.json go.mod pom.xml build.gradle requirements.txt pyproject.toml Makefile docker-compose.yml; do
  [ -e "$TARGET/$marker" ] && echo "  $marker"
done

echo
echo "--- delivery projection ---"
if [ -f "$TARGET/delivery/PROJECT-STATE.yml" ]; then
  echo "  delivery/PROJECT-STATE.yml is present — the dashboard already reads this project"
else
  echo "  delivery/PROJECT-STATE.yml is absent — the pilot's first run would establish it"
fi

echo
echo "This probe wrote nothing. Whether the pilot proceeds is a human decision."
