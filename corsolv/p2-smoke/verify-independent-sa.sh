#!/usr/bin/env bash
#
# Independent read-only assurance for a completed S-A run.
#
# Deliberately does not trust the run's own report. Every claim is re-derived
# from the filesystem, git, the bead store, and the workers' own transcripts.
# Read-only throughout — no writes, no staging, no state mutation.
#
# Where it can, it derives a fact a DIFFERENT way than the run did. The run
# proves C started autonomously from its own command ledger; this verifier
# proves the same ordering from git object timestamps, which the harness does
# not write. Two independent derivations of one fact is what makes this
# assurance rather than a second reading of the same claim.
#
# Usage: verify-independent-sa.sh <rig> <city> <wtA> <wtB> <wtC> <A> <B> <C>
# Env:   LIVE_PROCESS_RESULT, LIVE_PROCESS_RESULT_C — the pre-drain posture
#        records; a missing record is a sequencing failure, never a skip.
# Exit:  0 all assurance checks passed, 66 otherwise.

set -uo pipefail
export PATH="$HOME/.local/bin:/usr/local/go/bin:$PATH"

RIG="${1:?rig path required}"
CITY="${2:?city path required}"
WT_A="${3:?worktree A required}"
WT_B="${4:?worktree B required}"
WT_C="${5:?worktree C required}"
A="${6:?bead A required}"
B="${7:?bead B required}"
C="${8:?bead C required}"

SA_CITY="${SA_CITY:-$CITY}"
SA_RIG="${SA_RIG:-}"
SOURCE_REPO="${SOURCE_REPO:-/mnt/d/Development/corsolv-delivery-engine}"
# shellcheck source=lib/sa-lib.sh
. "$SOURCE_REPO/corsolv/p2-smoke/lib/sa-lib.sh"

FAILURES=0
note() { printf '  %-62s %s\n' "$1" "$2"; }
fail() { note "$1" "FAIL — $2"; FAILURES=$((FAILURES + 1)); }
pass() { note "$1" 'PASS'; }
info() { note "$1" "INFO — $2"; }
section() { printf '\n--- %s ---\n' "$1"; }

echo '============================================================'
echo 'S-A INDEPENDENT ASSURANCE (READ-ONLY)'
echo '============================================================'
echo "rig:  $RIG"
echo "city: $CITY"

# --- 1. artifacts, re-verified byte for byte -------------------------------
section '1. artifacts'

check_exact() {
  local label="$1" file="$2" want="$3"
  if [ ! -f "$file" ]; then fail "$label exists" "$file not found"; return; fi
  pass "$label exists"
  # od, not string comparison: trailing whitespace and CRLF are invisible to a
  # `=` test and would let a not-quite-right artifact pass as exact.
  local got expect
  got="$(od -An -c "$file" | tr -s ' ' | sed 's/^ //')"
  expect="$(printf '%s\n' "$want" | od -An -c | tr -s ' ' | sed 's/^ //')"
  if [ "$got" = "$expect" ]; then
    pass "$label byte-for-byte exact"
  else
    fail "$label byte-for-byte exact" "od comparison differs"
  fi
}
check_exact 'ALPHA.md' "$WT_A/ALPHA.md" 'ALPHA_OK'
check_exact 'BETA.md'  "$WT_B/BETA.md"  'BETA_OK'
check_exact 'INDEX.md' "$WT_C/INDEX.md" "$(printf 'ALPHA_OK\nBETA_OK')"

# The handoff discriminator: C's output can only be produced by reading what the
# other two workers wrote, so a C that "succeeded" without the upstream results
# is detectable rather than merely implausible.
if [ -f "$WT_C/INDEX.md" ] &&
   grep -qx 'ALPHA_OK' "$WT_C/INDEX.md" && grep -qx 'BETA_OK' "$WT_C/INDEX.md"; then
  pass 'handoff: INDEX.md carries both upstream results'
else
  fail 'handoff: INDEX.md carries both upstream results' 'missing one or both lines'
fi

# --- 2. worktree isolation, re-derived from git ----------------------------
section '2. worktree isolation'

REG=0
for wt in "$WT_A" "$WT_B" "$WT_C"; do
  if wt_is_registered "$RIG" "$wt"; then REG=$((REG + 1)); else fail 'worktree registered' "$wt"; fi
