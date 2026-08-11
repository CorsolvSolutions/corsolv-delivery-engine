#!/usr/bin/env bash
#
# S-A preflight — prove the per-task worktree route on ONE bead before spending
# a full three-worker run on it.
#
# The authoritative S-A run costs three live Claude workers and two controller
# integrations. Everything in it depends on a route that had never been executed
# end to end: a rig-scoped, single-capacity worker agent whose session cwd is its
# own registered git worktree, dispatched as a RAW routed bead, with the
# controller's pre-dispatch legacy `work_dir` stamp mirrored to canonical
# `gc.work_dir` by reconciliation.
#
# Each link in that chain is cheap to get wrong and expensive to discover late,
# so this probe executes exactly it, once:
#
#   controller creates the worktree
#     -> stamps legacy work_dir BEFORE dispatch
#     -> routes the raw bead (no formula, so the dependency gate governs the
#        dispatched entity directly — see the D3 note in shadow-run.sh)
#     -> the pool spawns the agent from unassigned-routed demand
#     -> the session's cwd IS the worktree
#     -> reconciliation mirrors gc.work_dir through the ownership guard
#     -> the artifact lands inside the worktree
#     -> the bead closes with a truthful typed disposition
#
# Usage: sa-preflight.sh
# Exit:  0 the route holds, 70 otherwise.

set -uo pipefail

export PATH="$HOME/.local/bin:/usr/local/go/bin:$PATH"
export GC_HOME="$HOME/.gc-corsolv-p2"
export GOTOOLCHAIN=auto
export GC_WORK_RECORD_ENFORCE=1

SOURCE_REPO="${SOURCE_REPO:-/mnt/d/Development/corsolv-delivery-engine}"
# shellcheck source=lib/sa-lib.sh
. "$SOURCE_REPO/corsolv/p2-smoke/lib/sa-lib.sh"

TIMESTAMP="$(date -u +%Y%m%dT%H%M%SZ)"
CITY="$HOME/corsolv-p2/pre-city-$TIMESTAMP"
TARGET="$HOME/corsolv-p2/pre-rig-$TIMESTAMP"
RIG_NAME="pre-rig-$TIMESTAMP"
EVIDENCE="$HOME/corsolv-p2/pre-evidence-$TIMESTAMP"
mkdir -p "$EVIDENCE"

SA_CITY="$CITY"
SA_RIG="$RIG_NAME"

AGENT='worker-a'
QUALIFIED="$RIG_NAME/$AGENT"
WT_A="$CITY/.gc/worktrees/$RIG_NAME/$AGENT"

FAILURES=0
note() { printf '  %-60s %s\n' "$1" "$2"; }
pass() { note "$1" 'PASS'; }
fail() { note "$1" "FAIL — $2"; FAILURES=$((FAILURES + 1)); }
info() { note "$1" "INFO — $2"; }
section() { printf '\n--- %s ---\n' "$1"; }

echo '============================================================'
echo 'S-A PREFLIGHT — PER-TASK WORKTREE ROUTE'
echo '============================================================'
echo "city:     $CITY"
echo "rig:      $TARGET"
echo "evidence: $EVIDENCE"

sa_ledger_init "$EVIDENCE/gc-commands.log"

# --- foundation -------------------------------------------------------------
section '0. foundation'
DIRTY="$(git -C "$SOURCE_REPO" status --porcelain)"
if [ -n "$DIRTY" ]; then
  info 'source tree' 'dirty — acceptable for a preflight probe, never for the run'
fi
install -m 755 "$SOURCE_REPO/bin/gc" "$HOME/.local/bin/gc"
BIN_SHA="$(sha256sum "$SOURCE_REPO/bin/gc" | awk '{print $1}')"
gc supervisor stop >/dev/null 2>&1 || true
SUP_OK=0
for _ in $(seq 1 45); do
  SUP_PID="$(pgrep -f 'gc supervisor run' | head -1)"
  if [ -n "$SUP_PID" ]; then
    RUNNING_SHA="$(sha256sum "/proc/$SUP_PID/exe" 2>/dev/null | awk '{print $1}')"
    [ "$RUNNING_SHA" = "$BIN_SHA" ] && { SUP_OK=1; break; }
  fi
  sleep 2
