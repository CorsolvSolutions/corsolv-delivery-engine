#!/usr/bin/env bash
#
# Corsolv P2 final gate — Claude startup-command contract.
#
# Twenty checks over the launch path that the P2.1 managed-Claude acceptance
# proof depends on. Every check is deterministic and offline; the live managed
# spawn is proven separately by corsolv/p2-smoke/run-smoke.sh.
#
# Ordering note: checks 1 and 2 keep the positions they had in the previous
# run of this gate (resume command, then Phase 2 startup materialization) so
# failure numbering stays comparable across runs.
#
# Exit codes:
#   0   all 20 checks passed
#   66  a check failed (first failure is reported; the run continues)

set -uo pipefail

export PATH="$HOME/.local/bin:/usr/local/go/bin:$PATH"
export GOTOOLCHAIN=auto

REPO="${REPO:-/mnt/d/Development/corsolv-delivery-engine}"
cd "$REPO" || exit 66

PASSED=0
FAILED=0
FIRST_FAILURE=''

# check <n> <package> <test-regex> <description>
check() {
  local n="$1" pkg="$2" re="$3" desc="$4"
  printf 'TEST %02d/20  %-58s ' "$n" "$desc"
  local out
  if out="$(go test "$pkg" -run "^${re}$" -count=1 2>&1)"; then
    # `go test -run` reports ok even when the regex matched nothing, so
    # require the test to have actually executed.
    local ran
    ran="$(go test "$pkg" -run "^${re}$" -count=1 -v 2>&1 | grep -c '^=== RUN')"
    if [ "$ran" -eq 0 ]; then
      echo 'FAIL (no such test)'
      FAILED=$((FAILED + 1))
      [ -z "$FIRST_FAILURE" ] && FIRST_FAILURE="$n $desc (no such test)"
      return
    fi
    echo 'PASS'
    PASSED=$((PASSED + 1))
  else
    echo 'FAIL'
    printf '%s\n' "$out" | sed 's/^/        /'
    FAILED=$((FAILED + 1))
    [ -z "$FIRST_FAILURE" ] && FIRST_FAILURE="$n $desc"
  fi
}

CFG=./internal/config/
GC=./cmd/gc/
API=./internal/api/

echo '============================================================'
echo 'CORSOLV P2 FINAL GATE — CLAUDE STARTUP CONTRACT'
echo '============================================================'
echo "repo:   $REPO"
echo "branch: $(git rev-parse --abbrev-ref HEAD)"
echo "sha:    $(git rev-parse HEAD)"
echo "claude: $(claude --version 2>/dev/null || echo 'not found')"
echo

# --- resume + startup materialization -------------------------------------
check  1 "$GC"  'TestBuildResumeCommandIncludesSettingsAndDefaultArgs'          'resume command carries settings + defaults'
check  2 "$GC"  'TestPhase2StartupMaterialization'                              'Phase 2 startup materialization (WC-START-001/002)'
check  3 "$GC"  'TestResolvedSessionCommandIncludesDefaultsAndSettings'         'resolved session command: defaults + settings'
check  4 "$GC"  'TestResolvedSessionCommandAppliesOverridesOverDefaults'        'resolved session command: overrides beat defaults'
check  5 "$GC"  'TestResolvedWorkerRuntimeWithConfigUsesProviderLaunchCommand'  'worker runtime uses provider launch command'
check  6 "$GC"  'TestResolvedWorkerRuntimeResumesPoolSessionPreservesLaunchFlags' 'pool resume preserves launch flags'

# --- API resume projection -------------------------------------------------
check  7 "$API" 'TestBuildSessionResumePreservesStoredResolvedCommand'          'API resume preserves stored resolved command'
check  8 "$API" 'TestBuildSessionResumeRebuildsBareStoredCommandForPoolClaudeAgent' 'API resume rebuilds bare pool command'

# --- launch-command construction ------------------------------------------
check  9 "$CFG" 'TestBuildProviderLaunchCommandAddsDefaultsAndSettings'         'launch command: defaults + settings file'
check 10 "$CFG" 'TestBuildProviderLaunchCommandIgnoresInitialMessageOverride'   'launch command: initial_message is not a flag'
check 11 "$CFG" 'TestBuildProviderLaunchCommandAppliesOptionOverrides'          'launch command: explicit option overrides'

# --- provider defaults -----------------------------------------------------
check 12 "$CFG" 'TestResolveDefaultArgs_ClaudeSchema'                           'claude schema resolves default args'
check 13 "$CFG" 'TestBuiltinProviders_ClaudeHasNilArgsAndOptionDefaults'        'claude has nil Args + option defaults'
check 14 "$CFG" 'TestBuiltinProvidersClaude'                                    'builtin claude provider shape'
check 15 "$CFG" 'TestBuiltinClaudeCommandString'                                'claude CommandString + default args'
check 16 "$CFG" 'TestResolveProviderAgentProvider'                              'agent-level provider resolution'

# --- bounded-permission safety control ------------------------------------
check 17 "$CFG" 'TestClaudeBoundedAutoIsTheAutonomousDefault'                   'bounded-auto is the autonomous default, no bypass'
check 18 "$CFG" 'TestClaudeBoundedAutoGrantsOnlyLifecycleTools'                 'allowlist exact: lifecycle grants only, no global Bash'
check 19 "$CFG" 'TestClaudeBoundedAutoSurvivesIntoLaunchCommand'                'bounded mode survives into launch command'
check 20 "$CFG" 'TestClaudeAllowedToolsCannotSwallowPositionalPrompt'           'allowlist cannot swallow positional prompt'

echo
echo '============================================================'
printf 'RESULT: %d/20 passed, %d failed\n' "$PASSED" "$FAILED"
if [ "$FAILED" -ne 0 ]; then
  echo "FIRST FAILURE: $FIRST_FAILURE"
  echo '============================================================'
  exit 66
fi
echo 'FINAL GATE: PASS'
echo '============================================================'
exit 0
