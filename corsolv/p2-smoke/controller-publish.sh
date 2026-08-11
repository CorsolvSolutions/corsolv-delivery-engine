#!/usr/bin/env bash
#
# Controller-side publication of a worker's output.
#
# Workers are denied git by policy: they modify the workspace and close their
# bead, and the controller is what inspects, stages, and commits. This script
# is that step for the rig repo. It stages ONLY the named artifact, so bead
# store churn and .gc/ runtime state never enter a commit.
#
# Usage: controller-publish.sh <rig-path> <artifact-name> <work-bead-id>
# Exit:  0 committed, 66 refused.

set -uo pipefail

RIG="${1:?rig path required}"
ARTIFACT="${2:?artifact name required}"
WORK_ID="${3:?work bead id required}"

echo '============================================================'
echo 'CONTROLLER PUBLICATION'
echo '============================================================'
echo "rig:      $RIG"
echo "artifact: $ARTIFACT"

echo
echo '--- exact git status before staging ---'
git -C "$RIG" status --short

if [ ! -f "$RIG/$ARTIFACT" ]; then
  echo "REFUSED: $ARTIFACT does not exist"
  exit 66
fi

echo
echo '--- exact diff of the artifact (untracked: showing content) ---'
if git -C "$RIG" ls-files --error-unmatch "$ARTIFACT" >/dev/null 2>&1; then
  git -C "$RIG" diff -- "$ARTIFACT"
else
  echo "(new file)"
  sed 's/^/+ /' "$RIG/$ARTIFACT"
fi

echo
echo '--- staging only the artifact ---'
git -C "$RIG" add -- "$ARTIFACT"

staged="$(git -C "$RIG" diff --cached --name-only)"
echo "staged: $(printf '%s' "$staged" | tr '\n' ' ')"
if [ "$staged" != "$ARTIFACT" ]; then
  echo "REFUSED: staging area holds more than the artifact"
  git -C "$RIG" reset -- . >/dev/null 2>&1
  exit 66
fi

echo
echo '--- committing ---'
git -C "$RIG" -c user.name='Gas City Controller' -c user.email='support@corsolv.com' \
  commit -m "feat: add ${ARTIFACT} from Gas City work ${WORK_ID}

Published by the controller. The worker created and byte-verified this file
but is denied git by policy: commit, push, merge, and release stay on the
controller side of the worker boundary." >/dev/null || { echo 'REFUSED: commit failed'; exit 66; }

SHA="$(git -C "$RIG" rev-parse HEAD)"
echo "controller commit: $SHA"

echo
echo '--- verification after commit ---'
git -C "$RIG" show --stat --oneline HEAD | head -5
echo
echo "committed content: $(git -C "$RIG" show "HEAD:$ARTIFACT")"
echo
echo 'CONTROLLER PUBLICATION: PASS'
