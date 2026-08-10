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
gc supervisor stop >/dev/null 2>&1 || true

# STALENESS IS A FINGERPRINT QUESTION, NOT A PRESENCE QUESTION.
#
# The hazard this guards is stated in this file already: "A supervisor started
# from an older binary keeps running that older code, and the supervisor -- not
# this script -- is what materializes each session's launch command." The thing
# that matters is therefore WHICH IMAGE the supervisor is executing, not whether
# one exists.
#
# Testing for absence is also unsatisfiable here. The supervisor is a systemd
# user service (cgroup gascity-supervisor-gc-*.service), so systemd restarts it
# within seconds of every `gc supervisor stop`. A wait-for-none loop can only
# ever time out, and this run aborted on exactly that -- with the supervisor
# already executing the correct, freshly built image.
#
# So: stop it, let it come back, and require its running image to be byte-equal
# to the binary just installed. /proc/<pid>/exe is the inode the kernel gave the
# process, so it stays correct even though PATH was replaced underneath it.
# Never converging is a HARD ABORT -- dispatching against an unknown image is
# the failure this check exists to prevent.
EXPECTED_GC_SHA="$(sha256sum "$HOME/.local/bin/gc" | awk '{print $1}')"
SUP_OK=0
for _ in $(seq 1 45); do
    SUP_PID="$(pgrep -f 'gc supervisor run' | head -1)"
    if [ -n "$SUP_PID" ]; then
        RUNNING_SHA="$(sha256sum "/proc/$SUP_PID/exe" 2>/dev/null | awk '{print $1}')"
        if [ "$RUNNING_SHA" = "$EXPECTED_GC_SHA" ]; then
            SUP_OK=1
            break
        fi
    fi
    sleep 2
done

if [ "$SUP_OK" -ne 1 ]; then
    echo "FAIL: no supervisor is running the expected binary; it would serve stale provider config."
    echo "  expected: $EXPECTED_GC_SHA"
    echo "  running : ${RUNNING_SHA:-<none>} (pid ${SUP_PID:-none})"
    exit 56
fi
echo "PASS - supervisor runs the fingerprinted build (pid $SUP_PID, $EXPECTED_GC_SHA)"

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

# Make the typed work-record contract BLOCK rather than warn, for every managed
# session in this city.
#
# `[workspace] env` is the documented boundary -- "workspace-wide environment
# variables applied to every managed session" -- merged into the spawned agent's
# environment in cmd/gc/template_resolve.go. Setting the variable in this shell
# instead would govern only the harness's own `gc` calls, which is exactly how
# the earlier P2.1 acceptance bead r2-xm1 came to record
# `gc.work_outcome: shipped` while its own transcript shows `git commit` denied
# by policy: an untruthful work record that nothing rejected.
#
# Written before the rig is added and before any session spawns, so the first
# worker already has it.
cat >> "$CITY/city.toml" <<'TOML'

# Corsolv acceptance: a worker may not close a bead with an invented, absent, or
# unearned disposition. `shipped` requires a reachable commit, which this policy
# withholds from workers by design.
[workspace.env]
GC_WORK_RECORD_ENFORCE = "1"
TOML

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

# The closing sentences name the correct typed disposition. Under enforcement a
# `shipped` claim without a commit is refused ("requires gc.work_commit"), and
# this policy withholds git from workers, so `blocked` is the honest terminal
# outcome for a worker that produced an artifact the controller must publish.
# Naming it avoids the worker guessing a value the gate will reject -- earlier
# runs invented `completed-uncommitted`, which is outside the vocabulary.
TASK="Create the file CORSOLV_GASCITY_SMOKE.txt in the repository root. The file must contain exactly this single line: CORSOLV_GASCITY_MANAGED_CLAUDE_PASS. Do not merely describe the change. Make the filesystem change, verify the exact file contents, and then mark the assigned Gas City work complete. You are not permitted to run git; the controller performs publication. Close with gc.work_outcome=blocked plus a gc.work_blocked_reason; do NOT claim shipped, because you did not commit."

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

    # Closure is adjudicated by the STORE's status field, never by matching
    # CLOSED in the rendered bead. That rendering includes the worker-authored
    # title and notes, so a bead titled "... is CLOSED ..." satisfied the old
    # text match while its status was `open` (demonstrated on bead mr2-c5n).
    #
    # `gc bd show --json` returns an ARRAY; [0].status is the authority. An
    # absent or unparseable status keeps waiting rather than declaring success.
    WORK_STATUS="$(jq -r '.[0].status // empty' \
        <<<"$(gc bd show "$WORK_ID" --json 2>/dev/null)" 2>/dev/null)"
    if [ "$WORK_STATUS" = 'closed' ]; then
        FINAL_WORK_STATE="CLOSED"
        break
    fi

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