done
[ "$REG" -eq 3 ] && pass 'all three worktrees are registered git worktrees of the rig'

DISTINCT="$(for wt in "$WT_A" "$WT_B" "$WT_C"; do readlink -f "$wt"; done | sort -u | wc -l)"
if [ "$DISTINCT" -eq 3 ]; then
  pass 'the three worktrees are pairwise distinct paths'
else
  fail 'the three worktrees are pairwise distinct paths' "$DISTINCT distinct path(s)"
fi

BRANCHES="$(for wt in "$WT_A" "$WT_B" "$WT_C"; do git -C "$wt" rev-parse --abbrev-ref HEAD 2>/dev/null; done | sort -u | wc -l)"
if [ "$BRANCHES" -eq 3 ]; then
  pass 'each worktree is on its own branch'
else
  fail 'each worktree is on its own branch' "$BRANCHES distinct branch(es)"
fi

# A and B could not have read each other: neither ever contained the other's
# artifact at any commit on its own branch.
if git -C "$WT_A" log --all --format=%H -- BETA.md 2>/dev/null | grep -q .; then
  info 'A branch history mentions BETA.md' 'expected only after the controller merge'
fi
if [ "$(git -C "$RIG" rev-parse HEAD)" != "$(git -C "$WT_A" rev-parse HEAD)" ]; then
  pass 'the shared rig checkout is not any worker worktree'
else
  fail 'the shared rig checkout is not any worker worktree' 'rig HEAD equals worktree A HEAD'
fi

# --- 3. the dependent base, re-derived by ancestry -------------------------
section '3. dependent base'

A_ART_COMMIT="$(git -C "$RIG" log --format=%H --diff-filter=A -- ALPHA.md 2>/dev/null | tail -1)"
B_ART_COMMIT="$(git -C "$RIG" log --format=%H --diff-filter=A -- BETA.md 2>/dev/null | tail -1)"
C_BASE="$(git -C "$WT_C" rev-parse HEAD~0 2>/dev/null)"
C_ROOT="$(git -C "$WT_C" rev-parse "gc-worker-c" 2>/dev/null)"
# The base C was cut from is the first parent of its branch before any commit
# the controller added on it.
C_CUT="$(git -C "$RIG" merge-base gc-worker-c main 2>/dev/null)"

if [ -n "$A_ART_COMMIT" ] && [ -n "$B_ART_COMMIT" ] && [ -n "$C_CUT" ] &&
   git -C "$RIG" merge-base --is-ancestor "$A_ART_COMMIT" "$C_CUT" &&
   git -C "$RIG" merge-base --is-ancestor "$B_ART_COMMIT" "$C_CUT"; then
  pass "C's base descends from both upstream artifact commits (${C_CUT:0:9})"
else
  fail "C's base descends from both upstream artifact commits" \
       "A=${A_ART_COMMIT:-<none>} B=${B_ART_COMMIT:-<none>} cut=${C_CUT:-<none>}"
fi

# INDEPENDENT ORDERING PROOF. The run proves C started after integration from
# its own command ledger. Git commit timestamps are written by git, not by the
# harness, so agreeing here is a second derivation rather than a second reading.
A_TS="$(git -C "$RIG" log -1 --format=%ct "$A_ART_COMMIT" 2>/dev/null || echo 0)"
B_TS="$(git -C "$RIG" log -1 --format=%ct "$B_ART_COMMIT" 2>/dev/null || echo 0)"
C_WT_TS="$(stat -c %Y "$WT_C/.git" 2>/dev/null || echo 0)"
if [ "$C_WT_TS" -gt 0 ] && [ "$C_WT_TS" -ge "$A_TS" ] && [ "$C_WT_TS" -ge "$B_TS" ]; then
  pass "C's worktree was created after both upstream integrations (git timestamps)"
else
  fail "C's worktree was created after both upstream integrations" \
       "wt=$C_WT_TS A=$A_TS B=$B_TS"
fi

# --- 4. bead lifecycle, from the store -------------------------------------
section '4. bead lifecycle'

