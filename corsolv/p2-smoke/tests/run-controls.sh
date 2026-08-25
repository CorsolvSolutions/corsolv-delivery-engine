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

# --- 4b. no gc invocation can bypass ledger capture -------------------------
#
# Capture used to live one layer up, in gcx(), with sa_gc() executing gc
# directly underneath. Any call routed through sa_gc — including a future
# mutating one — therefore never reached the ledger, so the D3 claim was about
# the calls someone remembered to route through gcx rather than about every
# call made. No actual bypass occurred in the committed run; this pins the
# structure so none can.

sa_ledger_init "$WORK/bypass.log"
PATH="$STUB:$PATH"
BYPASS_EPOCH="$(date +%s)"
sleep 1
sa_gc bd update sa-ctl-9 --set-metadata k=v >/dev/null 2>&1   # MUTATING, via the low wrapper
sa_gc bd show sa-ctl-9 >/dev/null 2>&1                        # read-only, via the low wrapper
gcx bd close sa-ctl-9 >/dev/null 2>&1                         # mutating, via the historical spelling
PATH="$OLDPATH"

if [ -n "$(sa_ledger_mentions_after "$BYPASS_EPOCH" 'bd update sa-ctl-9')" ]; then
  pass 'a mutating call made through the LOW-level wrapper reaches the ledger'
else
  fail 'a mutating call made through the LOW-level wrapper reaches the ledger' \
       'sa_gc executed gc without recording it — the D3 assertion would be blind to it'
fi
if [ -n "$(sa_ledger_directives_after "$BYPASS_EPOCH" 'bd update sa-ctl-9')" ]; then
  pass 'that mutating call is classified as a DIRECTIVE'
else
  fail 'that mutating call is classified as a directive' 'it was treated as read-only'
fi
if [ -n "$(sa_ledger_mentions_after "$BYPASS_EPOCH" 'bd show sa-ctl-9')" ] &&
   [ -z "$(sa_ledger_directives_after "$BYPASS_EPOCH" 'bd show sa-ctl-9')" ]; then
  pass 'read-only calls are recorded but NOT classified as directives'
else
  fail 'read-only calls are recorded but not classified as directives' \
       'read/directive discrimination was lost when capture moved down'
fi
if [ -n "$(sa_ledger_directives_after "$BYPASS_EPOCH" 'bd close sa-ctl-9')" ]; then
  pass 'the historical gcx spelling still records directives'
else
  fail 'the historical gcx spelling still records directives' 'gcx stopped recording'
fi
# Capture happens once, not twice: gcx delegates to sa_gc rather than logging
# itself. A double-counted ledger is a corrupted evidence artifact.
DOUBLE="$(awk -F'\t' '$3 == "bd close sa-ctl-9"' "$WORK/bypass.log" | wc -l)"
if [ "$DOUBLE" -eq 1 ]; then
  pass 'each invocation is recorded exactly once'
else
  fail 'each invocation is recorded exactly once' "recorded $DOUBLE times"
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

# ===========================================================================
section '6. publication scope — the bounded-project boundary'
# ===========================================================================
#
# bounded-project lets a worker Write/Edit and run npm scripts, so it can edit
# package.json and have `npm test` execute the result. The permission list does
# not contain that; the AUTHORITY split does — and only if the controller
# inspects what changed before publishing. These controls prove the inspection
# refuses what it must.

SCOPE_RIG="$(new_rig scope-rig)"
printf 'export const add = (a: number, b: number): number => a + b;\n' > "$SCOPE_RIG/add.ts"
printf 'test\n' > "$SCOPE_RIG/add.test.ts"

if [ -z "$(publication_scope_violations "$SCOPE_RIG" 'add.ts,add.test.ts')" ]; then
  pass 'publication proceeds when the changed set is exactly what the bead authorised'
else
  fail 'publication proceeds when the changed set is exactly what the bead authorised' \
       "$(publication_scope_violations "$SCOPE_RIG" 'add.ts,add.test.ts' | tr '\n' ' ')"
fi

