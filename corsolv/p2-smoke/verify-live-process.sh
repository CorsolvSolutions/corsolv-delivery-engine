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
mapfile -t PIDS < <(
  for p in /proc/[0-9]*; do
    [ -r "$p/cmdline" ] || continue
    exe="$(tr '\0' '\n' < "$p/cmdline" 2>/dev/null | head -1)"
    case "$exe" in
      *claude) ;;
      *) continue ;;
    esac
    if tr '\0' '\n' < "$p/cmdline" 2>/dev/null | grep -qF "$CITY"; then
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

  # 3. every mandatory lifecycle grant present
  missing_grants=''
  for g in "${APPROVED_GRANTS[@]}"; do
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

echo '============================================================'
if [ "$FAILURES" -ne 0 ]; then
  echo "LIVE PROCESS POSTURE: FAIL ($FAILURES assertion(s))"
  exit 66
fi
echo 'LIVE PROCESS POSTURE: PASS'
