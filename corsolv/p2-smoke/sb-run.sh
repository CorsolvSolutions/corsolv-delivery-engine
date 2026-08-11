#!/usr/bin/env bash
#
# STAGE S-B — promoted run against the real project, closing the REMOTE half.
#
# S-A proved the local coordination properties: per-task worktrees, concurrent
# workers, controller-owned integration, autonomous release of the dependent
# workstream. It deliberately claimed only the LOCAL half of criterion 10 and
# left the remote half NOT REACHED. This stage closes it, on a real GitHub
# repository, with real pull requests and real CI.
#
#   S-A  merged = the controller integrates validated commits into a local base
#   S-B  merged = GitHub merge after PR + exact-head CI + independent assurance
#
# THE ACCEPTANCE AUTHORITY IS NOT IN THIS REPOSITORY. It is the 14 numbered
# criteria in D:\Development\corsolv-autonomy-poc\POC-BRIEF.md, whose W1/W2/W3
# graph this run reproduces verbatim: W1 add, W2 multiply, W3 calculator — W3
# dependent on BOTH W1 and W2 being MERGED. No fourth task is invented.
#
# ---------------------------------------------------------------------------
# TWO SCOPE DECISIONS, RECORDED RATHER THAN BURIED.
#
# 1. THE RUN USES A WORKING CLONE, NOT THE AUTHORITATIVE CHECKOUT.
#    `gc rig add` writes a bead store, runtime state and a .gitignore into the
#    rig and makes its own "bd init" commit. Pointing that at
#    D:\Development\corsolv-autonomy-poc would mutate the repository that holds
#    the POC's own acceptance record. The rig is therefore a fresh clone of the
#    same GitHub remote: the PRs, CI runs and merges are real and land on the
#    real repository, while the authoritative local checkout is untouched.
#
# 2. THE INTEGRATION TARGET IS sb/base, NOT main.
#    W1, W2 and W3 are ALREADY merged into main — PRs #1/#2/#3, merge commits
#    fd6494b / 3cd6d79 / 931f943 — by the original PowerShell controller. Their
#    files exist there now, so re-running the same three tasks into main is not
#    expressible as a diff: the workers would have nothing to change and no PR
#    could be opened. The graph is therefore replayed onto `sb/base`, cut from
#    the POC's own pre-workstream base (8c4f7c7, where src/ holds only index.ts),
#    which is the last commit at which W1/W2/W3 are genuinely outstanding work.
#
#    What this preserves: real PRs, real CI on exact head SHAs, real GitHub
#    merges, and the dependency property — W3 released only after W1 and W2 are
#    MERGED remotely. What it does not claim: criterion 11 verbatim ("all
#    successful work entered main through PRs") is already satisfied on main by
#    the original run and is NOT re-proved here against main. Reading this stage
#    as re-proving it would be exactly the reinterpretation this note prevents.
#
# CI reaches these PRs because sb/base is added to the workflow's pull_request
# branch filter. The `validate` job itself — npm ci, typecheck, test — is
# unchanged, so "required CI ran and passed on the exact PR head SHA" is proved
# with the authoritative workflow, not a bespoke one.
# ---------------------------------------------------------------------------
#
# WORKER / CONTROLLER AUTHORITY. Workers run `bounded-project`: Read/Write/Edit/
# Glob/Grep, the pool-worker lifecycle, and three named project gates
# (typecheck/build/test). They have no git, no gh, no shell family. Commit,
# push, PR and merge are the controller's alone — and the controller inspects
# the changed-file set before publishing, because bounded-project can edit
# package.json and `npm test` executes what that file names. The allowlist buys
# scope clarity; the authority split is what contains the worker.
#
# Exit: 0 S-B passed, 70 otherwise.

set -uo pipefail

export PATH="$HOME/.local/bin:/usr/local/go/bin:$PATH"
export GC_HOME="$HOME/.gc-corsolv-p2"
export GOTOOLCHAIN=auto
export GC_WORK_RECORD_ENFORCE=1

SOURCE_REPO="${SOURCE_REPO:-/mnt/d/Development/corsolv-delivery-engine}"
REPORT="$SOURCE_REPO/engdocs/corsolv/S-B-RESULT.md"
# shellcheck source=lib/sa-lib.sh
. "$SOURCE_REPO/corsolv/p2-smoke/lib/sa-lib.sh"

GH="${GH:-/mnt/c/Program Files/GitHub CLI/gh.exe}"
REPO_SLUG='CorsolvSolutions/corsolv-autonomy-poc'
REPO_URL="https://github.com/$REPO_SLUG.git"
# The POC's own pre-workstream base: src/ holds only index.ts here, so W1/W2/W3
# are genuinely outstanding work again.
PRE_BASE='8c4f7c7'
REQUIRED_WORKFLOW='CI'

TIMESTAMP="$(date -u +%Y%m%dT%H%M%SZ)"
# Branches are RUN-SCOPED. The first S-B run merged W1 and W2 into its base, so
# a re-run against fixed names would find its own workstreams already merged and
# have nothing left to replay — the same reason this stage cannot replay onto
# main. A run tag keeps every run genuinely outstanding work.
RUN_TAG="${RUN_TAG:-$TIMESTAMP}"
SB_BASE_BRANCH="${SB_BASE_BRANCH:-sb/$RUN_TAG/base}"
RUN_ID="sb-$TIMESTAMP"
CITY="$HOME/corsolv-p2/sb-city-$TIMESTAMP"
TARGET="$HOME/corsolv-p2/sb-rig-$TIMESTAMP"
RIG_NAME="sb-rig-$TIMESTAMP"
EVIDENCE="$HOME/corsolv-p2/sb-evidence-$TIMESTAMP"
mkdir -p "$EVIDENCE"

SA_CITY="$CITY"
SA_RIG="$RIG_NAME"

WT_ROOT="$CITY/.gc/worktrees/$RIG_NAME"
WT_W1="$WT_ROOT/worker-w1"
WT_W2="$WT_ROOT/worker-w2"
WT_W3="$WT_ROOT/worker-w3"

WORK_DEADLINE="${WORK_DEADLINE:-2400}"
CI_DEADLINE="${CI_DEADLINE:-1800}"

FAILURES=0
NOT_REACHED_COUNT=0
PASS_COUNT=0

CONTROLS="$EVIDENCE/controls.tsv"
printf 'control\tstatus\treason\tsubject\n' > "$CONTROLS"
record() { printf '%s\t%s\t%s\t%s\n' "$1" "$2" "${3:-}" "${4:-}" >> "$CONTROLS"; }
note() { printf '  %-66s %s\n' "$1" "$2"; }
pass() { note "$1" 'PASS'; record "$1" PASS '' "${2:-}"; PASS_COUNT=$((PASS_COUNT + 1)); }
fail() { note "$1" "FAIL — $2"; record "$1" FAIL "$2" "${3:-}"; FAILURES=$((FAILURES + 1)); }
info() { note "$1" "INFO — $2"; record "$1" INFO "$2" "${3:-}"; }
not_reached() {
  note "$1" "NOT REACHED — $2"; record "$1" NOT_REACHED "$2" "${3:-}"
  NOT_REACHED_COUNT=$((NOT_REACHED_COUNT + 1))
}
section() { printf '\n--- %s ---\n' "$1"; }
abort() { printf '\nABORT: %s\n' "$1"; exit 70; }

