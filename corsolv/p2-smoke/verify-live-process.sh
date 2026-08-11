#!/usr/bin/env bash
#
# Assert the approved permission posture against the ACTUAL running Claude
# worker process, not against config or a rendered template. Reads /proc argv
# so what is checked is what the kernel was handed at exec time.
#
# Usage: verify-live-process.sh [city-path]
# Exit:  0 every assertion held, 66 otherwise, 65 no worker process found.

set -uo pipefail

CITY="${1:-}"
if [ -z "$CITY" ]; then
  CITY="$(ls -d "$HOME"/corsolv-p2/city-* 2>/dev/null | sort | tail -1)"
fi

# The complete approved shell surface: the mandatory pool-worker lifecycle and
# nothing else. Kept literal here so this verifier fails independently of the
# Go constant it is checking.
APPROVED_GRANTS=(
  'Bash(gc hook --claim:*)'
  'Bash(gc bd show:*)'
  'Bash(gc bd mol current:*)'
  'Bash(gc bd mol progress:*)'
  'Bash(gc bd heartbeat:*)'
  'Bash(gc bd update:*)'
  'Bash(gc bd close:*)'
  'Bash(gc convoy status:*)'
  'Bash(gc runtime drain-ack:*)'
)

# MANDATORY_GRANTS is what EVERY managed process must carry. APPROVED_GRANTS is
# the widest set any process may carry. They are separate because the project
# gates are an OPT-IN mode, and conflating the two was a real defect.
#
# The first version simply appended the project gates to APPROVED_GRANTS and
# then required every entry of that array to be present on every process. A
# city runs more than its workers: its own mayor and bd.dog agents run
# bounded-auto and legitimately have no npm grants, so a bounded-project run
# reported them as failures while the two actual workers — both provably
# correct — passed. The verifier was demanding an opt-in mode's grants of
# agents that never opted in.
#
# Splitting the sets keeps every strictness that mattered: the lifecycle grants
# are still mandatory everywhere, no process may carry anything outside the
# approved superset, and without BOUNDED_PROJECT=1 the project gates are not
# approved at all — so a bounded-auto worker that acquired them still fails.
MANDATORY_GRANTS=("${APPROVED_GRANTS[@]}")
PROJECT_GRANTS=(
  'Bash(npm run typecheck:*)'
  'Bash(npm run build:*)'
  'Bash(npm test:*)'
)
if [ "${BOUNDED_PROJECT:-0}" = '1' ]; then
  APPROVED_GRANTS+=("${PROJECT_GRANTS[@]}")
fi
FAILURES=0

is_approved_grant() {
  local candidate="$1" g
  for g in "${APPROVED_GRANTS[@]}"; do
    [ "$candidate" = "$g" ] && return 0
  done
  return 1
}

note() { printf '  %-52s %s\n' "$1" "$2"; }
fail() { note "$1" "FAIL — $2"; FAILURES=$((FAILURES + 1)); }
pass() { note "$1" 'PASS'; }

echo '============================================================'
echo 'LIVE CLAUDE PROCESS — PERMISSION POSTURE'
echo '============================================================'
echo "city: $CITY"
echo

# Collect argv of every live claude process for this city, NUL-delimited so
# arguments containing spaces stay intact.
# Capture each cmdline once, then match against the variable.
#
# The previous form was `tr < cmdline | grep -qF "$CITY"`, and under
# `set -o pipefail` that is actively dangerous HERE of all places: grep -q exits
# at the first match, `tr` takes SIGPIPE, pipefail promotes it, and the `if`
# goes false -- so a process that DOES belong to this city is skipped. A
# security verifier that silently omits workers is worse than one that fails,
# because it still prints PASS.
mapfile -t PIDS < <(
  for p in /proc/[0-9]*; do
    [ -r "$p/cmdline" ] || continue
    cmdline="$(tr '\0' '\n' < "$p/cmdline" 2>/dev/null || true)"
    [ -n "$cmdline" ] || continue
    case "$(head -1 <<<"$cmdline")" in
      *claude) ;;
      *) continue ;;
    esac
    if grep -qF "$CITY" <<<"$cmdline"; then
      basename "$p"
    fi
  done
)

if [ "${#PIDS[@]}" -eq 0 ]; then
  echo 'No live claude worker process found for this city.'
  exit 65
fi

echo "found ${#PIDS[@]} live claude process(es): ${PIDS[*]}"
echo

