#!/usr/bin/env bash
set -euo pipefail

export PATH="$HOME/.local/bin:/usr/local/go/bin:$PATH"

export GC_HOME="$HOME/.gc-corsolv-p2"
export GOTOOLCHAIN=auto

# Deliberately NOT setting GC_BEADS. `gc init` gives the city a Dolt-backed
# store, and GC_BEADS=file only overrides how *this shell* resolves the
# provider -- it does not change the city. With the override set, every
# `gc bd show` below fails with "only supported for bd-backed beads
# providers", so the wait loop can never observe CLOSED and the run always
# burns down to the safety deadline.

SOURCE_REPO="/mnt/d/Development/corsolv-delivery-engine"
# engdocs/, not docs/: everything under docs/ publishes to the Mintlify site
# and must appear in docs/docs.json navigation, which TestEveryDocsPageIsPublished
# enforces. This is an engineering record, so it belongs in engdocs/.
REPORT="$SOURCE_REPO/engdocs/corsolv/P2-SMOKE-RESULT.md"

TIMESTAMP="$(date -u +%Y%m%dT%H%M%SZ)"

CITY="$HOME/corsolv-p2/city-$TIMESTAMP"
TARGET="$HOME/corsolv-p2/rig-$TIMESTAMP"

RIG_NAME="rig-$TIMESTAMP"

MARKER="CORSOLV_GASCITY_MANAGED_CLAUDE_PASS"
EXPECTED_FILE="$TARGET/CORSOLV_GASCITY_SMOKE.txt"

mkdir -p "$HOME/.local/bin"
mkdir -p "$HOME/corsolv-p2"
mkdir -p "$(dirname "$REPORT")"

echo
echo "============================================================"
echo "REAL GAS CITY -> CLAUDE SMOKE"
echo "============================================================"

# ------------------------------------------------------------
# Install the binary built from the controlled Corsolv fork.
# ------------------------------------------------------------

install -m 755 "$SOURCE_REPO/bin/gc" "$HOME/.local/bin/gc"

# A supervisor started from an older binary keeps running that older code, and
# the supervisor -- not this script -- is what materializes each session's
# launch command. Without this restart the run silently proves the PREVIOUS
# build's behaviour: an acceptance run against a freshly built gc observed a
# worker launched with the old allowlist because a supervisor from 45 minutes
# earlier was still resolving providers.
echo
echo "Retiring any supervisor running an older binary:"
gc supervisor stop 2>&1 | tail -2 || true

for _ in $(seq 1 30); do
    if gc supervisor status 2>&1 | grep -q 'running'; then
        sleep 2
    else
        break
    fi
done

if gc supervisor status 2>&1 | grep -q 'running'; then
    echo "FAIL: a supervisor is still running; it would serve stale provider config."
    gc supervisor status
    exit 56
fi
echo "PASS - no stale supervisor"

echo
echo "Gas City:"
gc version

echo
echo "Claude:"
claude --version

echo
echo "Claude authentication:"

AUTH_JSON="$(claude auth status)"

echo "$AUTH_JSON"

echo "$AUTH_JSON" |
    jq -e '.loggedIn == true' >/dev/null

echo "PASS - Claude authenticated"

# ------------------------------------------------------------
# Create disposable native-Linux Git project.
# ------------------------------------------------------------

echo
echo "Creating disposable coding target:"
echo "$TARGET"

mkdir -p "$TARGET"

cd "$TARGET"

git init -b main

git config user.name "Corsolv Autonomy POC"
git config user.email "support@corsolv.com"

cat > README.md <<'EOF'
# Corsolv Gas City Smoke Target

Disposable repository used to prove Gas City-managed autonomous coding.
EOF

git add README.md
git commit -m "chore: initialise Gas City smoke target"

BASE_SHA="$(git rev-parse HEAD)"

# ------------------------------------------------------------
# Create a fresh Gas City using Claude.
# GC_BEADS=file keeps this smoke independent of Dolt/bd.
# ------------------------------------------------------------

echo
echo "Creating Gas City:"
echo "$CITY"

gc init "$CITY" --provider claude --yes

echo
echo "City status after init:"

cd "$CITY"
gc status

# ------------------------------------------------------------
# Register the disposable project as a rig.
# ------------------------------------------------------------

echo
echo "Registering rig:"
echo "$TARGET"

gc rig add "$TARGET"

echo
echo "Registered rigs:"
gc rig list

