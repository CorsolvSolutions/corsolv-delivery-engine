#!/usr/bin/env bash
#
# Targeted controls for the S-A controller primitives.
#
# These run against sa-lib.sh — the same file the authoritative run sources —
# on real git repositories and a stubbed `gc`. They exist so the mechanisms the
# run depends on are proved BEFORE a multi-agent run is spent discovering that
# one of them never worked.
#
# Two properties every control here must have:
#
#   1. it fails when the mechanism is broken. Several controls therefore assert
#      the DEFECT as well as the fix — a stale-snapshot control that cannot
#      detect a stale snapshot is decoration.
#   2. it exercises the shipped function, not a copy of it.
#
# Usage: run-controls.sh
# Exit:  0 all controls passed, 70 otherwise.

set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../lib/sa-lib.sh
. "$HERE/../lib/sa-lib.sh"

FAILURES=0
pass() { printf '  %-64s %s\n' "$1" 'PASS'; }
fail() { printf '  %-64s %s\n' "$1" "FAIL — $2"; FAILURES=$((FAILURES + 1)); }
section() { printf '\n--- %s ---\n' "$1"; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

git_quiet() { git -C "$1" -c user.name=t -c user.email=t@e "${@:2}"; }

# new_rig <name> — a disposable git repo with one commit on main.
new_rig() {
  local r="$WORK/$1"
  mkdir -p "$r"
  git -C "$r" init -q -b main
  git -C "$r" config user.name 'Corsolv Control'
  git -C "$r" config user.email 'support@corsolv.com'
  printf '# %s\n' "$1" > "$r/README.md"
  git -C "$r" add README.md
  git -C "$r" -c user.name='Corsolv Control' -c user.email='support@corsolv.com' \
    commit -qm 'chore: base'
  printf '%s' "$r"
}

echo '============================================================'
echo 'S-A CONTROLLER CONTROLS'
echo '============================================================'

# ===========================================================================
section '1. per-task worktree provisioning'
# ===========================================================================

RIG="$(new_rig rig1)"
BASE="$(git -C "$RIG" rev-parse HEAD)"
WT_A="$WORK/wt/worker-a"
WT_B="$WORK/wt/worker-b"

if wt_add "$RIG" "$WT_A" gc-worker-a "$BASE" && [ -d "$WT_A" ]; then
  pass 'controller creates task worktree A'
else
  fail 'controller creates task worktree A' 'wt_add failed'
fi
if wt_add "$RIG" "$WT_B" gc-worker-b "$BASE"; then
  pass 'controller creates task worktree B'
else
  fail 'controller creates task worktree B' 'wt_add failed'
fi

# Directory existence is not worktree registration. A plain mkdir would satisfy
# a `-d` check and satisfy nothing else, so the authority is git's own list.
if wt_is_registered "$RIG" "$WT_A" && wt_is_registered "$RIG" "$WT_B"; then
  pass 'both worktrees are registered git worktrees of the rig'
else
  fail 'both worktrees are registered git worktrees of the rig' \
       "$(git -C "$RIG" worktree list | tr '\n' ' ')"
fi

# The negative half: an ordinary directory must NOT read as registered.
mkdir -p "$WORK/wt/not-a-worktree"
if wt_is_registered "$RIG" "$WORK/wt/not-a-worktree"; then
  fail 'registration check rejects a plain directory' 'a bare mkdir read as a worktree'
else
  pass 'registration check rejects a plain directory'
fi

if [ "$(readlink -f "$WT_A")" != "$(readlink -f "$WT_B")" ]; then
  pass 'A and B worktrees are pairwise distinct'
else
  fail 'A and B worktrees are pairwise distinct' 'same path'
fi

if [ "$(git -C "$WT_A" rev-parse --abbrev-ref HEAD)" = 'gc-worker-a' ] &&
   [ "$(git -C "$WT_B" rev-parse --abbrev-ref HEAD)" = 'gc-worker-b' ]; then
  pass 'each worktree is on its own branch'
else
  fail 'each worktree is on its own branch' 'branch mismatch'
fi

# ATTEMPT FRESHNESS. A work_dir left behind by a previous attempt must not be
# adopted by a later one. wt_add refuses an existing path rather than reusing
# it, so a stale directory produces a loud failure instead of silent ownership.
if wt_add "$RIG" "$WT_A" gc-worker-a-2 "$BASE" 2>/dev/null; then
  fail 'stale attempt work_dir cannot satisfy a later attempt' \
       'wt_add adopted an existing path'
else
  pass 'stale attempt work_dir cannot satisfy a later attempt'
fi

# ===========================================================================
section '2. controller-owned integration and the dependent base'
# ===========================================================================

printf 'ALPHA_OK\n' > "$WT_A/ALPHA.md"
printf 'BETA_OK\n'  > "$WT_B/BETA.md"

A_COMMIT="$(controller_commit "$WT_A" 'feat: publish ALPHA.md' ALPHA.md)"
B_COMMIT="$(controller_commit "$WT_B" 'feat: publish BETA.md' BETA.md)"
if [ -n "$A_COMMIT" ] && [ -n "$B_COMMIT" ] && [ "$A_COMMIT" != "$B_COMMIT" ]; then
  pass 'controller commits each validated result on its own branch'
else
  fail 'controller commits each validated result on its own branch' \
       "A=$A_COMMIT B=$B_COMMIT"
fi

# Isolation is real: neither task worktree can see the other's artifact.
if [ ! -e "$WT_A/BETA.md" ] && [ ! -e "$WT_B/ALPHA.md" ]; then
  pass 'task worktrees are isolated from each other'
else
  fail 'task worktrees are isolated from each other' 'cross-visible artifact'
fi

INT1="$(controller_integrate "$RIG" main gc-worker-a 'integrate: A')"
INT2="$(controller_integrate "$RIG" main gc-worker-b 'integrate: B')"
if [ -n "$INT2" ] && [ "$INT2" != "$BASE" ] && [ "$INT1" != "$INT2" ]; then
  pass 'controller integrates both results into the run base'
else
  fail 'controller integrates both results into the run base' \
       "base=$BASE int1=$INT1 int2=$INT2"
fi

INTEGRATED_BASE="$(git -C "$RIG" rev-parse main)"
if [ -f "$RIG/ALPHA.md" ] && [ -f "$RIG/BETA.md" ]; then
  pass 'integrated base carries both upstream artifacts'
else
  fail 'integrated base carries both upstream artifacts' "$(ls "$RIG")"
fi

# The dependent worktree is cut FROM the integrated base, which is the only
# reason the dependent task can consume both upstreams without reading either
# upstream worktree path.
WT_C="$WORK/wt/worker-c"
if wt_add "$RIG" "$WT_C" gc-worker-c "$INTEGRATED_BASE"; then
  pass 'dependent worktree created from the integrated base'
else
  fail 'dependent worktree created from the integrated base' 'wt_add failed'
fi
if [ -f "$WT_C/ALPHA.md" ] && [ -f "$WT_C/BETA.md" ]; then
  pass 'dependent worktree sees both upstream artifacts via repository state'
else
  fail 'dependent worktree sees both upstream artifacts via repository state' \
       "$(ls "$WT_C" 2>/dev/null | tr '\n' ' ')"
fi
if [ "$(git -C "$WT_C" merge-base --is-ancestor "$A_COMMIT" HEAD; echo $?)" = 0 ] &&
   [ "$(git -C "$WT_C" merge-base --is-ancestor "$B_COMMIT" HEAD; echo $?)" = 0 ]; then
  pass 'dependent base descends from both validated commits'
else
  fail 'dependent base descends from both validated commits' 'ancestry check failed'
fi

# NEGATIVE CONTROL: a dependent worktree cut from the PRE-integration base must
# NOT see the upstream artifacts. Without this, "C saw both files" could be
# explained by the rig having been dirty rather than by integration working.
WT_STALE="$WORK/wt/worker-c-stale"
wt_add "$RIG" "$WT_STALE" gc-stale-c "$BASE" >/dev/null 2>&1
if [ -f "$WT_STALE/ALPHA.md" ] || [ -f "$WT_STALE/BETA.md" ]; then
  fail 'pre-integration base does NOT carry the upstream artifacts' \
       'artifacts visible from the un-integrated base — the positive control proves nothing'
else
  pass 'pre-integration base does NOT carry the upstream artifacts'
fi

# ===========================================================================
section '3. final-state snapshot (stale-evidence regression)'
# ===========================================================================
#
# The defect: capture the rendered bead, THEN test closure from a second read.
# A bead that closes between the two reads leaves the durable report showing
# OPEN — and missing the typed gc.work_outcome written at close.

STUB="$WORK/stubbin"
mkdir -p "$STUB"
cat > "$STUB/gc" <<'STUBGC'
#!/usr/bin/env bash
# Stub gc. The bead is open until the flip file exists, closed afterwards, and
# the typed work record only appears on the closed record — exactly as a real
# close transition behaves.
[ "$1" = 'bd' ] && [ "$2" = 'show' ] || exit 2
id="$3"
if [ -f "$STUB_FLIP" ]; then
  status=closed
else
  status=open
fi
if [ "${4:-}" = '--json' ]; then
  printf '[{"id":"%s","status":"%s"}]\n' "$id" "$status"
  exit 0
fi
printf 'Issue: %s\n  status: %s\n' "$id" "$status"
if [ "$status" = closed ]; then
  printf '  gc.work_outcome: blocked\n  gc.work_blocked_reason: git withheld by policy\n'
fi
exit 0
STUBGC
chmod +x "$STUB/gc"

SNAPDIR="$WORK/snap"
export STUB_FLIP="$WORK/flip"
OLDPATH="$PATH"
PATH="$STUB:$PATH"

# --- 3a. the control must be able to DETECT the defect -----------------------
# Reproduce the old ordering: render first, then test closure. The flip happens
# between the two reads, so the captured render is the pre-close one.
rm -f "$STUB_FLIP"
NAIVE_SHOW="$(gc bd show sa-ctl-1 2>&1)"
: > "$STUB_FLIP"                       # the bead closes here
NAIVE_STATUS="$(bead_status sa-ctl-1)"
if [ "$NAIVE_STATUS" = 'closed' ] && ! grep -q 'status: closed' <<<"$NAIVE_SHOW"; then
  pass 'control detects the stale-snapshot defect (naive capture shows open)'
else
  fail 'control detects the stale-snapshot defect' \
       "naive capture was not stale; this control cannot fail and proves nothing"
fi
if grep -q 'gc.work_outcome' <<<"$NAIVE_SHOW"; then
  fail 'stale capture lacks the typed work record' 'unexpectedly present'
else
  pass 'stale capture lacks the typed work record (the second half of the defect)'
fi

# --- 3b. the fix -------------------------------------------------------------
# Once the terminal predicate holds, the authoritative record is re-read and
# THAT is what the durable evidence stores.
if bead_is_closed sa-ctl-1; then
  FINAL_STATUS="$(capture_final_bead_state sa-ctl-1 "$SNAPDIR")"
else
  FINAL_STATUS='<predicate-false>'
fi
if [ "$FINAL_STATUS" = 'closed' ]; then
  pass 'final snapshot records the terminal status'
else
  fail 'final snapshot records the terminal status' "got '$FINAL_STATUS'"
fi
if grep -q 'status: closed' "$SNAPDIR/final-sa-ctl-1.txt" 2>/dev/null; then
  pass 'durable final evidence shows CLOSED, not the pre-close render'
else
  fail 'durable final evidence shows CLOSED, not the pre-close render' \
       "$(head -3 "$SNAPDIR/final-sa-ctl-1.txt" 2>/dev/null | tr '\n' ' ')"
fi
if [ "$(final_meta sa-ctl-1 "$SNAPDIR" 'gc.work_outcome')" = 'blocked' ]; then
  pass 'typed gc.work_outcome read from the same authoritative final state'
else
  fail 'typed gc.work_outcome read from the same authoritative final state' \
       "got '$(final_meta sa-ctl-1 "$SNAPDIR" 'gc.work_outcome')'"
fi

# --- 3c. it must refuse to store a non-final record as final -----------------
rm -f "$STUB_FLIP"
if capture_final_bead_state sa-ctl-2 "$SNAPDIR" >/dev/null; then
  fail 'final capture refuses a non-terminal record' 'returned success on an open bead'
else
  pass 'final capture refuses a non-terminal record'
fi
PATH="$OLDPATH"

# ===========================================================================
section '4. command ledger — no post-release dispatch naming the dependent'
# ===========================================================================

sa_ledger_init "$WORK/gc-commands.log"
PATH="$STUB:$PATH"
gcx bd show sa-ctl-1 >/dev/null 2>&1     # pre-release: names the dependent
sleep 1
RELEASE_EPOCH="$(date +%s)"
sleep 1
gcx bd show sa-other >/dev/null 2>&1     # post-release: names something else
PATH="$OLDPATH"

if [ -z "$(sa_ledger_mentions_after "$RELEASE_EPOCH" 'sa-ctl-1')" ]; then
  pass 'ledger shows no post-release command naming the dependent'
else
  fail 'ledger shows no post-release command naming the dependent' \
       "$(sa_ledger_mentions_after "$RELEASE_EPOCH" 'sa-ctl-1')"
fi
# The detector half: a post-release command naming the dependent MUST be caught.
if [ -n "$(sa_ledger_mentions_after "$RELEASE_EPOCH" 'sa-other')" ]; then
  pass 'ledger detects a post-release command when one exists'
else
  fail 'ledger detects a post-release command when one exists' \
       'the ledger cannot see post-release commands and proves nothing'
fi

# ===========================================================================
section '5. ready-set membership is decided by the ID column'
# ===========================================================================
#
# Regression for a defect the live D3 probe caught. `gc sling` creates an
# auto-convoy titled "sling-<bead-id>", so a blocked bead's own id appears
# inside a DIFFERENT bead's title in the same `bd ready` listing. A whole-line
# grep therefore reported a correctly-withheld bead as ready — and the same
# match, used after release, would have reported "released" for a bead that
# never was. Both directions of the dependency gate were unreliable.

READY="$WORK/ready.txt"
cat > "$READY" <<'READYOUT'
○ pr2-16a ● P2 sling-pr2-c8f
○ pr2-0fi ● P2 D3 probe blocker: the controller closes this by hand

--------------------------------------------------------------------------------
Ready: 2 issues with no active blockers
READYOUT

if sa_bead_in_ready 'pr2-0fi' "$READY"; then
  pass 'a bead present in the ready set is detected'
else
  fail 'a bead present in the ready set is detected' 'pr2-0fi not found'
fi
if sa_bead_in_ready 'pr2-c8f' "$READY"; then
  fail 'a bead named only inside another bead title is NOT ready' \
       "pr2-c8f matched via the auto-convoy title 'sling-pr2-c8f'"
else
  pass 'a bead named only inside another bead title is NOT ready'
fi
# The detector half: the naive predicate this replaced must be shown to fail on
# the same input, or the control above is untested.
if grep -q 'pr2-c8f' "$READY"; then
  pass 'control detects the defect (a whole-line grep does match the convoy title)'
else
  fail 'control detects the defect' 'the fixture no longer reproduces the defect'
fi
if sa_bead_in_ready 'pr2-zzz' "$READY"; then
  fail 'an absent bead is not reported ready' 'false positive'
else
  pass 'an absent bead is not reported ready'
fi

echo
echo '============================================================'
if [ "$FAILURES" -ne 0 ]; then
  echo "S-A CONTROLS: FAIL ($FAILURES check(s))"
  exit 70
fi
echo 'S-A CONTROLS: PASS'
