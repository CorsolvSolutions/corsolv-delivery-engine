#!/usr/bin/env bash
#
# STAGE S-A — local controlled first-runner acceptance.
#
# SCOPE NOTE — READ BEFORE CITING THIS RUN AS EVIDENCE.
#
# The authoritative acceptance standard is NOT in this repository. It is the
# 14 numbered criteria in D:\Development\corsolv-autonomy-poc\POC-BRIEF.md,
# recorded as passing in that repo's artifacts/POC-RESULT.md against real PRs
# (#1/#2/#3), exact-SHA CI runs, and a final main SHA. W1/W2/W3 are defined
# there: W1 add, W2 multiply, W3 calculator — W3 dependent on BOTH W1 and W2
# being MERGED.
#
# WHAT "MERGED" MEANS ACROSS THE TWO STAGES — a recorded decision, not a
# finding. The POC had no local/remote split; this programme introduces one, so
# the split is written down where it cannot quietly become a weakening:
#
#   S-A  merged = the controller integrates VALIDATED A/B commits into the
#                 local run base. Proves dependency ordering, per-task
#                 isolation, and autonomous dispatch.
#   S-B  merged = remote GitHub merge after PR + exact-head CI + independent
#                 assurance. Proves criteria 7, 11, and the remote half of 10.
#
# Criterion 10 is therefore satisfied only ACROSS BOTH stages. S-A may claim
# its LOCAL HALF and must not claim the criterion: the remote half stays NOT
# REACHED until S-B. Per the POC brief, NOT REACHED is never reported as PASS.
#
# ---------------------------------------------------------------------------
# THE SHAPE, AND WHY IT IS THIS SHAPE.
#
# P2.1 proved ONE bead end-to-end. That cannot show whether the ENGINE
# remembers: which work is ready, what blocks what, who owns which worktree,
# and what happens next — without a human tracking it. Three beads can:
#
#   A ──┐
#       ├──> C          A and B independent, C dependent on both
#   B ──┘
#
# Two requirements pull against each other here, and the conflict is invisible
# until both are attempted together:
#
#   - PER-TASK WORKTREES mean C cannot read A's and B's files directly. It must
#     receive them through repository state, from a base that already contains
#     both — and that base only exists AFTER the controller integrates them.
#   - AUTONOMOUS DISPATCH (D3) forbids any harness action naming C once its
#     dependencies clear. So C must be routed up front.
#
# If C gated on A and B CLOSING, it would become ready BEFORE the controller
# had integrated anything, and a worker could claim it against a base with no
# ALPHA.md or BETA.md in it. Closing that race by polling, delaying or
# re-stamping C would itself be a post-release action naming C, and inventing a
# "dependency integrated" event is out of bounds.
#
# The resolution uses only primitives already in play — beads and dependencies:
#
#     A  <- A-int                A-int: controller validates and integrates A
#     B  <- B-int                B-int: controller validates and integrates B
#     A-int, B-int  <- C         C gates on INTEGRATION, not on close
#
# The controller owns and closes the two integration beads, which is the same
# authority boundary enforced everywhere else here: workers mutate a working
# tree, the controller publishes. Readiness projection then holds C until the
# integrated base exists — with no new event type, no harness command naming C
# after release, and no weakening of workDirStampHasOwnershipEvidence.
#
# WHY --no-formula ON EVERY ROUTE. A formula sling attaches a wisp and routes
# the WISP ROOT. The dependency gate is on C, not on a root created beside it,
# so a routed wisp root could present as unblocked demand and spawn C's worker
# while A and B were still running — the exact race the integration beads
# exist to remove. Routing the raw bead keeps the dispatched entity and the
# gated entity the same object.
#
# Exit: 0 S-A passed, 70 otherwise.

set -uo pipefail

export PATH="$HOME/.local/bin:/usr/local/go/bin:$PATH"
export GC_HOME="$HOME/.gc-corsolv-p2"
export GOTOOLCHAIN=auto

# Make the typed work-record contract BLOCK rather than warn for the harness's
# own gc calls. `[workspace.env]` below does the same for every managed session,
# which is the half that matters: a worker's `gc bd close` does not inherit this
# shell. Without it, beads closed as `shipped` with no commit, closed with no
# disposition at all, and twice closed as `completed-uncommitted` — a value
# outside the typed vocabulary.
export GC_WORK_RECORD_ENFORCE=1

SOURCE_REPO="${SOURCE_REPO:-/mnt/d/Development/corsolv-delivery-engine}"
REPORT="$SOURCE_REPO/engdocs/corsolv/S-A-RESULT.md"
# shellcheck source=lib/sa-lib.sh
. "$SOURCE_REPO/corsolv/p2-smoke/lib/sa-lib.sh"

TIMESTAMP="$(date -u +%Y%m%dT%H%M%SZ)"
RUN_ID="sa-$TIMESTAMP"
CITY="$HOME/corsolv-p2/sa-city-$TIMESTAMP"
TARGET="$HOME/corsolv-p2/sa-rig-$TIMESTAMP"
RIG_NAME="sa-rig-$TIMESTAMP"
EVIDENCE="$HOME/corsolv-p2/sa-evidence-$TIMESTAMP"
mkdir -p "$EVIDENCE"

SA_CITY="$CITY"
SA_RIG="$RIG_NAME"

WT_ROOT="$CITY/.gc/worktrees/$RIG_NAME"
WT_A="$WT_ROOT/worker-a"
WT_B="$WT_ROOT/worker-b"
WT_C="$WT_ROOT/worker-c"

AB_DEADLINE="${AB_DEADLINE:-1500}"
C_DEADLINE="${C_DEADLINE:-1500}"

FAILURES=0
NOT_REACHED_COUNT=0
PASS_COUNT=0

# D1/E3 — EVERY ASSERTION GETS A DURABLE IDENTITY.
#
# The console is not evidence: not committed, not attached to the run, gone once
# the terminal scrolls. A report that says only "OVERALL: FAIL (2)" cannot tell
# a reader WHICH controls failed, so the durable artefact would be strictly
# weaker than the transcript nobody kept. Every result is appended to a TSV as
# it happens, so the report can name each failure exactly and a run that dies
# mid-flight still leaves the controls it reached.
CONTROLS="$EVIDENCE/controls.tsv"
printf 'control\tstatus\treason\tsubject\n' > "$CONTROLS"
record() { printf '%s\t%s\t%s\t%s\n' "$1" "$2" "${3:-}" "${4:-}" >> "$CONTROLS"; }
note() { printf '  %-66s %s\n' "$1" "$2"; }
pass() { note "$1" 'PASS'; record "$1" PASS '' "${2:-}"; PASS_COUNT=$((PASS_COUNT + 1)); }
fail() { note "$1" "FAIL — $2"; record "$1" FAIL "$2" "${3:-}"; FAILURES=$((FAILURES + 1)); }
info() { note "$1" "INFO — $2"; record "$1" INFO "$2" "${3:-}"; }
# not_reached: a mandatory control that could not be evaluated. Never a pass.
not_reached() {
  note "$1" "NOT REACHED — $2"; record "$1" NOT_REACHED "$2" "${3:-}"
  NOT_REACHED_COUNT=$((NOT_REACHED_COUNT + 1))
}
section() { printf '\n--- %s ---\n' "$1"; }
abort() { printf '\nABORT: %s\n' "$1"; exit 70; }

