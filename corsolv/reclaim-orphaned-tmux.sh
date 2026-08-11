#!/usr/bin/env bash
#
# Inventory (and optionally reclaim) tmux servers that are provably orphaned
# test debris.
#
# Dry run is the default and the only thing that happens without --reclaim.
#
# A server qualifies ONLY if every one of these holds:
#
#   1. it is a tmux server process;
#   2. its TMUX_TMPDIR was recorded in its own environment, is non-empty, and
#      that directory no longer exists on disk;
#   3. it cannot list its own sessions — nothing can attach to it, ever;
#   4. its socket name is not "default";
#   5. it is not the server backing the caller's own $TMUX;
#   6. it is older than --min-age-hours (default 1).
#
# Failing to PROVE any condition keeps the process. Unknown means keep.
#
# There is deliberately no matching on process name, socket name pattern, or
# user. `pkill tmux`-shaped cleanup is what this script exists to avoid: it
# cannot distinguish an operator's live session from test debris, and this
# machine has both.
set -uo pipefail

DRY_RUN=1
MIN_AGE_HOURS=1
while [ $# -gt 0 ]; do
  case "$1" in
    --reclaim)         DRY_RUN=0 ;;
    --dry-run)         DRY_RUN=1 ;;
    --min-age-hours)   MIN_AGE_HOURS="${2:-1}"; shift ;;
    -h|--help)
      sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
  shift
done

# The socket behind the caller's own tmux, if they are inside one. Never a
# candidate, whatever else is true of it.
SELF_SOCKET=""
if [ -n "${TMUX:-}" ]; then
  SELF_SOCKET="${TMUX%%,*}"
fi

now_s=$(cut -d. -f1 /proc/uptime)
min_age_s=$(( MIN_AGE_HOURS * 3600 ))
clk=$(getconf CLK_TCK)

candidates=()
kept=0
total=0

for pid in $(pgrep -f 'tmux ' 2>/dev/null); do
  [ -d "/proc/$pid" ] || continue
  comm=$(cat "/proc/$pid/comm" 2>/dev/null || echo "")
  case "$comm" in
    tmux*) ;;
    *) continue ;;
  esac
  total=$((total + 1))

  cmdline=$(tr '\0' ' ' < "/proc/$pid/cmdline" 2>/dev/null)
  socket=$(printf '%s' "$cmdline" | grep -oP '(?<=-L )[^ ]+' | head -1)
  [ -n "$socket" ] || socket="default"

  # (4) the default socket is an operator's, not test debris.
  if [ "$socket" = "default" ]; then kept=$((kept + 1)); continue; fi

  # (2) TMUX_TMPDIR must be recorded AND gone.
  tmpdir=$(tr '\0' '\n' < "/proc/$pid/environ" 2>/dev/null | grep -m1 '^TMUX_TMPDIR=' | cut -d= -f2-)
  if [ -z "$tmpdir" ]; then kept=$((kept + 1)); continue; fi
  if [ -d "$tmpdir" ]; then kept=$((kept + 1)); continue; fi

  # (3) it must be unreachable: if it can still list sessions, someone owns it.
  if TMUX_TMPDIR="$tmpdir" timeout 3 tmux -L "$socket" list-sessions >/dev/null 2>&1; then
    kept=$((kept + 1)); continue
  fi

  # (5) never the caller's own server.
  if [ -n "$SELF_SOCKET" ] && printf '%s' "$SELF_SOCKET" | grep -q "$socket"; then
    kept=$((kept + 1)); continue
  fi

  # (6) age guard, so a run starting up right now is never a candidate.
  start_ticks=$(awk '{print $22}' "/proc/$pid/stat" 2>/dev/null)
  [ -n "$start_ticks" ] || { kept=$((kept + 1)); continue; }
  age_s=$(( now_s - start_ticks / clk ))
  if [ "$age_s" -lt "$min_age_s" ]; then kept=$((kept + 1)); continue; fi

  candidates+=("$pid|$socket|$tmpdir|$((age_s / 3600))h")
done

echo "tmux servers examined : $total"
echo "preserved             : $kept"
echo "provably orphaned     : ${#candidates[@]}"
echo
if [ ${#candidates[@]} -eq 0 ]; then
  echo "Nothing to reclaim."
  exit 0
fi

printf '%-10s %-22s %-8s %s\n' PID SOCKET AGE "DELETED TMUX_TMPDIR"
for c in "${candidates[@]}"; do
  IFS='|' read -r pid socket tmpdir age <<< "$c"
  printf '%-10s %-22s %-8s %s\n' "$pid" "$socket" "$age" "$tmpdir"
done
echo

if [ "$DRY_RUN" -eq 1 ]; then
  echo "DRY RUN. Nothing was terminated."
  echo "To reclaim exactly these processes:  $0 --reclaim"
  exit 0
fi

reclaimed=0
for c in "${candidates[@]}"; do
  IFS='|' read -r pid socket tmpdir age <<< "$c"
  # Re-prove liveness and the deleted-tmpdir condition immediately before
  # signalling: the inventory above may be seconds old, and PIDs are reused.
  [ -d "/proc/$pid" ] || continue
  [ -d "$tmpdir" ] && continue
  if kill -TERM "$pid" 2>/dev/null; then
    reclaimed=$((reclaimed + 1))
  fi
done
echo "reclaimed: $reclaimed"
