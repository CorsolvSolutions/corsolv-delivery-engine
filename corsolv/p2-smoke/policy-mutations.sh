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
TESTS='TestClaudeBoundedAutoGrantsOnlyLifecycleTools|TestClaudeBoundedAutoSurvivesIntoLaunchCommand|TestClaudeBoundedAutoIsTheAutonomousDefault|TestClaudeAllowedToolsCannotSwallowPositionalPrompt|TestClaudeBoundedProjectIsOptInNotTheDefault|TestClaudeBoundedProjectGrantsExactSurface|TestClaudeBoundedProjectPreservesDontAskAndSingleSelection|TestClaudeBoundedProjectDeniesPublicationAndInstallAuthority|TestClaudeBoundedProjectAllowsTheAuthorisedProjectGates|TestClaudeBoundedProjectAllowlistCannotSwallowPositionalPrompt'

cp "$SRC" "$BAK"
restore() { cp "$BAK" "$SRC"; }
trap 'restore; rm -f "$BAK"' EXIT

BAD=0

# The policy constant is a multi-line concatenation; replace the whole
# declaration with a single-line literal so mutations are exact.
mutate_const() {
  python3 - "$SRC" "$1" <<'PY'
import re, sys
path, new = sys.argv[1], sys.argv[2]
s = open(path).read()
pat = re.compile(
    r'const ClaudeBoundedAutoAllowedToolsArg = "--allowedTools=.*?"(?:\s*\+\s*\n\s*"[^"]*")*',
    re.S)
m = pat.search(s)
assert m, "policy constant not found -- update this script alongside the policy"
s = s[:m.start()] + 'const ClaudeBoundedAutoAllowedToolsArg = "%s"' % new + s[m.end():]
open(path, 'w').write(s)
PY
}

# The approved policy as one line, so mutations can be expressed as deltas.
APPROVED='--allowedTools=Read,Write,Edit,Glob,Grep,Bash(gc hook --claim:*),Bash(gc bd show:*),Bash(gc bd mol current:*),Bash(gc bd mol progress:*),Bash(gc bd heartbeat:*),Bash(gc bd update:*),Bash(gc bd close:*),Bash(gc convoy status:*),Bash(gc runtime drain-ack:*)'