done
[ "$SUP_OK" -eq 1 ] || { fail 'supervisor runs the fingerprinted build' "expected $BIN_SHA"; exit 70; }
pass "supervisor runs the fingerprinted build (pid $SUP_PID)"

# --- target and city --------------------------------------------------------
section '1. target and city'
mkdir -p "$TARGET" && cd "$TARGET" || exit 70
git init -q -b main
git config user.name 'Corsolv Autonomy POC'
git config user.email 'support@corsolv.com'
printf '# preflight target\n' > README.md
git add README.md && git commit -qm 'chore: base'
BASE_SHA="$(git rev-parse HEAD)"
pass 'disposable git target created'

gcx init "$CITY" --provider claude --yes >"$EVIDENCE/init.txt" 2>&1 || fail 'gc init' 'see init.txt'
cat >> "$CITY/city.toml" <<'TOML'

[workspace.env]
GC_WORK_RECORD_ENFORCE = "1"
TOML
mkdir -p "$CITY/scripts"
install -m 755 "$SOURCE_REPO/corsolv/p2-smoke/scripts/worktree-setup.sh" "$CITY/scripts/worktree-setup.sh"

cd "$CITY" || exit 70
gcx rig add "$TARGET" >"$EVIDENCE/rigadd.txt" 2>&1 || fail 'gc rig add' 'see rigadd.txt'
if sa_wait_rig_beads "$CITY" "$RIG_NAME" 120; then
  pass 'rig beads store initialized'
else
  fail 'rig beads store initialized' 'not ready within 120s'; exit 70
fi

PROMPT="$(sa_pool_worker_prompt "$CITY")"
if [ -n "$PROMPT" ]; then
  pass "resolved the SDK pool-worker prompt template"
else
  fail 'resolved the SDK pool-worker prompt template' 'not found in gc config show'
fi
sa_declare_worker_agent "$CITY" "$RIG_NAME" "$TARGET" "$AGENT" "$WT_A" "$PROMPT"
gcx config show > "$EVIDENCE/config-show.txt" 2>&1 || true
if grep -qE "^name = \"$AGENT\"$" "$EVIDENCE/config-show.txt"; then
  pass "per-task agent $QUALIFIED is configured"
else
  fail "per-task agent $QUALIFIED is configured" 'absent from resolved config'
  exit 70
fi

# --- controller-owned worktree, stamped before dispatch ---------------------
section '2. controller worktree and pre-dispatch stamp'
if wt_add "$TARGET" "$WT_A" "gc-$AGENT" "$BASE_SHA"; then
  pass 'controller created the task worktree'
else
  fail 'controller created the task worktree' 'wt_add failed'; exit 70
fi
if wt_is_registered "$TARGET" "$WT_A"; then
  pass 'task worktree is a registered git worktree of the rig'
else
  fail 'task worktree is a registered git worktree of the rig' 'not in git worktree list'
fi

cd "$TARGET" || exit 70
LIFECYCLE='Make the change, verify the exact contents, then close the assigned bead. You cannot run git; the controller publishes. Close with gc.work_outcome=blocked plus a gc.work_blocked_reason; do NOT claim shipped.'
A="$(gcx bd q "Create PRE.md in the repository root containing exactly this single line: PRE_OK. $LIFECYCLE" 2>&1 | tail -1)"
if [ -z "$A" ]; then fail 'work bead created' 'no id'; exit 70; fi
pass "work bead created ($A)"

gcx bd update "$A" --set-metadata "work_dir=$WT_A" >/dev/null 2>&1
gcx bd update "$A" --set-metadata 'gc.required_artifact=PRE.md' >/dev/null 2>&1
STAMP_SHOW="$(sa_gc bd show "$A" 2>&1)"
if grep -qF "work_dir: $WT_A" <<<"$STAMP_SHOW"; then
  pass 'legacy work_dir stamped before dispatch'
else
  fail 'legacy work_dir stamped before dispatch' 'stamp did not land'
fi

# --- dispatch ---------------------------------------------------------------
section '3. dispatch and execution'
gcx sling "$QUALIFIED" "$A" --no-formula > "$EVIDENCE/sling.txt" 2>&1
cat "$EVIDENCE/sling.txt"

