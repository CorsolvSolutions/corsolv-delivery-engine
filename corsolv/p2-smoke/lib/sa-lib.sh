#!/usr/bin/env bash
#
# sa-lib.sh — controller-side primitives for the S-A acceptance run.
#
# Sourced by BOTH the authoritative run (shadow-run.sh) and the targeted
# controls (tests/*.sh), so a control exercises the same code the run executes
# rather than a re-implementation of it. A control that passes against a
# private copy proves nothing about the run; that was the failure mode this
# file exists to remove.
#
# Every function here is controller authority: worktree provisioning, the
# legacy work_dir stamp, validated integration into the run base, and the
# durable final-state read. Workers never call any of it.
#
# THE PIPEFAIL/SIGPIPE RULE. Never write `if <producer> | grep -q ...`. With
# `set -o pipefail`, grep -q exits at the first match, the producer takes
# SIGPIPE (141), and pipefail promotes that to the pipeline status — so a MATCH
# reads as failure. Capture first, then match with a herestring (`<<<` is a
# redirect, not a pipe).

# ---------------------------------------------------------------------------
# Command ledger.
#
# D3 forbids any harness/operator command naming the dependent bead after its
# dependencies are satisfied. "We did not run one" is an assertion; a ledger of
# every gc invocation with its timestamp is evidence. Every gc call the
# controller makes goes through gcx, so the absence of a post-release C command
# is provable from an artifact rather than from the author's memory.
# ---------------------------------------------------------------------------

# sa_gc <args...> — gc, explicitly scoped to the run's city and rig.
#
# Scope is passed, never inferred from the working directory. `gc bd` discovers
# its city by looking for city.toml or .gc/ in the cwd, and a freshly added rig
# has NEITHER: `gc rig add` creates .beads/ in the rig but .gc/ only appears
# once a session has run there. So every bead command issued from the rig
# directory before the first worker starts fails with "not in a city directory"
# — and a harness that captures stdout into a variable stores that error message
# as the bead id and carries on. Setting SA_CITY/SA_RIG makes the scope
# explicit and independent of when .gc/ happens to materialize.
#
# Unset (the control tests, which stub gc) it degrades to a plain call.
#
# WHICH gc comes from SA_GC_BIN when the caller sets it, and from PATH when it
# does not. A detached run does not inherit an interactive shell's PATH, so a
# caller that knows where the binary is says so; the control tests that stub gc
# on PATH leave it unset and are unaffected.
# LEDGER CAPTURE LIVES HERE, AT THE LOWEST WRAPPER — deliberately.
#
# It used to live in gcx(), one layer up, with sa_gc() executing gc directly
# underneath it. That left a structural bypass: any call made through sa_gc —
# including a future mutating one — executed without ever reaching the ledger,
# so "no post-release directive naming C" would have been a claim about the
# calls someone remembered to route through gcx rather than about every call
# made. An independent audit of the committed S-A run found no actual bypass
# (its post-release calls were all reads), so the recorded evidence stands; but
# the gap is closed structurally rather than by continued care.
#
# Recording here means EVERY gc invocation is captured. Read-versus-directive
# discrimination is not weakened by that — it was never done at capture time.
# It is applied at query time by sa_ledger_directives_after, which classifies
# from the recorded argv, so reads remain reads and the extra rows are extra
# evidence rather than noise in the D3 assertion.
sa_gc() {
  local -a pre=()
  [ -n "${SA_CITY:-}" ] && pre+=(--city "$SA_CITY")
  [ -n "${SA_RIG:-}" ] && pre+=(--rig "$SA_RIG")
  if [ -n "${SA_CMD_LEDGER:-}" ]; then
    printf '%s\t%s\t%s\n' "$(date +%s)" "$(date -u +%FT%TZ)" "$*" >> "$SA_CMD_LEDGER"
  fi
  command "${SA_GC_BIN:-gc}" "${pre[@]}" "$@"
}

# sa_ledger_init <path> — start a fresh command ledger.
sa_ledger_init() {
  SA_CMD_LEDGER="$1"
  printf 'epoch\tutc\targv\n' > "$SA_CMD_LEDGER"
}

# gcx <args...> — historical spelling of sa_gc, kept because the harness reads
# more clearly where a call is a deliberate controller action. It must NOT log
# again: capture happens once, in sa_gc.
gcx() { sa_gc "$@"; }