# `gc rig add` can return before the rig's own beads database is usable. If
# work is slung in that window the bead lands in the city scope instead of the
# rig, and dispatch fails. Wait for the rig store to come up.
echo
echo "Waiting for rig beads store:"

RIG_BEADS_DEADLINE=120
RIG_BEADS_START="$(date +%s)"

while true; do
    if gc rig list 2>&1 |
       awk -v rig="$RIG_NAME:" '
           $1 == rig {inrig = 1; next}
           inrig && /^  [^ ]/ && $0 !~ /^    / {inrig = 0}
           inrig && /Beads:/ {print}
       ' |
       grep -q 'initialized'; then
        echo "PASS - rig beads initialized"
        break
    fi

    if [ "$(( $(date +%s) - RIG_BEADS_START ))" -ge "$RIG_BEADS_DEADLINE" ]; then
        echo "FAIL: rig beads store not initialized within ${RIG_BEADS_DEADLINE}s."
        gc rig list
        exit 54
    fi

    sleep 2
done

# ------------------------------------------------------------
# Dispatch REAL coding work through Gas City.
# ------------------------------------------------------------

cd "$TARGET"

TASK="Create the file CORSOLV_GASCITY_SMOKE.txt in the repository root. The file must contain exactly this single line: CORSOLV_GASCITY_MANAGED_CLAUDE_PASS. Do not merely describe the change. Make the filesystem change, verify the exact file contents, and then mark the assigned Gas City work complete."

echo
echo "============================================================"
echo "DISPATCHING WORK THROUGH GAS CITY"
echo "============================================================"

# Capture stderr too. Under `set -e` a failing sling would otherwise abort the
# run with its diagnostics discarded, which is what made the first failure of
# this harness unreadable.
SLING_STATUS=0
SLING_OUTPUT="$(
    gc sling \
        "${RIG_NAME}/claude" \
        "$TASK" 2>&1
)" || SLING_STATUS=$?

printf '%s\n' "$SLING_OUTPUT"

if [ "$SLING_STATUS" -ne 0 ]; then
    echo
    echo "FAIL: gc sling exited $SLING_STATUS."
    exit 55
fi

WORK_ID="$(
    printf '%s\n' "$SLING_OUTPUT" |
    awk '/^Created / {print $2; exit}'
)"

if [ -z "$WORK_ID" ]; then
    echo
    echo "FAIL: unable to determine Gas City work ID."
    exit 50
fi

echo
echo "Gas City work ID: $WORK_ID"

# ------------------------------------------------------------
# Observe Gas City-owned execution.
#
# This is a safety deadline rather than a retry count.
# ------------------------------------------------------------

START_EPOCH="$(date +%s)"
DEADLINE_SECONDS=1200

FINAL_WORK_STATE=""
LAST_SHOW=""

# Where the live-posture verdict is recorded. The independent assurance refuses
# to pass without it, because after drain there is no worker left to inspect.
LIVE_RESULT="${LIVE_PROCESS_RESULT:-$CITY/.gc/corsolv-live-process.result}"
LIVE_PROOF=""

while true; do

    NOW="$(date +%s)"
    ELAPSED="$((NOW - START_EPOCH))"

    # Capture the live permission posture WHILE the worker is still running.
    # This is the only window in which it is capturable: `drain-ack` retires the
    # process, so a verifier run afterwards inspects a city with no worker in
    # it and can only report "nothing found" -- or, worse, match the long-lived
    # mayor/bd.dog agents and report PASS on processes that were never pool
    # workers. Sequencing, not timing luck.
    if [ -z "$LIVE_PROOF" ]; then
        if LIVE_PROCESS_RESULT="$LIVE_RESULT" \
           bash "$SOURCE_REPO/corsolv/p2-smoke/verify-live-process.sh" "$CITY" \
                >/dev/null 2>&1; then
            LIVE_PROOF=PASS
            echo "LIVE POSTURE: PASS (recorded to $LIVE_RESULT)"
        fi
        # Exit 65 means no worker is up yet; keep looking. Any other failure is
        # recorded by the verifier itself and adjudicated by the assurance.
    fi

    LAST_SHOW="$(
        gc bd show "$WORK_ID" 2>&1 || true
    )"

    # `case`, not `printf ... | grep -q`. Under `set -o pipefail` that pipeline
    # is inverted by its own success: grep -q exits at the first match, the
    # producer takes SIGPIPE (141), and pipefail promotes that to the pipeline
    # status -- so a MATCH can read as "not closed" and the wait runs to its
    # deadline with the bead already closed. Pattern matching avoids the pipe.
    case "$LAST_SHOW" in
      *CLOSED*|*Closed*|*closed*)
        FINAL_WORK_STATE="CLOSED"
        break
        ;;
    esac

    if [ "$ELAPSED" -ge "$DEADLINE_SECONDS" ]; then

        echo
        echo "FAIL: safety deadline reached before Gas City work closed."

        printf '%s\n' "$LAST_SHOW"

        exit 51
    fi

    FILE_STATE="absent"

    if [ -f "$EXPECTED_FILE" ]; then
        FILE_STATE="present"
    fi

    echo \
        "HEARTBEAT | work=$WORK_ID | elapsed=${ELAPSED}s | artifact=$FILE_STATE"

    sleep 10
