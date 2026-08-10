#!/usr/bin/env bash
#
# Live proof that the argv arrangement the Delivery Engine emits is ACCEPTED
# and INTERPRETED AS INTENDED by the real Claude CLI.
#
# The startup-contract gate (final-gate.sh) is entirely offline: it proves our
# builder emits `--allowedTools=A,B,C` as one token rather than
# `--allowedTools A B C`. That is a claim ABOUT the CLI, and until this script
# existed it was asserted only in a Go comment. The hazard it guards is real
# and documented by the CLI itself:
#
#   --allowedTools, --allowed-tools <tools...>
#       Comma or space-separated list of tool names to allow
#   Usage: claude [options] [command] [prompt]
#
# `<tools...>` is variadic and `prompt` is a bare positional, so a trailing
# space-separated allowlist eats the prompt. Claude's provider spec uses
# prompt_mode = "arg", and the allowlist IS last whenever the settings file is
# absent and effort is configured away -- exactly the case
# TestClaudeAllowedToolsCannotSwallowPositionalPrompt pins.
#
# Four probes, each keyed on a unique token echoed by the model, so a probe
# cannot pass on an empty or unrelated response:
#
#   A  engine form, allowlist trailing        prompt SURVIVES
#   B  variadic form, allowlist trailing      prompt SWALLOWED   (negative control)
#   C  full engine argv incl. --settings      prompt SURVIVES, flags accepted
#   D  comma list splits into real rules      Read allowed under dontAsk
#
# B is what makes A meaningful. Without it, A only shows the CLI tolerating our
# flags; with it, A shows the encoding is load-bearing.
#
# Exit codes: 0 every probe behaved as specified, 66 otherwise.

set -uo pipefail
export PATH="$HOME/.local/bin:$PATH"

WORK=/tmp/gc-cli-option-order
rm -rf "$WORK" && mkdir -p "$WORK" || exit 66

# The exact policy the engine ships. Kept as a literal, not sourced from Go:
# this script must fail loudly if the two drift, and reading the constant back
# would make it follow a widened grant silently.
ALLOWLIST='Read,Write,Edit,Glob,Grep,Bash(gc hook --claim:*),Bash(gc bd show:*),Bash(gc bd mol current:*),Bash(gc bd mol progress:*),Bash(gc bd heartbeat:*),Bash(gc bd update:*),Bash(gc bd close:*),Bash(gc convoy status:*),Bash(gc runtime drain-ack:*)'

REPO="${REPO:-/mnt/d/Development/corsolv-delivery-engine}"
CONST_FILE="$REPO/internal/worker/builtin/profiles.go"

# Drift guard: every rule this script probes must still be in the shipped
# constant. A rule that left the policy must not keep passing here.
missing=''
IFS=',' read -ra RULES <<< "$ALLOWLIST"
for r in "${RULES[@]}"; do
  grep -qF "$r" "$CONST_FILE" || missing="$missing $r"
done
if [ -n "$missing" ]; then
  echo "FAIL: probed rules absent from $CONST_FILE:$missing"
  exit 66
fi

# `--strict-mcp-config` with no `--mcp-config` loads zero MCP servers, so a
# project-level MCP entry cannot inject a setup prompt into a probe and stall
# it against the timeout.
COMMON=(--strict-mcp-config --permission-mode dontAsk)

FAILS=0

# probe <label> <token> <expect present|absent> -- <argv...>
probe() {
  local label="$1" token="$2" expect="$3"; shift 4
  local got
  printf '%-52s ' "$label"
  timeout -s KILL 180 "$@" </dev/null >"$WORK/$token.out" 2>&1
  if grep -qF "$token" "$WORK/$token.out"; then got=present; else got=absent; fi
  if [ "$got" = "$expect" ]; then
    printf 'token %-7s (want %-7s)  ok\n' "$got" "$expect"
  else
    printf 'token %-7s (want %-7s)  XX\n' "$got" "$expect"
    sed 's/^/        /' "$WORK/$token.out" | head -12
    FAILS=$((FAILS + 1))
  fi
}

echo '============================================================'
echo 'CLAUDE CLI OPTION-ORDER CONTRACT — LIVE'
echo '============================================================'
echo "claude: $(claude --version)"
echo

# ---------------------------------------------------------------------------
# A / B: the encoding is load-bearing.
#
# Identical in every respect except how the allowlist binds its value, and in
# both the allowlist is the LAST option before the positional prompt.
# ---------------------------------------------------------------------------
echo '--- allowlist trailing, prompt appended as a positional ---'

probe 'A  engine form  --allowedTools=A,B,C  PROMPT' OPTORDER_A present -- \
  claude -p "${COMMON[@]}" --allowedTools="$ALLOWLIST" \
  'Reply with exactly this word and nothing else: OPTORDER_A'

probe 'B  variadic     --allowedTools A B C  PROMPT' OPTORDER_B absent -- \
  claude -p "${COMMON[@]}" --allowedTools Read Write Edit Glob Grep \
  'Reply with exactly this word and nothing else: OPTORDER_B'

# ---------------------------------------------------------------------------
# C: the complete arrangement the engine emits, flags in engine order, with a
# real settings file present. Proves nothing in the combination is rejected
# and the prompt still lands.
# ---------------------------------------------------------------------------
echo
echo '--- full engine argv ---'

cat > "$WORK/settings.json" <<'JSON'
{"env":{"CORSOLV_OPTION_ORDER_PROBE":"1"}}
JSON

probe 'C  --permission-mode/--allowedTools=/--effort/--settings' OPTORDER_C present -- \
  claude -p "${COMMON[@]}" --allowedTools="$ALLOWLIST" \
  --effort low --settings "$WORK/settings.json" \
  'Reply with exactly this word and nothing else: OPTORDER_C'

# ---------------------------------------------------------------------------
# D: the comma-joined value is split into individual rules rather than taken as
# one opaque tool name. Under dontAsk nothing is permitted that the allowlist
# does not name, so a successful Read of a file only this probe knows about is
# the discriminator: if the value were one literal, `Read` would be denied.
#
# `--add-dir` is placed BEFORE the allowlist deliberately. It is variadic too
# (`--add-dir <directories...>`), so trailing it would swallow the prompt and
# this probe would fail for the wrong reason -- which is exactly what the first
# run of this script did. The engine emits no `--add-dir`, so that is a
# property of this probe rather than of the product, but it is the same hazard
# class the `=` encoding exists to defeat, found a second time by accident.
# ---------------------------------------------------------------------------
echo
echo '--- comma-joined value splits into real rules ---'

echo 'OPTORDER_D' > "$WORK/marker.txt"

probe 'D  Read granted from inside the comma list' OPTORDER_D present -- \
  claude -p "${COMMON[@]}" --effort low --add-dir "$WORK" \
  --allowedTools="$ALLOWLIST" \
  "Use the Read tool on $WORK/marker.txt and reply with its exact contents and nothing else."

echo
if [ "$FAILS" -ne 0 ]; then
  echo "CLI OPTION-ORDER CONTRACT: FAIL ($FAILS probe(s) behaved unexpectedly)"
  exit 66
fi
echo 'CLI OPTION-ORDER CONTRACT: PASS'