gh_api() { "$GH" api "$@"; }

echo '============================================================'
echo 'CORSOLV S-B — PROMOTED RUN (REMOTE ACCEPTANCE HALF)'
echo '============================================================'
echo "run:      $RUN_ID"
echo "repo:     $REPO_SLUG"
echo "city:     $CITY"
echo "rig:      $TARGET"
echo "evidence: $EVIDENCE"

sa_ledger_init "$EVIDENCE/gc-commands.log"

# ===========================================================================
section '0. foundation'
# ===========================================================================

SOURCE_SHA="$(git -C "$SOURCE_REPO" rev-parse HEAD)"
SOURCE_BRANCH="$(git -C "$SOURCE_REPO" rev-parse --abbrev-ref HEAD)"
if [ -n "$(git -C "$SOURCE_REPO" status --porcelain)" ]; then
  fail 'delivery-engine source tree is clean' 'refusing to dispatch from a dirty tracked tree'
  abort 'acceptance requires a clean committed source tree'
fi
pass "delivery-engine source tree is clean ($SOURCE_BRANCH @ ${SOURCE_SHA:0:9})"

install -m 755 "$SOURCE_REPO/bin/gc" "$HOME/.local/bin/gc"
BIN_SHA="$(sha256sum "$SOURCE_REPO/bin/gc" | awk '{print $1}')"
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
[ "$SUP_OK" -eq 1 ] || { fail 'supervisor runs the fingerprinted build' "expected $BIN_SHA"; abort 'stale supervisor'; }
pass "supervisor runs the fingerprinted build (pid $SUP_PID)"
info 'source sha' "$SOURCE_SHA"
info 'binary sha256' "$BIN_SHA"

# LOCAL GATES MUST BE MATERIALLY COMPARABLE TO CI.
# The workflow pins Node 24. A local gate run on a different major proves
# something adjacent to, but not the same as, what CI will decide.
NODE_VER="$(node --version 2>/dev/null || true)"
case "$NODE_VER" in
  v24.*) pass "local Node matches the CI major ($NODE_VER)" ;;
  '')    fail 'local Node is available' 'node not found; local gates cannot be run' ;;
  *)     fail 'local Node matches the CI major' "local $NODE_VER, CI pins Node 24" ;;
esac
info 'npm version' "$(npm --version 2>/dev/null || echo '<none>')"

if "$GH" auth status >"$EVIDENCE/gh-auth.txt" 2>&1; then
  pass 'controller holds GitHub authentication'
else
  fail 'controller holds GitHub authentication' 'gh auth status failed'
  abort 'no GitHub authority'
fi
GH_SCOPES="$(grep -o "Token scopes.*" "$EVIDENCE/gh-auth.txt" | head -1)"
info 'gh token scopes' "${GH_SCOPES:-<unknown>}"

# ===========================================================================
section '1. target reconciliation'
# ===========================================================================

REMOTE_MAIN="$(gh_api "repos/$REPO_SLUG" --jq '.default_branch' 2>/dev/null)"
if [ "$REMOTE_MAIN" = 'main' ]; then
  pass "target repository reachable ($REPO_SLUG, default branch $REMOTE_MAIN)"
else
  fail 'target repository reachable' "default branch read as '${REMOTE_MAIN:-<none>}'"
  abort 'cannot reconcile the target repository'
fi

# Git authentication for a PRIVATE repository, without writing a token to disk.
#
# The machine's configured credential helper points at a git-credential-manager
# binary that is not present in this environment, so https operations against
# the private target would prompt or fail. The helper below stores a COMMAND,
# not a secret: it asks gh for a token at the moment git needs one. That keeps
# the credential exactly where it already lives (gh's keyring) instead of
# copying it into a clone's .git/config, and it is the same authority the
# controller uses for PRs and merges — never granted to a worker.
GIT_CRED_HELPER="!f() { echo username=x-access-token; echo \"password=\$(\"$GH\" auth token)\"; }; f"

git -c credential.helper="$GIT_CRED_HELPER" clone -q "$REPO_URL" "$TARGET" 2>"$EVIDENCE/clone.txt" \
  || { fail 'clone target repository' "$(tail -2 "$EVIDENCE/clone.txt" | tr '\n' ' ')"; abort 'clone failed'; }
git -C "$TARGET" config user.name 'Gas City Controller'
git -C "$TARGET" config user.email 'support@corsolv.com'
git -C "$TARGET" config credential.helper "$GIT_CRED_HELPER"
CLONE_MAIN_SHA="$(git -C "$TARGET" rev-parse main)"
pass "working clone created (main ${CLONE_MAIN_SHA:0:9})"

PRE_BASE_SHA="$(git -C "$TARGET" rev-parse "$PRE_BASE^{commit}" 2>/dev/null)"
if [ -z "$PRE_BASE_SHA" ]; then
  fail 'pre-workstream base resolves' "$PRE_BASE not found in the target history"
  abort 'no base to replay the graph from'
fi
PRE_BASE_SRC="$(git -C "$TARGET" ls-tree -r --name-only "$PRE_BASE_SHA" -- src | tr '\n' ' ')"
if grep -q 'add.ts' <<<"$PRE_BASE_SRC"; then
  fail 'the replay base predates the workstreams' "src at $PRE_BASE already contains add.ts"
else
  pass "replay base predates the workstreams (${PRE_BASE_SHA:0:9}; src: $PRE_BASE_SRC)"
fi

# ===========================================================================
section '2. S-B base branch and CI reachability'
# ===========================================================================

git -C "$TARGET" checkout -q -B "$SB_BASE_BRANCH" "$PRE_BASE_SHA"

# Add the S-B base to the workflow's pull_request filter so the AUTHORITATIVE
# job runs on these PRs. The job's steps are untouched; only which base
# branches it watches changes. The W1 CI-only delay is extended to the S-B W1
# branch too, because that delay is what forces the controller to keep W2
# progressing while W1 sits in WAITING_EXTERNAL.
WF="$TARGET/.github/workflows/ci.yml"
if [ ! -f "$WF" ]; then
  fail 'authoritative CI workflow present at the replay base' "$WF missing"
  abort 'no workflow to run'
fi
sed -i "s|^\(\s*\)branches: \[main\]$|\1branches: [main, 'sb/**']|" "$WF"
# The W1 CI-only delay is what forces the controller to keep W2 progressing
# while W1 sits in WAITING_EXTERNAL. Matched by suffix so it survives the
# run-scoped branch names.
sed -i "s|github.head_ref == 'poc/w1-add'|endsWith(github.head_ref, 'w1-add')|" "$WF"
if grep -q "sb/\*\*" "$WF"; then
  pass "workflow watches $SB_BASE_BRANCH (job steps unchanged)"
else
  fail "workflow watches $SB_BASE_BRANCH" 'branch filter not updated'
fi
cp -f "$WF" "$EVIDENCE/ci.yml"
git -C "$TARGET" add .github/workflows/ci.yml
git -C "$TARGET" commit -q -m "ci: run the authoritative validate job on $SB_BASE_BRANCH