# THE CASE THE SECURITY SEMANTIC NAMES EXPLICITLY.
printf '{"scripts":{"build":"echo pwned"}}\n' > "$SCOPE_RIG/package.json"
VIOL="$(publication_scope_violations "$SCOPE_RIG" 'add.ts,add.test.ts')"
if grep -qx 'package.json' <<<"$VIOL"; then
  pass 'publication REFUSES an unauthorised package.json change'
else
  fail 'publication refuses an unauthorised package.json change' \
       "violations were: $(printf '%s' "$VIOL" | tr '\n' ' ')"
fi

# Any other unauthorised file, not just the one we thought of.
printf 'x\n' > "$SCOPE_RIG/src-unrelated.ts"
VIOL="$(publication_scope_violations "$SCOPE_RIG" 'add.ts,add.test.ts')"
if grep -qx 'src-unrelated.ts' <<<"$VIOL"; then
  pass 'publication refuses any file the bead did not authorise'
else
  fail 'publication refuses any file the bead did not authorise' \
       "violations were: $(printf '%s' "$VIOL" | tr '\n' ' ')"
fi

# Authorisation is bounded by the path separator, never the string prefix: a
# near-miss filename must not inherit an authorised file's permission.
rm -f "$SCOPE_RIG/package.json" "$SCOPE_RIG/src-unrelated.ts"
printf 'x\n' > "$SCOPE_RIG/add.ts.bak"
if grep -qx 'add.ts.bak' <<<"$(publication_scope_violations "$SCOPE_RIG" 'add.ts,add.test.ts')"; then
  pass 'authorisation is separator-bounded, not a filename prefix'
else
  fail 'authorisation is separator-bounded, not a filename prefix' 'add.ts.bak was treated as authorised'
fi

# An authorised DIRECTORY authorises its subtree. The validator has always
# accepted directory entries, and a package that generates an open-ended set
# of files cannot spell them out before a worker writes them — a validator
# that admits "lib" beside an adjudicator that refuses "lib/button.ts"
# accepts plans it can never publish (scorm-studio-redesign-2 finished two
# gated packages and published neither).
rm -f "$SCOPE_RIG/add.ts.bak"
mkdir -p "$SCOPE_RIG/lib/nested"
printf 'x\n' > "$SCOPE_RIG/lib/button.ts"
printf 'x\n' > "$SCOPE_RIG/lib/nested/deep.ts"
if [ -z "$(publication_scope_violations "$SCOPE_RIG" 'add.ts,add.test.ts,lib')" ]; then
  pass 'an authorised directory authorises its subtree'
else
  fail 'an authorised directory authorises its subtree' \
       "$(publication_scope_violations "$SCOPE_RIG" 'add.ts,add.test.ts,lib' | tr '\n' ' ')"
fi

# A trailing slash on the entry means the same thing.
if [ -z "$(publication_scope_violations "$SCOPE_RIG" 'add.ts,add.test.ts,lib/')" ]; then
  pass 'a trailing slash on a directory entry changes nothing'
else
  fail 'a trailing slash on a directory entry changes nothing' \
       "$(publication_scope_violations "$SCOPE_RIG" 'add.ts,add.test.ts,lib/' | tr '\n' ' ')"
fi

# And the directory grant is still separator-bounded: "lib" does not
# authorise "library.ts".
printf 'x\n' > "$SCOPE_RIG/library.ts"
if grep -qx 'library.ts' <<<"$(publication_scope_violations "$SCOPE_RIG" 'add.ts,add.test.ts,lib')"; then
  pass 'a directory grant does not leak past the separator'
else
  fail 'a directory grant does not leak past the separator' 'library.ts was treated as authorised'
fi
rm -rf "$SCOPE_RIG/lib" "$SCOPE_RIG/library.ts"

# Controller/toolchain paths are excluded from attribution rather than
# authorised — they are not the worker's to author, and treating them as
# violations would make every real run refuse to publish.
rm -f "$SCOPE_RIG/add.ts.bak"
mkdir -p "$SCOPE_RIG/node_modules/x" "$SCOPE_RIG/dist" "$SCOPE_RIG/.gc"
printf 'x\n' > "$SCOPE_RIG/node_modules/x/y.js"
printf 'x\n' > "$SCOPE_RIG/dist/out.js"
printf 'x\n' > "$SCOPE_RIG/.gc/state"
if [ -z "$(publication_scope_violations "$SCOPE_RIG" 'add.ts,add.test.ts')" ]; then
  pass 'build output and runtime state do not block publication'
