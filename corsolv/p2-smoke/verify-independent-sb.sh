#!/usr/bin/env bash
#
# Independent read-only assurance for ONE S-B workstream, run BEFORE the merge.
#
# It does not trust the run's own report. Every claim is re-derived: the PR head
# is re-queried from GitHub rather than taken from the caller, the CI run is
# re-read and checked at JOB level as well as run level, and the project gates
# are re-executed from a fresh checkout of the exact PR head with its own
# dependency install. Where it can, it derives a fact a different way than the
# harness did — that is what makes this assurance rather than a second reading
# of the same claim.
#
# It runs before the merge on purpose: assurance that only ever looks at
# already-merged work cannot withhold anything.
#
# Usage: verify-independent-sb.sh <rig> <city> <worktree> <bead> <pr> <pr-head> <ci-run> <artifact>
# Env:   SB_REPO_SLUG, GH, SB_BASE_BRANCH
# Exit:  0 all assurance checks passed, 66 otherwise.

set -uo pipefail
export PATH="$HOME/.local/bin:/usr/local/go/bin:$PATH"

RIG="${1:?rig path required}"
CITY="${2:?city path required}"
WT="${3:?worktree required}"
BEAD="${4:?bead id required}"
PR_NUM="${5:?pr number required}"
PR_HEAD="${6:?pr head sha required}"
CI_RUN="${7:?ci run id required}"
ARTIFACT="${8:?artifact path required}"

REPO_SLUG="${SB_REPO_SLUG:?SB_REPO_SLUG required}"
GH="${GH:-/mnt/c/Program Files/GitHub CLI/gh.exe}"
SB_BASE_BRANCH="${SB_BASE_BRANCH:-sb/base}"
SOURCE_REPO="${SOURCE_REPO:-/mnt/d/Development/corsolv-delivery-engine}"
# shellcheck source=lib/sa-lib.sh
. "$SOURCE_REPO/corsolv/p2-smoke/lib/sa-lib.sh"

SA_CITY="${SA_CITY:-$CITY}"

FAILURES=0
note() { printf '  %-62s %s\n' "$1" "$2"; }
fail() { note "$1" "FAIL — $2"; FAILURES=$((FAILURES + 1)); }
pass() { note "$1" 'PASS'; }
info() { note "$1" "INFO — $2"; }
section() { printf '\n--- %s ---\n' "$1"; }

echo '============================================================'
echo "S-B INDEPENDENT ASSURANCE — PR #$PR_NUM (pre-merge)"
echo '============================================================'

# --- 1. the pull request, re-queried ---------------------------------------
section '1. pull request'

PR_JSON="$("$GH" pr view "$PR_NUM" --repo "$REPO_SLUG" --json headRefOid,baseRefName,state,mergedAt,headRefName 2>/dev/null)"
[ -n "$PR_JSON" ] || { fail 'pull request is readable' "gh pr view #$PR_NUM returned nothing"; exit 66; }

RQ_HEAD="$(jq -r '.headRefOid // empty' <<<"$PR_JSON")"
RQ_BASE="$(jq -r '.baseRefName // empty' <<<"$PR_JSON")"
RQ_STATE="$(jq -r '.state // empty' <<<"$PR_JSON")"
RQ_MERGED="$(jq -r '.mergedAt // empty' <<<"$PR_JSON")"

if [ "$RQ_HEAD" = "$PR_HEAD" ]; then
  pass "PR head SHA re-queried independently matches ($RQ_HEAD)"
else
  fail 'PR head SHA re-queried independently matches' "GitHub says $RQ_HEAD, run claimed $PR_HEAD"
fi
if [ "$RQ_BASE" = "$SB_BASE_BRANCH" ]; then
  pass "PR targets the governed base branch ($RQ_BASE)"
else
  fail 'PR targets the governed base branch' "base is '$RQ_BASE'"
fi
if [ "$RQ_STATE" = 'OPEN' ] && [ -z "$RQ_MERGED" ]; then
  pass 'assurance is running BEFORE the merge'