echo '============================================================'
echo 'CORSOLV S-A — LOCAL CONTROLLED FIRST-RUNNER ACCEPTANCE'
echo '============================================================'
echo "run:      $RUN_ID"
echo "city:     $CITY"
echo "rig:      $TARGET"
echo "evidence: $EVIDENCE"

sa_ledger_init "$EVIDENCE/gc-commands.log"

# ===========================================================================
section '0. foundation'
# ===========================================================================

# E1 — SOURCE INTEGRITY. Refuse to run from a dirty tracked tree.
#
# An acceptance report that names a source SHA must have executed that source.
# An earlier report recorded SHA 7c99403 while containing behaviour from later
# uncommitted edits: true of HEAD, false of what ran. That is an
# evidence-integrity failure, so it is a HARD ABORT before anything is
# dispatched rather than one failure among many.
SOURCE_SHA="$(git -C "$SOURCE_REPO" rev-parse HEAD)"
SOURCE_BRANCH="$(git -C "$SOURCE_REPO" rev-parse --abbrev-ref HEAD)"
DIRTY="$(git -C "$SOURCE_REPO" status --porcelain)"
if [ -n "$DIRTY" ]; then
  fail 'source tree is clean' 'refusing to dispatch from a dirty tracked tree'
  printf '%s\n' "$DIRTY" | sed 's/^/      /' | head -20
  abort 'acceptance requires a clean committed source tree (E1).'
fi
pass "source tree is clean ($SOURCE_BRANCH @ ${SOURCE_SHA:0:9})"

install -m 755 "$SOURCE_REPO/bin/gc" "$HOME/.local/bin/gc"
BIN_SHA="$(sha256sum "$SOURCE_REPO/bin/gc" | awk '{print $1}')"

# E2 — STALE SUPERVISOR IS A HARD ABORT, adjudicated by fingerprint.
#
# The supervisor materializes every session's launch command, so one running an
# older image silently proves the previous build. "Is a supervisor running" is
# the wrong test: it is a systemd user service restarted within seconds of every
# stop, so a wait-for-none loop can only time out — which is exactly how a run
# once aborted while the supervisor was already executing the correct image.
# Require instead that the running image is byte-equal to the binary just
# installed, read from /proc/<pid>/exe (the inode the kernel gave the process,
# so it survives PATH being replaced underneath it).
command gc supervisor stop >/dev/null 2>&1 || true
SUP_OK=0
for _ in $(seq 1 45); do
  SUP_PID="$(pgrep -f 'gc supervisor run' | head -1)"
  if [ -n "$SUP_PID" ]; then
    RUNNING_SHA="$(sha256sum "/proc/$SUP_PID/exe" 2>/dev/null | awk '{print $1}')"
    [ "$RUNNING_SHA" = "$BIN_SHA" ] && { SUP_OK=1; break; }
  fi
  sleep 2
done
if [ "$SUP_OK" -ne 1 ]; then
  fail 'supervisor runs the fingerprinted build' \
       "expected $BIN_SHA, running ${RUNNING_SHA:-<none>} (pid ${SUP_PID:-none})"
  abort 'refusing to dispatch against a supervisor of unknown provenance (E2).'
fi
pass "supervisor runs the fingerprinted build (pid $SUP_PID)"
info 'source sha' "$SOURCE_SHA"
info 'binary sha256' "$BIN_SHA"
info 'gc version' "$(command gc version 2>&1 | head -1) (dev is expected on an untagged branch)"

# ===========================================================================
section '1. disposable target and city'
# ===========================================================================

mkdir -p "$TARGET" && cd "$TARGET" || abort 'cannot enter target'
git init -q -b main
git config user.name 'Corsolv Autonomy POC'
git config user.email 'support@corsolv.com'
printf '# Corsolv S-A target\n\nDisposable repository for the S-A acceptance run.\n' > README.md
git add README.md
git -c user.name='Corsolv Autonomy POC' -c user.email='support@corsolv.com' \
  commit -qm 'chore: initialise S-A target'
BASE_SHA="$(git rev-parse HEAD)"
pass "disposable git target created (base ${BASE_SHA:0:9})"

command gc init "$CITY" --provider claude --yes >"$EVIDENCE/init.txt" 2>&1 \
  || { fail 'gc init' 'see init.txt'; abort 'city creation failed'; }

# Deliver work-record enforcement to the WORKERS. `[workspace] env` is the
# documented boundary — "workspace-wide environment variables applied to every
# managed session" — merged into the spawned agent's environment in
# cmd/gc/template_resolve.go. Written before the rig is added and before any
# session spawns, so the first worker already has it.
cat >> "$CITY/city.toml" <<'TOML'

# Corsolv acceptance: a worker may not close a bead with an invented, absent, or
# unearned disposition. `shipped` requires a reachable commit, which this policy
# withholds from workers by design.
[workspace.env]
GC_WORK_RECORD_ENFORCE = "1"
TOML
if grep -q 'GC_WORK_RECORD_ENFORCE' "$CITY/city.toml"; then
  pass 'work-record enforcement delivered to managed sessions'
else
  fail 'work-record enforcement delivered to managed sessions' 'city.toml not updated'
fi

mkdir -p "$CITY/scripts"
install -m 755 "$SOURCE_REPO/corsolv/p2-smoke/scripts/worktree-setup.sh" \
  "$CITY/scripts/worktree-setup.sh"

cd "$CITY" || abort 'cannot enter city'
gcx rig add "$TARGET" >"$EVIDENCE/rigadd.txt" 2>&1 || fail 'gc rig add' 'see rigadd.txt'
if sa_wait_rig_beads "$CITY" "$RIG_NAME" 180; then
  pass 'rig beads store initialized'
else
  fail 'rig beads store initialized' 'not ready within 180s'
  abort 'rig store never came up'
fi

# Three single-capacity worker agents, one per task. The work_dir template
# surface carries no per-slot variable, so an unbounded pool would resolve every
# concurrent slot to ONE directory; distinct agents are the only configuration
# that yields distinct worktrees. They are pure configuration — no role name
# reaches Go.
PROMPT="$(sa_pool_worker_prompt)"
if [ -n "$PROMPT" ]; then
  pass 'resolved the SDK pool-worker prompt template'
else
  info 'pool-worker prompt template' 'not resolved; agents fall back to the embedded baseline'
fi
sa_declare_worker_agent "$CITY" "$RIG_NAME" "$TARGET" worker-a "$WT_A" "$PROMPT"
sa_declare_worker_agent "$CITY" "$RIG_NAME" "$TARGET" worker-b "$WT_B" "$PROMPT"
sa_declare_worker_agent "$CITY" "$RIG_NAME" "$TARGET" worker-c "$WT_C" "$PROMPT"
gcx config show > "$EVIDENCE/config-show.txt" 2>&1 || true
MISSING_AGENTS=''
for a in worker-a worker-b worker-c; do
  grep -qE "^name = \"$a\"$" "$EVIDENCE/config-show.txt" || MISSING_AGENTS="$MISSING_AGENTS $a"
done
if [ -z "$MISSING_AGENTS" ]; then
  pass 'three per-task worker agents are configured'
else
  fail 'three per-task worker agents are configured' "missing:$MISSING_AGENTS"
  abort 'agent configuration did not load'
fi

# ===========================================================================
section '2. work graph, created before any dispatch'
# ===========================================================================

