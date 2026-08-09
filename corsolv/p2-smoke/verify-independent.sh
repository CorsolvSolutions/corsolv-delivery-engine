#!/usr/bin/env bash
#
# Independent read-only assurance for a completed P2.1 acceptance run.
#
# Deliberately does not trust the run's own report: it re-derives every claim
# from the filesystem, git, and the bead store. Read-only throughout -- no
# writes, no state mutation.
#
# Usage: verify-independent.sh <rig-path> <city-path> <work-bead-id>
# Exit:  0 all assurance checks passed, 66 otherwise.

set -uo pipefail
export PATH="$HOME/.local/bin:/usr/local/go/bin:$PATH"

RIG="${1:?rig path required}"
CITY="${2:?city path required}"
WORK_ID="${3:?work bead id required}"

MARKER='CORSOLV_GASCITY_MANAGED_CLAUDE_PASS'
ARTIFACT="$RIG/CORSOLV_GASCITY_SMOKE.txt"
FAILURES=0

note() { printf '  %-52s %s\n' "$1" "$2"; }
fail() { note "$1" "FAIL — $2"; FAILURES=$((FAILURES + 1)); }
pass() { note "$1" 'PASS'; }

echo '============================================================'
echo 'INDEPENDENT ASSURANCE (READ-ONLY)'
echo '============================================================'
echo "rig:  $RIG"
echo "city: $CITY"
echo "work: $WORK_ID"
echo

# --- artifact -------------------------------------------------------------
if [ -f "$ARTIFACT" ]; then
  pass 'artifact exists'
  actual="$(cat "$ARTIFACT")"
  if [ "$actual" = "$MARKER" ]; then
    pass 'artifact content matches marker exactly'
  else
    fail 'artifact content matches marker exactly' "got '$actual'"
  fi
  if [ "$(wc -l <"$ARTIFACT")" -eq 1 ]; then
    pass 'artifact is a single line'
  else
    fail 'artifact is a single line' "$(wc -l <"$ARTIFACT") lines"
  fi
else
  fail 'artifact exists' "$ARTIFACT not found"
fi

# --- git ------------------------------------------------------------------
if git -C "$RIG" rev-parse HEAD >/dev/null 2>&1; then
  head_sha="$(git -C "$RIG" rev-parse HEAD)"
  pass "git HEAD readable ($head_sha)"
  if git -C "$RIG" cat-file -e "HEAD:CORSOLV_GASCITY_SMOKE.txt" 2>/dev/null; then
    committed="$(git -C "$RIG" show "HEAD:CORSOLV_GASCITY_SMOKE.txt")"
    if [ "$committed" = "$MARKER" ]; then
      pass 'artifact committed with correct content'
    else
      fail 'artifact committed with correct content' "committed '$committed'"
    fi
  else
    note 'artifact committed' 'INFO — present in worktree, not committed'
  fi
else
  fail 'git HEAD readable' 'not a git repo'
fi

# --- bead -----------------------------------------------------------------
bead="$(cd "$CITY" && gc bd show "$WORK_ID" 2>&1)"
if printf '%s' "$bead" | grep -Eq 'CLOSED'; then
  pass 'work bead is CLOSED'
else
  fail 'work bead is CLOSED' "$(printf '%s' "$bead" | head -1)"
fi
if printf '%s' "$bead" | grep -q 'gc.outcome: pass'; then
  pass 'bead records gc.outcome=pass'
else
  fail 'bead records gc.outcome=pass' 'metadata missing'
fi

# --- no human intervention ------------------------------------------------
# A nudge or a prompt injected by a human would show as a session nudge.
sessions="$(cd "$CITY" && gc session list 2>&1)"
if printf '%s' "$sessions" | awk 'NR>1 && $NF != "-" && $NF != "" {found=1} END {exit !found}'; then
  note 'no manual nudge recorded' 'INFO — review session list below'
else
  pass 'no manual nudge recorded'
fi

echo
echo '--- session list (evidence) ---'
printf '%s\n' "$sessions" | head -8

echo
echo '============================================================'
if [ "$FAILURES" -ne 0 ]; then
  echo "INDEPENDENT ASSURANCE: FAIL ($FAILURES check(s))"
  exit 66
fi
echo 'INDEPENDENT ASSURANCE: PASS'
