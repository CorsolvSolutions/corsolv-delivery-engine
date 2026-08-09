#!/usr/bin/env bash
#
# Independent read-only assurance for a completed P2.1 acceptance run, executed
# BEFORE the controller commits anything.
#
# Deliberately does not trust the run's own report: every claim is re-derived
# from the filesystem, git, the bead store, and the worker's own transcript.
# Read-only throughout -- no writes, no staging, no state mutation.
#
# Usage: verify-independent.sh <rig-path> <city-path> <work-bead-id>
# Exit:  0 all assurance checks passed, 66 otherwise.

set -uo pipefail
export PATH="$HOME/.local/bin:/usr/local/go/bin:$PATH"

RIG="${1:?rig path required}"
CITY="${2:?city path required}"
WORK_ID="${3:?work bead id required}"

MARKER='CORSOLV_GASCITY_MANAGED_CLAUDE_PASS'
ARTIFACT_NAME='CORSOLV_GASCITY_SMOKE.txt'
ARTIFACT="$RIG/$ARTIFACT_NAME"
FAILURES=0

note() { printf '  %-54s %s\n' "$1" "$2"; }
fail() { note "$1" "FAIL — $2"; FAILURES=$((FAILURES + 1)); }
pass() { note "$1" 'PASS'; }
info() { note "$1" "INFO — $2"; }

section() { printf '\n--- %s ---\n' "$1"; }

echo '============================================================'
echo 'INDEPENDENT ASSURANCE (READ-ONLY, PRE-COMMIT)'
echo '============================================================'
echo "rig:  $RIG"
echo "city: $CITY"
echo "work: $WORK_ID"