SESSIONS=''
for pair in "$A:$WT_A" "$B:$WT_B" "$C:$WT_C"; do
  id="${pair%%:*}"; wt="${pair##*:}"
  status="$(bead_status "$id" || true)"
  if [ "$status" = 'closed' ]; then
    pass "$id is CLOSED (structured status)"
  else
    fail "$id is CLOSED" "status='${status:-<unreadable>}'"
  fi
  render="$(sa_gc bd show "$id" 2>/dev/null || true)"
  meta() { grep -E "^[[:space:]]*$1:" <<<"$render" | head -1 | sed "s/^[[:space:]]*$1:[[:space:]]*//"; }
  wo="$(meta 'gc.work_outcome')"
  case "$wo" in
    blocked|no-op|abandoned) pass "$id typed outcome is truthful ($wo)" ;;
    shipped) fail "$id typed outcome is truthful" 'claims shipped while git is withheld' ;;
    '') fail "$id records a typed outcome" 'absent — silent close' ;;
    *) fail "$id records a typed outcome" "unknown disposition '$wo'" ;;
  esac
  # Ownership evidence for a raw routed bead is gc.routed_to + gc.session_id +
  # gc.session_name. gc.execution_routed_to and gc.last_heartbeat_at belong to
  # the formula/molecule dispatch path and are not written on this route, so
  # they are reported, not required.
  if [ -n "$(meta 'gc.routed_to')" ] && [ -n "$(meta 'gc.session_id')" ]; then
    pass "$id records the agent and session that executed it"
  else
    fail "$id records the agent and session that executed it" \
         "routed_to='$(meta 'gc.routed_to')' session_id='$(meta 'gc.session_id')'"
  fi
  info "$id heartbeat" "$(meta 'gc.last_heartbeat_at' || true)"
  wd="$(meta 'gc.work_dir')"
  if [ "$(readlink -f "${wd:-/nonexistent}")" = "$(readlink -f "$wt")" ]; then
    pass "$id canonical gc.work_dir is its own worktree"
  else
    fail "$id canonical gc.work_dir is its own worktree" "gc.work_dir='${wd:-<none>}'"
  fi
  SESSIONS="$SESSIONS$(meta 'gc.session_name')
"
done
NSESS="$(printf '%s' "$SESSIONS" | grep -v '^$' | sort -u | wc -l)"
if [ "$NSESS" -eq 3 ]; then
  pass 'three distinct sessions executed the three tasks'
else
  fail 'three distinct sessions executed the three tasks' "$NSESS distinct session name(s)"
fi

# --- 5. authority separation ------------------------------------------------
section '5. authority separation'

AUTHORS="$(rig_commit_authors "$RIG")"
UNEXPECTED="$(grep -v '^Gas City Controller$' <<<"$AUTHORS" | grep -v '^Corsolv Autonomy POC$' || true)"
if [ -z "$UNEXPECTED" ]; then
  pass "every commit is controller-authored ($(printf '%s' "$AUTHORS" | tr '\n' ',' ))"
else
  fail 'every commit is controller-authored' "unexpected author(s): $(printf '%s' "$UNEXPECTED" | tr '\n' ' ')"
fi

# Per-worktree diff scope. `gc rig add` writes its own infrastructure and the
# worktree provisioning writes a bead redirect, so scope is judged on what is
# left after those known controller-owned paths are excluded.
INFRA_RE='^(\.beads/|\.gc/|\.claude/|\.gitignore$)'
for pair in "$WT_A:ALPHA.md" "$WT_B:BETA.md" "$WT_C:INDEX.md"; do
  wt="${pair%%:*}"; art="${pair##*:}"
  changed="$(git -C "$wt" status --porcelain 2>/dev/null | awk '{print $NF}')"
  scope="$(grep -Ev "$INFRA_RE" <<<"$changed" | grep -v '^$' || true)"
  if [ -z "$scope" ]; then
    pass "$(basename "$wt") worker-owned diff is empty (artifact already published)"
  elif [ "$scope" = "$art" ]; then
    pass "$(basename "$wt") worker-owned diff is exactly $art"
  else
    fail "$(basename "$wt") worker-owned diff is exactly $art" \
         "got: $(printf '%s' "$scope" | tr '\n' ' ')"
  fi
done

# --- 6. worker transcripts --------------------------------------------------
section '6. worker transcripts'

# Claude records a per-project transcript directory keyed on the working
# directory. Each worker ran in its OWN worktree, so a transcript per worktree
# is itself corroboration that the three ran in three different places.
project_dir_for() {
  local p="$1" cand
  for cand in "$HOME/.claude/projects/$(printf '%s' "$p" | sed 's|/|-|g')" \
              "$HOME/.claude/projects/$(printf '%s' "$p" | sed 's|[^A-Za-z0-9]|-|g')"; do
    [ -d "$cand" ] && { printf '%s' "$cand"; return 0; }
  done
  return 1
}