The job steps are unchanged. Only the pull_request branch filter is widened so
the S-B replay of W1/W2/W3 is validated by the same workflow that validated the
original run."
if git -C "$TARGET" push -q -u origin "$SB_BASE_BRANCH" 2>"$EVIDENCE/push-base.txt"; then
  SB_BASE_SHA="$(git -C "$TARGET" rev-parse "$SB_BASE_BRANCH")"
  pass "S-B base pushed (${SB_BASE_SHA:0:9})"
else
  fail 'S-B base pushed' "$(tail -2 "$EVIDENCE/push-base.txt" | tr '\n' ' ')"
  abort 'cannot publish the replay base'
fi

# ===========================================================================
section '3. city, rig and bounded-project agents'
# ===========================================================================

command gc init "$CITY" --provider claude --yes >"$EVIDENCE/init.txt" 2>&1 \
  || { fail 'gc init' 'see init.txt'; abort 'city creation failed'; }
cat >> "$CITY/city.toml" <<'TOML'

# A worker may not close a bead with an invented, absent, or unearned
# disposition. `shipped` requires a reachable commit, which this policy
# withholds from workers by design.
[workspace.env]
GC_WORK_RECORD_ENFORCE = "1"
TOML
mkdir -p "$CITY/scripts"
install -m 755 "$SOURCE_REPO/corsolv/p2-smoke/scripts/worktree-setup.sh" "$CITY/scripts/worktree-setup.sh"

cd "$CITY" || abort 'cannot enter city'
gcx rig add "$TARGET" >"$EVIDENCE/rigadd.txt" 2>&1 || fail 'gc rig add' 'see rigadd.txt'
if sa_wait_rig_beads "$CITY" "$RIG_NAME" 180; then
  pass 'rig beads store initialized'
else
  fail 'rig beads store initialized' 'not ready within 180s'; abort 'rig store never came up'
fi

PROMPT="$(sa_pool_worker_prompt)"
[ -n "$PROMPT" ] && pass 'resolved the SDK pool-worker prompt template' \
  || info 'pool-worker prompt template' 'not resolved; embedded baseline will be used'

# The bounded-project opt-in, per agent. This is the only way to reach the
# mode: the autonomous default remains bounded-auto.
declare_project_agent() {
  local agent="$1" wt="$2" dir="$CITY/agents/$1"
  mkdir -p "$dir"
  {
    printf 'dir = "%s"\n' "$RIG_NAME"
    printf 'scope = "rig"\n'
    printf 'provider = "claude"\n'
    printf 'max_active_sessions = 1\n'
    printf 'work_dir = "%s"\n' "$wt"
    printf 'pre_start = ["%s/scripts/worktree-setup.sh %s %s %s"]\n' "$CITY" "$TARGET" "$wt" "$agent"
    [ -n "$PROMPT" ] && printf 'prompt_template = "%s"\n' "$PROMPT"
    printf '\n[option_defaults]\n'
    printf 'permission_mode = "bounded-project"\n'
  } > "$dir/agent.toml"
}
declare_project_agent worker-w1 "$WT_W1"
declare_project_agent worker-w2 "$WT_W2"
declare_project_agent worker-w3 "$WT_W3"
gcx config show > "$EVIDENCE/config-show.txt" 2>&1 || true
MISSING=''
for a in worker-w1 worker-w2 worker-w3; do
  grep -qE "^name = \"$a\"$" "$EVIDENCE/config-show.txt" || MISSING="$MISSING $a"
done
[ -z "$MISSING" ] && pass 'three bounded-project worker agents are configured' \
  || { fail 'three bounded-project worker agents are configured' "missing:$MISSING"; abort 'agent config did not load'; }

# The opt-in must be visible in the resolved config, not merely written to disk.
if grep -q 'bounded-project' "$EVIDENCE/config-show.txt"; then
  pass 'bounded-project selection survives config resolution'
else
  fail 'bounded-project selection survives config resolution' 'not present in gc config show'
fi

# ===========================================================================
section '4. the W1/W2/W3 work graph'
# ===========================================================================

LIFECYCLE='Verify with npm run typecheck and npm test before closing. You cannot run git; the controller publishes. Close the assigned bead with gc.work_outcome=blocked plus a gc.work_blocked_reason; do NOT claim shipped.'

mk_bead() {
  local text="$1" out rc
  if [ "${#text}" -gt 500 ]; then
    fail 'work bead title within the 500-char limit' "${#text} chars"; return 1
  fi
  out="$(gcx bd q "$text" 2>&1)"; rc=$?
  [ "$rc" -eq 0 ] || { fail 'work bead created' "$(printf '%s' "$out" | tail -1)"; return 1; }
  printf '%s' "$out" | tail -1
}

W1="$(mk_bead "Create src/add.ts exporting a correctly typed add(a: number, b: number): number, plus src/add.test.ts covering it with node:test. Do not modify multiply functionality. $LIFECYCLE")"
W2="$(mk_bead "Create src/multiply.ts exporting a correctly typed multiply(a: number, b: number): number, plus src/multiply.test.ts covering it with node:test. Do not modify add functionality. $LIFECYCLE")"
W3="$(mk_bead "Create src/calculator.ts exporting a typed calculator that composes the already-merged add and multiply from ./add.js and ./multiply.js, plus src/calculator.test.ts. Import them; do not reimplement. $LIFECYCLE")"
for v in "$W1" "$W2" "$W3"; do [ -n "$v" ] || abort 'work beads not created'; done
pass "three work beads created (W1=$W1 W2=$W2 W3=$W3)"

W1INT="$(mk_bead "S-B control: controller publishes and MERGES the result of $W1 on GitHub")"
W2INT="$(mk_bead "S-B control: controller publishes and MERGES the result of $W2 on GitHub")"
for v in "$W1INT" "$W2INT"; do [ -n "$v" ] || abort 'integration beads not created'; done
for i in "$W1INT" "$W2INT"; do
  gcx bd update "$i" -a 'corsolv-controller' -s in_progress >/dev/null 2>&1
done
pass "two controller merge beads created and claimed (W1-int=$W1INT W2-int=$W2INT)"

# The authorised change set per bead. Publication refuses anything outside it —
# including package.json, which bounded-project can edit and `npm test` would
# then execute.
stamp_scope() {
  local id="$1" artifact="$2" authorised="$3"
  gcx bd update "$id" --set-metadata "gc.required_artifact=$artifact" >/dev/null 2>&1
  gcx bd update "$id" --set-metadata "gc.authorised_paths=$authorised" >/dev/null 2>&1
  local shown; shown="$(sa_gc bd show "$id" 2>/dev/null || true)"
  if grep -qF "gc.required_artifact: $artifact" <<<"$shown"; then
    pass "$id declares its required artifact and authorised paths ($artifact)"
  else
    fail "$id declares its required artifact" 'stamp did not land'
  fi
}
stamp_scope "$W1" src/add.ts 'src/add.ts,src/add.test.ts'
stamp_scope "$W2" src/multiply.ts 'src/multiply.ts,src/multiply.test.ts'
stamp_scope "$W3" src/calculator.ts 'src/calculator.ts,src/calculator.test.ts'