# Every task states the lifecycle expectation explicitly. The first version of
# these tasks described only the file to write, and three workers each
# improvised a different ending: one never closed its bead, one closed
# `shipped`, one closed `blocked`. That spread is three reasonable readings of
# an underspecified instruction, not the engine disagreeing with itself.
#
# The closing sentence cannot coach a FALSE `shipped`: it states that
# publication is the controller's, so a worker that cannot commit records that
# honestly instead of claiming an artifact it never produced. `blocked` is named
# because it is the repository's own typed value — under enforcement a `shipped`
# claim with no commit is refused ("requires gc.work_commit"), as is any
# invented value, so telling the worker the correct word avoids it guessing one
# the gate will reject.
#
# No apostrophes in this single-quoted string. An apostrophe closes the quote,
# and the first draft ("the controller's") silently corrupted every construct
# after it; bash reported the failure 117 lines later at an unrelated `done`.
LIFECYCLE='Make the change, verify the exact contents, then close the assigned bead. You cannot run git; the controller publishes. Close with gc.work_outcome=blocked plus a gc.work_blocked_reason; do NOT claim shipped.'

# mk_bead <text> — create a work bead, failing loudly on validation.
# A bead title is capped at 500 characters and an over-long one fails
# validation; an earlier version threw the error away with 2>/dev/null and died
# reporting only an empty bead id.
mk_bead() {
  local text="$1" out rc
  if [ "${#text}" -gt 500 ]; then
    fail 'work bead title within the 500-char limit' "${#text} chars: ${text:0:60}..."
    return 1
  fi
  out="$(gcx bd q "$text" 2>&1)"; rc=$?
  if [ "$rc" -ne 0 ]; then
    fail 'work bead created' "$(printf '%s' "$out" | tail -1)"
    return 1
  fi
  printf '%s' "$out" | tail -1
}

A="$(mk_bead "Create ALPHA.md in the repository root containing exactly this single line: ALPHA_OK. $LIFECYCLE")"
B="$(mk_bead "Create BETA.md in the repository root containing exactly this single line: BETA_OK. $LIFECYCLE")"
C="$(mk_bead "Read ALPHA.md and BETA.md in the repository root, then create INDEX.md containing exactly two lines: the line from ALPHA.md, then the line from BETA.md. Do not invent the contents; read both files. $LIFECYCLE")"
if [ -z "$A" ] || [ -z "$B" ] || [ -z "$C" ]; then
  fail 'three work beads created' "A='$A' B='$B' C='$C'"
  abort 'work beads not created'
fi
echo "  A=$A  B=$B  C=$C"
pass "three work beads created (A=$A B=$B C=$C)"

# The two controller-owned integration beads. They are claimed by the controller
# at creation so nothing else in the city can pick them up, and they carry no
# gc.routed_to — an unrouted bead produces no pool demand, so no worker is ever
# offered controller authority.
AINT="$(mk_bead "S-A control: controller validates and integrates the result of $A into the run base")"
BINT="$(mk_bead "S-A control: controller validates and integrates the result of $B into the run base")"
if [ -z "$AINT" ] || [ -z "$BINT" ]; then
  fail 'two controller integration beads created' "AINT='$AINT' BINT='$BINT'"
  abort 'integration beads not created'
fi
for i in "$AINT" "$BINT"; do
  gcx bd update "$i" -a 'corsolv-controller' -s in_progress >/dev/null 2>&1
done
pass "two controller integration beads created and claimed (A-int=$AINT B-int=$BINT)"

# REQUIRED ARTIFACT PER BEAD — the deterministic controller-side guard against
# silent scope drift. The bead declares what the work must produce, so "the
# worker said it was done" and "the thing exists" are separate claims that can
# disagree.
stamp_required() {
  local id="$1" artifact="$2" out shown
  out="$(gcx bd update "$id" --set-metadata "gc.required_artifact=$artifact" 2>&1)" || {
    fail "$id declares its required artifact" "update failed: $(printf '%s' "$out" | tail -1)"
    return
  }
  shown="$(sa_gc bd show "$id" 2>/dev/null || true)"
  if grep -qF "gc.required_artifact: $artifact" <<<"$shown"; then
    pass "$id declares its required artifact ($artifact)"
  else
    fail "$id declares its required artifact" "stamp did not land for $artifact"
  fi
}
stamp_required "$A" ALPHA.md
stamp_required "$B" BETA.md
stamp_required "$C" INDEX.md

# M3 NEGATIVE CONTROL, run against this very rig. The closure predicate is the
# most load-bearing assertion here: everything downstream keys off "the bead
# closed". Prove every run that worker-controlled free text cannot forge it.
SPOOF="$(mk_bead 'M3 control: this bead says CLOSED in its title but its status is open')"
if [ -n "$SPOOF" ]; then
  if bead_is_closed "$SPOOF"; then
    fail 'closure predicate rejects free-text CLOSED' \
         "$SPOOF has status open but the predicate reported closed"
  else
    pass 'closure predicate rejects free-text CLOSED (structured status only)'
  fi
else
  fail 'closure predicate rejects free-text CLOSED' 'could not create the control bead'
fi

# The graph: work gates on INTEGRATION, never on close.
gcx bd dep "$A" --blocks "$AINT" >/dev/null 2>&1
gcx bd dep "$B" --blocks "$BINT" >/dev/null 2>&1
gcx bd dep "$AINT" --blocks "$C" >/dev/null 2>&1
gcx bd dep "$BINT" --blocks "$C" >/dev/null 2>&1
sa_gc bd dep tree "$C" > "$EVIDENCE/dep-tree.txt" 2>&1
if grep -q 'BLOCKED' "$EVIDENCE/dep-tree.txt"; then
  pass 'C is BLOCKED by both integration beads'
else
  fail 'C is BLOCKED by both integration beads' 'dependency tree does not show BLOCKED'
fi

sa_gc bd ready > "$EVIDENCE/ready-before.txt" 2>&1
if sa_bead_in_ready "$C" "$EVIDENCE/ready-before.txt"; then
  fail 'C withheld from ready work before its dependencies are integrated' 'C is already ready'
else
  pass 'C withheld from ready work before its dependencies are integrated'
fi
if sa_bead_in_ready "$A" "$EVIDENCE/ready-before.txt" && sa_bead_in_ready "$B" "$EVIDENCE/ready-before.txt"; then
  pass 'A and B are ready'
else
  fail 'A and B are ready' 'one or both absent from ready work'
fi

# ===========================================================================
section '3. controller-owned worktrees, stamped before dispatch'
# ===========================================================================

# The ownership contract the SDK guard demands: the worktree CREATOR writes the
# legacy artifact path first, and reconciliation may only mirror that value.
# gc.work_dir appearing later on a pool-managed bead is therefore not decoration
# — it is proof that this stamp matched the directory the live session was
# actually started in (workDirStampHasOwnershipEvidence).
provision_task_worktree() {
  local bead="$1" agent="$2" wt="$3" base="$4"
  sa_ledger_note "worktree add $wt for $bead ($agent) from ${base:0:9}"
  if ! wt_add "$TARGET" "$wt" "gc-$agent" "$base"; then
    fail "$bead worktree created by the controller" "wt_add failed for $wt"
    return 1
  fi
  if ! wt_is_registered "$TARGET" "$wt"; then
    fail "$bead worktree is a registered git worktree" "$wt absent from git worktree list"
    return 1
  fi
  gcx bd update "$bead" --set-metadata "work_dir=$wt" >/dev/null 2>&1
  gcx bd update "$bead" --set-metadata "gc.sa_run=$RUN_ID" >/dev/null 2>&1
  local shown
  shown="$(sa_gc bd show "$bead" 2>/dev/null || true)"
  if grep -qF "work_dir: $wt" <<<"$shown"; then
    pass "$bead worktree created and legacy work_dir stamped before dispatch ($agent)"
  else
    fail "$bead legacy work_dir stamped before dispatch" 'stamp did not land'
    return 1
  fi
  return 0
}