deadline=$(( $(date +%s) + 900 ))
closed=0
SESSION_WD=''
LIVE_PROOF=''
while true; do
  if [ -z "$SESSION_WD" ]; then
    SESSION_WD="$(sa_session_workdir "$CITY" "$QUALIFIED")"
    [ -n "$SESSION_WD" ] && info 'session work_dir observed' "$SESSION_WD"
  fi
  if [ -z "$LIVE_PROOF" ] && [ -n "$SESSION_WD" ]; then
    if LIVE_PROCESS_RESULT="$EVIDENCE/live-process.result" \
       bash "$SOURCE_REPO/corsolv/p2-smoke/verify-live-process.sh" "$CITY" \
         > "$EVIDENCE/live-process.txt" 2>&1; then
      LIVE_PROOF=PASS
      info 'live worker posture' 'PASS'
    fi
  fi
  if bead_is_closed "$A"; then closed=1; break; fi
  [ "$(date +%s)" -ge "$deadline" ] && break
  sleep 5
done

sa_gc session list --json > "$EVIDENCE/sessions.json" 2>&1
sa_gc session list > "$EVIDENCE/sessions.txt" 2>&1
cd "$TARGET" || exit 70

if [ "$closed" -eq 1 ]; then
  pass 'work bead closed'
else
  fail 'work bead closed' 'deadline reached'
fi

# --- the route's own assertions ---------------------------------------------
section '4. the route'
FINAL_STATUS="$(capture_final_bead_state "$A" "$EVIDENCE" || true)"
if [ "$FINAL_STATUS" = 'closed' ]; then
  pass 'final authoritative record is closed'
else
  fail 'final authoritative record is closed' "got '${FINAL_STATUS:-<none>}'"
fi

SESSION_WD="$(sa_session_workdir "$CITY" "$QUALIFIED")"
if [ "$(readlink -f "${SESSION_WD:-/nonexistent}")" = "$(readlink -f "$WT_A")" ]; then
  pass 'session cwd IS the controller-created worktree'
else
  fail 'session cwd IS the controller-created worktree' "session work_dir='${SESSION_WD:-<none>}'"
fi

CANON="$(final_meta "$A" "$EVIDENCE" 'gc.work_dir')"
LEGACY="$(final_meta "$A" "$EVIDENCE" 'work_dir')"
if [ "$LEGACY" = "$WT_A" ]; then
  pass 'legacy work_dir survived dispatch'
else
  fail 'legacy work_dir survived dispatch' "got '${LEGACY:-<none>}'"
fi
if [ "$CANON" = "$WT_A" ]; then
  pass 'canonical gc.work_dir mirrored through the ownership guard'
else
  fail 'canonical gc.work_dir mirrored through the ownership guard' "got '${CANON:-<none>}'"
fi

if [ -f "$WT_A/PRE.md" ] && [ "$(cat "$WT_A/PRE.md")" = 'PRE_OK' ]; then
  pass 'artifact produced inside the assigned worktree'
else
  fail 'artifact produced inside the assigned worktree' \
       "$( [ -f "$WT_A/PRE.md" ] && head -c 80 "$WT_A/PRE.md" || echo 'absent' )"
fi
if [ -e "$TARGET/PRE.md" ]; then
  fail 'artifact did NOT leak into the rig root' 'present in the shared checkout'
else
  pass 'artifact did NOT leak into the rig root'
fi

WO="$(final_meta "$A" "$EVIDENCE" 'gc.work_outcome')"
case "$WO" in
  blocked|no-op|abandoned) pass "typed work outcome is truthful ($WO)" ;;
  shipped) fail 'typed work outcome is truthful' 'claims shipped while git is withheld' ;;
  '') fail 'typed work outcome present' 'absent — silent close' ;;
  *) fail 'typed work outcome present' "unknown disposition '$WO'" ;;
esac

case "$LIVE_PROOF" in
  PASS) pass 'live worker posture proved while the worker was alive' ;;
  *) fail 'live worker posture proved while the worker was alive' \
          'NOT REACHED — no live capture' ;;
esac

echo
echo '============================================================'
if [ "$FAILURES" -ne 0 ]; then
  echo "S-A PREFLIGHT: FAIL ($FAILURES check(s))"
  echo "evidence: $EVIDENCE"
  exit 70
fi
echo 'S-A PREFLIGHT: PASS'
echo "evidence: $EVIDENCE"