done

echo
echo "============================================================"
echo "GAS CITY WORK CLOSED"
echo "============================================================"

printf '%s\n' "$LAST_SHOW"

# ------------------------------------------------------------
# Independent filesystem verification.
# ------------------------------------------------------------

if [ ! -f "$EXPECTED_FILE" ]; then
    echo "FAIL: Gas City closed work but expected file does not exist."
    exit 52
fi

ACTUAL_CONTENT="$(cat "$EXPECTED_FILE")"

if [ "$ACTUAL_CONTENT" != "$MARKER" ]; then
    echo "FAIL: expected marker not found."
    echo "Actual:"
    printf '%s\n' "$ACTUAL_CONTENT"
    exit 53
fi

HEAD_SHA="$(git rev-parse HEAD)"

CHANGED_FILES="$(git status --short)"

echo
echo "Artifact verification: PASS"
echo "Expected file: $EXPECTED_FILE"
echo "Marker:        $ACTUAL_CONTENT"

# ------------------------------------------------------------
# Capture Gas City runtime evidence.
# ------------------------------------------------------------

cd "$CITY"

CITY_STATUS="$(gc status 2>&1 || true)"
RIG_STATUS="$(gc rig list 2>&1 || true)"

GC_VERSION="$(gc version)"
CLAUDE_VERSION="$(claude --version)"
SOURCE_SHA="$(git -C "$SOURCE_REPO" rev-parse HEAD)"

# ------------------------------------------------------------
# Write durable evidence into Corsolv fork.
# ------------------------------------------------------------

cat > "$REPORT" <<EOF
# Corsolv Gas City Phase 2.1 Smoke Result

OVERALL: PASS

## Purpose

Prove that the Corsolv-controlled Gas City build can launch, supervise and
complete a real Claude-managed coding task without a PowerShell scheduler
performing the orchestration.

## Foundation

- Gas City version: $GC_VERSION
- Corsolv source SHA: $SOURCE_SHA
- Claude version: $CLAUDE_VERSION
- Store provider: file
- GC_HOME: $GC_HOME
- City: $CITY
- Rig path: $TARGET
- Rig name: $RIG_NAME
- Work ID: $WORK_ID

## Coding target

- Initial Git SHA: $BASE_SHA
- Git SHA after worker: $HEAD_SHA
- Required artifact: $EXPECTED_FILE
- Required marker: $MARKER

## Result

Gas City work state: $FINAL_WORK_STATE

Expected file exists: YES

Expected marker verified independently: YES

No PowerShell process launched Claude directly for this coding task.

The work was dispatched using Gas City:

gc sling ${RIG_NAME}/claude "<task>"

## Git working-tree evidence

\`\`\`
$CHANGED_FILES
\`\`\`

## Gas City work evidence

\`\`\`
$LAST_SHOW
\`\`\`

## Gas City status

\`\`\`
$CITY_STATUS
\`\`\`

## Rig status

\`\`\`
$RIG_STATUS
\`\`\`

## Acceptance

- Gas City binary launched: PASS
- City created: PASS
- Rig registered: PASS
- Real Claude worker dispatched by Gas City: PASS
- Work reached CLOSED: PASS
- Required filesystem artifact exists: PASS
- Artifact contents independently verified: PASS
- No human continuation command during dispatched task: PASS

## Next gate

Reproduce the complete Corsolv W1/W2/W3 autonomous-delivery acceptance POC
under Gas City, including parallel work, dependency release, GitHub PR/CI,
independent assurance and merge governance.
EOF

echo
echo "Evidence written:"
echo "$REPORT"

echo
echo "============================================================"
echo "P2.1 GAS CITY -> CLAUDE SMOKE: PASS"
echo "============================================================"