# W3 gates on the MERGE beads, not on W1/W2 closing. That is the remote half of
# criterion 10: the dependent workstream may not start until both upstreams are
# merged on GitHub.
gcx bd dep "$W1" --blocks "$W1INT" >/dev/null 2>&1
gcx bd dep "$W2" --blocks "$W2INT" >/dev/null 2>&1
gcx bd dep "$W1INT" --blocks "$W3" >/dev/null 2>&1
gcx bd dep "$W2INT" --blocks "$W3" >/dev/null 2>&1
sa_gc bd dep tree "$W3" > "$EVIDENCE/dep-tree.txt" 2>&1
grep -q 'BLOCKED' "$EVIDENCE/dep-tree.txt" && pass 'W3 is BLOCKED by both merge beads' \
  || fail 'W3 is BLOCKED by both merge beads' 'dependency tree does not show BLOCKED'

sa_gc bd ready > "$EVIDENCE/ready-before.txt" 2>&1
sa_bead_in_ready "$W3" "$EVIDENCE/ready-before.txt" \
  && fail 'W3 withheld before its upstreams merge' 'W3 is already ready' \
  || pass 'W3 withheld before its upstreams merge'
sa_bead_in_ready "$W1" "$EVIDENCE/ready-before.txt" && sa_bead_in_ready "$W2" "$EVIDENCE/ready-before.txt" \
  && pass 'W1 and W2 are ready' || fail 'W1 and W2 are ready' 'one or both absent'

# ===========================================================================
section '5. per-task worktrees and dispatch'
# ===========================================================================

# Branch names follow the POC's own convention so the CI delay condition and
# the acceptance record read the same.
W1_BRANCH="sb/$RUN_TAG/w1-add"
W2_BRANCH="sb/$RUN_TAG/w2-multiply"
W3_BRANCH="sb/$RUN_TAG/w3-calculator"

wt_add_named() { # <wt> <branch> <base>
  sa_ledger_note "worktree add $1 branch $2 from ${3:0:9}"
  wt_add "$TARGET" "$1" "$2" "$3"
}

provision_named() { # <bead> <agent> <wt> <branch> <base>
  local bead="$1" agent="$2" wt="$3" branch="$4" base="$5"
  wt_add_named "$wt" "$branch" "$base" || { fail "$bead worktree created" "wt_add failed"; return 1; }
  wt_is_registered "$TARGET" "$wt" || { fail "$bead worktree is registered" "$wt"; return 1; }
  ( cd "$wt" && npm ci --silent ) >"$EVIDENCE/npm-ci-$agent.txt" 2>&1 \
    && pass "$bead worktree dependencies installed by the controller" \
    || fail "$bead worktree dependencies installed by the controller" "see npm-ci-$agent.txt"
  gcx bd update "$bead" --set-metadata "work_dir=$wt" >/dev/null 2>&1
  gcx bd update "$bead" --set-metadata "gc.sb_run=$RUN_ID" >/dev/null 2>&1
  local shown; shown="$(sa_gc bd show "$bead" 2>/dev/null || true)"
  grep -qF "work_dir: $wt" <<<"$shown" \
    && pass "$bead worktree created and legacy work_dir stamped before dispatch ($agent)" \
    || { fail "$bead legacy work_dir stamped before dispatch" 'stamp did not land'; return 1; }
}

provision_named "$W1" worker-w1 "$WT_W1" "$W1_BRANCH" "$SB_BASE_SHA" || abort 'W1 provisioning failed'
provision_named "$W2" worker-w2 "$WT_W2" "$W2_BRANCH" "$SB_BASE_SHA" || abort 'W2 provisioning failed'

gcx bd update "$W3" --set-metadata "work_dir=$WT_W3" >/dev/null 2>&1
gcx bd update "$W3" --set-metadata "gc.sb_run=$RUN_ID" >/dev/null 2>&1
[ ! -e "$WT_W3" ] && pass 'W3 worktree deliberately absent until both upstreams merge' \
  || fail 'W3 worktree deliberately absent' "$WT_W3 already present"

[ "$(readlink -f "$WT_W1")" != "$(readlink -f "$WT_W2")" ] \
  && pass 'W1 and W2 worktrees are pairwise distinct' \
  || fail 'W1 and W2 worktrees are pairwise distinct' 'same path'

ROUTE_EPOCH="$(date +%s)"
gcx sling "$RIG_NAME/worker-w1" "$W1" --no-formula --no-convoy > "$EVIDENCE/route-w1.txt" 2>&1
gcx sling "$RIG_NAME/worker-w2" "$W2" --no-formula --no-convoy > "$EVIDENCE/route-w2.txt" 2>&1
gcx sling "$RIG_NAME/worker-w3" "$W3" --no-formula --no-convoy > "$EVIDENCE/route-w3.txt" 2>&1
for bid in "$W1" "$W2" "$W3"; do
  shown="$(sa_gc bd show "$bid" 2>/dev/null || true)"
  grep -qE 'gc.routed_to: \S+' <<<"$shown" \
    && pass "$bid routed before any worker started" \
    || fail "$bid routed before any worker started" 'gc.routed_to absent'
done
sa_gc bd ready > "$EVIDENCE/ready-after-route.txt" 2>&1
sa_bead_in_ready "$W3" "$EVIDENCE/ready-after-route.txt" \
  && fail 'W3 remains blocked after being routed' 'routing made W3 ready' \
  || pass 'W3 remains blocked after being routed'

# ===========================================================================
section '6. parallel execution of W1 and W2'
# ===========================================================================

DISPATCH_START="$(date +%s)"
MAXPAR=0; PARPIDS=''
closed_1=0; closed_2=0
LIVE_PROOF=''
W3_EARLY=0
deadline=$(( $(date +%s) + WORK_DEADLINE ))
while true; do
  pids="$(sa_worker_pids "$CITY" '*worker-w[123]*')"
  n="$(printf '%s' "$pids" | wc -w)"
  [ "$n" -gt "$MAXPAR" ] && { MAXPAR="$n"; PARPIDS="$pids"; }
  [ "$(sa_worker_pids "$CITY" '*worker-w3*' | wc -w)" -gt 0 ] && W3_EARLY=1
  if [ -z "$LIVE_PROOF" ] && [ "$n" -ge 1 ]; then
    if LIVE_PROCESS_RESULT="$EVIDENCE/live-process.result" \
       BOUNDED_PROJECT=1 \
       bash "$SOURCE_REPO/corsolv/p2-smoke/verify-live-process.sh" "$CITY" \
         > "$EVIDENCE/live-process.txt" 2>&1; then
      LIVE_PROOF=PASS
    else
      LIVE_PROOF="FAIL($?)"
    fi
    info 'live worker posture captured' "$LIVE_PROOF (pids:$pids)"
  fi
  [ "$closed_1" -eq 0 ] && bead_is_closed "$W1" && closed_1=1
  [ "$closed_2" -eq 0 ] && bead_is_closed "$W2" && closed_2=1
  [ "$closed_1" -eq 1 ] && [ "$closed_2" -eq 1 ] && break
  if [ "$(date +%s)" -ge "$deadline" ]; then
    fail 'W1 and W2 both closed' "deadline reached (W1=$closed_1 W2=$closed_2)"; break
  fi
  sleep 10
