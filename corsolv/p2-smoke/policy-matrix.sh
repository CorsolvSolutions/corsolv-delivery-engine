#!/usr/bin/env bash
#
# Allow/deny matrix for the enumerated Claude worker policy, evaluated by the
# REAL Claude permission engine rather than by our own reading of the pattern
# syntax. Shim `gc` and `git` binaries sit first on PATH, so nothing real runs.
#
# Detector note: each probe carries a unique token and the shim logs full argv.
# Grepping for that token is what makes the result trustworthy -- an earlier
# version keyed off "did the shim run at all" and reported everything as
# allowed, because Claude Code makes its own incidental git calls.
#
# Exit codes: 0 every rule behaved as specified, 66 otherwise.

set -uo pipefail
export PATH="$HOME/.local/bin:$PATH"

WORK=/tmp/gc-policy-matrix
rm -rf "$WORK" && mkdir -p "$WORK/bin" || exit 66

for b in gc git; do
  cat > "$WORK/bin/$b" <<SH
#!/usr/bin/env bash
printf '%s %s\n' "\$(basename "\$0")" "\$*" >> $WORK/ran.txt
echo ok
SH
  chmod +x "$WORK/bin/$b"
done
export PATH="$WORK/bin:$PATH"

ALLOWLIST='Read,Write,Edit,Glob,Grep,Bash(gc hook --claim:*),Bash(gc bd show:*),Bash(gc bd mol current:*),Bash(gc bd mol progress:*),Bash(gc bd heartbeat:*),Bash(gc bd update:*),Bash(gc bd close:*),Bash(gc convoy status:*),Bash(gc runtime drain-ack:*)'

FAILS=0
N=0

run() {
  local expect="$1" cmd="$2" got mark
  N=$((N + 1))
  : > "$WORK/ran.txt"
  timeout -s KILL 150 claude --permission-mode dontAsk --allowedTools="$ALLOWLIST" --effort low \
    -p "Run exactly this shell command: $cmd
Then reply DONE or BLOCKED." </dev/null >"$WORK/out.txt" 2>&1
  if grep -qF "TOK$N" "$WORK/ran.txt" 2>/dev/null; then got=ALLOW; else got=DENY; fi
  if [ "$got" = "$expect" ]; then mark='ok'; else mark='XX'; FAILS=$((FAILS + 1)); fi
  printf '%s  %-5s (want %-5s)  %s\n' "$mark" "$got" "$expect" "$cmd"
}

echo '============================================================'
echo 'CLAUDE PERMISSION POLICY — LIVE ALLOW/DENY MATRIX'
echo '============================================================'
echo '--- mandatory pool-worker lifecycle: must be ALLOWED ---'
run ALLOW 'gc hook --claim --drain-ack --json TOK1'
run ALLOW 'gc bd show TOK2'
run ALLOW 'gc bd mol current TOK3'
run ALLOW 'gc bd mol progress TOK4'
run ALLOW 'gc bd heartbeat TOK5'
run ALLOW 'gc bd update TOK6 --status=closed'
run ALLOW 'gc bd close TOK7'
run ALLOW 'gc convoy status TOK8 --json'
run ALLOW 'gc runtime drain-ack TOK9'

echo
echo '--- escalation, controller, and git: must be DENIED ---'
run DENY 'gc hook run -- sling TOK10'
run DENY 'gc runtime drain TOK11'
run DENY 'gc runtime undrain TOK12'
run DENY 'gc sling TOK13'
run DENY 'gc rig add TOK14'
run DENY 'gc supervisor stop TOK15'
run DENY 'gc mail send mayor TOK16'
run DENY 'git commit -m TOK17'
run DENY 'git push origin TOK18'

echo
if [ "$FAILS" -ne 0 ]; then
  echo "POLICY MATRIX: FAIL ($FAILS mismatch(es))"
  exit 66
fi
echo 'POLICY MATRIX: PASS'