provision_task_worktree "$A" worker-a "$WT_A" "$BASE_SHA" || abort 'A worktree provisioning failed'
provision_task_worktree "$B" worker-b "$WT_B" "$BASE_SHA" || abort 'B worktree provisioning failed'

# C's legacy work_dir is stamped now — the path is deterministic — but its
# worktree is deliberately NOT created yet. It must be cut from a base that
# already contains both upstream results, and that base does not exist until
# the controller has integrated them. Both actions are pre-release; the ledger
# proves nothing names C after release.
gcx bd update "$C" --set-metadata "work_dir=$WT_C" >/dev/null 2>&1
gcx bd update "$C" --set-metadata "gc.sa_run=$RUN_ID" >/dev/null 2>&1
if [ ! -e "$WT_C" ]; then
  pass 'C worktree deliberately absent until the integrated base exists'
else
  fail 'C worktree deliberately absent until the integrated base exists' "$WT_C already present"
fi

if [ "$(readlink -f "$WT_A")" != "$(readlink -f "$WT_B")" ]; then
  pass 'A and B worktrees are pairwise distinct'
else
  fail 'A and B worktrees are pairwise distinct' 'same path'
fi
if [ ! -e "$WT_A/BETA.md" ] && [ ! -e "$WT_B/ALPHA.md" ]; then
  pass 'task worktrees start isolated from each other'
else
  fail 'task worktrees start isolated from each other' 'cross-visible artifact before dispatch'
fi

# ===========================================================================
section '4. route all three before execution (D3)'
# ===========================================================================

# All three are routed NOW, before any worker runs. C is therefore already known
# to the normal controller/dispatcher, and its release later requires no new
# instruction from anyone. Because C is blocked, the live open-routed demand read
# excludes it (blocked work must never count as spawn capacity), so routing it
# early cannot start it early.
ROUTE_EPOCH="$(date +%s)"
gcx sling "$RIG_NAME/worker-a" "$A" --no-formula --no-convoy > "$EVIDENCE/route-a.txt" 2>&1
gcx sling "$RIG_NAME/worker-b" "$B" --no-formula --no-convoy > "$EVIDENCE/route-b.txt" 2>&1
gcx sling "$RIG_NAME/worker-c" "$C" --no-formula --no-convoy > "$EVIDENCE/route-c.txt" 2>&1
for pair in "$A:route-a" "$B:route-b" "$C:route-c"; do
  bid="${pair%%:*}"; f="${pair##*:}"
  shown="$(sa_gc bd show "$bid" 2>/dev/null || true)"
  if grep -qE 'gc.routed_to: \S+' <<<"$shown"; then
    pass "$bid routed before any worker started ($(grep -oE 'gc.routed_to: \S+' <<<"$shown" | head -1 | awk '{print $2}'))"
  else
    fail "$bid routed before any worker started" "see $f.txt"
  fi
done

sa_gc bd ready > "$EVIDENCE/ready-after-route.txt" 2>&1
if sa_bead_in_ready "$C" "$EVIDENCE/ready-after-route.txt"; then
  fail 'C remains blocked after being routed' 'routing made C ready'
else
  pass 'C remains blocked after being routed'
fi

# ===========================================================================
section '5. parallel execution of A and B'
# ===========================================================================

DISPATCH_START="$(date +%s)"
MAXPAR=0
PARPIDS=''
closed_a=0; closed_b=0
A_CLOSED_AT=''; B_CLOSED_AT=''
LIVE_PROOF=''
C_SESSION_SEEN_EARLY=0
deadline=$(( $(date +%s) + AB_DEADLINE ))

while true; do
  pids="$(sa_worker_pids "$CITY" '*worker-[abc]*')"
  n="$(printf '%s' "$pids" | wc -w)"
  if [ "$n" -gt "$MAXPAR" ]; then MAXPAR="$n"; PARPIDS="$pids"; fi

  # A worker for C must not exist while C is blocked. Sampled continuously
  # rather than checked once at the end, because a premature spawn that later
  # drained would be invisible to a single post-hoc look.
  if [ "$(sa_worker_pids "$CITY" '*worker-c*' | wc -w)" -gt 0 ]; then
    C_SESSION_SEEN_EARLY=1
  fi

  # LIVE SECURITY PROOF, TAKEN WHILE THE WORKERS ARE STILL ALIVE.
  #
  # This has to happen here and nowhere later. A post-run verifier that waits
  # for drain has, by construction, waited until there is nothing left to
  # inspect — and a check that then reports INFO/"no process" reads as coverage
  # while proving nothing. Worse, a naive post-drain scan can match the city's
  # long-lived mayor/bd.dog agents and "pass" without ever having looked at a
  # pool worker.
  if [ -z "$LIVE_PROOF" ] && [ "$n" -ge 1 ]; then
    if LIVE_PROCESS_RESULT="$EVIDENCE/live-process.result" \
       bash "$SOURCE_REPO/corsolv/p2-smoke/verify-live-process.sh" "$CITY" \
         > "$EVIDENCE/live-process.txt" 2>&1; then
      LIVE_PROOF=PASS
    else
      LIVE_PROOF="FAIL($?)"
    fi
    info 'live worker posture captured' "$LIVE_PROOF (pids:$pids)"
  fi

  if [ "$closed_a" -eq 0 ] && bead_is_closed "$A"; then
    closed_a=1; A_CLOSED_AT="$(date -u +%FT%TZ)"
  fi
  if [ "$closed_b" -eq 0 ] && bead_is_closed "$B"; then
    closed_b=1; B_CLOSED_AT="$(date -u +%FT%TZ)"
  fi
  [ "$closed_a" -eq 1 ] && [ "$closed_b" -eq 1 ] && break
  if [ "$(date +%s)" -ge "$deadline" ]; then
    fail 'A and B both closed' "deadline reached (A=$closed_a B=$closed_b)"
    break
  fi
  sleep 5
done

AB_ELAPSED=$(( $(date +%s) - DISPATCH_START ))
[ "$closed_a" -eq 1 ] && [ "$closed_b" -eq 1 ] && pass 'A and B both closed'

# Concurrency is recorded durably at the moment it is observed, not merely
# printed: once the workers exit, no artifact on disk distinguishes "ran
# together" from "ran one after the other", and near-adjacent timestamps are not
# evidence of overlap. Two distinct pids alive in the same sample are.
printf 'maxpar=%s pids=%s observed_at=%s\n' \
  "$MAXPAR" "$PARPIDS" "$(date -u +%FT%TZ)" > "$EVIDENCE/parallelism.result"
if [ "$MAXPAR" -ge 2 ]; then
  pass "A and B genuinely overlapped (${MAXPAR} concurrent workers:$PARPIDS)"
else
  fail 'A and B genuinely overlapped' \
       "max concurrent workers = $MAXPAR; the two beads serialized"
fi
if [ "$C_SESSION_SEEN_EARLY" -eq 0 ]; then
  pass 'no C worker existed while C was blocked'
else
  fail 'no C worker existed while C was blocked' 'a worker-c process was observed pre-release'
fi
info 'A+B wall clock' "${AB_ELAPSED}s"

sa_gc session list --json > "$EVIDENCE/sessions-ab.json" 2>&1
sa_gc session list > "$EVIDENCE/sessions-ab.txt" 2>&1

# ===========================================================================
section '6. controller validation and integration'
# ===========================================================================