done
WORK_ELAPSED=$(( $(date +%s) - DISPATCH_START ))
[ "$closed_1" -eq 1 ] && [ "$closed_2" -eq 1 ] && pass 'W1 and W2 both closed'
printf 'maxpar=%s pids=%s observed_at=%s\n' "$MAXPAR" "$PARPIDS" "$(date -u +%FT%TZ)" \
  > "$EVIDENCE/parallelism.result"
[ "$MAXPAR" -ge 2 ] && pass "W1 and W2 genuinely overlapped ($MAXPAR concurrent workers:$PARPIDS)" \
  || fail 'W1 and W2 genuinely overlapped' "max concurrent workers = $MAXPAR"
[ "$W3_EARLY" -eq 0 ] && pass 'no W3 worker existed while W3 was blocked' \
  || fail 'no W3 worker existed while W3 was blocked' 'a worker-w3 process was observed pre-release'
info 'W1+W2 wall clock' "${WORK_ELAPSED}s"

# ===========================================================================
section '7. controller publication pipeline'
# ===========================================================================

PR_NUMS=''; PR_HEADS=''; CI_RUNS=''; MERGE_SHAS=''

# publish_workstream <bead> <worktree> <branch> <label>
#
# The whole remote half, per workstream, in the order the POC brief requires:
# controller commits -> captures exact HEAD -> pushes -> opens PR -> observes
# CI -> proves the CI run's head SHA IS the PR head SHA -> independent
# assurance -> governed merge.
publish_workstream() {
  local bead="$1" wt="$2" branch="$3" label="$4"
  local authorised artifact viol commit pr_num pr_head run_id run_head concl merged_sha

  authorised="$(final_meta "$bead" "$EVIDENCE" 'gc.authorised_paths')"
  artifact="$(final_meta "$bead" "$EVIDENCE" 'gc.required_artifact')"
  [ -n "$authorised" ] || authorised="$(sa_gc bd show "$bead" 2>/dev/null | grep -oE 'gc.authorised_paths: \S+' | head -1 | awk '{print $2}')"

  # --- the boundary: what actually changed -------------------------------
  viol="$(publication_scope_violations "$wt" "$authorised")"
  if [ -z "$viol" ]; then
    pass "$label publication scope is exactly what the bead authorised ($authorised)"
  else
    fail "$label publication scope is exactly what the bead authorised" \
         "unauthorised: $(printf '%s' "$viol" | tr '\n' ' ')"
    return 1
  fi
  if [ ! -f "$wt/$artifact" ]; then
    fail "$label produced its required artifact" "$artifact absent"; return 1
  fi
  pass "$label produced its required artifact ($artifact)"

  # --- controller re-runs the gates itself, independently -----------------
  if ( cd "$wt" && npm run typecheck --silent ) >"$EVIDENCE/gate-typecheck-$label.txt" 2>&1; then
    pass "$label controller typecheck passes"
  else
    fail "$label controller typecheck passes" "see gate-typecheck-$label.txt"; return 1
  fi
  if ( cd "$wt" && npm test --silent ) >"$EVIDENCE/gate-test-$label.txt" 2>&1; then
    pass "$label controller tests pass"
  else
    fail "$label controller tests pass" "see gate-test-$label.txt"; return 1
  fi

  # --- commit, capture the exact head, push -------------------------------
  local -a paths=()
  IFS=',' read -r -a paths <<< "$authorised"
  commit="$(controller_commit "$wt" "feat($label): $artifact

Published by the controller. The worker created and verified this change but is
denied git by policy." "${paths[@]}")"
  [ -n "$commit" ] || { fail "$label controller committed the validated result" 'commit failed'; return 1; }
  pass "$label controller committed (${commit:0:9})"

  if git -C "$wt" push -q -u origin "$branch" 2>"$EVIDENCE/push-$label.txt"; then
    pass "$label controller pushed $branch"
  else
    fail "$label controller pushed $branch" "$(tail -2 "$EVIDENCE/push-$label.txt" | tr '\n' ' ')"; return 1
  fi

  # --- pull request -------------------------------------------------------
  "$GH" pr create --repo "$REPO_SLUG" --base "$SB_BASE_BRANCH" --head "$branch" \
    --title "feat($label): $artifact" \
    --body "Autonomous delivery via Gas City. Worker produced the change under bounded-project; the controller validated, committed, pushed and opened this PR. Bead \`$bead\`, run \`$RUN_ID\`." \
    > "$EVIDENCE/pr-$label.txt" 2>&1 || true
  pr_num="$("$GH" pr list --repo "$REPO_SLUG" --head "$branch" --json number --jq '.[0].number' 2>/dev/null)"
  if [ -n "$pr_num" ]; then
    pass "$label pull request opened (#$pr_num)"
  else
    fail "$label pull request opened" "$(tail -2 "$EVIDENCE/pr-$label.txt" | tr '\n' ' ')"; return 1
  fi

  pr_head="$("$GH" pr view "$pr_num" --repo "$REPO_SLUG" --json headRefOid --jq '.headRefOid' 2>/dev/null)"
  if [ "$pr_head" = "$commit" ]; then
    pass "$label PR head SHA is the controller commit ($pr_head)"
  else
    fail "$label PR head SHA is the controller commit" "PR head $pr_head, commit $commit"; return 1
  fi
  PR_NUMS="$PR_NUMS$label=#$pr_num "
  PR_HEADS="$PR_HEADS$label=$pr_head "

  # --- CI, tied to the exact head ----------------------------------------
  local ci_deadline=$(( $(date +%s) + CI_DEADLINE ))
  run_id=''; concl=''
  while [ "$(date +%s)" -lt "$ci_deadline" ]; do
    run_id="$(gh_api "repos/$REPO_SLUG/actions/runs?head_sha=$pr_head&event=pull_request" \
      --jq "[.workflow_runs[] | select(.name == \"$REQUIRED_WORKFLOW\")] | sort_by(.id) | last | .id" 2>/dev/null)"
    if [ -n "$run_id" ] && [ "$run_id" != 'null' ]; then
      concl="$(gh_api "repos/$REPO_SLUG/actions/runs/$run_id" --jq '.conclusion' 2>/dev/null)"
      [ -n "$concl" ] && [ "$concl" != 'null' ] && break
    fi
    sleep 15
  done
  if [ -z "$run_id" ] || [ "$run_id" = 'null' ]; then
    not_reached "$label required CI ran" "no $REQUIRED_WORKFLOW run found for head $pr_head" "$bead"
    return 1
  fi
  run_head="$(gh_api "repos/$REPO_SLUG/actions/runs/$run_id" --jq '.head_sha' 2>/dev/null)"
  gh_api "repos/$REPO_SLUG/actions/runs/$run_id" > "$EVIDENCE/ci-run-$label.json" 2>/dev/null || true
  if [ "$run_head" = "$pr_head" ]; then
    pass "$label CI tested the exact PR head SHA (run $run_id, head $run_head)"
  else
    fail "$label CI tested the exact PR head SHA" "run head $run_head != PR head $pr_head"; return 1
  fi
  if [ "$concl" = 'success' ]; then
    pass "$label required CI passed (run $run_id)"
  else
    fail "$label required CI passed" "conclusion '${concl:-<none>}' (run $run_id)"; return 1
  fi
  CI_RUNS="$CI_RUNS$label=$run_id($run_head) "

  # --- independent assurance ---------------------------------------------
  if SB_REPO_SLUG="$REPO_SLUG" GH="$GH" SB_BASE_BRANCH="$SB_BASE_BRANCH" \
     SA_CITY="$CITY" SA_RIG="$RIG_NAME" SOURCE_REPO="$SOURCE_REPO" \
     bash "$SOURCE_REPO/corsolv/p2-smoke/verify-independent-sb.sh" \
       "$TARGET" "$CITY" "$wt" "$bead" "$pr_num" "$pr_head" "$run_id" "$artifact" \
       > "$EVIDENCE/assurance-$label.txt" 2>&1; then
    pass "$label independent assurance passed"
  else
    fail "$label independent assurance passed" "see assurance-$label.txt"; return 1
  fi

  # --- governed merge -----------------------------------------------------
  # Squash first, matching how the original run entered main. If the repository
  # has squash disabled the merge strategy is the repository's decision, not
  # ours, so fall back rather than fail the criterion on a settings mismatch.
  if "$GH" pr merge "$pr_num" --repo "$REPO_SLUG" --squash --delete-branch=false \
       > "$EVIDENCE/merge-$label.txt" 2>&1; then
    pass "$label merged through repository governance (PR #$pr_num, squash)"
  elif "$GH" pr merge "$pr_num" --repo "$REPO_SLUG" --merge --delete-branch=false \
       >> "$EVIDENCE/merge-$label.txt" 2>&1; then
    pass "$label merged through repository governance (PR #$pr_num, merge commit)"
  else
    fail "$label merged through repository governance" "$(tail -2 "$EVIDENCE/merge-$label.txt" | tr '\n' ' ')"; return 1
  fi
  local state; state="$("$GH" pr view "$pr_num" --repo "$REPO_SLUG" --json state,mergeCommit --jq '.state' 2>/dev/null)"
  merged_sha="$("$GH" pr view "$pr_num" --repo "$REPO_SLUG" --json mergeCommit --jq '.mergeCommit.oid' 2>/dev/null)"
  if [ "$state" = 'MERGED' ] && [ -n "$merged_sha" ]; then
    pass "$label merge state reconciled (MERGED, ${merged_sha:0:9})"
  else
    fail "$label merge state reconciled" "state='$state' mergeCommit='${merged_sha:-<none>}'"; return 1
  fi

  # RECONCILE THE LOCAL BASE WITH WHAT GITHUB JUST DID.
  #
  # The squash merge exists on the REMOTE base; the clone's local branch still
  # points where it did before. Closing the merge bead as `shipped` against a
  # stale local branch is a claim the work-record gate cannot verify — and it
  # correctly refused it: "gc.work_commit <sha> is not reachable on
  # gc.work_branch". The gate was right; the harness was asserting reachability
  # on a ref it had not moved. Fast-forwarding here makes the claim true before
  # it is made, rather than relaxing the gate that caught it.
  git -C "$TARGET" fetch -q origin "$SB_BASE_BRANCH" 2>/dev/null
  git -C "$TARGET" update-ref "refs/heads/$SB_BASE_BRANCH" "refs/remotes/origin/$SB_BASE_BRANCH" 2>/dev/null
  if git -C "$TARGET" merge-base --is-ancestor "$merged_sha" "$SB_BASE_BRANCH" 2>/dev/null; then
    pass "$label merge commit is reachable on the local base after reconciliation"
  else
    fail "$label merge commit is reachable on the local base after reconciliation" \
         "$merged_sha not an ancestor of $SB_BASE_BRANCH"; return 1
  fi

  MERGE_SHAS="$MERGE_SHAS$label=$merged_sha "
  return 0
}

# Capture terminal bead state before publishing, so the authorised-path and
# artifact metadata come from the record the worker actually closed.
for b in "$W1" "$W2"; do capture_final_bead_state "$b" "$EVIDENCE" >/dev/null || true; done

W1_PUBLISHED=0; W2_PUBLISHED=0
publish_workstream "$W1" "$WT_W1" "$W1_BRANCH" w1 && W1_PUBLISHED=1
publish_workstream "$W2" "$WT_W2" "$W2_BRANCH" w2 && W2_PUBLISHED=1

# ===========================================================================
section '8. release of the dependent workstream'
# ===========================================================================

close_merge_bead() {
  local this="$1" other="$2" merge_sha="$3"
  local other_status; other_status="$(bead_status "$other" || true)"
  if [ "$other_status" = 'closed' ]; then
    # Both upstreams are merged on GitHub. Refresh the base and cut W3's
    # worktree from it, so the dependent task consumes its upstreams through
    # repository state rather than by reading either upstream worktree.
    # NEVER MUTATE THE RIG WORKING TREE.
    #
    # This used to `checkout` the base and `reset --hard` to the remote. That
    # discarded the bd-init commit `gc rig add` had made in the rig and took the
    # tracked bead-store files with it, so the very next `gc bd close` failed
    # with "no issue found matching <id>" — the store the controller was talking
    # to had been reset out from under it. The remote SHA is all that is needed;
    # reading it does not require moving the rig's own checkout anywhere.
    git -C "$TARGET" fetch -q origin "$SB_BASE_BRANCH" 2>/dev/null
    MERGED_BASE="$(git -C "$TARGET" rev-parse "refs/remotes/origin/$SB_BASE_BRANCH")"
    sa_ledger_note "worktree add $WT_W3 branch $W3_BRANCH from merged base ${MERGED_BASE:0:9}"
    if wt_add "$TARGET" "$WT_W3" "$W3_BRANCH" "$MERGED_BASE" && wt_is_registered "$TARGET" "$WT_W3"; then
      pass "W3 worktree created from the MERGED base (${MERGED_BASE:0:9})"
    else
      fail 'W3 worktree created from the merged base' "wt_add failed for $WT_W3"
    fi
    if [ -f "$WT_W3/src/add.ts" ] && [ -f "$WT_W3/src/multiply.ts" ]; then
      pass 'W3 worktree carries both merged upstreams via repository state'
    else
      fail 'W3 worktree carries both merged upstreams via repository state' \
           "$(ls "$WT_W3/src" 2>/dev/null | tr '\n' ' ')"
    fi
    ( cd "$WT_W3" && npm ci --silent ) >"$EVIDENCE/npm-ci-worker-w3.txt" 2>&1 \
      && pass 'W3 worktree dependencies installed by the controller' \
      || fail 'W3 worktree dependencies installed by the controller' 'see npm-ci-worker-w3.txt'
    sa_ledger_mark release
    RELEASE_EPOCH="$(date +%s)"
    RELEASE_UTC="$(date -u +%FT%TZ)"
  fi
  if [ -n "$merge_sha" ]; then
    gcx bd update "$this" \
      --set-metadata 'gc.work_outcome=shipped' \
      --set-metadata "gc.work_commit=$merge_sha" \
      --set-metadata "gc.work_branch=$SB_BASE_BRANCH" >/dev/null 2>&1
  fi
  local out rc
  out="$(gcx bd close "$this" --reason 'controller published, CI-verified and merged the upstream result' 2>&1)"; rc=$?
  if [ "$rc" -eq 0 ] && bead_is_closed "$this"; then
    pass "$this merge bead closed by the controller with a typed shipped record"
  else
    fail "$this merge bead closed by the controller" "$(printf '%s' "$out" | tail -1)"
  fi
}

RELEASE_EPOCH=''; RELEASE_UTC=''; MERGED_BASE=''
W1_MERGE="$(grep -o 'w1=[0-9a-f]*' <<<"$MERGE_SHAS" | head -1 | cut -d= -f2)"
W2_MERGE="$(grep -o 'w2=[0-9a-f]*' <<<"$MERGE_SHAS" | head -1 | cut -d= -f2)"
[ "$W1_PUBLISHED" -eq 1 ] && close_merge_bead "$W1INT" "$W2INT" "$W1_MERGE"
[ "$W2_PUBLISHED" -eq 1 ] && close_merge_bead "$W2INT" "$W1INT" "$W2_MERGE"
RELEASE_EPOCH="${RELEASE_EPOCH:-$(date +%s)}"
RELEASE_UTC="${RELEASE_UTC:-$(date -u +%FT%TZ)}"

W3_READY_AT=''
deadline=$(( $(date +%s) + 300 ))
while true; do
  sa_gc bd ready > "$EVIDENCE/ready-after-merge.txt" 2>&1
  sa_bead_in_ready "$W3" "$EVIDENCE/ready-after-merge.txt" && { W3_READY_AT="$(date -u +%FT%TZ)"; break; }
  [ "$(date +%s)" -ge "$deadline" ] && break
  sleep 5
done
[ -n "$W3_READY_AT" ] && pass "readiness projection exposed W3 automatically at $W3_READY_AT" \
  || fail 'readiness projection exposed W3 automatically' 'W3 never became ready'

W3_START="$(date +%s)"; W3_CLAIMED_AT=''; closed_3=0; LIVE_PROOF_W3=''
deadline=$(( $(date +%s) + WORK_DEADLINE ))
while true; do
  if [ -z "$W3_CLAIMED_AT" ]; then
    p="$(sa_worker_pids "$CITY" '*worker-w3*')"
    if [ -n "$p" ]; then
      W3_CLAIMED_AT="$(date -u +%FT%TZ)"
      info 'W3 worker started autonomously' "$W3_CLAIMED_AT (pids:$p)"
      if LIVE_PROCESS_RESULT="$EVIDENCE/live-process-w3.result" BOUNDED_PROJECT=1 \
         bash "$SOURCE_REPO/corsolv/p2-smoke/verify-live-process.sh" "$CITY" \
           > "$EVIDENCE/live-process-w3.txt" 2>&1; then
        LIVE_PROOF_W3=PASS
      else
        LIVE_PROOF_W3="FAIL($?)"
      fi
      info 'live W3 worker posture captured' "$LIVE_PROOF_W3"
    fi
  fi
  bead_is_closed "$W3" && { closed_3=1; break; }
  [ "$(date +%s)" -ge "$deadline" ] && break
  sleep 10
done
W3_ELAPSED=$(( $(date +%s) - W3_START ))
[ "$closed_3" -eq 1 ] && pass 'W3 closed' || fail 'W3 closed' 'deadline reached'
info 'W3 wall clock' "${W3_ELAPSED}s"
[ -n "$W3_CLAIMED_AT" ] \
  && pass "normal controller demand claimed and started W3 with no operator command ($W3_CLAIMED_AT)" \
  || not_reached 'normal controller demand claimed and started W3' 'no W3 worker was observed' "$W3"

W3_DIRECTIVES="$(sa_ledger_directives_since_mark release "$W3")$(sa_ledger_directives_since_mark release worker-w3)"
[ -z "$W3_DIRECTIVES" ] \
  && pass "zero post-release directives naming W3 (release $RELEASE_UTC)" \
  || fail 'zero post-release directives naming W3' "$(printf '%s' "$W3_DIRECTIVES" | tr '\n' ';' | head -c 200)"

capture_final_bead_state "$W3" "$EVIDENCE" >/dev/null || true
W3_PUBLISHED=0
publish_workstream "$W3" "$WT_W3" "$W3_BRANCH" w3 && W3_PUBLISHED=1

# ===========================================================================
section '9. final reconciliation'
# ===========================================================================

for id in "$W1" "$W2" "$W3"; do
  capture_final_bead_state "$id" "$EVIDENCE" >/dev/null || true
  wo="$(final_meta "$id" "$EVIDENCE" 'gc.work_outcome')"
  case "$wo" in
    blocked|no-op|abandoned) pass "$id typed work outcome is truthful ($wo)" ;;
    shipped) fail "$id typed work outcome is truthful" 'claims shipped while git is withheld from workers' ;;
    '') fail "$id records a work outcome" 'absent — silent close' ;;
    *) fail "$id records a work outcome" "unknown disposition '$wo'" ;;
  esac
  wd="$(final_meta "$id" "$EVIDENCE" 'gc.work_dir')"
  lg="$(final_meta "$id" "$EVIDENCE" 'work_dir')"
  [ -n "$lg" ] && [ "$wd" = "$lg" ] \
    && pass "$id canonical gc.work_dir mirrored from the controller stamp" \
    || fail "$id canonical gc.work_dir mirrored from the controller stamp" "legacy='${lg:-<none>}' canonical='${wd:-<none>}'"