else
  fail 'build output and runtime state do not block publication' \
       "$(publication_scope_violations "$SCOPE_RIG" 'add.ts,add.test.ts' | tr '\n' ' ')"
fi

# ===========================================================================
section '6b. publication scope — attribution is per FILE'
# ===========================================================================
#
# A package that creates a new directory is the normal case, not the exception:
# `src/`, `tests/`, `public/` do not exist before the scaffold package runs. Git
# reports an untracked directory as the directory alone unless asked otherwise,
# so attribution built on the default listing judges a path the bead can never
# authorise — and every compliant package that creates a directory is refused.
# These controls pin attribution to the file the bead actually named.

DIR_RIG="$(new_rig dir-rig)"
mkdir -p "$DIR_RIG/src" "$DIR_RIG/tests"
printf 'export const x = 1;\n' > "$DIR_RIG/src/config.js"
printf 'test\n'                > "$DIR_RIG/tests/config.test.js"

VIOL="$(publication_scope_violations "$DIR_RIG" 'src/config.js,tests/config.test.js')"
if [ -z "$VIOL" ]; then
  pass 'a fully authorised NEW directory does not block publication'
else
  fail 'a fully authorised NEW directory does not block publication' \
       "violations were: $(printf '%s' "$VIOL" | tr '\n' ' ')"
fi

# The same listing must still catch the file nobody authorised — collapsing to
# the directory would hide it just as readily as it hid the authorised ones.
printf 'x\n' > "$DIR_RIG/src/smuggled.js"
VIOL="$(publication_scope_violations "$DIR_RIG" 'src/config.js,tests/config.test.js')"
if grep -qx 'src/smuggled.js' <<<"$VIOL"; then
  pass 'an unauthorised file inside an authorised directory is still refused'
else
  fail 'an unauthorised file inside an authorised directory is still refused' \
       "violations were: $(printf '%s' "$VIOL" | tr '\n' ' ')"
fi
rm -f "$DIR_RIG/src/smuggled.js"

# Attribution must survive a filename git would otherwise quote and a reader
# would otherwise split on whitespace.
printf 'x\n' > "$DIR_RIG/src/two words.js"
VIOL="$(publication_scope_violations "$DIR_RIG" 'src/config.js,tests/config.test.js')"
if grep -qx 'src/two words.js' <<<"$VIOL"; then
  pass 'a path containing a space is attributed whole'
else
  fail 'a path containing a space is attributed whole' \
       "violations were: $(printf '%s' "$VIOL" | tr '\n' ' ')"
fi
if [ -z "$(publication_scope_violations "$DIR_RIG" 'src/config.js,tests/config.test.js,src/two words.js')" ]; then
  pass 'a path containing a space can be authorised'
else
  fail 'a path containing a space can be authorised' \
       "$(publication_scope_violations "$DIR_RIG" 'src/config.js,tests/config.test.js,src/two words.js' | tr '\n' ' ')"
fi
rm -f "$DIR_RIG/src/two words.js"

# A rename moves content between two paths. Both ends are the worker's doing, so
# both must be attributed — otherwise a tracked file can be emptied out of scope
# by moving it somewhere the bead did authorise.
git_quiet "$DIR_RIG" add src/config.js tests/config.test.js
git_quiet "$DIR_RIG" commit -qm 'chore: track the authorised files'
git -C "$DIR_RIG" mv src/config.js src/renamed.js
VIOL="$(publication_scope_violations "$DIR_RIG" 'src/config.js,tests/config.test.js')"
if grep -qx 'src/renamed.js' <<<"$VIOL"; then
  pass 'a rename attributes its destination path'
else
  fail 'a rename attributes its destination path' \
       "violations were: $(printf '%s' "$VIOL" | tr '\n' ' ')"
fi
git -C "$DIR_RIG" mv src/renamed.js src/config.js