else
  fail 'assurance is running before the merge' "state=$RQ_STATE mergedAt=${RQ_MERGED:-<none>}"
fi

# --- 2. CI, at run AND job level -------------------------------------------
section '2. required CI'

RUN_JSON="$("$GH" api "repos/$REPO_SLUG/actions/runs/$CI_RUN" 2>/dev/null)"
[ -n "$RUN_JSON" ] || { fail 'CI run is readable' "run $CI_RUN not found"; exit 66; }

RUN_HEAD="$(jq -r '.head_sha // empty' <<<"$RUN_JSON")"
RUN_CONCL="$(jq -r '.conclusion // empty' <<<"$RUN_JSON")"
RUN_NAME="$(jq -r '.name // empty' <<<"$RUN_JSON")"
RUN_EVENT="$(jq -r '.event // empty' <<<"$RUN_JSON")"

# The criterion is not "CI passed" but "the CI that passed tested THIS head".
# A run against an earlier push on the same branch would otherwise be
# indistinguishable from one against the PR head.
if [ "$RUN_HEAD" = "$RQ_HEAD" ]; then
  pass "CI tested the exact PR head SHA ($RUN_HEAD)"
else
  fail 'CI tested the exact PR head SHA' "run head $RUN_HEAD != PR head $RQ_HEAD"
fi
if [ "$RUN_CONCL" = 'success' ]; then
  pass "required CI concluded success (run $CI_RUN, workflow '$RUN_NAME', event $RUN_EVENT)"
else
  fail 'required CI concluded success' "conclusion '${RUN_CONCL:-<none>}'"
fi

# Run-level success is not job-level success: a workflow whose only job was
# skipped also concludes "success". The validate job is the gate.
JOBS_JSON="$("$GH" api "repos/$REPO_SLUG/actions/runs/$CI_RUN/jobs" 2>/dev/null)"
VALIDATE_CONCL="$(jq -r '[.jobs[] | select(.name | test("typecheck|validate"))] | .[0].conclusion // empty' <<<"$JOBS_JSON" 2>/dev/null)"
VALIDATE_NAME="$(jq -r '[.jobs[] | select(.name | test("typecheck|validate"))] | .[0].name // empty' <<<"$JOBS_JSON" 2>/dev/null)"
if [ "$VALIDATE_CONCL" = 'success' ]; then
  pass "the validating job itself succeeded ('$VALIDATE_NAME')"
else
  fail 'the validating job itself succeeded' \
       "job '${VALIDATE_NAME:-<none>}' concluded '${VALIDATE_CONCL:-<none>}' — a skipped job still yields a green run"
fi
# And it must have actually run the gates, not skipped them.
STEP_FAILS="$(jq -r '[.jobs[].steps[]? | select(.conclusion != "success" and .conclusion != "skipped" and .conclusion != null)] | length' <<<"$JOBS_JSON" 2>/dev/null)"
if [ "${STEP_FAILS:-0}" = '0' ]; then
  pass 'no CI step concluded anything other than success'
else
  fail 'no CI step concluded anything other than success' "$STEP_FAILS step(s) did not succeed"
fi

# --- 3. the gates, re-executed from the exact head -------------------------
section '3. independent local validation at the PR head'

# A fresh detached checkout of the exact PR head, with its own dependency
# install. Re-running the gates inside the worker's own worktree would be
# checking the same tree the worker left behind; this checks what GitHub is
# actually being asked to merge.
ASSURE_WT="$(mktemp -d -p /var/tmp assure-XXXXXX)"
cleanup() { git -C "$RIG" worktree remove --force "$ASSURE_WT" >/dev/null 2>&1 || rm -rf "$ASSURE_WT"; }
trap cleanup EXIT