for pid in "${PIDS[@]}"; do
  mapfile -d '' -t ARGV < "/proc/$pid/cmdline"
  echo "--- pid $pid ---"

  allowlist=''
  perm_mode=''
  bypass=0
  for i in "${!ARGV[@]}"; do
    a="${ARGV[$i]}"
    case "$a" in
      --permission-mode) perm_mode="${ARGV[$((i + 1))]:-}" ;;
      --permission-mode=*) perm_mode="${a#--permission-mode=}" ;;
      --allowedTools=*) allowlist="${a#--allowedTools=}" ;;
      --allowedTools|--allowed-tools)
        fail 'allowlist uses the bound = form' \
             'space-separated --allowedTools can swallow the positional prompt' ;;
      --dangerously-skip-permissions|--allow-dangerously-skip-permissions) bypass=1 ;;
    esac
  done

  # 1. dontAsk present
  if [ "$perm_mode" = 'dontAsk' ]; then
    pass 'permission mode is dontAsk'
  else
    fail 'permission mode is dontAsk' "got '${perm_mode:-<none>}'"
  fi

  # 2. the five edit/inspect tools present
  missing=''
  for t in Read Write Edit Glob Grep; do
    case ",$allowlist," in
      *",$t,"*) ;;
      *) missing="$missing $t" ;;
    esac
  done
  if [ -z "$missing" ]; then
    pass 'Read,Write,Edit,Glob,Grep present'
  else
    fail 'Read,Write,Edit,Glob,Grep present' "missing:$missing"
  fi

  # 3. every mandatory lifecycle grant present. MANDATORY, not APPROVED: the
  #    project gates are opt-in per agent, so requiring them of every managed
  #    process fails the city's own mayor/bd.dog for running the mode they are
  #    supposed to run.
  missing_grants=''
  for g in "${MANDATORY_GRANTS[@]}"; do
    case ",$allowlist," in
      *",$g,"*) ;;
      *) missing_grants="$missing_grants '$g'" ;;
    esac
  done
  if [ -z "$missing_grants" ]; then
    pass 'all mandatory lifecycle grants present'
  else
    fail 'all mandatory lifecycle grants present' "missing:$missing_grants"
  fi

  # 3b. under BOUNDED_PROJECT=1, record which processes carry the opt-in gates.
  #     Counted across the scan so the positive half — that the mode reached a
  #     worker at all — is adjudicated once, after every process is seen.
  if [ "${BOUNDED_PROJECT:-0}" = '1' ]; then
    has_all=1
    for g in "${PROJECT_GRANTS[@]}"; do
      case ",$allowlist," in
        *",$g,"*) ;;
        *) has_all=0 ;;
      esac
    done
    if [ "$has_all" -eq 1 ]; then
      BOUNDED_PROJECT_SEEN=$(( ${BOUNDED_PROJECT_SEEN:-0} + 1 ))
      note 'carries the opt-in project gates' 'yes'
    else
      note 'carries the opt-in project gates' 'no (not an opted-in agent)'
    fi
  fi

  # 4. no shell grant outside the approved set (catches bare Bash, Bash(gc:*),
  #    Bash(gc hook:*), Bash(git:*))
  offenders=''
  IFS=',' read -r -a tools <<< "$allowlist"
  for t in "${tools[@]}"; do
    case "$t" in
      Bash*) is_approved_grant "$t" || offenders="$offenders '$t'" ;;
    esac
  done
  if [ -z "$offenders" ]; then
    pass 'no Bash grant outside the approved lifecycle set'
  else
    fail 'no Bash grant outside the approved lifecycle set' "found:$offenders"
  fi

  # 5. no permission bypass
  if [ "$bypass" -eq 0 ]; then
    pass 'no --dangerously-skip-permissions'
  else
    fail 'no --dangerously-skip-permissions' 'bypass flag present in live argv'
  fi

  echo "  allowlist: $allowlist"
  echo
done

# The positive half of the opt-in, adjudicated once across the whole scan: the
# mode must have reached at least one live process. Without this, a run in
# which bounded-project silently failed to apply would look identical to one in
# which it applied correctly — every process would simply carry the lifecycle
# set and nothing would object.
if [ "${BOUNDED_PROJECT:-0}" = '1' ]; then
  if [ "${BOUNDED_PROJECT_SEEN:-0}" -ge 1 ]; then
    pass "bounded-project reached ${BOUNDED_PROJECT_SEEN} live process(es)"
  else
    fail 'bounded-project reached a live process' \
         'no live process carried the opt-in project gates'
  fi
fi

# Record the verdict so the post-drain independent assurance can defer to this
# verifier instead of re-deriving live posture weakly against a city whose
# workers have already gone. Written only when a verdict was actually reached:
# the no-worker case exits 65 above without producing a record, which is what
# makes a missing record mean "not sequenced correctly" rather than "passed".
record_result() {
  [ -n "${LIVE_PROCESS_RESULT:-}" ] || return 0
  mkdir -p "$(dirname "$LIVE_PROCESS_RESULT")" 2>/dev/null
  printf '%s\n' "$1" > "$LIVE_PROCESS_RESULT"
  printf 'city=%s pids=%s at=%s\n' "$CITY" "${PIDS[*]}" "$(date -u +%FT%TZ)" \
    >> "$LIVE_PROCESS_RESULT"
}

echo '============================================================'
if [ "$FAILURES" -ne 0 ]; then
  record_result "FAIL($FAILURES)"
  echo "LIVE PROCESS POSTURE: FAIL ($FAILURES assertion(s))"
  exit 66
fi
record_result PASS
echo 'LIVE PROCESS POSTURE: PASS'
