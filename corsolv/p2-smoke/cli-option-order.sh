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
# absent and effort is configured away.
#
# ---------------------------------------------------------------------------
# TWO ASSURANCE PROPERTIES THIS SCRIPT MUST HOLD (both were defects once)
# ---------------------------------------------------------------------------
#
# 1. A NEGATIVE PROBE MUST NOT PASS BY FAILING TO RUN.
#    Probe B expects the prompt to be swallowed, observed as "the token never
#    came back". A timeout, an auth failure, a rate limit or any CLI error also
#    produces no token, so absence alone made B pass for the wrong reason. B
#    now additionally requires the CLI to have failed in the SPECIFIC way a
#    swallowed prompt fails -- claude reports it has no prompt at all -- and
#    the self-test below forces a 1s timeout to prove B rejects it.
#
# 2. A POSITIVE PROBE MUST NOT PASS ON ITS OWN ARGV.
#    Probes A and C originally grepped merged stdout+stderr for a literal that
#    was also present in the prompt, i.e. in argv. A usage error that echoes
#    argv would satisfy that search without the model ever running. Fixed two
#    ways at once: stdout and stderr are captured separately and only stdout is
#    searched, AND the success token is COMPUTED BY THE MODEL (a small
#    arithmetic result) so the string being searched for does not appear
#    anywhere in argv. The self-test injects a bogus flag to prove A rejects an
#    argv echo.
#
# Probes, each requiring a successful model turn:
#
#   A  engine form, allowlist trailing        prompt SURVIVES
#   B  variadic form, allowlist trailing      prompt SWALLOWED   (negative control)
#   C  full engine argv incl. --settings      prompt SURVIVES, flags accepted
#   D  comma list splits into real rules      Read allowed under dontAsk
#
# Exit codes: 0 every probe and self-test behaved as specified, 66 otherwise.

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

# The signature a swallowed positional prompt produces: the CLI is left with no
# prompt at all. Anything else -- a timeout, an auth failure -- is an
# invocation fault and must NOT satisfy the negative probe.
SWALLOW_SIG='Input must be provided either through stdin or as a prompt argument'

FAILS=0

# run_probe <slug> -- <argv...>
# Captures stdout, stderr and exit status separately.
#
# The timeout is read from PROBE_TIMEOUT at CALL time, not once at script load.
# Resolving it into a fixed variable up front silently ignored the
# `PROBE_TIMEOUT=1 run_probe ...` prefix the S1 self-test uses to force a kill,
# so S1 ran with the full budget, the model answered normally, and the
# self-test reported "1s was long enough to answer" -- which was this bug, not
# a fast model.
run_probe() {
  local slug="$1"; shift 1
  timeout -s KILL "${PROBE_TIMEOUT:-180}" "$@" </dev/null \
    >"$WORK/$slug.out" 2>"$WORK/$slug.err"
  echo $? > "$WORK/$slug.rc"
}

# expect_prompt_survived <label> <slug> <token>
# A positive probe: the CLI must have SUCCEEDED and the model must have emitted
# the computed token on stdout.
expect_prompt_survived() {
  local label="$1" slug="$2" token="$3"
  local rc; rc="$(cat "$WORK/$slug.rc")"
  printf '%-52s ' "$label"
  if [ "$rc" -ne 0 ]; then
    printf 'XX  invocation failed (exit %s): %s\n' "$rc" "$(head -c 120 "$WORK/$slug.err")"
    FAILS=$((FAILS + 1)); return
  fi
  if grep -qF "$token" "$WORK/$slug.out"; then
    printf 'ok  model returned %s\n' "$token"
  else
    printf 'XX  token %s absent from stdout\n' "$token"
    head -c 200 "$WORK/$slug.out" | sed 's/^/        /'
    FAILS=$((FAILS + 1))
  fi
}

# expect_prompt_swallowed <label> <slug> <token>
# The negative control. Requires BOTH the absence of a model answer AND the
# specific "no prompt supplied" failure, so an invocation fault cannot pass.
expect_prompt_swallowed() {
  local label="$1" slug="$2" token="$3"
  local rc; rc="$(cat "$WORK/$slug.rc")"
  printf '%-52s ' "$label"
  if grep -qF "$token" "$WORK/$slug.out"; then
    printf 'XX  token %s present: the prompt was NOT swallowed\n' "$token"
    FAILS=$((FAILS + 1)); return
  fi
  if [ "$rc" -eq 0 ]; then
    printf 'XX  CLI succeeded; expected it to report a missing prompt\n'
    FAILS=$((FAILS + 1)); return
  fi
  if grep -qF "$SWALLOW_SIG" "$WORK/$slug.err" || grep -qF "$SWALLOW_SIG" "$WORK/$slug.out"; then
    printf 'ok  prompt swallowed (CLI reports no prompt supplied)\n'
  else
    printf 'XX  failed for the WRONG reason (exit %s): %s\n' "$rc" "$(head -c 140 "$WORK/$slug.err")"
    FAILS=$((FAILS + 1))
  fi
}

echo '============================================================'
echo 'CLAUDE CLI OPTION-ORDER CONTRACT — LIVE'
echo '============================================================'
echo "claude: $(claude --version)"
echo "repo:   $REPO"
echo "sha:    $(git -C "$REPO" rev-parse HEAD 2>/dev/null || echo unknown)"
echo "branch: $(git -C "$REPO" rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)"
echo