# sa_ledger_note <text> — record a controller action that is not a gc call
# (a git worktree creation, say) so the ledger covers every action the
# controller took, not only the ones that happened to go through gc.
sa_ledger_note() {
  [ -n "${SA_CMD_LEDGER:-}" ] || return 0
  printf '%s\t%s\t%s\n' "$(date +%s)" "$(date -u +%FT%TZ)" "[controller] $*" >> "$SA_CMD_LEDGER"
}

# sa_ledger_mentions_after <epoch> <needle> — print ledger lines issued at or
# after <epoch> whose argv contains <needle>. Empty output means no such
# command was issued.
sa_ledger_mentions_after() {
  local since="$1" needle="$2"
  [ -n "${SA_CMD_LEDGER:-}" ] || return 0
  awk -F'\t' -v since="$since" -v needle="$needle" \
    'NR>1 && $1 >= since && index($3, needle) > 0 {print $0}' "$SA_CMD_LEDGER"
}

# sa_ledger_directives_after <epoch> <needle> — the D3 assertion.
#
# Reading a bead after its dependencies clear is evidence collection; telling
# the engine to do something with it is continuation. Only the second is what
# D3 forbids, so the two are separated explicitly here rather than by hoping no
# read ever happens. Everything not on the read-only list counts as a
# directive, so the check fails closed: a new mutating verb is a violation
# until someone deliberately classifies it.
SA_READONLY_VERBS='bd show|bd ready|bd list|bd dep tree|bd dep list|session list|config show|rig list|status|version|events|trace'

# sa_ledger_mark <label> — write an ordering marker into the ledger.
#
# "After release" is a STRICT ORDERING claim, and the ledger's timestamps have
# one-second resolution. That is not enough: the dependent task's worktree is
# provisioned immediately before the release action, and on a fast run both
# landed in the same second — so a `>= release_epoch` comparison counted the
# pre-release provisioning as post-release and failed a correct run. Comparing
# by ledger POSITION instead of by clock makes the order total by construction:
# everything below the marker happened after it, whatever the clock says.
sa_ledger_mark() {
  [ -n "${SA_CMD_LEDGER:-}" ] || return 0
  printf '%s\t%s\t%s\n' "$(date +%s)" "$(date -u +%FT%TZ)" "[mark] $1" >> "$SA_CMD_LEDGER"
}

# sa_ledger_directives_since_mark <label> <needle> — directives naming <needle>
# recorded after the marker line. Empty output means none were issued.
sa_ledger_directives_since_mark() {
  local label="$1" needle="$2"
  [ -n "${SA_CMD_LEDGER:-}" ] || return 0
  awk -F'\t' -v mark="[mark] $label" -v needle="$needle" -v ro="$SA_READONLY_VERBS" '
    $3 == mark { seen = 1; next }
    !seen { next }
    index($3, needle) > 0 {
      n = split(ro, verbs, "|")
      for (i = 1; i <= n; i++) if (index($3, verbs[i]) == 1) next
      print $0
    }' "$SA_CMD_LEDGER"
}

sa_ledger_directives_after() {
  local since="$1" needle="$2"
  [ -n "${SA_CMD_LEDGER:-}" ] || return 0
  awk -F'\t' -v since="$since" -v needle="$needle" -v ro="$SA_READONLY_VERBS" '
    NR>1 && $1 >= since && index($3, needle) > 0 {
      argv = $3
      n = split(ro, verbs, "|")
      for (i = 1; i <= n; i++) {
        if (index(argv, verbs[i]) == 1) next          # read-only: evidence
        if (index(argv, "[controller] read " verbs[i]) == 1) next
      }
      print $0
    }' "$SA_CMD_LEDGER"
}

# sa_worker_pids <city> <agent-regex> — pids of live provider worker processes
# belonging to this city whose GC_AGENT matches the regex.
#
# Concurrency is the one property here that cannot be reconstructed after the
# fact: once the workers exit, nothing on disk distinguishes "ran together" from
# "ran one after the other", and near-adjacent timestamps are not evidence of
# overlap. Two distinct pids alive in the same sample are.
#
# stderr is discarded per-process: /proc entries vanish mid-scan and the
# resulting "No such file or directory" noise is not a finding.
sa_worker_pids() {
  local city="$1" re="$2" out='' p exe cl agent
  for p in /proc/[0-9]*; do
    [ -r "$p/cmdline" ] || continue
    cl="$(tr '\0' '\n' < "$p/cmdline" 2>/dev/null || true)"
    [ -n "$cl" ] || continue
    exe="$(head -1 <<<"$cl")"
    case "$exe" in *claude) ;; *) continue ;; esac
    grep -qF "$city" <<<"$cl" || continue
    agent="$( { tr '\0' '\n' < "$p/environ"; } 2>/dev/null | sed -n 's/^GC_AGENT=//p' | head -1)"
    case "$agent" in
      $re) out="$out $(basename "$p")" ;;
    esac
  done
  printf '%s' "$out"
}

