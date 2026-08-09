#!/usr/bin/env bash
#
# Mutation proof for the Claude autonomous permission policy.
#
# The gate only tells you the current policy passes. This tells you the
# assertions would actually catch a regression: each mutation below must turn
# the suite red. A test that references the production constant instead of a
# literal silently follows a widened grant and passes -- that happened once
# already, which is why this script exists.
#
# Exit codes: 0 all proofs behaved as required, 66 otherwise.

set -uo pipefail
export PATH="$HOME/.local/bin:/usr/local/go/bin:$PATH"

REPO="${REPO:-/mnt/d/Development/corsolv-delivery-engine}"
cd "$REPO" || exit 66

SRC=internal/worker/builtin/profiles.go
BAK="$(mktemp -p /var/tmp)"
TESTS='TestBuiltinClaudeDontAskGrantsOnlySafeTools|TestBuildProviderLaunchCommandClaudeStaysBounded|TestResolveProviderClaudeBoundedPermissionMode|TestClaudeAllowedToolsCannotSwallowPositionalPrompt'

cp "$SRC" "$BAK"
restore() { cp "$BAK" "$SRC"; }
trap 'restore; rm -f "$BAK"' EXIT

BAD=0

mutate_const() {
  python3 - "$SRC" "$1" <<'PY'
import sys
path, new = sys.argv[1], sys.argv[2]
s = open(path).read()
old = 'const ClaudeDontAskAllowedToolsArg = "--allowedTools=Read,Write,Edit,Glob,Grep,Bash(gc runtime drain-ack:*)"'
assert old in s, "anchor not found -- update this script alongside the policy"
open(path, 'w').write(s.replace(old, 'const ClaudeDontAskAllowedToolsArg = "%s"' % new))
PY
}

check() {
  local label="$1" expect="$2" got
  printf '%-62s ' "$label"
  if go test ./internal/config/ ./cmd/gc/ -run "$TESTS" -count=1 >/tmp/policy-mut.out 2>&1; then
    got=PASS
  else
    got=FAIL
  fi
  if [ "$got" = "$expect" ]; then
    echo "$got (as required)"
  else
    echo "$got  *** EXPECTED $expect ***"
    head -20 /tmp/policy-mut.out
    BAD=$((BAD + 1))
  fi
  restore
}

echo '============================================================'
echo 'CLAUDE PERMISSION POLICY — MUTATION PROOF'
echo '============================================================'

check 'baseline: approved policy' PASS

mutate_const '--allowedTools=Read,Write,Edit,Glob,Grep,Bash(gc runtime drain-ack:*),Bash'
check 'adding global Bash' FAIL

mutate_const '--allowedTools=Read,Write,Edit,Glob,Grep,Bash(gc:*)'
check 'broadening to Bash(gc:*)' FAIL

mutate_const '--allowedTools=Read,Write,Edit,Glob,Grep'
check 'removing the scoped drain grant' FAIL

mutate_const '--allowedTools=Read,Write,Edit,Glob,Grep,Bash(git:*)'
check 'granting Bash(git:*)' FAIL

python3 - "$SRC" <<'PY'
import sys
path = sys.argv[1]
s = open(path).read()
old = '{Value: "dontAsk", Label: "Don\'t ask", FlagArgs: []string{"--permission-mode", "dontAsk", ClaudeDontAskAllowedToolsArg}},'
new = '{Value: "dontAsk", Label: "Don\'t ask", FlagArgs: []string{"--permission-mode", "dontAsk", "--allowedTools", "Read", "Write", "Edit", "Glob", "Grep", "Bash(gc runtime drain-ack:*)"}},'
assert old in s, "FlagArgs anchor not found -- update this script alongside the policy"
open(path, 'w').write(s.replace(old, new))
PY
check 'reverting to the unsafe variadic encoding' FAIL

check 'restored: approved policy' PASS

echo
if [ "$BAD" -ne 0 ]; then
  echo "MUTATION PROOF: FAILED ($BAD mutation(s) behaved unexpectedly)"
  exit 66
fi
echo 'MUTATION PROOF: PASS'