# --- 1. requested artifact and content ------------------------------------
section '1. requested artifact and content'
if [ -f "$ARTIFACT" ]; then
  pass 'artifact exists'
  actual="$(cat "$ARTIFACT")"
  if [ "$actual" = "$MARKER" ]; then
    pass 'content matches marker exactly'
  else
    fail 'content matches marker exactly' "got '$actual'"
  fi
  if [ "$(wc -l <"$ARTIFACT")" -eq 1 ]; then
    pass 'exactly one line'
  else
    fail 'exactly one line' "$(wc -l <"$ARTIFACT") lines"
  fi
  bytes="$(wc -c <"$ARTIFACT")"
  if [ "$bytes" -eq $(( ${#MARKER} + 1 )) ]; then
    pass "byte length is marker + newline ($bytes)"
  else
    fail 'byte length is marker + newline' "$bytes bytes"
  fi
else
  fail 'artifact exists' "$ARTIFACT not found"
fi

# --- 2. diff scope and unexpected files -----------------------------------
section '2. diff scope / unexpected files'
#
# `gc rig add` writes its own infrastructure into the rig (.beads/ bead store,
# .gc/ runtime state, and a .gitignore covering them) and even makes its own
# "bd init" commit. Those paths are Gas City's, not the worker's, so scope is
# judged on what the WORKER touched: the authority is its transcript, and the
# infrastructure paths are reported for visibility rather than counted against
# it.
INFRA_RE='^(\.beads/|\.gc/|\.gitignore$)'

if git -C "$RIG" rev-parse HEAD >/dev/null 2>&1; then
  changed="$(git -C "$RIG" status --porcelain 2>/dev/null | awk '{print $2}')"
  worker_scope="$(printf '%s\n' "$changed" | grep -Ev "$INFRA_RE" | grep -v '^$' || true)"
  infra_scope="$(printf '%s\n' "$changed" | grep -E "$INFRA_RE" | grep -v '^$' || true)"

  if [ "$worker_scope" = "$ARTIFACT_NAME" ]; then
    pass "worker-owned diff is exactly $ARTIFACT_NAME"
  else
    fail "worker-owned diff is exactly $ARTIFACT_NAME" \
         "got: $(printf '%s' "$worker_scope" | tr '\n' ' ')"
  fi
  [ -n "$infra_scope" ] && info 'Gas City infrastructure paths (not worker-owned)' \
    "$(printf '%s' "$infra_scope" | tr '\n' ' ')"
else
  fail 'rig is a git repo' 'git rev-parse failed'
fi

# Independent attribution: the worker's transcript must show exactly one file
# write, and it must be the artifact. This is what makes the classification
# above evidence rather than an assumption.
PROJ_ATTR="$HOME/.claude/projects/$(printf '%s' "$RIG" | sed 's|/|-|g')"
writes="$(grep -ohE '"file_path":"[^"]*"' "$PROJ_ATTR"/*.jsonl 2>/dev/null | sort -u)"
write_count="$(printf '%s\n' "$writes" | grep -c . || true)"
if [ "$write_count" -eq 1 ] && printf '%s' "$writes" | grep -qF "$ARTIFACT_NAME"; then
  pass 'worker transcript shows exactly one file write, the artifact'
else
  fail 'worker transcript shows exactly one file write, the artifact' \
       "$write_count write(s): $(printf '%s' "$writes" | tr '\n' ' ')"
fi

# --- 3. relevant verification ---------------------------------------------
section '3. relevant verification'
# The bead asked for exact single-line content; re-run that check ourselves
# rather than trusting the worker's recorded verification command.
if [ -f "$ARTIFACT" ] && [ "$(od -An -c "$ARTIFACT" | tr -s ' ' | sed 's/^ //')" = "$(printf '%s\n' "$MARKER" | od -An -c | tr -s ' ' | sed 's/^ //')" ]; then
  pass 'byte-for-byte identical to expected content'
else
  fail 'byte-for-byte identical to expected content' 'od comparison differs'
fi

# --- 4. worker lifecycle completion ---------------------------------------
section '4. worker lifecycle completion'
bead="$(cd "$CITY" && gc bd show "$WORK_ID" 2>&1)"
if printf '%s' "$bead" | grep -q 'CLOSED'; then
  pass 'work bead is CLOSED'
else
  fail 'work bead is CLOSED' "$(printf '%s' "$bead" | head -1)"
fi
if printf '%s' "$bead" | grep -q 'gc.outcome: pass'; then
  pass 'control-plane outcome is pass'
else
  fail 'control-plane outcome is pass' 'metadata missing'
fi
if printf '%s' "$bead" | grep -q 'gc.last_heartbeat_at:'; then
  pass 'worker heartbeated'
else
  fail 'worker heartbeated' 'no gc.last_heartbeat_at'
fi
if printf '%s' "$bead" | grep -q 'gc.work_outcome: shipped'; then
  pass 'work_outcome is shipped'
else
  wo="$(printf '%s' "$bead" | grep -oE 'gc.work_outcome: \S+' | head -1)"
  info 'work_outcome' "${wo:-unset} — no commit SHA because git is withheld from workers by policy; the controller publishes"
fi

# Worker sessions must be gone: drain-ack releases the slot.
sessions="$(cd "$CITY" && gc session list 2>&1)"
if printf '%s' "$sessions" | awk 'NR>1 && $2 ~ /claude/ {found=1} END {exit !found}'; then
  fail 'worker session drained' 'a worker session is still active'
else
  pass 'worker session drained'
fi

# Transcript evidence that the worker drove its own lifecycle.
PROJ="$HOME/.claude/projects/$(printf '%s' "$RIG" | sed 's|/|-|g')"
if [ -d "$PROJ" ]; then
  cmds="$(grep -ohE '"command":"gc [^"]*' "$PROJ"/*.jsonl 2>/dev/null | sed 's/"command":"//')"
  for want in 'gc hook --claim' 'gc bd show' 'gc bd heartbeat' 'gc bd update' 'gc runtime drain-ack'; do
    if printf '%s' "$cmds" | grep -qF "$want"; then
      pass "worker ran: $want"
    else
      fail "worker ran: $want" 'not found in transcript'
    fi
  done
else
  fail 'worker transcript available' "$PROJ not found"
fi

# --- 5. security policy regression ----------------------------------------
section '5. security policy (no regression)'
PROC_OK=0
for p in /proc/[0-9]*; do
  [ -r "$p/cmdline" ] || continue
  exe="$(tr '\0' '\n' < "$p/cmdline" 2>/dev/null | head -1)"
  case "$exe" in *claude) ;; *) continue ;; esac
  tr '\0' '\n' < "$p/cmdline" 2>/dev/null | grep -qF "$CITY" || continue
  PROC_OK=1
  argv="$(tr '\0' ' ' < "$p/cmdline")"
  case "$argv" in
    *--dangerously-skip-permissions*) fail 'no bypass flag in live argv' "pid $(basename "$p")" ;;
    *) pass "no bypass flag in live argv (pid $(basename "$p"))" ;;
  esac
  case "$argv" in
    *'--allowedTools=Read,Write,Edit,Glob,Grep,Bash(gc hook --claim:*)'*) pass 'live allowlist is the approved set' ;;
    *) fail 'live allowlist is the approved set' 'allowlist differs' ;;
  esac
done
[ "$PROC_OK" -eq 0 ] && info 'live process check' 'no claude process left for this city (already drained)'

# The worker must have been refused git; if the transcript shows a successful
# commit, the policy leaked.
if [ -d "$PROJ" ] && git -C "$RIG" log --oneline -n 5 2>/dev/null | grep -qi 'CORSOLV_GASCITY_SMOKE'; then
  fail 'worker did not commit' 'a worker commit is present in git history'
else
  pass 'worker did not commit (git withheld by policy)'
fi

echo
echo '============================================================'
if [ "$FAILURES" -ne 0 ]; then
  echo "INDEPENDENT ASSURANCE: FAIL ($FAILURES check(s))"
  exit 66
fi
echo 'INDEPENDENT ASSURANCE: PASS'