rmdir "$ASSURE_WT" 2>/dev/null
if git -C "$RIG" fetch -q origin "$RQ_HEAD" 2>/dev/null &&
   GIT_LFS_SKIP_SMUDGE=1 git -C "$RIG" worktree add -q --detach "$ASSURE_WT" "$RQ_HEAD" 2>/dev/null; then
  pass "fresh checkout of the exact PR head (${RQ_HEAD:0:9})"
else
  fail 'fresh checkout of the exact PR head' "could not create a worktree at $RQ_HEAD"
  exit 66
fi

if [ -f "$ASSURE_WT/$ARTIFACT" ]; then
  pass "required artifact present at the PR head ($ARTIFACT)"
else
  fail 'required artifact present at the PR head' "$ARTIFACT absent"
fi

if ( cd "$ASSURE_WT" && npm ci --silent ) >/tmp/assure-npmci.$$ 2>&1; then
  pass 'dependencies install from the locked manifest'
else
  fail 'dependencies install from the locked manifest' "$(tail -3 /tmp/assure-npmci.$$ | tr '\n' ' ')"
fi
if ( cd "$ASSURE_WT" && npm run typecheck --silent ) >/tmp/assure-tc.$$ 2>&1; then
  pass 'typecheck passes at the PR head (independently executed)'
else
  fail 'typecheck passes at the PR head' "$(tail -5 /tmp/assure-tc.$$ | tr '\n' ' ')"
fi
if ( cd "$ASSURE_WT" && npm test --silent ) >/tmp/assure-test.$$ 2>&1; then
  pass 'tests pass at the PR head (independently executed)'
else
  fail 'tests pass at the PR head' "$(tail -5 /tmp/assure-test.$$ | tr '\n' ' ')"
fi
rm -f /tmp/assure-npmci.$$ /tmp/assure-tc.$$ /tmp/assure-test.$$

# --- 4. authority separation -----------------------------------------------
section '4. authority separation'

BASE_REF="origin/$SB_BASE_BRANCH"
git -C "$RIG" fetch -q origin "$SB_BASE_BRANCH" 2>/dev/null
ADDED_AUTHORS="$(git -C "$RIG" log --format='%an' "$BASE_REF..$RQ_HEAD" 2>/dev/null | sort -u)"
UNEXPECTED="$(grep -v '^Gas City Controller$' <<<"$ADDED_AUTHORS" | grep -v '^Corsolv' || true)"
if [ -z "$UNEXPECTED" ]; then
  pass "every commit this PR adds is controller-authored ($(printf '%s' "$ADDED_AUTHORS" | tr '\n' ','))"
else
  fail 'every commit this PR adds is controller-authored' \
       "unexpected author(s): $(printf '%s' "$UNEXPECTED" | tr '\n' ' ')"
fi