done

git -C "$TARGET" fetch -q origin "$SB_BASE_BRANCH" 2>/dev/null
FINAL_BASE="$(git -C "$TARGET" rev-parse "refs/remotes/origin/$SB_BASE_BRANCH" 2>/dev/null)"
info 'final S-B base sha' "${FINAL_BASE:-<none>}"

# The final gates run in a throwaway detached worktree at the merged base, not
# in the rig root: the rig holds the live bead store, and resetting it is what
# broke the controller's own store on the previous run.
FINAL_WT="$CITY/.gc/worktrees/$RIG_NAME/final-check"
if [ -n "$FINAL_BASE" ] && GIT_LFS_SKIP_SMUDGE=1 \
   git -C "$TARGET" worktree add -q --detach "$FINAL_WT" "$FINAL_BASE" 2>/dev/null; then
  pass "final merged base checked out for verification (${FINAL_BASE:0:9})"
  ( cd "$FINAL_WT" && npm ci --silent ) >"$EVIDENCE/final-npm-ci.txt" 2>&1 || true
  if ( cd "$FINAL_WT" && npm run typecheck --silent ) >"$EVIDENCE/final-typecheck.txt" 2>&1; then
    pass 'final merged base typechecks'
  else
    fail 'final merged base typechecks' 'see final-typecheck.txt'
  fi
  if ( cd "$FINAL_WT" && npm test --silent ) >"$EVIDENCE/final-test.txt" 2>&1; then
    pass 'final merged base tests pass'
  else
    fail 'final merged base tests pass' 'see final-test.txt'
  fi