# ---------------------------------------------------------------------------
# Bead state, always adjudicated from the store's structured status.
# ---------------------------------------------------------------------------

# bead_status <id> — print the store's status field, or nothing if unreadable.
#
# Never free text. The rendered bead includes the worker-authored title and
# notes, so a bead titled "this task is CLOSED ..." satisfied the old text
# predicate while its status was `open` (demonstrated on bead mr2-c5n).
bead_status() {
  local json
  json="$(sa_gc bd show "$1" --json 2>/dev/null || true)"
  [ -n "$json" ] || return 1
  jq -r '.[0].status // empty' <<<"$json" 2>/dev/null
}

# sa_bead_in_ready <id> <ready-output-file> — true when THIS bead is in the
# ready set.
#
# Matched on the ID COLUMN, never anywhere in the line. `gc sling` creates an
# auto-convoy titled "sling-<bead-id>", so the dependent bead's own id appears
# inside a DIFFERENT bead's title in the same listing — and a whole-line grep
# reported a correctly-withheld bead as ready. That was demonstrated, not
# theorised: the D3 probe failed "routing does not make a blocked bead ready"
# while no worker spawned for the full 120s watch and the bead only genuinely
# became ready after its blocker closed. A gate that a sibling bead's title can
# satisfy is not a gate.
#
# `gc bd ready` renders one row per bead as "<glyph> <id> <glyph> <priority>
# <title>", so field 2 is the id.
sa_bead_in_ready() {
  local id="$1" file="$2"
  [ -f "$file" ] || return 1
  awk -v id="$id" '$2 == id {found = 1} END {exit found ? 0 : 1}' "$file"
}

# bead_is_closed <id> — true only when the store says closed.
bead_is_closed() {
  local status
  status="$(bead_status "$1")" || return 1
  [ "$status" = 'closed' ]
}

# ---------------------------------------------------------------------------
# THE FINAL-STATE READ.
#
# The defect this replaces: the acceptance loop captured a rendered bead, THEN
# tested closure from a second read. When the bead closed between those two
# reads, the durable report embedded the pre-close render — so a report could
# say OPEN for a bead that had demonstrably closed, and the typed
# gc.work_outcome written at close was absent from the evidence entirely. The
# predicate was correct; the evidence path was stale.
#
# The rule is now explicit: once the terminal predicate is satisfied, the
# authoritative record is RE-READ and that read is what the report stores.
# Nothing captured before the transition may appear as final evidence.
# ---------------------------------------------------------------------------

# capture_final_bead_state <id> <dir> — re-read <id> after closure and write the
# durable final record to <dir>/final-<id>.{txt,json}. Prints the status it
# recorded. Returns non-zero if the re-read did not observe a closed bead, so a
# caller can never silently store a non-final record as final.
capture_final_bead_state() {
  local id="$1" dir="$2" status
  mkdir -p "$dir"
  sa_gc bd show "$id" > "$dir/final-$id.txt" 2>&1 || true
  sa_gc bd show "$id" --json > "$dir/final-$id.json" 2>/dev/null || true
  status="$(jq -r '.[0].status // empty' < "$dir/final-$id.json" 2>/dev/null)"
  printf '%s' "$status"
  [ "$status" = 'closed' ]
}

# final_render <id> <dir> — the durable final rendered bead.
final_render() { cat "$2/final-$1.txt" 2>/dev/null; }

# final_meta <id> <dir> <key> — a gc.* metadata value read from the durable
# FINAL record, never from a mid-flight capture.
final_meta() {
  local render line
  render="$(final_render "$1" "$2")"
  line="$(grep -E "^[[:space:]]*$3:([[:space:]]|\$)" <<<"$render" | head -1)"
  [ -n "$line" ] || return 0
  # Strip leading indentation first, then the "<key>:" prefix. Cutting on the
  # first space instead drops an empty leading field and returns the key.
  line="${line#"${line%%[![:space:]]*}"}"
  line="${line#"$3":}"
  printf '%s' "${line#"${line%%[![:space:]]*}"}"
}