# The success tokens are COMPUTED by the model, so the string each probe
# searches for is absent from argv and cannot be satisfied by an argv echo.
ASK_A='Compute 6*7 and reply with exactly the word OPTORDER_A_ followed immediately by the result, and nothing else.'
TOK_A='OPTORDER_A_42'
ASK_B='Compute 6*7 and reply with exactly the word OPTORDER_B_ followed immediately by the result, and nothing else.'
TOK_B='OPTORDER_B_42'
ASK_C='Compute 6*7 and reply with exactly the word OPTORDER_C_ followed immediately by the result, and nothing else.'
TOK_C='OPTORDER_C_42'

# ---------------------------------------------------------------------------
# A / B: the encoding is load-bearing.
#
# Identical in every respect except how the allowlist binds its value, and in
# both the allowlist is the LAST option before the positional prompt.
# ---------------------------------------------------------------------------
echo '--- allowlist trailing, prompt appended as a positional ---'

run_probe a claude -p "${COMMON[@]}" --allowedTools="$ALLOWLIST" "$ASK_A"
expect_prompt_survived 'A  engine form  --allowedTools=A,B,C  PROMPT' a "$TOK_A"

run_probe b claude -p "${COMMON[@]}" --allowedTools Read Write Edit Glob Grep "$ASK_B"
expect_prompt_swallowed 'B  variadic     --allowedTools A B C  PROMPT' b "$TOK_B"

# ---------------------------------------------------------------------------
# C: the complete arrangement the engine emits, flags in engine order, with a
# real settings file present.
# ---------------------------------------------------------------------------
echo
echo '--- full engine argv ---'

cat > "$WORK/settings.json" <<'JSON'
{"env":{"CORSOLV_OPTION_ORDER_PROBE":"1"}}
JSON

run_probe c claude -p "${COMMON[@]}" --allowedTools="$ALLOWLIST" \
  --effort low --settings "$WORK/settings.json" "$ASK_C"
expect_prompt_survived 'C  mode/allowedTools=/effort/settings' c "$TOK_C"

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

# The marker is generated here and never appears in argv.
MARKER="OPTORDER_D_$$_$(od -An -N3 -tx1 /dev/urandom | tr -d ' \n')"
echo "$MARKER" > "$WORK/marker.txt"

run_probe d claude -p "${COMMON[@]}" --effort low --add-dir "$WORK" \
  --allowedTools="$ALLOWLIST" \
  "Use the Read tool on $WORK/marker.txt and reply with its exact contents and nothing else."
expect_prompt_survived 'D  Read granted from inside the comma list' d "$MARKER"

# ---------------------------------------------------------------------------
# SELF-TESTS. Each proves a probe rejects the failure mode that once let it
# pass for the wrong reason. Without these, the probes above are assertions
# about assertions.
# ---------------------------------------------------------------------------
echo
echo '--- self-tests: the probes must reject wrong-reason passes ---'

# S1 (guards property 1): an invocation that is KILLED must not be mistaken for
# a swallowed prompt. Both produce "no token came back", which is why absence
# alone was never sufficient evidence.
#
# The shape here is deliberately probe A's -- the `=` form, where the prompt
# genuinely survives -- with a 1-second timeout forcing the kill. Using probe
# B's shape would prove nothing: that form swallows the prompt during argument
# parsing, so the CLI emits the swallow signature immediately and the timeout
# never bites. The first version of this self-test made exactly that mistake
# and reported "timeout produced the swallow signature", which was the
# self-test misfiring rather than the checker failing to discriminate.
PROBE_TIMEOUT=1 run_probe s1 claude -p "${COMMON[@]}" \
  --allowedTools="$ALLOWLIST" "$ASK_A"
printf '%-52s ' 'S1 forced 1s timeout must not satisfy probe B'
s1_rc="$(cat "$WORK/s1.rc")"
if grep -qF "$TOK_A" "$WORK/s1.out"; then
  printf 'XX  1s was long enough to answer; raise the forced timeout margin\n'
  FAILS=$((FAILS + 1))
elif grep -qF "$SWALLOW_SIG" "$WORK/s1.err" || grep -qF "$SWALLOW_SIG" "$WORK/s1.out"; then
  printf 'XX  a killed invocation produced the swallow signature; cannot discriminate\n'
  FAILS=$((FAILS + 1))
elif [ "$s1_rc" -eq 0 ]; then
  printf 'XX  killed invocation reported success (exit 0)\n'
  FAILS=$((FAILS + 1))
else
  printf 'ok  a kill yields no token AND no swallow signature (exit %s)\n' "$s1_rc"
fi

# S2 (guards property 2): give A a bogus flag. The CLI errors and echoes argv;
# because the token is model-computed and only stdout is searched, A's checker
# must not be satisfied.
run_probe s2 claude -p "${COMMON[@]}" --corsolv-not-a-real-flag \
  --allowedTools="$ALLOWLIST" "$ASK_A"
printf '%-52s ' 'S2 bogus flag must not satisfy probe A via argv echo'
s2_rc="$(cat "$WORK/s2.rc")"
if [ "$s2_rc" -eq 0 ]; then
  printf 'XX  CLI accepted a bogus flag (exit 0)\n'
  FAILS=$((FAILS + 1))
elif grep -qF "$TOK_A" "$WORK/s2.out"; then
  printf 'XX  computed token appeared on stdout without a model turn\n'
  FAILS=$((FAILS + 1))
else
  printf 'ok  rejected (exit %s, no computed token on stdout)\n' "$s2_rc"
fi

echo
if [ "$FAILS" -ne 0 ]; then
  echo "CLI OPTION-ORDER CONTRACT: FAIL ($FAILS check(s) behaved unexpectedly)"
  exit 66
fi
echo 'CLI OPTION-ORDER CONTRACT: PASS (4 probes + 2 self-tests)'