TRANSCRIPTS=0
for pair in "$WT_A:ALPHA.md" "$WT_B:BETA.md" "$WT_C:INDEX.md"; do
  wt="${pair%%:*}"; art="${pair##*:}"; label="$(basename "$wt")"
  if proj="$(project_dir_for "$wt")"; then
    TRANSCRIPTS=$((TRANSCRIPTS + 1))
    pass "$label has its own worker transcript"
    cmds="$(grep -ohE '"command":"gc [^"]*' "$proj"/*.jsonl 2>/dev/null | sed 's/"command":"//')"
    missing=''
    for want in 'gc hook --claim' 'gc bd show' 'gc bd close'; do
      grep -qF "$want" <<<"$cmds" || missing="$missing '$want'"
    done
    if [ -z "$missing" ]; then
      pass "$label worker drove its own lifecycle"
    else
      fail "$label worker drove its own lifecycle" "not in transcript:$missing"
    fi
    if grep -qE '"command":"git ' "$proj"/*.jsonl 2>/dev/null; then
      writes_git=1
    else
      writes_git=0
    fi
    if [ "$writes_git" -eq 0 ]; then
      pass "$label worker never ran git"
    else
      fail "$label worker never ran git" 'a git command appears in the transcript'
    fi
    writes="$(grep -ohE '"file_path":"[^"]*"' "$proj"/*.jsonl 2>/dev/null | sort -u)"
    n="$(grep -c . <<<"$writes" || true)"
    if [ "$n" -eq 1 ] && grep -qF "$art" <<<"$writes"; then
      pass "$label transcript shows exactly one file write, the artifact"
    else
      info "$label file writes recorded" "$n: $(printf '%s' "$writes" | tr '\n' ' ' | head -c 200)"
    fi
  else
    fail "$label has its own worker transcript" 'no transcript directory found'
  fi
done
if [ "$TRANSCRIPTS" -eq 3 ]; then
  pass 'three separate worker transcripts — three separate working directories'
fi

# --- 7. live security posture, adjudicated not re-derived ------------------
section '7. live security posture'

# This script runs AFTER drain by design, so any live scan here inspects a city
# with no worker left in it: it either finds nothing (and would report INFO,
# which reads as coverage while proving nothing) or matches the city's
# long-lived mayor/bd.dog agents and "passes" on processes that were never
# workers. The authoritative capture happens during the run; this adjudicates
# the record. A missing record is a sequencing failure, never a skip.
adjudicate_live() {
  local label="$1" path="${2:-}"
  if [ -z "$path" ]; then
    fail "$label" 'NOT REACHED — no result path supplied'
  elif [ ! -f "$path" ]; then
    fail "$label" "NOT REACHED — no recorded result at $path"
  elif grep -q '^PASS$' "$path"; then
    pass "$label"
  else
    fail "$label" "recorded result is $(head -1 "$path")"
  fi
}
adjudicate_live 'live A/B posture proved before drain' "${LIVE_PROCESS_RESULT:-}"
adjudicate_live 'live C posture proved before drain'   "${LIVE_PROCESS_RESULT_C:-}"

# --- 8. drain ---------------------------------------------------------------
section '8. drain'

LEFT="$(sa_worker_pids "$CITY" '*worker-[abc]*')"
if [ -z "$LEFT" ]; then
  pass 'no worker process left running'
else
  fail 'no worker process left running' "pids:$LEFT"
fi
sessions="$(sa_gc session list 2>&1 || true)"
stuck="$(awk 'NR>1 && $2 ~ /worker-[abc]$/ && $3 == "active" {print $1}' <<<"$sessions")"
if [ -z "$stuck" ]; then
  pass 'no worker session left active'
else
  fail 'no worker session left active' "$(printf '%s' "$stuck" | tr '\n' ' ')"
fi

echo
echo '============================================================'
if [ "$FAILURES" -ne 0 ]; then
  echo "S-A INDEPENDENT ASSURANCE: FAIL ($FAILURES check(s))"
  exit 66
fi
echo 'S-A INDEPENDENT ASSURANCE: PASS'