# ---------------------------------------------------------------------------
# Worktree provisioning — controller authority.
#
# Per-task isolation means the dependent task cannot reach its upstreams by
# reading their filesystem paths; it must receive them through repository
# state. So each task gets a real registered git worktree on its own branch,
# and the controller — not a worker — creates it, records it, and stamps the
# legacy work_dir the session layer resolves.
# ---------------------------------------------------------------------------

# wt_add <rig-root> <worktree-path> <branch> <base-committish>
# Create a real registered git worktree. Idempotent only in the sense that an
# existing path is an error: a stale worktree from a previous attempt must not
# be silently adopted by this one.
wt_add() {
  local rig="$1" path="$2" branch="$3" base="$4"
  if [ -e "$path" ]; then
    echo "wt_add: refusing to reuse an existing path: $path" >&2
    return 1
  fi
  mkdir -p "$(dirname "$path")"
  GIT_LFS_SKIP_SMUDGE=1 git -C "$rig" worktree add -q "$path" -b "$branch" "$base" || return 1
  # Keep the worker's runtime noise out of the tracked tree, the same local
  # excludes the pack's worktree-setup.sh writes.
  local exclude
  exclude="$(git -C "$path" rev-parse --git-path info/exclude)"
  case "$exclude" in /*) ;; *) exclude="$path/$exclude" ;; esac
  mkdir -p "$(dirname "$exclude")"
  {
    printf '%s\n' '.beads/redirect' '.beads/hooks/' '.beads/formulas/' \
      '.logs/' '.claude/' '.codex/' '.gemini/' '.opencode/' 'state.json'
  } >> "$exclude"
  mkdir -p "$path/.beads"
  printf '%s\n' "$rig/.beads" > "$path/.beads/redirect"
  return 0
}

# wt_is_registered <rig-root> <worktree-path> — true when git itself lists the
# path as a worktree of the rig. Directory existence is not registration.
wt_is_registered() {
  local rig="$1" path="$2" listing real
  real="$(readlink -f "$path" 2>/dev/null)"
  [ -n "$real" ] || return 1
  listing="$(git -C "$rig" worktree list --porcelain 2>/dev/null || true)"
  local line
  while IFS= read -r line; do
    case "$line" in
      worktree\ *)
        [ "$(readlink -f "${line#worktree }" 2>/dev/null)" = "$real" ] && return 0 ;;
    esac
  done <<<"$listing"
  return 1
}

# wt_head <path> — the commit the worktree currently points at.
wt_head() { git -C "$1" rev-parse HEAD 2>/dev/null; }

# ---------------------------------------------------------------------------
# Controller-owned integration.
#
# A worker produces a change in its own worktree and cannot publish it: git is
# withheld by policy precisely so publication stays with the controller. The
# controller validates the result, commits it on the task branch under its own
# identity, and merges that branch into the run base. THAT — not the worker
# closing its bead — is what "merged" means at this stage.
# ---------------------------------------------------------------------------

CONTROLLER_NAME='Gas City Controller'
CONTROLLER_EMAIL='support@corsolv.com'

# controller_commit <worktree> <message> <paths...>
controller_commit() {
  local wt="$1" msg="$2"; shift 2
  git -C "$wt" add -- "$@" || return 1
  git -C "$wt" -c "user.name=$CONTROLLER_NAME" -c "user.email=$CONTROLLER_EMAIL" \
    commit -q -m "$msg" || return 1
  git -C "$wt" rev-parse HEAD
}

# controller_integrate <rig-root> <base-branch> <task-branch> <message>
# Merge a validated task branch into the run base and print the new base SHA.
controller_integrate() {
  local rig="$1" base="$2" branch="$3" msg="$4"
  git -C "$rig" -c "user.name=$CONTROLLER_NAME" -c "user.email=$CONTROLLER_EMAIL" \
    merge --no-ff -q -m "$msg" "$branch" >/dev/null 2>&1 || return 1
  git -C "$rig" rev-parse "$base"
}

# ---------------------------------------------------------------------------
# City bootstrap.
#
# Shared by the preflight probe and the authoritative run so the run cannot
# execute against a city shaped differently from the one the probe validated.
# ---------------------------------------------------------------------------

# sa_pool_worker_prompt <city> — the prompt template the SDK gives an implicit
# provider agent. The explicitly declared per-task agents must render the same
# pool-worker behaviour as the implicit `<rig>/claude` target the earlier runs
# used; silently falling back to the embedded baseline would change what the
# worker was told while every assertion kept passing.
sa_pool_worker_prompt() {
  local shown
  shown="$(sa_gc config show 2>/dev/null || true)"
  grep -oE '^prompt_template = "[^"]*pool-worker\.md"' <<<"$shown" \
    | head -1 | sed 's/^prompt_template = "//; s/"$//'
}

# sa_declare_worker_agent <city> <rig-name> <rig-path> <agent> <work-dir> <prompt>
#
# Declares one single-capacity, rig-scoped worker agent whose session cwd is its
# OWN worktree. Three of these are what make per-task isolation reachable: the
# work_dir template surface (Agent/Rig/City/WorktreesRoot) carries no per-slot
# variable, so an unbounded pool would resolve every concurrent slot to one
# directory. Distinct agents are the only configuration that yields distinct
# worktrees — and they stay pure configuration, with no role name in Go.
#
# max_active_sessions = 1 keeps each agent a single-capacity pool rather than a
# configured named session. That matters: a configured named session is not
# pool-managed, and the pool-managed branch is exactly where
# workDirStampHasOwnershipEvidence governs whether gc.work_dir may be mirrored.
# Keeping these agents pool-managed keeps that guard operative, so the canonical
# stamp appearing at all is evidence that the pre-dispatch legacy stamp matched
# the directory the live session was really started in.
#
# pre_start re-runs the pack's idempotent worktree-setup as a safety net. The
# controller creates the worktree itself before dispatch (that is the ownership
# contract the guard demands), so pre_start normally finds it already present
# and only re-applies the local excludes and bead redirect.
#
# Declared as a DIRECTORY agent (agents/<name>/agent.toml), not a `[[agent]]`
# table. Pack v2 removed the inline table surface outright — `gc config show`
# refuses to load a city that still carries one ("unsupported PackV1 [[agent]]
# tables; move each agent to agents/<name>/agent.toml"), so the inline form is
# not a style choice here, it is a load failure. The agent's name comes from the
# directory, which is why no `name` key appears below.
sa_declare_worker_agent() {
  local city="$1" rigName="$2" rigPath="$3" agent="$4" workDir="$5" prompt="$6"
  local dir="$city/agents/$agent"
  mkdir -p "$dir"
  {
    printf 'dir = "%s"\n' "$rigName"
    printf 'scope = "rig"\n'
    printf 'provider = "claude"\n'
    printf 'max_active_sessions = 1\n'
    printf 'work_dir = "%s"\n' "$workDir"
    printf 'pre_start = ["%s/scripts/worktree-setup.sh %s %s %s"]\n' \
      "$city" "$rigPath" "$workDir" "$agent"
    [ -n "$prompt" ] && printf 'prompt_template = "%s"\n' "$prompt"
  } > "$dir/agent.toml"
}

# sa_wait_rig_beads <city> <rig-name> <deadline-seconds>
#
# `gc rig add` can return before the rig's own beads database is usable. Work
# slung in that window lands in the city scope instead of the rig and dispatch
# fails.
sa_wait_rig_beads() {
  local city="$1" rig="$2" deadline=$(( $(date +%s) + "${3:-120}" )) listing rigbeads
  while true; do
    listing="$(sa_gc rig list 2>&1 || true)"
    rigbeads="$(awk -v rig="$rig:" '
          $1 == rig {inrig = 1; next}
          inrig && /^  [^ ]/ && $0 !~ /^    / {inrig = 0}
          inrig && /Beads:/ {print}' <<<"$listing")"
    grep -q 'initialized' <<<"$rigbeads" && return 0
    [ "$(date +%s)" -ge "$deadline" ] && return 1
    sleep 2
  done
}

# sa_session_workdir <city> <agent-qualified-name> — the cwd the runtime
# actually started the agent's session in, read back from the session record
# rather than assumed from config.
sa_session_workdir() {
  local city="$1" agent="$2" json
  json="$(sa_gc session list --json 2>/dev/null || true)"
  [ -n "$json" ] || return 1
  jq -r --arg a "$agent" '
      (if type == "array" then . else (.sessions // .items // []) end)
      | map(select((.template // "") == $a or (.agent // "") == $a))
      | (.[0].work_dir // .[0].workDir // .[0].dir // empty)' <<<"$json" 2>/dev/null
}

# ---------------------------------------------------------------------------
# Publication scope — the boundary that actually holds.
#
# `bounded-project` grants Write/Edit and three npm scripts. A worker can
# therefore edit package.json, and `npm run build` / `npm test` execute whatever
# that file names, so the permission list buys scope clarity, not containment.
# Describing it as an arbitrary-code sandbox would be false.
#
# What contains the worker is the AUTHORITY split: it may mutate a working tree,
# and only the controller commits, pushes, opens the PR and merges. For that
# split to mean anything, the controller must look at WHAT changed before it
# publishes — otherwise an unauthorised edit rides along inside an authorised
# commit and the boundary is decorative.
#
# So publication is gated on the changed-file set matching what the bead
# authorised. A change to package.json, or to any other file the bead did not
# name, stops publication. The fix for a worker that needs to change more is a
# bead that authorises more — never a git grant.
# ---------------------------------------------------------------------------

# SA_PUBLICATION_INFRA_RE are paths owned by the controller or the toolchain
# rather than the worker: the bead store and runtime state Gas City writes into
# a rig, provider scaffolding, and build/dependency output that is not source.
# They are excluded from attribution rather than authorised, because they are
# not the worker's to author in the first place.
SA_PUBLICATION_INFRA_RE='^(\.beads/|\.gc/|\.claude/|node_modules/|dist/|\.gitignore$)'

# publication_changed_status <worktree>
#
# Emits the worktree's change set as NUL-terminated "<XY><TAB><path>" records —
# the single reading of git status every attribution decision below is built on.
#
# Three properties the default reading does not have:
#
#   -uall  an untracked DIRECTORY is otherwise reported as the directory alone.
#          A bead authorises files, so "src/" is a path it can never name, and
#          collapsing hides the authorised and unauthorised alike: every package
#          that creates a directory is refused, and a stray inside one that
#          already existed is invisible.
#   -z     git quotes a path containing a space or a newline, and any reader
#          splitting on whitespace attributes a fragment of it.
#   R/C    a rename emits its destination and then its origin as two records.
#          Both ends are the worker's doing, so both must be attributable.
publication_changed_status() {
  local wt="$1" rec status wantOrigin=0
  while IFS= read -r -d '' rec; do
    if [ "$wantOrigin" = '1' ]; then
      wantOrigin=0
      printf '%s\t%s\0' 'R ' "$rec"
      continue
    fi
    status="${rec:0:2}"
    printf '%s\t%s\0' "$status" "${rec:3}"
    case "$status" in
      R*|C*|*R|*C) wantOrigin=1 ;;
    esac
  done < <(git -C "$wt" status --porcelain=v1 -uall -z 2>/dev/null)
}

# publication_path_is_attributable <path> <authorised-csv>
#
# True when the path is the worker's to answer for and the bead did not name it.
#
# An entry authorises itself exactly, and — when the changed path continues
# past a path separator — everything beneath it as a directory. The boundary
# is the separator, never the string prefix: "src/add.ts" still does not
# authorise "src/add.ts.bak". The subtree grant exists because the plan
# validator has always accepted directory entries, and a package that
# generates an open-ended set of files (a component system, a feature
# directory) cannot spell its files out before a worker writes them; a
# validator that admits "src/ui" beside an adjudicator that then refuses
# "src/ui/button.tsx" accepts plans it can never publish, which is how
# scorm-studio-redesign-2 finished two gated packages and published neither.
publication_path_is_attributable() {
  local path="$1" authorised="$2" entry
  grep -qE "$SA_PUBLICATION_INFRA_RE" <<<"$path" && return 1
  local -a entries
  IFS=',' read -ra entries <<<"$authorised"
  for entry in "${entries[@]}"; do
    [ -n "$entry" ] || continue
    entry="${entry%/}"
    case "$path" in
      "$entry"|"$entry"/*) return 1 ;;
    esac
  done
  return 0
}

# publication_scope_violations <worktree> <authorised-csv>
#
# Prints every changed path the bead did not authorise, one per line. Empty
# output means publication may proceed. Deliberately reports rather than
# decides, so the caller records the violation in its own control ledger.
publication_scope_violations() {
  local wt="$1" authorised="$2" rec path
  while IFS= read -r -d '' rec; do
    path="${rec#*$'\t'}"
    [ -n "$path" ] || continue
    publication_path_is_attributable "$path" "$authorised" || continue
    printf '%s\n' "$path"
  done < <(publication_changed_status "$wt")
}

# quarantine_untracked_out_of_scope <worktree> <authorised-csv> <dest-dir>
#
# Moves every UNTRACKED out-of-scope file into <dest-dir> under its original
# relative path and prints what it moved. Returns 0 whether or not it moved
# anything; a caller that needs to know reads the output.
#
# Why the controller may do this, when it may not revert a worker:
#
# `bounded-project` grants Write and Edit and nothing that REMOVES a file, and a
# cleanup command cannot be declared as a verification gate. So a worker that
# writes a transient probe — to prove its lint config really covers `public/`,
# say — can create the file and cannot take it back, and the package it just
# completed correctly is unpublishable forever.
#
# An untracked out-of-scope file was never going to be published: the controller
# commits the bead's named paths and nothing else. The only harm it can still do
# is contaminate the gate run the controller performs before publishing. Moving
# it out of the tree removes exactly that harm and destroys nothing — the file
# is kept as evidence, and the same file remains a scope violation if the worker
# ever gets it committed.
#
# A TRACKED file changed out of scope is deliberately NOT touched. That is a
# mutation of content the project already had; quarantining it would be the
# controller silently reverting the worker, and it stays a refusal.
quarantine_untracked_out_of_scope() {
  local wt="$1" authorised="$2" dest="$3" rec path dir
  while IFS= read -r -d '' rec; do
    [ "${rec%%$'\t'*}" = '??' ] || continue
    path="${rec#*$'\t'}"
    [ -n "$path" ] || continue
    publication_path_is_attributable "$path" "$authorised" || continue
    dir="$(dirname "$path")"
    mkdir -p "$dest/$dir" || continue
    mv -f "$wt/$path" "$dest/$path" || continue
    # Leave the tree as though the file had never been written. Single level and
    # only when empty, so this can never reach a directory holding real work.
    [ "$dir" = '.' ] || rmdir "$wt/$dir" 2>/dev/null || true
    printf '%s\n' "$path"
  done < <(publication_changed_status "$wt")
}

# ---------------------------------------------------------------------------
# Required-job CI adjudication.
#
# "CI passed" is not one fact. A workflow run concludes success when its jobs
# did — including when the only job was SKIPPED — and a project may gate on
# several jobs or on a matrix. Adjudicating the first job whose name matches is
# correct for a single-required-job project and quietly wrong for any other: a
# second required job could fail while the first passed and the verdict would
# still read green.
#
# So the contract is explicit: a regex naming the REQUIRED jobs. Jobs outside it
# are not adjudicated, because optional and skipped jobs must stay
# distinguishable from required ones — "every job must be green" would fail a
# run for an advisory job the project never gated on.
# ---------------------------------------------------------------------------

# sb_required_job_verdict <jobs-json> <required-regex>
#
# Prints one of:
#   missing              no job matches the contract — NOT REACHED, never a pass
#   fail:<name=concl,…>  at least one required job did not succeed
#   ok:<n>:<names>       all n required jobs succeeded
sb_required_job_verdict() {
  local jobs="$1" re="$2" selected count bad
  selected="$(jq -c --arg re "$re" \
    '[.jobs[] | select(.name | test($re)) | {name, conclusion}]' <<<"$jobs" 2>/dev/null)"
  [ -n "$selected" ] || { printf 'missing'; return 0; }
  count="$(jq -r 'length' <<<"$selected" 2>/dev/null || echo 0)"
  if [ "${count:-0}" -eq 0 ]; then
    printf 'missing'
    return 0
  fi
  bad="$(jq -r '[.[] | select(.conclusion != "success")] | map(.name + "=" + (.conclusion // "null")) | join(",")' \
    <<<"$selected" 2>/dev/null)"
  if [ -n "$bad" ]; then
    printf 'fail:%s' "$bad"
    return 0
  fi
  printf 'ok:%s:%s' "$count" "$(jq -r 'map(.name) | join(",")' <<<"$selected")"
}

# rig_worker_commits <rig-root> — author names of every commit in the base
# branch's history, so "no worker committed" is checked against authorship
# rather than against commit-message wording.
rig_commit_authors() { git -C "$1" log --format='%an' 2>/dev/null | sort -u; }