# ===========================================================================
section '6c. transient files — a worker that cannot delete is not unpublishable'
# ===========================================================================
#
# bounded-project grants Write and Edit and nothing that REMOVES a file, and a
# cleanup command cannot be declared as a verification gate. A worker that
# writes a transient probe to prove its lint or type configuration covers a
# directory therefore permanently blocks its own package: it can create the file
# and it cannot take it back.
#
# The controller can. It owns the worktree, it commits only the paths the bead
# named — so an untracked out-of-scope file was never going to reach the commit
# anyway — and the only real risk such a file carries is contaminating the gate
# run the controller does before publishing. So the controller QUARANTINES it:
# moves it out of the tree, keeps it as evidence, and gates the clean tree.
#
# The authority split is unchanged, and these controls pin the two edges of it:
# an untracked stray is recoverable, a TRACKED file changed out of scope is not.

Q_RIG="$(new_rig quarantine-rig)"
mkdir -p "$Q_RIG/src" "$Q_RIG/public"
printf 'export const x = 1;\n'    > "$Q_RIG/src/config.js"
printf 'tracked\n'                > "$Q_RIG/keep.ts"
git_quiet "$Q_RIG" add keep.ts
git_quiet "$Q_RIG" commit -qm 'chore: a tracked file the bead does not authorise'
printf 'probe\n'                  > "$Q_RIG/public/__lintprobe.js"
printf 'probe\n'                  > "$Q_RIG/src/__typeprobe.js"
Q_DEST="$WORK/quarantined"

MOVED="$(quarantine_untracked_out_of_scope "$Q_RIG" 'src/config.js' "$Q_DEST")"
if grep -qx 'public/__lintprobe.js' <<<"$MOVED" && grep -qx 'src/__typeprobe.js' <<<"$MOVED"; then
  pass 'the controller reports every untracked file it quarantined'
else
  fail 'the controller reports every untracked file it quarantined' \
       "reported: $(printf '%s' "$MOVED" | tr '\n' ' ')"
fi
if [ ! -e "$Q_RIG/public/__lintprobe.js" ] && [ ! -e "$Q_RIG/src/__typeprobe.js" ]; then
  pass 'a quarantined file is gone from the tree the controller gates and commits'
else
  fail 'a quarantined file is gone from the tree the controller gates and commits' \
       'a probe file survived quarantine'
fi
if [ -f "$Q_DEST/public/__lintprobe.js" ] && [ -f "$Q_DEST/src/__typeprobe.js" ]; then
  pass 'a quarantined file is kept as evidence, not destroyed'
else
  fail 'a quarantined file is kept as evidence, not destroyed' \
       "$(ls -R "$Q_DEST" 2>&1 | tr '\n' ' ')"
fi
if [ -f "$Q_RIG/src/config.js" ]; then
  pass 'quarantine leaves the authorised work untouched'
else
  fail 'quarantine leaves the authorised work untouched' 'src/config.js was removed'
fi

# THE EDGE THAT MUST NOT MOVE. A tracked file changed outside the bead's scope
# is a mutation of content the project already had. Quarantining it would be the
# controller silently reverting the worker, and treating it as transient would
# let an out-of-scope edit ride to publication. It stays a refusal.
printf 'tampered\n' > "$Q_RIG/keep.ts"
MOVED="$(quarantine_untracked_out_of_scope "$Q_RIG" 'src/config.js' "$Q_DEST")"
if grep -qx 'keep.ts' <<<"$MOVED"; then
  fail 'quarantine never touches a TRACKED out-of-scope change' 'keep.ts was quarantined'
else
  pass 'quarantine never touches a TRACKED out-of-scope change'
fi
if [ "$(cat "$Q_RIG/keep.ts")" = 'tampered' ]; then
  pass 'a tracked out-of-scope change is left exactly as the worker left it'
else
  fail 'a tracked out-of-scope change is left exactly as the worker left it' \
       "keep.ts now reads: $(cat "$Q_RIG/keep.ts")"
fi
VIOL="$(publication_scope_violations "$Q_RIG" 'src/config.js')"
if grep -qx 'keep.ts' <<<"$VIOL"; then
  pass 'a tracked out-of-scope change still REFUSES publication after quarantine'
else
  fail 'a tracked out-of-scope change still refuses publication after quarantine' \
       "violations were: $(printf '%s' "$VIOL" | tr '\n' ' ')"
fi