else
  fail 'final merged base checked out for verification' "could not create a worktree at ${FINAL_BASE:-<none>}"
fi

AUTHORS="$(git -C "$TARGET" log --format='%an' "$PRE_BASE_SHA..$FINAL_BASE" 2>/dev/null | sort -u | tr '\n' ',')"
if grep -qv 'Gas City Controller\|Corsolv' <<<"$(git -C "$TARGET" log --format='%an' "$PRE_BASE_SHA..$FINAL_BASE" 2>/dev/null | sort -u)"; then
  fail 'every commit added by this run is controller-authored' "authors: $AUTHORS"
else
  pass "every commit added by this run is controller-authored ($AUTHORS)"
fi

case "$LIVE_PROOF" in
  PASS) pass 'live W1/W2 worker posture proved while those workers were alive' ;;
  '')   not_reached 'live W1/W2 worker posture proved while alive' 'no worker observed alive' ;;
  *)    fail 'live W1/W2 worker posture proved while alive' "verify-live-process reported $LIVE_PROOF" ;;
esac
case "${LIVE_PROOF_W3:-}" in
  PASS) pass 'live W3 worker posture proved while that worker was alive' ;;
  '')   not_reached 'live W3 worker posture proved while alive' 'no W3 worker observed alive' ;;
  *)    fail 'live W3 worker posture proved while alive' "verify-live-process reported $LIVE_PROOF_W3" ;;
