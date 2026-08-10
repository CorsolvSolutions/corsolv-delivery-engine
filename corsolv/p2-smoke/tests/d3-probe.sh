#!/usr/bin/env bash
#
# D3 probe — the autonomous-continuation mechanism, in miniature, on a live city.
#
# The S-A run stakes 25+ minutes and three live workers on one unproven claim:
# that a bead routed while BLOCKED is accepted by the router, withheld from
# demand until its blocker closes, and then claimed and started by normal
# controller demand with no further instruction. Every part of that is cheap to
# test on two throwaway beads and expensive to discover late.
#
# It runs against an EXISTING city and rig (the preflight's, typically) so it
# costs one short worker rather than a whole bootstrap.
#
# Usage: d3-probe.sh <city> <rig-name> <agent> [rig-path]
# Exit:  0 the mechanism holds, 70 otherwise.

set -uo pipefail
export PATH="$HOME/.local/bin:/usr/local/go/bin:$PATH"
export GC_HOME="${GC_HOME:-$HOME/.gc-corsolv-p2}"
export GC_WORK_RECORD_ENFORCE=1

SOURCE_REPO="${SOURCE_REPO:-/mnt/d/Development/corsolv-delivery-engine}"
# shellcheck source=../lib/sa-lib.sh
. "$SOURCE_REPO/corsolv/p2-smoke/lib/sa-lib.sh"

CITY="${1:?city path required}"
RIG_NAME="${2:?rig name required}"
AGENT="${3:?agent name required}"
SA_CITY="$CITY"
SA_RIG="$RIG_NAME"

BLOCKED_WATCH="${BLOCKED_WATCH:-120}"
RELEASE_DEADLINE="${RELEASE_DEADLINE:-600}"

FAILURES=0
note() { printf '  %-62s %s\n' "$1" "$2"; }
pass() { note "$1" 'PASS'; }
fail() { note "$1" "FAIL — $2"; FAILURES=$((FAILURES + 1)); }
info() { note "$1" "INFO — $2"; }

echo '============================================================'
echo 'D3 PROBE — ROUTE-WHILE-BLOCKED, AUTONOMOUS RELEASE'
echo '============================================================'
echo "city:  $CITY"
echo "agent: $RIG_NAME/$AGENT"

LEDGER="$(mktemp)"
sa_ledger_init "$LEDGER"

X="$(sa_gc bd q 'D3 probe blocker: the controller closes this by hand' 2>&1 | tail -1)"
Y="$(sa_gc bd q 'D3 probe dependent: create D3_OK.md in the repository root containing exactly this single line: D3_OK. Then close the assigned bead with gc.work_outcome=blocked plus a gc.work_blocked_reason; you cannot run git.' 2>&1 | tail -1)"
if [ -z "$X" ] || [ -z "$Y" ]; then
  fail 'probe beads created' "X='$X' Y='$Y'"; exit 70
fi
pass "probe beads created (blocker=$X dependent=$Y)"

sa_gc bd dep "$X" --blocks "$Y" >/dev/null 2>&1
sa_gc bd ready > "$LEDGER.ready" 2>&1 || true
if sa_bead_in_ready "$Y" "$LEDGER.ready"; then
  fail 'dependent is withheld from ready work' 'it is already ready'
else
  pass 'dependent is withheld from ready work'
fi

# 1. Can a BLOCKED bead be routed at all?
out="$(gcx sling "$RIG_NAME/$AGENT" "$Y" --no-formula --no-convoy 2>&1)"; rc=$?
shown="$(sa_gc bd show "$Y" 2>/dev/null || true)"
if [ "$rc" -eq 0 ] && grep -qE 'gc.routed_to: \S+' <<<"$shown"; then
  pass "a blocked bead accepts routing (gc.routed_to set, exit $rc)"
else
  fail 'a blocked bead accepts routing' "exit $rc: $(printf '%s' "$out" | tail -2 | tr '\n' ' ')"
  exit 70
fi
sa_gc bd ready > "$LEDGER.ready" 2>&1 || true
if sa_bead_in_ready "$Y" "$LEDGER.ready"; then
  fail 'routing does not make a blocked bead ready' 'it became ready'
else
  pass 'routing does not make a blocked bead ready'
fi

# 2. Does routed-but-blocked produce spawn demand? It must not.
info 'watching for a premature worker' "${BLOCKED_WATCH}s"
premature=''
deadline=$(( $(date +%s) + BLOCKED_WATCH ))
while [ "$(date +%s)" -lt "$deadline" ]; do
  p="$(sa_worker_pids "$CITY" "*$AGENT*")"
  [ -n "$p" ] && { premature="$p"; break; }
  sleep 5
done
if [ -z "$premature" ]; then
  pass "no worker spawned while the bead was blocked (${BLOCKED_WATCH}s)"
else
  fail 'no worker spawned while the bead was blocked' "pids:$premature"
fi

# 3. Close the blocker. From here the probe issues NO command naming the
#    dependent — release, claim and start must all be the engine's.
RELEASE_EPOCH="$(date +%s)"
sa_gc bd update "$X" --set-metadata 'gc.work_outcome=no-op' >/dev/null 2>&1
sa_gc bd close "$X" --reason 'probe blocker cleared by the controller' >/dev/null 2>&1
if bead_is_closed "$X"; then
  pass 'blocker closed by the controller'
else
  fail 'blocker closed by the controller' 'still open'; exit 70
fi

started=''; ready_at=''; closed=0
deadline=$(( $(date +%s) + RELEASE_DEADLINE ))
while true; do
  if [ -z "$ready_at" ]; then
    sa_gc bd ready > "$LEDGER.ready" 2>&1 || true
    sa_bead_in_ready "$Y" "$LEDGER.ready" && ready_at="$(date -u +%FT%TZ)"
  fi
  if [ -z "$started" ]; then
    p="$(sa_worker_pids "$CITY" "*$AGENT*")"
    [ -n "$p" ] && started="$(date -u +%FT%TZ) (pids:$p)"
  fi
  if bead_is_closed "$Y"; then closed=1; break; fi
  [ "$(date +%s)" -ge "$deadline" ] && break
  sleep 5
done

[ -n "$ready_at" ] && pass "readiness projection exposed the dependent automatically ($ready_at)" \
  || fail 'readiness projection exposed the dependent automatically' 'never became ready'
[ -n "$started" ] && pass "normal demand started a worker with no operator command ($started)" \
  || fail 'normal demand started a worker with no operator command' 'no worker ever appeared'
[ "$closed" -eq 1 ] && pass 'dependent closed autonomously' \
  || fail 'dependent closed autonomously' 'deadline reached'

directives="$(sa_ledger_directives_after "$RELEASE_EPOCH" "$Y")"
if [ -z "$directives" ]; then
  pass 'zero post-release directives naming the dependent'
else
  fail 'zero post-release directives naming the dependent' "$(printf '%s' "$directives" | tr '\n' ';')"
fi

rm -f "$LEDGER" "$LEDGER.ready"
echo
echo '============================================================'
if [ "$FAILURES" -ne 0 ]; then
  echo "D3 PROBE: FAIL ($FAILURES check(s))"; exit 70
fi
echo 'D3 PROBE: PASS'