INTEGRATED_BASE=''
A_COMMIT=''; B_COMMIT=''; A_MERGE=''; B_MERGE=''
C_PROVISIONED=0
LIVE_PROOF_C=''
RELEASE_EPOCH=''
RELEASE_UTC=''

# validate_result <bead> <worktree> <artifact> <expected-content>
# The controller refuses to integrate what it has not itself verified: the bead
# must be closed with a truthful typed disposition AND the declared artifact
# must exist inside the assigned worktree with exactly the requested content.
validate_result() {
  local bead="$1" wt="$2" artifact="$3" want="$4" ok=0 status wo
  status="$(capture_final_bead_state "$bead" "$EVIDENCE" || true)"
  if [ "$status" = 'closed' ]; then
    pass "$bead final authoritative record is closed"
  else
    fail "$bead final authoritative record is closed" "got '${status:-<none>}'"; ok=1
  fi
  wo="$(final_meta "$bead" "$EVIDENCE" 'gc.work_outcome')"
  case "$wo" in
    blocked|no-op|abandoned)
      pass "$bead typed work outcome is truthful ($wo)" ;;
    shipped)
      fail "$bead typed work outcome is truthful" \
           'claims shipped (a reachable commit) while git is withheld from workers'; ok=1 ;;
    '')
      fail "$bead records a work outcome" 'absent — silent close'; ok=1 ;;
    *)
      fail "$bead records a work outcome" "unknown disposition '$wo'"; ok=1 ;;
  esac
  if [ -f "$wt/$artifact" ] && [ "$(cat "$wt/$artifact")" = "$want" ]; then
    pass "$bead produced $artifact inside its own worktree with exact content"
  else
    fail "$bead produced $artifact inside its own worktree with exact content" \
         "$( [ -f "$wt/$artifact" ] && head -c 100 "$wt/$artifact" || echo 'absent' )"; ok=1
  fi
  if [ -e "$TARGET/$artifact" ]; then
    fail "$bead artifact did not leak into the shared rig checkout" "$artifact present in $TARGET"; ok=1
  else
    pass "$bead artifact did not leak into the shared rig checkout"
  fi
  return "$ok"
}

# integrate_result <bead> <worktree> <artifact> <agent> — controller publishes.
#
# Results come back in the globals INT_COMMIT / INT_BASE rather than on stdout.
# Returning them by echo would have been wrong here: this function also emits
# pass/fail notes, and `X="$(integrate_result ...)"` would capture those notes
# into the SHA.
INT_COMMIT=''
INT_BASE=''
integrate_result() {
  local bead="$1" wt="$2" artifact="$3" agent="$4"
  INT_COMMIT=''; INT_BASE=''
  INT_COMMIT="$(controller_commit "$wt" "feat: publish $artifact from $bead

Published by the controller. The worker created and verified this file but is
denied git by policy." "$artifact")"
  if [ -z "$INT_COMMIT" ]; then
    fail "$bead controller committed the validated result" 'commit failed'
    return 1
  fi
  INT_BASE="$(controller_integrate "$TARGET" main "gc-$agent" "integrate: $bead via gc-$agent")"
  if [ -z "$INT_BASE" ]; then
    fail "$bead controller integrated the result into the run base" 'merge failed'
    return 1
  fi
  pass "$bead validated result committed (${INT_COMMIT:0:9}) and integrated (${INT_BASE:0:9})"
  return 0
}

# close_integration_bead <this> <other> — closes a controller integration bead,
# provisioning C's worktree FIRST if this is the second one to close.
#
# The ordering is the whole point: C becomes ready the instant the second
# integration bead closes, so its worktree must already exist, cut from the base
# that now contains both results. Doing it in the other order would leave a
# window in which a claimed C had no worktree — and closing it afterwards would
# be a post-release action naming C.
#
# The integration bead is closed with a TYPED work record like any other, not
# exempted. `shipped` is defined as requiring a commit reachable on the work
# branch, and the controller — unlike a worker — genuinely has one: the merge it
# just made on main. So this is the single truthful `shipped` in the run, and
# closing it exercises the positive half of the same gate that refuses a
# worker's unearned `shipped`. (Marking these beads with gc.kind would have
# exempted them from the contract entirely and, worse, would present them to the
# control dispatcher as its own control beads.)
close_integration_bead() {
  local this="$1" other="$2" commit="$3" other_status
  other_status="$(bead_status "$other" || true)"
  if [ "$other_status" = 'closed' ]; then
    INTEGRATED_BASE="$(git -C "$TARGET" rev-parse main)"
    sa_ledger_note "worktree add $WT_C for $C (worker-c) from integrated base ${INTEGRATED_BASE:0:9}"
    if wt_add "$TARGET" "$WT_C" gc-worker-c "$INTEGRATED_BASE" &&
       wt_is_registered "$TARGET" "$WT_C"; then
      pass "C worktree created from the integrated base (${INTEGRATED_BASE:0:9})"
      C_PROVISIONED=1
    else
      fail 'C worktree created from the integrated base' "wt_add failed for $WT_C"
    fi
    if [ -f "$WT_C/ALPHA.md" ] && [ -f "$WT_C/BETA.md" ]; then
      pass 'C worktree carries both upstream artifacts via repository state'
    else
      fail 'C worktree carries both upstream artifacts via repository state' \
           "$(ls "$WT_C" 2>/dev/null | tr '\n' ' ')"
    fi
  fi
  if [ "$other_status" = 'closed' ]; then
    # The release moment is the instant before the second integration bead
    # closes, not a timestamp taken afterwards: a later mark would narrow the
    # forbidden window and let a directive issued in the gap escape the D3
    # check. Everything C-specific — its worktree, its work_dir stamp — has
    # already happened above, on purpose.
    RELEASE_EPOCH="$(date +%s)"
    RELEASE_UTC="$(date -u +%FT%TZ)"
  fi
  if [ -n "$commit" ]; then
    gcx bd update "$this" \
      --set-metadata 'gc.work_outcome=shipped' \
      --set-metadata "gc.work_commit=$commit" \
      --set-metadata 'gc.work_branch=main' >/dev/null 2>&1
  fi
  local out rc
  out="$(gcx bd close "$this" --reason 'controller validated and integrated the upstream result' 2>&1)"; rc=$?
  if [ "$rc" -eq 0 ] && bead_is_closed "$this"; then
    pass "$this integration bead closed by the controller with a typed shipped record"
  else
    fail "$this integration bead closed by the controller" "$(printf '%s' "$out" | tail -1)"
  fi
}

if validate_result "$A" "$WT_A" ALPHA.md 'ALPHA_OK'; then
  integrate_result "$A" "$WT_A" ALPHA.md worker-a && A_COMMIT="$INT_COMMIT" && A_MERGE="$INT_BASE"
fi
close_integration_bead "$AINT" "$BINT" "${A_MERGE:-}"

if validate_result "$B" "$WT_B" BETA.md 'BETA_OK'; then
  integrate_result "$B" "$WT_B" BETA.md worker-b && B_COMMIT="$INT_COMMIT" && B_MERGE="$INT_BASE"
fi
close_integration_bead "$BINT" "$AINT" "${B_MERGE:-}"