# drop_grant <rule> — remove one mandatory lifecycle rule.
drop_grant() {
  mutate_const "$(printf '%s' "$APPROVED" | sed "s|,$1||")"
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

echo
echo '-- widening the shell surface --'

mutate_const "$APPROVED,Bash"
check 'adding global Bash' FAIL

mutate_const '--allowedTools=Read,Write,Edit,Glob,Grep,Bash(gc:*)'
check 'broadening to Bash(gc:*)' FAIL

mutate_const "$APPROVED,Bash(git:*)"
check 'adding Bash(git:*)' FAIL

# gc hook run -- <gc args...> re-executes the binary with arbitrary arguments,
# so the unscoped hook family is Bash(gc:*) by another name.
mutate_const "$(printf '%s' "$APPROVED" | sed 's|Bash(gc hook --claim:\*)|Bash(gc hook:*)|')"
check 'broadening gc hook to the whole family' FAIL

# gc runtime also carries controller-side drain / undrain.
mutate_const "$(printf '%s' "$APPROVED" | sed 's|Bash(gc runtime drain-ack:\*)|Bash(gc runtime:*)|')"
check 'broadening gc runtime to the whole family' FAIL

echo
echo '-- removing any mandatory lifecycle permission --'

for rule in \
  'Bash(gc hook --claim:\*)' \
  'Bash(gc bd show:\*)' \
  'Bash(gc bd mol current:\*)' \
  'Bash(gc bd mol progress:\*)' \
  'Bash(gc bd heartbeat:\*)' \
  'Bash(gc bd update:\*)' \
  'Bash(gc bd close:\*)' \
  'Bash(gc convoy status:\*)' \
  'Bash(gc runtime drain-ack:\*)'
do
  drop_grant "$rule"
  check "removing $(printf '%s' "$rule" | tr -d '\\')" FAIL
done

echo
echo '-- unsafe encodings and bypass --'

python3 - "$SRC" <<'PY'
import sys
path = sys.argv[1]
s = open(path).read()
old = '{Value: "bounded-auto", Label: "Bounded autonomous", FlagArgs: []string{"--permission-mode", "dontAsk", ClaudeBoundedAutoAllowedToolsArg}},'
new = '{Value: "bounded-auto", Label: "Bounded autonomous", FlagArgs: []string{"--permission-mode", "dontAsk", "--allowedTools", "Read", "Write", "Edit", "Glob", "Grep"}},'
assert old in s, "FlagArgs anchor not found -- update this script alongside the policy"
open(path, 'w').write(s.replace(old, new))
PY
check 'reverting to the unsafe variadic encoding' FAIL

# Reinstating unrestricted as the autonomous default puts
# --dangerously-skip-permissions back on every launch.
python3 - "$SRC" <<'PY'
import sys
path = sys.argv[1]
s = open(path).read()
old = '"permission_mode": "bounded-auto",'
assert old in s, "OptionDefaults anchor not found -- update this script alongside the policy"
open(path, 'w').write(s.replace(old, '"permission_mode": "unrestricted",', 1))
PY
check 'reinstating --dangerously-skip-permissions as the default' FAIL

echo
echo '-- bounded-project: the opt-in project gates --'
#
# bounded-project is bounded-auto plus three named project scripts. Its two
# dangers are opposite: widening the project half into a command family (which
# quietly restores install/publish/arbitrary-script execution), and making it
# the default (which would hand project commands to every autonomous worker in
# every city without anyone choosing it). Both must turn the suite red.

# The project half as one line, so mutations are expressible as deltas.
PROJECT_APPROVED='Bash(npm run typecheck:*),Bash(npm run build:*),Bash(npm test:*)'

mutate_project_const() {
  python3 - "$SRC" "$1" <<'PY'
import re, sys
path, new = sys.argv[1], sys.argv[2]
s = open(path).read()
pat = re.compile(
    r'const ClaudeProjectValidationGrants = "[^"]*"(?:\s*\+\s*\n\s*"[^"]*")*',
    re.S)
m = pat.search(s)
assert m, "project grant constant not found -- update this script alongside the policy"
s = s[:m.start()] + 'const ClaudeProjectValidationGrants = "%s"' % new + s[m.end():]
open(path, 'w').write(s)
PY
}

drop_project_grant() {
  mutate_project_const "$(printf '%s' "$PROJECT_APPROVED" | sed "s|$1,\{0,1\}||; s|,$||")"
}

mutate_project_const "$PROJECT_APPROVED"
check 'baseline: approved project gates' PASS

# `npm run` is a general execution surface: the family grants every script in
# package.json, including one the worker just added.
mutate_project_const 'Bash(npm run:*),Bash(npm test:*)'
check 'broadening to Bash(npm run:*)' FAIL

# Bash(npm:*) carries npm install and npm publish -- dependency mutation and
# package publication, neither of which is validation.
mutate_project_const 'Bash(npm:*)'
check 'broadening to Bash(npm:*)' FAIL

mutate_project_const "$PROJECT_APPROVED,Bash(npm install:*)"
check 'adding npm install' FAIL

mutate_project_const "$PROJECT_APPROVED,Bash(npm publish:*)"
check 'adding npm publish' FAIL

# npx fetches and executes arbitrary packages.
mutate_project_const "$PROJECT_APPROVED,Bash(npx:*)"
check 'adding npx' FAIL

# Publication authority must stay with the controller.
mutate_project_const "$PROJECT_APPROVED,Bash(gh:*)"
check 'adding gh (PR/merge authority)' FAIL

mutate_project_const "$PROJECT_APPROVED,Bash(git:*)"
check 'adding git to the project half' FAIL

echo
echo '-- removing any authorised project gate --'
for rule in \
  'Bash(npm run typecheck:\*)' \
  'Bash(npm run build:\*)' \
  'Bash(npm test:\*)'
do
  drop_project_grant "$rule"
  check "removing $(printf '%s' "$rule" | tr -d '\\')" FAIL
done

echo
echo '-- bounded-project must stay opt-in --'
# Making it the default is the highest-blast-radius regression available here:
# it grants project commands to every autonomous claude worker in every city.
python3 - "$SRC" <<'PY'
import sys
path = sys.argv[1]
s = open(path).read()
old = '"permission_mode": "bounded-auto",'
assert old in s, "OptionDefaults anchor not found -- update this script alongside the policy"
open(path, 'w').write(s.replace(old, '"permission_mode": "bounded-project",', 1))
PY
check 'making bounded-project the autonomous default' FAIL

check 'restored: approved policy' PASS

echo
if [ "$BAD" -ne 0 ]; then
  echo "MUTATION PROOF: FAILED ($BAD mutation(s) behaved unexpectedly)"
  exit 66
fi
echo 'MUTATION PROOF: PASS'