# The worker's own transcript is the attribution authority: it must show the
# project gates and must show no publication command at all.
project_dir_for() {
  local p="$1" cand
  for cand in "$HOME/.claude/projects/$(printf '%s' "$p" | sed 's|/|-|g')" \
              "$HOME/.claude/projects/$(printf '%s' "$p" | sed 's|[^A-Za-z0-9]|-|g')"; do
    [ -d "$cand" ] && { printf '%s' "$cand"; return 0; }
  done
  return 1
}
if PROJ="$(project_dir_for "$WT")"; then
  pass 'worker transcript located for this worktree'
  CMDS="$(grep -ohE '"command":"[^"]*' "$PROJ"/*.jsonl 2>/dev/null | sed 's/"command":"//')"

  # WHAT THE WORKER INVOKED, NOT WHAT ITS TEXT CONTAINS.
  #
  # The first version searched the whole command string for "git ". A worker
  # closing honestly writes a blocked-reason explaining that it cannot run git —
  # so `gc bd update <id> --set-metadata gc.work_blocked_reason=...` matched, and
  # a correct worker was reported as having run git. That is the S-A lesson in a
  # new place: worker-authored free text must never be able to drive an
  # acceptance gate, in either direction.
  #
  # Each command is split on the shell operators that can start a new command,
  # and only the FIRST TOKEN of each segment is matched. A chained `cd x && git
  # push` is still caught; prose inside a quoted argument is not.
  invoked_binaries() {
    printf '%s\n' "$CMDS" \
      | sed 's/&&/\n/g; s/||/\n/g; s/;/\n/g; s/|/\n/g' \
      | sed 's/^[[:space:]]*//' \
      | awk 'NF {print $1, $2}'
  }
  INVOKED="$(invoked_binaries)"

  # THE TRANSCRIPT CORROBORATES; IT DOES NOT ADJUDICATE.
  #
  # This was a FAIL gate twice and was wrong twice, both times passing judgement
  # on TEXT rather than on execution. First a worker's own truthful
  # blocked-reason ("cannot run git") matched. Then, after narrowing to invoked
  # binaries, a permission-settings fragment — `Bash(git status)` — matched,
  # because a transcript records proposed and denied commands and permission
  # structures alongside executed ones, with no reliable marker separating them.
  # A correct worker failed assurance twice for text it was right to contain.
  #
  # There are two proofs of the same fact that text cannot spoof, and both are
  # asserted as hard gates elsewhere in this run:
  #
  #   - the LIVE permission posture, read from /proc argv while the worker was
  #     running: the allowlist carries no git, gh or npm-install grant, so the
  #     engine would refuse any such call;
  #   - AUTHORSHIP: every commit this PR adds is controller-authored (asserted
  #     below), and the changed-file set matched what the bead authorised.
  #
  # An attempted-and-denied git is not a breach — it is the policy working. So
  # what the transcript shows is reported, in full, as corroboration.
  report_invocation() {
    local label="$1" pattern="$2" hit
    hit="$(grep -E "$pattern" <<<"$INVOKED" | head -1)"
    if [ -n "$hit" ]; then
      info "transcript mentions $label" "$hit (denied by policy unless the live posture says otherwise)"
    else
      pass "transcript shows no $label invocation"
    fi
  }
  report_invocation 'git'         '^(/[^ ]*/)?git( |$)'
  report_invocation 'gh'          '^(/[^ ]*/)?gh(\.exe)?( |$)'
  report_invocation 'npx'         '^(/[^ ]*/)?npx( |$)'
  report_invocation 'npm install' '^(/[^ ]*/)?npm (install|i|ci|add)$'
  report_invocation 'npm publish' '^(/[^ ]*/)?npm publish$'
  if grep -qE 'npm (run typecheck|test)' <<<"$CMDS"; then
    pass 'worker ran its own project gates'
  else
    info 'worker project gates in transcript' 'no typecheck/test command recorded'
  fi
else
  fail 'worker transcript located for this worktree' "no transcript directory for $WT"
fi

# --- 5. the bead ------------------------------------------------------------
section '5. bead lifecycle'

STATUS="$(bead_status "$BEAD" || true)"
if [ "$STATUS" = 'closed' ]; then
  pass "$BEAD is CLOSED (structured status)"
else
  fail "$BEAD is CLOSED" "status='${STATUS:-<unreadable>}'"
fi
RENDER="$(sa_gc bd show "$BEAD" 2>/dev/null || true)"
WO="$(grep -E '^[[:space:]]*gc.work_outcome:' <<<"$RENDER" | head -1 | sed 's/.*gc.work_outcome:[[:space:]]*//')"
case "$WO" in
  blocked|no-op|abandoned) pass "$BEAD typed outcome is truthful ($WO)" ;;
  shipped) fail "$BEAD typed outcome is truthful" 'claims shipped while git is withheld from workers' ;;
  '') fail "$BEAD records a typed outcome" 'absent — silent close' ;;
  *) fail "$BEAD records a typed outcome" "unknown disposition '$WO'" ;;
esac

echo
echo '============================================================'
if [ "$FAILURES" -ne 0 ]; then
  echo "S-B INDEPENDENT ASSURANCE: FAIL ($FAILURES check(s))"
  exit 66
fi
echo 'S-B INDEPENDENT ASSURANCE: PASS'