esac

DRAIN_DEADLINE="${DRAIN_DEADLINE:-240}"
settle_start="$(date +%s)"
leftover="$(sa_worker_pids "$CITY" '*worker-w[123]*')"
while [ -n "$leftover" ]; do
  [ "$(( $(date +%s) - settle_start ))" -ge "$DRAIN_DEADLINE" ] && break
  sleep 5
  leftover="$(sa_worker_pids "$CITY" '*worker-w[123]*')"
done
DRAIN_TOOK=$(( $(date +%s) - settle_start ))
[ -z "$leftover" ] && pass "managed workers drained (settled in ${DRAIN_TOOK}s)" \
  || fail 'managed workers drained' "pids:$leftover still present after ${DRAIN_DEADLINE}s"

sa_gc session list > "$EVIDENCE/sessions.txt" 2>&1

# ===========================================================================
# Report.
# ===========================================================================

OVERALL=PASS
{ [ "$FAILURES" -ne 0 ] || [ "$NOT_REACHED_COUNT" -ne 0 ]; } && OVERALL=FAIL

mkdir -p "$(dirname "$REPORT")"
cat > "$REPORT" <<EOF
# Corsolv S-B — Promoted Run (Remote Acceptance Half)

S-B OVERALL: $OVERALL

| Adjudication | Count |
| --- | --- |
| Mandatory PASS | $PASS_COUNT |
| Mandatory FAIL | $FAILURES |
| Mandatory NOT REACHED | $NOT_REACHED_COUNT |

## What this stage closes

S-A proved the local coordination properties and claimed only the LOCAL half of
criterion 10. This stage runs the same W1/W2/W3 graph against the real GitHub
repository, so the dependent workstream is released only after its upstreams are
**merged remotely**.

| Half | Meaning | Status |
| --- | --- | --- |
| Local (S-A) | controller integrates validated commits into the run base | PASS (run \`sa-20260810T142219Z\`) |
| Remote (S-B) | GitHub merge after PR + exact-head CI + independent assurance | $( [ "$OVERALL" = PASS ] && echo PASS || echo FAIL ) |
| Full criterion 10 | both halves | $( [ "$OVERALL" = PASS ] && echo COMPLETE || echo INCOMPLETE ) |

## Scope decisions, recorded

**The rig is a working clone.** \`gc rig add\` writes a bead store, runtime state
and a .gitignore into its rig and makes its own commit; pointing that at the
authoritative checkout would mutate the repository holding the POC's own
acceptance record. PRs, CI runs and merges are real and land on
\`$REPO_SLUG\`.

**The integration target is \`$SB_BASE_BRANCH\`, not \`main\`.** W1/W2/W3 are already
merged into main (PRs #1/#2/#3) by the original PowerShell controller, so the
same three tasks cannot be re-expressed as a diff against main. The graph is
replayed from the POC's own pre-workstream base \`$PRE_BASE\`. Criterion 11
("all successful work entered main through PRs") is already satisfied on main by
the original run and is **not** re-proved here against main.

## Foundation

| Item | Value |
| --- | --- |
| Run ID | \`$RUN_ID\` |
| Delivery-engine source SHA | \`$SOURCE_SHA\` |
| Binary SHA256 | \`$BIN_SHA\` |
| Target repository | \`$REPO_SLUG\` |
| Replay base | \`$PRE_BASE_SHA\` |
| S-B base branch | \`$SB_BASE_BRANCH\` @ \`$SB_BASE_SHA\` |
| Final S-B base | \`${FINAL_BASE:-<none>}\` |
| Local Node | \`$NODE_VER\` (CI pins Node 24) |
| Work beads | W1=\`$W1\` W2=\`$W2\` W3=\`$W3\` |
| Merge beads | W1-int=\`$W1INT\` W2-int=\`$W2INT\` |

## Remote evidence

| Item | Value |
| --- | --- |
| Pull requests | $PR_NUMS |
| PR head SHAs | $PR_HEADS |
| CI runs (run=head) | $CI_RUNS |
| Merge commits | $MERGE_SHAS |
| Release moment (both upstreams merged) | $RELEASE_UTC |
| W3 became ready | ${W3_READY_AT:-<never>} |
| W3 worker started | ${W3_CLAIMED_AT:-<never>} |
| Post-release directives naming W3 | $( [ -z "$W3_DIRECTIVES" ] && echo none || echo 'PRESENT — see ledger' ) |

## Worker posture

Workers ran \`bounded-project\`: Read/Write/Edit/Glob/Grep, the pool-worker
lifecycle, and three named project gates (typecheck/build/test). No git, no gh,
no shell family. Publication was gated on the changed-file set matching each
bead's \`gc.authorised_paths\`.

| Property | Evidence |
| --- | --- |
| Parallel work | max $MAXPAR concurrent workers ($PARPIDS) |
| W1+W2 wall clock | ${WORK_ELAPSED}s |
| W3 wall clock | ${W3_ELAPSED}s |
| Live W1/W2 posture | ${LIVE_PROOF:-<none>} |
| Live W3 posture | ${LIVE_PROOF_W3:-<none>} |
| Drain | settled in ${DRAIN_TOOK}s |

## Control ledger

| Control | Status | Reason | Subject |
| --- | --- | --- | --- |
$(awk -F'\t' 'NR>1 {printf "| %s | %s | %s | %s |\n", $1, $2, ($3==""?"—":$3), ($4==""?"—":$4)}' "$CONTROLS")

### Failures and unreached controls

$(awk -F'\t' 'NR>1 && ($2=="FAIL" || $2=="NOT_REACHED") {printf "- **%s** — %s: %s%s\n", $2, $1, ($3==""?"(no reason recorded)":$3), ($4==""?"":" [" $4 "]")}' "$CONTROLS" | grep . || echo 'None. Every mandatory control passed.')

## Evidence directory

\`$EVIDENCE\`
EOF

echo
echo '============================================================'
echo "S-B OVERALL: $OVERALL"
echo "  mandatory PASS:        $PASS_COUNT"
echo "  mandatory FAIL:        $FAILURES"
echo "  mandatory NOT REACHED: $NOT_REACHED_COUNT"
echo "report: $REPORT"
echo "evidence: $EVIDENCE"
[ "$OVERALL" = PASS ] || exit 70