# After quarantine the only thing standing between a compliant package and
# publication must be nothing at all.
git_quiet "$Q_RIG" checkout -- keep.ts
if [ -z "$(publication_scope_violations "$Q_RIG" 'src/config.js')" ]; then
  pass 'a package whose only strays were transient publishes after quarantine'
else
  fail 'a package whose only strays were transient publishes after quarantine' \
       "$(publication_scope_violations "$Q_RIG" 'src/config.js' | tr '\n' ' ')"
fi

# Quarantine is not a licence to launder infrastructure: paths excluded from
# attribution were never violations, so moving them would delete a real
# node_modules the very next gate needs.
mkdir -p "$Q_RIG/node_modules/left-pad"
printf 'x\n' > "$Q_RIG/node_modules/left-pad/index.js"
quarantine_untracked_out_of_scope "$Q_RIG" 'src/config.js' "$Q_DEST" >/dev/null
if [ -f "$Q_RIG/node_modules/left-pad/index.js" ]; then
  pass 'quarantine does not remove installed dependencies'
else
  fail 'quarantine does not remove installed dependencies' 'node_modules was quarantined'
fi

# ===========================================================================
section '7. required-job CI adjudication'
# ===========================================================================
#
# "CI passed" is not one fact. A workflow run concludes success when its jobs
# did — including when the only job was skipped — and a project may gate on
# several jobs or a matrix. Adjudicating the first matching job is right for one
# required job and quietly wrong for any other. These controls pin every case
# the contract has to answer.

RE='typecheck|validate'
jobs() { printf '{"jobs":[%s]}' "$1"; }

V="$(sb_required_job_verdict "$(jobs '{"name":"typecheck + test","conclusion":"success"}')" "$RE")"
case "$V" in
  ok:1:*) pass 'one required job succeeding is a pass' ;;
  *) fail 'one required job succeeding is a pass' "verdict '$V'" ;;
esac

V="$(sb_required_job_verdict "$(jobs '{"name":"typecheck","conclusion":"success"},{"name":"validate (18)","conclusion":"success"}')" "$RE")"
case "$V" in
  ok:2:*) pass 'two required jobs succeeding is a pass' ;;
  *) fail 'two required jobs succeeding is a pass' "verdict '$V'" ;;
esac

# THE CASE THE OLD LOGIC GOT WRONG: the first required job passed, the second
# did not. Adjudicating only .[0] reported this green.
V="$(sb_required_job_verdict "$(jobs '{"name":"typecheck","conclusion":"success"},{"name":"validate (20)","conclusion":"failure"}')" "$RE")"
case "$V" in
  fail:*validate*) pass 'one of two required jobs failing is a FAIL' ;;
  *) fail 'one of two required jobs failing is a FAIL' "verdict '$V' — a failing required job read as green" ;;
esac

# Optional/advisory jobs are outside the contract and must not manufacture a
# failure; requiring every GitHub job green would be the wrong repair.
V="$(sb_required_job_verdict "$(jobs '{"name":"typecheck + test","conclusion":"success"},{"name":"lint-advisory","conclusion":"failure"},{"name":"notify","conclusion":"skipped"}')" "$RE")"
case "$V" in
  ok:1:*) pass 'a non-required job failing does not create a false failure' ;;
  *) fail 'a non-required job failing does not create a false failure' "verdict '$V'" ;;
esac

# A skipped REQUIRED job is not a pass: the gate did not run.
V="$(sb_required_job_verdict "$(jobs '{"name":"validate","conclusion":"skipped"}')" "$RE")"
case "$V" in
  fail:*) pass 'a skipped required job is not a pass' ;;
  *) fail 'a skipped required job is not a pass' "verdict '$V'" ;;
esac

# A missing required job is NOT REACHED, never a pass.
V="$(sb_required_job_verdict "$(jobs '{"name":"build-docs","conclusion":"success"}')" "$RE")"
case "$V" in
  missing) pass 'a missing required job is NOT REACHED, not a pass' ;;
  *) fail 'a missing required job is NOT REACHED, not a pass' "verdict '$V'" ;;
esac

echo
echo '============================================================'
if [ "$FAILURES" -ne 0 ]; then
  echo "S-A CONTROLS: FAIL ($FAILURES check(s))"
  exit 70
fi
echo 'S-A CONTROLS: PASS'