RELEASE_EPOCH="${RELEASE_EPOCH:-$(date +%s)}"
RELEASE_UTC="${RELEASE_UTC:-$(date -u +%FT%TZ)}"
[ -n "$INTEGRATED_BASE" ] || INTEGRATED_BASE="$(git -C "$TARGET" rev-parse main)"
C_BASE_SHA="$(wt_head "$WT_C")"
info 'integrated base sha' "$INTEGRATED_BASE"
info 'C base sha' "${C_BASE_SHA:-<none>}"
if [ -n "$C_BASE_SHA" ] && [ "$C_BASE_SHA" = "$INTEGRATED_BASE" ]; then
  pass 'C base SHA equals the controller-integrated base'
else
  fail 'C base SHA equals the controller-integrated base' \
       "C=${C_BASE_SHA:-<none>} integrated=$INTEGRATED_BASE"
fi
if [ -n "$A_COMMIT" ] && [ -n "$B_COMMIT" ] &&
   git -C "$TARGET" merge-base --is-ancestor "$A_COMMIT" "$INTEGRATED_BASE" &&
   git -C "$TARGET" merge-base --is-ancestor "$B_COMMIT" "$INTEGRATED_BASE"; then
  pass 'integrated base descends from both validated commits'
else
  fail 'integrated base descends from both validated commits' \
       "A=${A_COMMIT:-<none>} B=${B_COMMIT:-<none>}"
fi

# ===========================================================================
section '7. autonomous continuation of C (D3)'
# ===========================================================================

# From here on the harness issues NO directive naming C. It reads, and it waits.
sa_gc bd ready > "$EVIDENCE/ready-after-integration.txt" 2>&1
C_READY_AT=''
deadline=$(( $(date +%s) + 300 ))
while true; do
  sa_gc bd ready > "$EVIDENCE/ready-after-integration.txt" 2>&1
  if sa_bead_in_ready "$C" "$EVIDENCE/ready-after-integration.txt"; then
    C_READY_AT="$(date -u +%FT%TZ)"; break
  fi
  [ "$(date +%s)" -ge "$deadline" ] && break
  sleep 5
done
if [ -n "$C_READY_AT" ]; then
  pass "readiness projection exposed C automatically at $C_READY_AT"
else
  fail 'readiness projection exposed C automatically' 'C never appeared in ready work'
fi

C_START="$(date +%s)"
C_CLAIMED_AT=''; C_SESSION=''
closed_c=0
deadline=$(( $(date +%s) + C_DEADLINE ))
while true; do
  if [ -z "$C_CLAIMED_AT" ]; then
    cpids="$(sa_worker_pids "$CITY" '*worker-c*')"
    if [ -n "$cpids" ]; then
      C_CLAIMED_AT="$(date -u +%FT%TZ)"
      info 'C worker started autonomously' "$C_CLAIMED_AT (pids:$cpids)"
      if LIVE_PROCESS_RESULT="$EVIDENCE/live-process-c.result" \
         bash "$SOURCE_REPO/corsolv/p2-smoke/verify-live-process.sh" "$CITY" \
           > "$EVIDENCE/live-process-c.txt" 2>&1; then
        LIVE_PROOF_C=PASS
      else
        LIVE_PROOF_C="FAIL($?)"
      fi
      info 'live C worker posture captured' "$LIVE_PROOF_C"
    fi
  fi
  if bead_is_closed "$C"; then closed_c=1; break; fi
  [ "$(date +%s)" -ge "$deadline" ] && break
  sleep 5
done
C_ELAPSED=$(( $(date +%s) - C_START ))
if [ "$closed_c" -eq 1 ]; then pass 'C closed'; else fail 'C closed' 'deadline reached'; fi
info 'C wall clock' "${C_ELAPSED}s"

if [ -n "$C_CLAIMED_AT" ]; then
  pass "normal controller demand claimed and started C with no operator command ($C_CLAIMED_AT)"
else
  not_reached 'normal controller demand claimed and started C' \
              'no C worker process was ever observed' "$C"
fi

# THE D3 ASSERTION, from the ledger rather than from memory. Reads after release
# are evidence collection; directives are continuation. Only directives are
# forbidden, and anything not explicitly classified read-only counts as one.
C_DIRECTIVES="$(sa_ledger_directives_after "$RELEASE_EPOCH" "$C")"
C_DIRECTIVES="$C_DIRECTIVES$(sa_ledger_directives_after "$RELEASE_EPOCH" 'worker-c')"
if [ -z "$C_DIRECTIVES" ]; then
  pass "zero post-release directives naming C (release $RELEASE_UTC)"
else
  fail 'zero post-release directives naming C' \
       "$(printf '%s' "$C_DIRECTIVES" | tr '\n' ';' | head -c 200)"
fi
sa_ledger_mentions_after "$RELEASE_EPOCH" "$C" > "$EVIDENCE/post-release-c-mentions.txt" 2>&1 || true

# ===========================================================================
section '8. artifacts, handoff and containment'
# ===========================================================================

if validate_result "$C" "$WT_C" INDEX.md "$(printf 'ALPHA_OK\nBETA_OK')"; then
  :
fi
if [ -f "$WT_C/INDEX.md" ]; then
  if grep -qx 'ALPHA_OK' "$WT_C/INDEX.md" && grep -qx 'BETA_OK' "$WT_C/INDEX.md"; then
    pass 'handoff: INDEX.md carries both upstream results'
  else
    fail 'handoff: INDEX.md carries both upstream results' \
         "got: $(tr '\n' '|' < "$WT_C/INDEX.md")"
  fi
else
  fail 'handoff: INDEX.md carries both upstream results' 'INDEX.md absent'
fi

