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

APPROVED_GRANT='Bash(gc runtime drain-ack:*)'
FAILURES=0

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

  # 3. the scoped drain grant present
  case ",$allowlist," in
    *",$APPROVED_GRANT,"*) pass 'scoped Bash(gc runtime drain-ack:*) present' ;;
    *) fail 'scoped Bash(gc runtime drain-ack:*) present' "allowlist='$allowlist'" ;;
  esac

  # 4. no shell grant other than the approved one
  offenders=''
  IFS=',' read -r -a tools <<< "$allowlist"
  for t in "${tools[@]}"; do
    case "$t" in
      Bash*) [ "$t" = "$APPROVED_GRANT" ] || offenders="$offenders '$t'" ;;
    esac
  done
  if [ -z "$offenders" ]; then
    pass 'no global or broader Bash grant'
  else
    fail 'no global or broader Bash grant' "found:$offenders"
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
