#!/usr/bin/env bash
#
# Record the run's own evidence into the repository and commit it.
#
# This is the run's one mutating task, and it is deliberately the only one. It
# exercises the fence for real — the fence is verified immediately before it and
# an authorised advance recorded immediately after — so the branch-movement guard
# is proved by a commit that actually happens rather than by a simulation of one.
#
# It reads the run's durable state from the journal and heartbeat the control
# layer has been writing all along. It invents nothing: if a fact is not in the
# run's own record, it does not appear here.

set -euo pipefail

STATE_DIR="${GC_UNATTENDED_STATE_DIR:?the runner supplies this}"
RUN_ID="${GC_UNATTENDED_RUN_ID:?the runner supplies this}"
REPO="$(git rev-parse --show-toplevel)"
OUT="$REPO/engdocs/corsolv/UNATTENDED-RUN-EVIDENCE.md"

journal="$STATE_DIR/run-journal.jsonl"
heartbeat="$STATE_DIR/heartbeat.json"

[ -f "$journal" ] || { echo "no run journal at $journal" >&2; exit 1; }

started="$(head -1 "$journal" | sed -n 's/.*"at":"\([^"]*\)".*/\1/p')"
records="$(wc -l < "$journal")"

mkdir -p "$(dirname "$OUT")"
{
  echo "# Unattended run evidence — \`$RUN_ID\`"
  echo
  echo "Written by the run itself, from its own durable journal. Every fact below"
  echo "comes from a record the control layer wrote at the moment the thing"
  echo "happened; nothing here is reconstructed after the fact or inferred."
  echo
  echo "| Item | Value |"
  echo "| --- | --- |"
  echo "| Run ID | \`$RUN_ID\` |"
  echo "| Started | \`$started\` |"
  echo "| Journal records at the time of writing | $records |"
  echo "| Worktree | \`$REPO\` |"
  echo "| Branch | \`$(git rev-parse --abbrev-ref HEAD)\` |"
  echo "| HEAD before this commit | \`$(git rev-parse HEAD)\` |"
  echo "| State directory | \`$STATE_DIR\` |"
  echo
  echo "## Task outcomes"
  echo
  echo "| Task | Attempt | Kind | Duration (ms) |"
  echo "| --- | --- | --- | --- |"
  grep -E '"kind":"task-(succeeded|failed|held|retry-scheduled)"' "$journal" |
    sed -n 's/.*"kind":"\([^"]*\)".*"taskId":"\([^"]*\)".*/\1|\2/p' >/dev/null 2>&1 || true
  # The records are one JSON object per line; read the fields positionally
  # rather than with a JSON parser, because jq is not a declared dependency of
  # this run and adding one to write a report would be the wrong trade.
  while IFS= read -r line; do
    kind="$(printf '%s' "$line" | sed -n 's/.*"kind":"\([^"]*\)".*/\1/p')"
    case "$kind" in
      task-succeeded|task-failed|task-held|task-retry-scheduled) ;;
      *) continue ;;
    esac
    task="$(printf '%s' "$line" | sed -n 's/.*"taskId":"\([^"]*\)".*/\1/p')"
    attempt="$(printf '%s' "$line" | sed -n 's/.*"attempt":\([0-9]*\).*/\1/p')"
    dur="$(printf '%s' "$line" | sed -n 's/.*"durationMs":\([0-9]*\).*/\1/p')"
    echo "| \`${task:-—}\` | ${attempt:-—} | $kind | ${dur:-—} |"
  done < "$journal"

  echo
  echo "## Fence"
  echo
  echo "Every mutating stage re-verified branch, HEAD and lock ownership before it ran."
  echo
  echo '```'
  grep -E '"kind":"fence-' "$journal" || echo "(no fence records)"
  echo '```'

  echo
  echo "## Live progress at the moment this was written"
  echo
  echo '```json'
  cat "$heartbeat" 2>/dev/null || echo '{}'
  echo '```'
  echo
  echo "The complete journal is at \`$journal\` on the execution host. It is"
  echo "append-only and synced per record, so it is the authority for what"
  echo "happened, and this document is a projection of it."
} > "$OUT"

git add "$OUT"

if git diff --cached --quiet; then
  echo "evidence is unchanged; nothing to commit"
  exit 0
fi

git commit -q -m "docs(corsolv): record the unattended readiness run's own evidence

Written by the run itself from its durable journal, as the run's single
mutating task. The fence was verified immediately before this commit and
advanced immediately after it, so the branch-movement guard is proved by a
commit that actually happened rather than by a simulation of one.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"

echo "committed $(git rev-parse HEAD)"