# REQUIRED-ARTIFACT CONTAINMENT. Each bead declared an artifact before dispatch;
# each must now exist and sit INSIDE the worktree the bead was assigned. The
# containment half is what makes this a scope guard rather than an existence
# check: a worker that satisfied its brief by writing outside its assigned tree
# has escaped the boundary even though the file it names is present.
for triple in "$A:ALPHA.md:$WT_A" "$B:BETA.md:$WT_B" "$C:INDEX.md:$WT_C"; do
  bid="${triple%%:*}"; rest="${triple#*:}"; want="${rest%%:*}"; wt="${rest##*:}"
  declared="$(final_meta "$bid" "$EVIDENCE" 'gc.required_artifact')"
  if [ "$declared" != "$want" ]; then
    fail "$bid required artifact survived dispatch" "declared '${declared:-<none>}', expected '$want'"
    continue
  fi
  resolved="$(cd "$wt" 2>/dev/null && readlink -f "$declared" 2>/dev/null)"
  wtreal="$(readlink -f "$wt")"
  case "$resolved" in
    "$wtreal"/*)
      if [ -f "$resolved" ]; then
        pass "$bid required artifact present and inside its own worktree ($declared)"
      else
        fail "$bid required artifact present" "$declared declared but absent"
      fi ;;
    *)
      fail "$bid required artifact contained in its own worktree" \
           "resolved to '${resolved:-<unresolvable>}', outside $wtreal" ;;
  esac
done

# ===========================================================================
section '9. ownership, identity and timings recorded by the engine'
# ===========================================================================

WD_SEEN=''
SESSIONS_SEEN=''
# Re-read every bead's terminal record here, after all three have closed and
# reconciliation has settled. The A/B snapshots taken during validation were
# taken at their own close moments; a canonical stamp written a beat later would
# be missing from them, and the report must not be adjudicated on a record that
# was current for one bead and stale for another.
for id in "$A" "$B" "$C"; do
  capture_final_bead_state "$id" "$EVIDENCE" >/dev/null || true
done
for id in "$A" "$B" "$C"; do
  # Ownership is asserted on what the ENGINE writes for a raw routed bead:
  # gc.routed_to (which agent), gc.session_id and gc.session_name (which
  # session). gc.execution_routed_to and gc.last_heartbeat_at are stamped on the
  # formula/molecule path, not this one — the earlier three-bead run routed
  # through mol-do-work and saw them, this run routes the raw bead so the
  # dependency gate governs the dispatched entity (see the D3 note at the top).
  # Asserting a key this route never writes would fail the run for a
  # non-defect, so heartbeat is recorded as INFO and the session triple is what
  # must hold.
  routed="$(final_meta "$id" "$EVIDENCE" 'gc.routed_to')"
  sid="$(final_meta "$id" "$EVIDENCE" 'gc.session_id')"
  if [ -n "$routed" ] && [ -n "$sid" ]; then
    pass "$id records the agent and session that executed it ($routed / $sid)"
  else
    fail "$id records the agent and session that executed it" \
         "gc.routed_to='${routed:-<none>}' gc.session_id='${sid:-<none>}'"
  fi
  hb="$(final_meta "$id" "$EVIDENCE" 'gc.last_heartbeat_at')"
  info "$id heartbeat" "${hb:-not stamped on the raw-bead route}"
  sn="$(final_meta "$id" "$EVIDENCE" 'gc.session_name')"
  SESSIONS_SEEN="$SESSIONS_SEEN${sn:-<none>}
"
  canon="$(final_meta "$id" "$EVIDENCE" 'gc.work_dir')"
  legacy="$(final_meta "$id" "$EVIDENCE" 'work_dir')"
  WD_SEEN="$WD_SEEN$id=${canon:-<unstamped>}
"
  if [ -n "$legacy" ] && [ "$canon" = "$legacy" ]; then
    # The mirror only happens for a pool-managed session when the legacy stamp
    # the controller wrote equals the workdir the live session was started in.
    # Its presence is therefore evidence of ownership, not decoration.
    pass "$id canonical gc.work_dir mirrored from the controller stamp ($canon)"
  else
    fail "$id canonical gc.work_dir mirrored from the controller stamp" \
         "legacy='${legacy:-<none>}' canonical='${canon:-<none>}'"
  fi
done

DISTINCT_WD="$(printf '%s' "$WD_SEEN" | awk -F= '{print $2}' | grep -v '^$' | sort -u | wc -l)"
if [ "$DISTINCT_WD" -eq 3 ]; then
  pass 'distinct worktree ownership per task (three distinct gc.work_dir values)'
else
  fail 'distinct worktree ownership per task' \
       "$DISTINCT_WD distinct work_dir value(s): $(printf '%s' "$WD_SEEN" | tr '\n' ' ')"
fi
DISTINCT_SESSIONS="$(printf '%s' "$SESSIONS_SEEN" | grep -v '^$' | grep -v '<none>' | sort -u | wc -l)"
if [ "$DISTINCT_SESSIONS" -eq 3 ]; then
  pass 'three distinct sessions executed the three tasks (no duplicate ownership)'
else
  fail 'three distinct sessions executed the three tasks' \
       "$DISTINCT_SESSIONS distinct session name(s): $(printf '%s' "$SESSIONS_SEEN" | tr '\n' ' ')"
fi

sa_gc session list --json > "$EVIDENCE/sessions.json" 2>&1
sa_gc session list > "$EVIDENCE/sessions.txt" 2>&1

# ===========================================================================
section '10. authority separation and final publication'
# ===========================================================================

AUTHORS="$(rig_commit_authors "$TARGET" | tr '\n' ',' )"
if grep -qv 'Gas City Controller\|Corsolv Autonomy POC' <<<"$(rig_commit_authors "$TARGET")"; then
  fail 'every commit in the run base is controller-authored' "authors: $AUTHORS"
else
  pass "every commit in the run base is controller-authored ($AUTHORS)"
fi
if [ -n "$A_COMMIT" ] && [ -n "$B_COMMIT" ]; then
  pass 'controller performed both local integrations; no worker published'
else
  fail 'controller performed both local integrations' \
       "A=${A_COMMIT:-<none>} B=${B_COMMIT:-<none>}"
fi

C_COMMIT=''
if [ -f "$WT_C/INDEX.md" ]; then
  C_COMMIT="$(controller_commit "$WT_C" "feat: publish INDEX.md from $C

Published by the controller. The worker created and verified this file but is
denied git by policy." INDEX.md)"
  FINAL_BASE="$(controller_integrate "$TARGET" main gc-worker-c "integrate: $C via gc-worker-c")"
  if [ -n "$C_COMMIT" ] && [ -n "$FINAL_BASE" ]; then
    pass "controller integrated C into the run base (${FINAL_BASE:0:9})"
  else
    fail 'controller integrated C into the run base' 'commit or merge failed'
  fi
else
  fail 'controller integrated C into the run base' 'no INDEX.md to publish'
  FINAL_BASE="$INTEGRATED_BASE"
fi

# ===========================================================================
section '11. security posture and drain (D4)'
# ===========================================================================

# The proofs were taken while the workers were alive. This section adjudicates
# the recorded results — it does NOT re-scan. Re-scanning after drain either
# finds nothing (and would report INFO, which reads as coverage while proving
# nothing) or matches the city's long-lived mayor/bd.dog agents and "passes" on
# processes that were never workers. A missing proof is an acceptance failure,
# never a skip.
case "$LIVE_PROOF" in
  PASS) pass 'live A/B worker posture proved while those workers were alive' ;;
  '')   not_reached 'live A/B worker posture proved while those workers were alive' \
                    'no managed worker was ever observed alive' ;;
  *)    fail 'live A/B worker posture proved while those workers were alive' \
             "verify-live-process reported $LIVE_PROOF (see live-process.txt)" ;;
esac
case "${LIVE_PROOF_C:-}" in
  PASS) pass 'live C worker posture proved while that worker was alive' ;;
  '')   not_reached 'live C worker posture proved while that worker was alive' \
                    'no C worker was ever observed alive' ;;
  *)    fail 'live C worker posture proved while that worker was alive' \
             "verify-live-process reported $LIVE_PROOF_C (see live-process-c.txt)" ;;
esac

# Drain is a settling window, not an instant. `drain-ack` returns as soon as the
# worker has released its slot; teardown of the process is the reconciler's and
# completes after the bead is already CLOSED. Sampling once, immediately,
# reports that window as a leak. The deadline is what keeps it honest: a
# genuinely leaked worker holds its slot indefinitely and still fails here.
DRAIN_DEADLINE="${DRAIN_DEADLINE:-180}"
settle_start="$(date +%s)"
leftover="$(sa_worker_pids "$CITY" '*worker-[abc]*')"
while [ -n "$leftover" ]; do
  [ "$(( $(date +%s) - settle_start ))" -ge "$DRAIN_DEADLINE" ] && break
  sleep 3
  leftover="$(sa_worker_pids "$CITY" '*worker-[abc]*')"
done
DRAIN_TOOK=$(( $(date +%s) - settle_start ))
if [ -z "$leftover" ]; then
  pass "managed workers drained (settled in ${DRAIN_TOOK}s)"
else
  fail 'managed workers drained' "pids:$leftover still present after ${DRAIN_DEADLINE}s"
fi
sa_gc session list > "$EVIDENCE/sessions-drained.txt" 2>&1
STUCK="$(awk 'NR>1 && $2 ~ /worker-[abc]$/ && $3 == "active" {print $1}' "$EVIDENCE/sessions-drained.txt")"
if [ -z "$STUCK" ]; then
  pass 'no worker session left in the active state'
else
  fail 'no worker session left in the active state' "$(printf '%s' "$STUCK" | tr '\n' ' ')"
fi

# ===========================================================================
section '12. independent assurance'
# ===========================================================================

ASSURANCE=FAIL
SA_CITY="$CITY" SA_RIG="$RIG_NAME" \
  LIVE_PROCESS_RESULT="$EVIDENCE/live-process.result" \
  LIVE_PROCESS_RESULT_C="$EVIDENCE/live-process-c.result" \
  bash "$SOURCE_REPO/corsolv/p2-smoke/verify-independent-sa.sh" \
       "$TARGET" "$CITY" "$WT_A" "$WT_B" "$WT_C" "$A" "$B" "$C" \
       > "$EVIDENCE/independent.txt" 2>&1
ASSURANCE_RC=$?
if [ "$ASSURANCE_RC" -eq 0 ]; then
  ASSURANCE=PASS
  pass 'independent assurance passed'
else
  fail 'independent assurance passed' "exit $ASSURANCE_RC — see independent.txt"
fi
tail -30 "$EVIDENCE/independent.txt" 2>/dev/null | sed 's/^/    /'

# ===========================================================================
# Report.
# ===========================================================================

TOTAL_FAIL="$FAILURES"
OVERALL=PASS
{ [ "$TOTAL_FAIL" -ne 0 ] || [ "$NOT_REACHED_COUNT" -ne 0 ]; } && OVERALL=FAIL

mkdir -p "$(dirname "$REPORT")"
cat > "$REPORT" <<EOF
# Corsolv S-A — Local Controlled First-Runner Acceptance

S-A OVERALL: $OVERALL

| Adjudication | Count |
| --- | --- |
| Mandatory PASS | $PASS_COUNT |
| Mandatory FAIL | $TOTAL_FAIL |
| Mandatory NOT REACHED | $NOT_REACHED_COUNT |

## Criterion 10 across the two stages

Criterion 10 of the POC brief — *"W3 started automatically only after W1 and W2
merged"* — spans both stages of this programme, and S-A may claim only its local
half.

| Half | Meaning | Status here |
| --- | --- | --- |
| Local (S-A) | the controller integrates validated A/B commits into the run base, and C is released, claimed and started with no operator command | $( [ "$OVERALL" = PASS ] && echo PASS || echo FAIL ) |
| Remote (S-B) | GitHub merge after PR + exact-head CI + independent assurance | **DEFERRED** |
| Full criterion | both halves | **INCOMPLETE** |

Per the POC brief, NOT REACHED is never reported as PASS. The remote half is not
attempted here and is not counted as a failure of S-A; it is the subject of S-B.

## Foundation

| Item | Value |
| --- | --- |
| Run ID | \`$RUN_ID\` |
| Source SHA | \`$SOURCE_SHA\` |
| Source branch | \`$SOURCE_BRANCH\` |
| Binary SHA256 | \`$BIN_SHA\` |
| City | \`$CITY\` |
| Rig | \`$TARGET\` |
| Work beads | A=\`$A\` B=\`$B\` C=\`$C\` |
| Integration beads | A-int=\`$AINT\` B-int=\`$BINT\` |

## The graph

\`\`\`
A ($A) ──> A-int ($AINT) ──┐
                            ├──> C ($C)
B ($B) ──> B-int ($BINT) ──┘
\`\`\`

C gates on INTEGRATION, not on close. That is what makes autonomous
continuation and per-task worktree isolation compatible: C only becomes ready
once a base containing both upstream results exists.

## Worktree ownership

| Task | Agent | Worktree | Base |
| --- | --- | --- | --- |
| A | \`$RIG_NAME/worker-a\` | \`$WT_A\` | \`$BASE_SHA\` |
| B | \`$RIG_NAME/worker-b\` | \`$WT_B\` | \`$BASE_SHA\` |
| C | \`$RIG_NAME/worker-c\` | \`$WT_C\` | \`${C_BASE_SHA:-<none>}\` (integrated) |

Canonical \`gc.work_dir\` per bead:

\`\`\`
$WD_SEEN
\`\`\`

## Integration

| Item | Value |
| --- | --- |
| A validated commit | \`${A_COMMIT:-<none>}\` |
| B validated commit | \`${B_COMMIT:-<none>}\` |
| Integrated base (C's base) | \`$INTEGRATED_BASE\` |
| C validated commit | \`${C_COMMIT:-<none>}\` |
| Final run base | \`${FINAL_BASE:-<none>}\` |

## Autonomous continuation (D3)

| Item | Value |
| --- | --- |
| All three routed before execution | yes (route epoch $ROUTE_EPOCH) |
| C blocked while routed | yes |
| Release moment (second integration bead closed) | $RELEASE_UTC |
| C became ready | ${C_READY_AT:-<never>} |
| C worker started | ${C_CLAIMED_AT:-<never>} |
| Post-release directives naming C | $( [ -z "$C_DIRECTIVES" ] && echo 'none' || echo 'PRESENT — see ledger' ) |

The command ledger (\`gc-commands.log\`) records every controller action with its
timestamp, so "no operator restarted C" is an artifact rather than an assertion.

## Execution

| Property | Evidence |
| --- | --- |
| Parallel work | max $MAXPAR concurrent workers ($PARPIDS) |
| A+B wall clock | ${AB_ELAPSED}s |
| C wall clock | ${C_ELAPSED}s |
| Distinct sessions | $DISTINCT_SESSIONS |
| Drain | settled in ${DRAIN_TOOK}s |
| Live A/B posture | ${LIVE_PROOF:-<none>} |
| Live C posture | ${LIVE_PROOF_C:-<none>} |
| Independent assurance | $ASSURANCE |

## Control ledger

Every mandatory assertion, with its own identity and result. Generated from
\`controls.tsv\`, not from the console.

| Control | Status | Reason | Subject |
| --- | --- | --- | --- |
$(awk -F'\t' 'NR>1 {printf "| %s | %s | %s | %s |\n", $1, $2, ($3==""?"—":$3), ($4==""?"—":$4)}' "$CONTROLS")

### Failures and unreached controls

$(awk -F'\t' 'NR>1 && ($2=="FAIL" || $2=="NOT_REACHED") {printf "- **%s** — %s: %s%s\n", $2, $1, ($3==""?"(no reason recorded)":$3), ($4==""?"":" [" $4 "]")}' "$CONTROLS" | grep . || echo 'None. Every mandatory control passed.')

## Evidence directory

\`$EVIDENCE\`

- \`controls.tsv\` — the control ledger above, machine-readable
- \`gc-commands.log\` — every controller command with its timestamp (the D3 evidence)
- \`parallelism.result\` — max concurrent workers, with pids and observation time
- \`live-process.result\`, \`live-process-c.result\` — live permission posture, captured pre-drain
- \`dep-tree.txt\`, \`ready-before.txt\`, \`ready-after-route.txt\`, \`ready-after-integration.txt\` — the dependency gate
- \`final-*.txt\` / \`final-*.json\` — per-bead TERMINAL state, re-read after closure
- \`independent.txt\` — the independent assurance transcript
EOF

echo
echo '============================================================'
echo "S-A OVERALL: $OVERALL"
echo "  mandatory PASS:        $PASS_COUNT"
echo "  mandatory FAIL:        $TOTAL_FAIL"
echo "  mandatory NOT REACHED: $NOT_REACHED_COUNT"
echo "report: $REPORT"
echo "evidence: $EVIDENCE"
[ "$OVERALL" = PASS ] || exit 70

