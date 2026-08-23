#!/usr/bin/env bash
#
# driver.sh — the execution mechanics of managed delivery.
#
# This is the engine-owned executable the compiled run invokes, and the ONLY
# thing that ever reaches a command line. It is generic: everything specific to
# a project comes from two files the Go layer wrote and validated —
# intent.json (what to deliver) and plan.json (the work packages) — and nothing
# in it names a project, a repository or a task.
#
# It is bash rather than Go on purpose. The controller primitives it needs
# already exist, proven, in corsolv/p2-smoke/lib/sa-lib.sh: worktree
# provisioning, the ready-set predicate that a sibling bead's title cannot
# satisfy, the final-state re-read, publication scope adjudication, required-job
# CI verdicts. Every one of those encodes a specific failure that was diagnosed
# once and must not be re-diagnosed. Porting them would mean rewriting the
# lessons, so this generalizes the harness that already carries them.
#
# STAGES. Each is a separate process, invoked by the run queue, and each is
# idempotent: a stage that has already done its work reports that and exits 0.
# That is what makes an interrupted delivery resumable — the queue's journal
# says which stages completed, and re-entering a partially-done stage is safe.
#
#   city-up             build the city, clone the working rig, declare workers
#   dispatch            create the beads, wire dependencies, route them
#   await               wait for the workers
#   publish -package X  scope-check, gate, commit, push, PR, CI, merge
#   project             render the delivery projection
#   publish-projection  install the projection into the project's repository
#
# Usage: driver.sh <stage> [-package <id>] -project <id> -state <dir>
#                  [-deadline <seconds>] [-gh <cli>] [-gc <cli>] [-bd <cli>]
#                  [-provider <name>] [-provider-bin <path>]
#
# WHAT A STAGE SAYS HAPPENED, AND WHAT IT LEAVES BEHIND. A stage started by a
# run states its outcome in the structured document the run exports a path for,
# and THAT is the verdict: COMPLETE, CONTINUE, HUMAN_BLOCKED, or FAILED with the
# terminal reason that separates an expired credential from a failing test. The
# exit status below is what remains for a caller that is not a run — a person at
# a terminal — and it is deliberately unchanged, because a residue is all such a
# caller ever had.
#
# Exit: 0 the stage completed  1 the stage did not  2 the invocation was wrong

set -uo pipefail

# The controller primitives come from THIS driver's own checkout by default.
#
# A named absolute default was a checkout on one machine, so a driver run from
# anywhere else — a second worktree, a release copy — sourced a library from a
# different tree than the one it shipped with, and a change made to the pair
# arrived half-applied. The environment override remains for a caller that
# genuinely means to point somewhere else.
SOURCE_REPO="${CORSOLV_ENGINE_REPO:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
# shellcheck source=../p2-smoke/lib/sa-lib.sh
. "$SOURCE_REPO/corsolv/p2-smoke/lib/sa-lib.sh"

# The controller-result contract comes from this driver's OWN directory, and not
# from the configured engine root above. The two are different questions: the
# shared controller primitives may deliberately be pointed elsewhere, while the
# document this driver states its outcome in is part of this driver.
DRIVER_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=controller-contract.sh
. "$DRIVER_DIR/controller-contract.sh"

CONTROLLER_ID='Gas City Controller <support@corsolv.com>'

# ---------------------------------------------------------------------------
# What this stage says happened to it.
#
# A supervised stage states its own outcome, and the run adjudicates THAT rather
# than the exit status this process happens to leave behind. Both directions of
# the residue were observed in the pilot: a stage exiting non-zero for a
# condition that was not merely benign but correct, and a wrapper exiting zero
# over work that had been cut off part-way through.
#
# The statement is made in exactly ONE place — here, on the way out. Stages set
# what they want said and never write a document of their own, because a stage
# that stated an intermediate outcome and then died would have that intermediate
# statement adjudicated as its final one. A stage that is KILLED states nothing
# at all, and nothing is exactly what the run must see: an absent result is an
# absence of knowledge, and the run fails it safe rather than assuming.
# ---------------------------------------------------------------------------

RESULT_STATE=''
RESULT_REASON=''
RESULT_DETAIL=''
RESULT_PUBLISHED=''

# PROGRESSION_REFUSAL is why the run refuses to let this packet progress, when
# it does. It is read from the run in stage_project and caps what the delivery
# projection may claim; empty means the run's mandatory gates permit progression,
# or that this stage is not part of a run at all.
PROGRESSION_REFUSAL=''

# Whatever is at the result path belongs to some earlier attempt. It is removed
# before this stage does anything, so an interruption leaves an absence rather
# than a previous attempt's answer.
cr_clear

publish_result() {
  local code=$?
  cr_supervised || return 0
  [ -z "$RESULT_PUBLISHED" ] || return 0
  RESULT_PUBLISHED=1
  if [ -z "$RESULT_STATE" ]; then
    # A stage that ended without saying so says the safest thing consistent with
    # what it did. Reaching the end of a stage function IS its work finishing;
    # every other ending is a path that has not been taught to speak, and the run
    # must not read one of those as success.
    if [ "$code" -eq 0 ]; then
      RESULT_STATE='COMPLETE'
    else
      RESULT_STATE='FAILED'
      RESULT_DETAIL="the stage ended with status $code without stating why"
    fi
  fi
  cr_write --state "$RESULT_STATE" --reason "$RESULT_REASON" --detail "$RESULT_DETAIL" ||
    printf 'driver[%s]: the structured controller result could not be written\n' "${STAGE:-}" >&2
}
trap publish_result EXIT

# ---------------------------------------------------------------------------
# Invocation.
# ---------------------------------------------------------------------------

STAGE="${1:-}"; shift || true
PACKAGE=''
PROJECT=''
STATE=''
DEADLINE=''

FORGE=''
GASCITY=''
BEADS=''
PROVIDER=''
PROVIDER_BIN=''
PROJECT_PATH=''

while [ $# -gt 0 ]; do
  case "$1" in
    -package) PACKAGE="${2:-}"; shift 2 ;;
    -project) PROJECT="${2:-}"; shift 2 ;;
    -state)   STATE="${2:-}"; shift 2 ;;
    -deadline) DEADLINE="${2:-}"; shift 2 ;;
    -gh)      FORGE="${2:-}"; shift 2 ;;
    -gc)      GASCITY="${2:-}"; shift 2 ;;
    -bd)      BEADS="${2:-}"; shift 2 ;;
    -provider)     PROVIDER="${2:-}"; shift 2 ;;
    -provider-bin) PROVIDER_BIN="${2:-}"; shift 2 ;;
    -project-path) PROJECT_PATH="${2:-}"; shift 2 ;;
    *) printf 'driver: unknown argument %s\n' "$1" >&2; exit 2 ;;
  esac
done

[ -n "$STAGE" ] && [ -n "$PROJECT" ] && [ -n "$STATE" ] || {
  printf 'usage: driver.sh <stage> [-package <id>] -project <id> -state <dir>\n' >&2
  exit 2
}

# The invocation is adjudicated before the environment. An unknown stage is a
# caller error and must read as one; discovering it only after the intent file
# turned out to be missing would report a wiring bug as an environment problem.
case "$STAGE" in
  city-up|dispatch|await|publish|verify|project|publish-projection) ;;
  *) printf 'driver: unknown stage %s\n' "$STAGE" >&2; exit 2 ;;
esac

# The forge CLI comes from the run's host profile, passed in on the command
# line. It is not defaulted to `gh` on PATH, because on this host the engine
# runs under WSL while the only authenticated gh is a Windows install — and a
# driver that silently fell back to a `gh` that is not there fails at the clone
# with an authentication error that names nothing.
GH="${FORGE:-${GH:-gh}}"

# The Gas City CLI arrives the same way and for the same reason. A run detached
# into its own process group does not inherit an interactive shell's PATH, and
# `gc` is installed under the operator's home rather than in a system directory
# — so a driver that took it from PATH failed at its very first stage with
# `gc: command not found`, having already cloned the rig. Exporting it is what
# makes the shared controller primitives in sa-lib.sh use it too: every gc
# invocation in a run goes through sa_gc, which reads this.
GC="${GASCITY:-${GC:-gc}}"
export SA_GC_BIN="$GC"

# The agent runtime is named by the host profile rather than assumed here. It
# is both a name and a location: Gas City looks the provider up BY NAME, so the
# declared binary has to be exposed under the name the city was built with.
PROVIDER="${PROVIDER:-claude}"

# HOW LONG A STAGE MAY WAIT FOR WORK, DECLARED BY THE RUN THAT STARTED IT.
#
# It has to be the run's number rather than this script's own default, and the
# reason is the turn cap in miniature. The run bounds every task with a timeout
# and KILLS a task that exceeds it — a killed stage states nothing, and nothing
# is adjudicated as an absence of knowledge. So a stage whose private deadline
# outlived its task's timeout could never say "the work is unfinished": it was
# killed a moment before it would have said so, and an interruption was recorded
# as silence. The compiler now passes the same number it bounds the task with,
# and bounds the task slightly longer so this stage always speaks first.
case "$DEADLINE" in
  ''|*[!0-9]*) DEADLINE="${DELIVERY_WORK_DEADLINE:-5400}" ;;
esac

INTENT="$STATE/intent.json"
BASE_PLAN="$STATE/plan.json"
RECORD="$STATE/delivery.json"
RUNTIME="$STATE/runtime.json"
EVIDENCE="$STATE/evidence"
mkdir -p "$EVIDENCE"

# THE PLAN THIS RUN EXECUTES IS THE PLAN PLUS ITS REMEDIATIONS.
#
# A criterion that was reported met can later be disproved, and the corrective
# work that repairs it is authorized as a separate, append-only document rather
# than by rewriting the plan — because plan.json is what this delivery's already
# merged work was measured against, and editing it would silently re-date every
# completion gate that has already passed.
#
# So the plan is composed here, at the one place every stage reads it from. The
# original packages come first and the remedial ones follow in the order they
# were authorized, which is the order the Go layer joins them in too. The
# composition is mechanical — a concatenation of validated documents, forming no
# verdict — and it is rebuilt on every invocation rather than cached, so a
# remediation authorized between two stages cannot leave one of them reading a
# plan the other does not have.
#
# A delivery with no remediations composes to exactly its plan, which is every
# delivery that ran before this existed.
#
# It is composed further down, once `die` exists to report a failure to compose
# it; only the path is named here, beside the documents it is derived from.
PLAN="$EVIDENCE/effective-plan.json"

# Not every dependency can be told where to look.
#
# Gas City refuses to build a city without beads, and finds it by PATH lookup
# from a script it shells out to — so `gc rig add` failed with `bd: not found`
# even once gc itself was named absolutely. It then refuses to finish building
# a city whose provider it cannot resolve by name, which leaves the city made
# but its pack imports uninstalled, and every later command failing on a
# missing packs.lock. What a detached run cannot do is assume PATH; what it can
# do is make the DECLARED binaries findable on it.
#
# They go in a directory this run owns, one symlink each, rather than by
# prepending the directory they happen to live in: the run should expose the
# tools it declared and nothing else that shares a folder with them.
TOOLBIN="$STATE/toolbin"
mkdir -p "$TOOLBIN"
expose_tool() {
  case "$2" in
    /*) ln -sfn "$2" "$TOOLBIN/$1" ;;
  esac
}
expose_tool gc "$GC"
expose_tool bd "$BEADS"
expose_tool "$PROVIDER" "$PROVIDER_BIN"
PATH="$TOOLBIN:$PATH"
export PATH

# run_project_command runs one of the PROJECT's own commands — a declared gate,
# or the dependency install that precedes it — under the toolchain this host
# declared for it, with nothing on its stdin.
#
# THE TWO DEFECTS THIS EXISTS FOR, both found by a pilot rather than by review,
# and both in the one place the controller's own verification happens.
#
# THE TOOLCHAIN. Everything the ENGINE runs is declared and exposed above; the
# commands a PROJECT declares were left to PATH. In the environment a detached
# run actually inherits on this host, `npm` resolves to `/mnt/c/.../nodejs/npm`
# — the Windows npm, reaching a Linux worktree through a `\\wsl.localhost\...`
# UNC path — and `node` does not resolve at all. So the controller re-ran a
# package's gates with a foreign toolchain: it reported REMOVING 130 packages
# from a tree it had just installed, and any gate spelled `node ...` could only
# ever have failed. Which toolchain a project is built with is a fact about the
# machine, so it is declared in the host profile and arrives as -project-path.
#
# THE STDIN. A gate that reads stdin used to consume the loop that was feeding
# it the gates, so the loop ended after the first one and the controller
# verified exactly one of every package's declared gates while reporting it had
# run them all. Closing stdin is what makes a gate's own appetite irrelevant;
# reading the list into an array first (below) is what makes it structural.
run_project_command() {
  local wt="$1"; shift
  ( cd "$wt" && PATH="${PROJECT_PATH:+$PROJECT_PATH:}$PATH" "$@" ) < /dev/null
}

say() { printf 'driver[%s] %s\n' "$STAGE" "$1" >&2; }

# die ends the stage on a failure, and states it as one.
#
# The optional second argument is the contract's terminal reason. Naming it is
# what separates an expired credential from a failing test: the first is a few
# seconds of a person's time and must not be retried into, the second is
# ordinary work the run should carry on around. A failure with no named reason
# is classified by the run from the text below, exactly as an unsupervised
# command's would be.
die() {
  RESULT_STATE='FAILED'
  RESULT_DETAIL="$1"
  RESULT_REASON="${2:-}"
  printf 'driver[%s]: %s\n' "$STAGE" "$1" >&2
  exit 1
}

# die_from ends the stage on a failure whose terminal reason is read from what
# the tool actually said.
#
# The stage captures its tools' output into evidence files rather than onto its
# own stdout, so a signature the run would have recognized never reaches the
# run's classifier — which is how an authentication refusal reached the queue as
# an ordinary command failure and was retried into.
die_from() { die "$2" "$(cr_reason_for_output "$1")"; }

# stop_human ends the stage on a limit only a person can lift.
#
# It is stated DISTINCTLY from an authentication boundary, and deliberately:
# both stop the run and neither is retried, but one is usually seconds of a
# person's time and the other is a conversation.
stop_human() {
  RESULT_STATE='HUMAN_BLOCKED'
  RESULT_DETAIL="$1"
  printf 'driver[%s]: %s\n' "$STAGE" "$1" >&2
  exit 1
}

# not_finished states that the stage made progress and has more to do.
#
# Nothing failed, so nothing is retried: the run re-offers the stage under the
# bounded resume budget its task declared, and holds it for a person if it never
# converges. A stage that said this and then exits non-zero is still a failed
# stage to a caller that is not a run — which is what an unsupervised invocation
# has always meant, and is left unchanged.
not_finished() {
  RESULT_STATE='CONTINUE'
  RESULT_DETAIL="$1"
  say "$1"
}

[ -f "$INTENT" ] || die "no delivery intent at $INTENT"
[ -f "$BASE_PLAN" ] || die "no delivery plan at $BASE_PLAN"

# compose_plan builds the effective plan: the packages the plan was written with,
# then the corrective work each authorized remediation added, in sequence order.
#
# jq -s slurps the documents into one array, so the base's own fields — schema
# version, project id, provenance — are kept exactly as written and only
# `packages` grows. Nothing is deduplicated or reordered here: the Go layer
# validated the union before any remediation was allowed to exist, and a shell
# script quietly resolving a conflict it found would be forming a verdict.
compose_plan() {
  local rems=() f
  for f in "$STATE"/remediation-[0-9][0-9][0-9].json; do
    [ -f "$f" ] && rems+=("$f")
  done
  if [ "${#rems[@]}" -eq 0 ]; then
    cp -f "$BASE_PLAN" "$PLAN" || die "composing the effective plan from $BASE_PLAN"
    return 0
  fi
  local tmp; tmp="$(mktemp "$PLAN.XXXXXX")"
  if jq -s '. as $all | $all[0] | .packages = ($all[0].packages + [$all[1:][].packages[]])' \
       "$BASE_PLAN" "${rems[@]}" > "$tmp" 2> "$EVIDENCE/compose-plan.txt"; then
    mv -f "$tmp" "$PLAN"
    return 0
  fi
  rm -f "$tmp"
  # A plan that could not be composed is not a plan this run may fall back from.
  # Executing the base alone would silently drop authorized corrective work and
  # go on reporting the criterion it repairs as outstanding with nothing saying
  # why — the exact failure this mechanism exists to make impossible.
  die "composing the effective plan from $BASE_PLAN and ${#rems[@]} remediation(s); see $EVIDENCE/compose-plan.txt"
}
compose_plan
[ -f "$PLAN" ] || die "no delivery plan at $PLAN"

jqi() { jq -r "$1" < "$INTENT"; }
jqp() { jq -r "$1" < "$PLAN"; }

REPO_SLUG="$(jqi '.repository.slug')"
REPO_ORIGIN="$(jqi '.repository.origin')"
DEFAULT_BRANCH="$(jqi '.repository.defaultBranch')"
NEED_MERGE="$(jqi '.policy.needMerge')"
NEED_CHECKS="$(jqi '.policy.needChecks')"

# ---------------------------------------------------------------------------
# Runtime facts, carried between stages.
#
# Each stage runs as its own process, so what city was built and which bead
# belongs to which package has to survive between them. This file is the run's
# scratch memory, not an authority: every fact in it is re-derivable, and the
# stages re-read the world rather than trusting it where the answer matters.
# ---------------------------------------------------------------------------

rt_init() { [ -f "$RUNTIME" ] || printf '{}\n' > "$RUNTIME"; }
rt_get() { rt_init; jq -r --arg k "$1" '.[$k] // empty' < "$RUNTIME"; }
rt_set() {
  rt_init
  local tmp; tmp="$(mktemp "$RUNTIME.XXXXXX")"
  jq --arg k "$1" --arg v "$2" '.[$k] = $v' < "$RUNTIME" > "$tmp" && mv -f "$tmp" "$RUNTIME" && return 0
  # A fact that was not recorded is a fact the next stage reads as absent, and
  # the stage that wrote it carries on believing it landed. The failure surfaces
  # an hour later as "no work bead for X" — in a stage that did nothing wrong.
  rm -f "$tmp"
  die "recording $1 in the run's runtime facts failed"
}

CITY="$(rt_get city)"
RIG_PATH="$(rt_get rigPath)"
RIG_NAME="$(rt_get rigName)"
RUN_TAG="$(rt_get runTag)"

# How many worker agents the last call to declare_worker_agents had to add. It
# is a count for the stage's own report, never a decision: what exists is read
# from the city, and what must exist is read from the effective plan.
DECLARED_WORKERS=0

export SA_CITY="$CITY"
export SA_RIG="$RIG_NAME"
export GC_WORK_RECORD_ENFORCE=1
sa_ledger_init "$EVIDENCE/gc-commands.log" 2>/dev/null || true

# Git credentials without writing a token to disk: the helper stores a COMMAND
# that asks the forge CLI for a token at the moment git needs one, so the
# credential stays where it already lives.
GIT_CRED_HELPER="!f() { echo username=x-access-token; echo \"password=\$(\"$GH\" auth token)\"; }; f"

packages() { jqp '.packages[].id'; }

# pkg_verifies_sha <id> — the merged commit a VERIFICATION package checks, or
# empty for an ordinary mutation package. Its presence is what distinguishes the
# two everywhere in this driver, so it is read from the plan rather than
# remembered anywhere.
pkg_verifies_sha() {
  jq -r --arg id "$1" '.packages[] | select(.id == $id) | .verifies.mergedSha // empty' < "$PLAN"
}

# pkg_is_verification <id> — true when this package checks existing evidence
# rather than producing work.
pkg_is_verification() { [ -n "$(pkg_verifies_sha "$1")" ]; }
pkg_field() { jq -r --arg id "$1" --arg f "$2" '.packages[] | select(.id == $id) | .[$f]' < "$PLAN"; }
pkg_paths_csv() { jq -r --arg id "$1" '[.packages[] | select(.id == $id) | .authorizedPaths[]] | join(",")' < "$PLAN"; }
pkg_deps() { jq -r --arg id "$1" '.packages[] | select(.id == $id) | .dependsOn[]?' < "$PLAN"; }
pkg_gates() { jq -r --arg id "$1" '.packages[] | select(.id == $id) | .gates[]?' < "$PLAN"; }

branch_for() { printf 'delivery/%s/%s' "$RUN_TAG" "$1"; }

# ===========================================================================
# STAGE city-up
# ===========================================================================

# A SUPERVISOR THAT EXISTS IS NOT A SUPERVISOR THAT WORKS.
#
# `gc init` registers the city and tries to start a supervisor; on a host that
# already has one it correctly declines, since only one may own the port, and
# leaves the city registered for the running one to pick up. Whether such a
# process exists is not the question this run needs answered — and the
# difference is not theoretical. A supervisor five days old held the API port
# with no control socket and no children, so `gc supervisor reload` could not
# reach it: the city was registered with a process that would never reconcile
# it, dispatch routed four packages to agents that were never spawned, and the
# run waited out its ninety-minute deadline for workers that did not exist.
#
# So the verdict is the supervisor's own answer to the one request this run
# actually needs honoured. Reconciling is also how a supervisor learns about a
# city registered after it started, which makes asking both the check and the
# step.
supervisor_must_reconcile() {
  if ! command "$GC" supervisor reload > "$EVIDENCE/supervisor-reload.txt" 2>&1 ||
     grep -qi 'not running\|unreachable' "$EVIDENCE/supervisor-reload.txt"; then
    stop_human "the machine-wide supervisor cannot be asked to reconcile this city, so its agents will never start: $(tr '\n' ' ' < "$EVIDENCE/supervisor-reload.txt")— restarting a machine-wide supervisor is a decision for its owner, not for this run"
  fi
  say 'the machine-wide supervisor reconciled this city'
}

# EVERY PACKAGE IN THE EFFECTIVE PLAN NEEDS A WORKER, INCLUDING THE ONES
# AUTHORIZED AFTER THE CITY WAS BUILT.
#
# The effective plan is the plan plus its remediations, recomposed on every
# invocation precisely so that no two stages can read different plans. Declaring
# the workers was not: it happened once, in the branch that builds a new city,
# below the early return a resumed run takes. So corrective work authorized
# after the city was built added packages that dispatch would route — to agents
# that had never been declared. `gc sling` refused with `agent
# "<rig>/worker-wp-remediate-architecture-artifact" not found in city.toml`, the
# dispatch stage exhausted its attempts, and two authorized remedial packages
# could not be executed at all. Dispatch already learned this lesson for beads;
# this is the same lesson for the agents the beads are routed to.
#
# So declaring workers is RECONCILIATION, not construction: asked of the
# effective plan every time this stage runs, and answered only for what is
# missing. An agent already declared is left exactly as it is — a worker's
# declaration is not rewritten underneath a delivery that is part-way through
# it, which is the same reason dispatch does not re-route a closed bead. The
# verification below is what makes this a verdict rather than a hope, and it is
# asked about every package, not only the ones this call added.
declare_worker_agents() {
  # One single-capacity, rig-scoped agent per work package. Distinct agents are
  # the only configuration that yields distinct worktrees: the work_dir template
  # surface carries no per-slot variable, so an unbounded pool would resolve
  # every concurrent slot to one directory. They remain pure configuration —
  # no role name appears in Go.
  local prompt; prompt="$(sa_pool_worker_prompt)"
  local id agent wt added=0
  for id in $(packages); do
    agent="worker-$id"
    if [ -f "$CITY/agents/$agent/agent.toml" ]; then
      continue
    fi
    wt="$CITY/.gc/worktrees/$RIG_NAME/$agent"
    sa_declare_worker_agent "$CITY" "$RIG_NAME" "$RIG_PATH" "$agent" "$wt" "$prompt"
    # bounded-project is the opt-in that gives a worker Read/Write/Edit and the
    # project's named gates, and denies it git, gh and the shell family.
    # Publication authority stays with the controller.
    printf '\n[option_defaults]\npermission_mode = "bounded-project"\n' >> "$CITY/agents/$agent/agent.toml"
    added=$(( added + 1 ))
  done

  # The `|| true` is not suppression: the verdict is the resolved configuration
  # below, and a config command that failed produces a file the greps refuse.
  gcx config show > "$EVIDENCE/config-show.txt" 2>&1 || true
  for id in $(packages); do
    grep -qE "^name = \"worker-$id\"$" "$EVIDENCE/config-show.txt" \
      || die "agent worker-$id did not load; see $EVIDENCE/config-show.txt"
  done
  grep -q 'bounded-project' "$EVIDENCE/config-show.txt" \
    || die 'the bounded-project selection did not survive config resolution'

  DECLARED_WORKERS="$added"
  return 0
}

stage_city_up() {
  if [ -n "$CITY" ] && [ -f "$CITY/city.toml" ]; then
    say "city already built at $CITY"
    # Still asked, and asked first: a resumed run inherits the city but not the
    # supervisor that was answering when it was built. Reconciling is idempotent
    # and is the one thing this stage promises downstream — that the agents
    # dispatch is about to route work to will actually be started.
    supervisor_must_reconcile
    # And the agents themselves, asked of the EFFECTIVE plan: a remediation
    # authorized since this city was built has packages dispatch will route.
    # SA_CITY and SA_RIG are already exported from the same runtime facts this
    # branch read $CITY from, so the scope is the resumed run's own.
    declare_worker_agents
    say "city reconciled: $DECLARED_WORKERS worker agent(s) added, $(packages | wc -w) declared in total"
    return 0
  fi

  RUN_TAG="$(date -u +%Y%m%dT%H%M%SZ)"
  CITY="$STATE/city-$RUN_TAG"
  RIG_PATH="$STATE/rig-$RUN_TAG"
  RIG_NAME="rig-$RUN_TAG"

  # THE RIG IS A WORKING CLONE, NEVER THE REGISTERED CHECKOUT.
  #
  # `gc rig add` writes a bead store, runtime state and a .gitignore into its
  # rig and makes its own commit. Pointing that at the checkout the portal
  # registered would mutate the user's own working copy. The clone is of the
  # same remote, so pull requests, checks and merges are real and land on the
  # real repository, while the registered checkout is never touched.
  say "cloning $REPO_SLUG into a working rig"
  git -c credential.helper="$GIT_CRED_HELPER" clone -q "$REPO_ORIGIN" "$RIG_PATH" \
    > "$EVIDENCE/clone.txt" 2>&1 \
    || die_from "$EVIDENCE/clone.txt" "cloning $REPO_SLUG: $(tail -2 "$EVIDENCE/clone.txt")"
  git -C "$RIG_PATH" config user.name 'Gas City Controller'
  git -C "$RIG_PATH" config user.email 'support@corsolv.com'
  git -C "$RIG_PATH" config credential.helper "$GIT_CRED_HELPER"

  say "initializing the city at $CITY"
  command "$GC" init "$CITY" --provider "$PROVIDER" --yes > "$EVIDENCE/init.txt" 2>&1
  # The exit code is not the verdict here, and treating it as one is wrong on
  # exactly the machines this is meant to run on. `gc init` also tries to start
  # the machine-wide supervisor, and on a host that already has one it reports a
  # non-zero exit for a condition that is not merely benign but correct — only
  # one supervisor may run per machine, and the city is registered with the one
  # already there. So the verdict is the state on disk, not the status code.
  if [ ! -f "$CITY/city.toml" ]; then
    die "the city was not created; see $EVIDENCE/init.txt"
  fi
  # A city on disk is not yet a city that works.
  #
  # `gc init` runs eight steps and can stop at the sixth — a provider it cannot
  # resolve halts it after the city exists but before its pack imports are
  # installed. On disk that reads as success, and the run then spends four
  # minutes failing at something else entirely: every later gc command refuses
  # with a missing packs.lock, so the rig's bead store "never became ready"
  # although it had been initialized correctly. The verdict is gc's own.
  if ! command "$GC" --city "$CITY" import check > "$EVIDENCE/import-check.txt" 2>&1; then
    die "the city was created but is not usable: $(tail -3 "$EVIDENCE/import-check.txt" | tr '\n' ' '); see $EVIDENCE/init.txt"
  fi
  supervisor_must_reconcile

  # A worker may not close a bead with an unearned disposition. `shipped`
  # requires a reachable commit, which this policy withholds from workers.
  cat >> "$CITY/city.toml" <<'TOML'

[workspace.env]
GC_WORK_RECORD_ENFORCE = "1"
TOML

  mkdir -p "$CITY/scripts"
  install -m 755 "$SOURCE_REPO/corsolv/p2-smoke/scripts/worktree-setup.sh" "$CITY/scripts/worktree-setup.sh"

  export SA_CITY="$CITY" SA_RIG="$RIG_NAME"
  cd "$CITY" || die "cannot enter $CITY"

  gcx rig add "$RIG_PATH" > "$EVIDENCE/rig-add.txt" 2>&1 || die "gc rig add failed"
  sa_wait_rig_beads "$CITY" "$RIG_NAME" 240 || die "the rig's bead store never became ready"
  say "rig bead store ready"

  declare_worker_agents

  rt_set city "$CITY"
  rt_set rigPath "$RIG_PATH"
  rt_set rigName "$RIG_NAME"
  rt_set runTag "$RUN_TAG"
  rt_set baseSha "$(git -C "$RIG_PATH" rev-parse "$DEFAULT_BRANCH")"
  say "city up: $(packages | wc -w) worker agent(s) declared"
}

# ---------------------------------------------------------------------------
# Worker liveness — re-derived, never remembered.
#
# `dispatched` is durable HISTORY: it records that routing happened once, and
# it stays true forever. Treating it as a statement about now — "a worker
# exists and owns every still-open bead" — is false the moment anything is
# interrupted, and that falsehood is what a resumed run acted on: it skipped
# dispatch, started no worker, and then waited out its whole deadline on a bead
# nobody was holding.
#
# So liveness is a question, asked of the things that actually know the answer:
# the bead store for whether the work is still open, and Gas City's own session
# list for whether a worker is running. Nothing new is written down.
# ---------------------------------------------------------------------------

# WORKER_LIVE_STATES are the session states in which a worker is running, or is
# on its way to running. Every other state — asleep, suspended, draining,
# drained, failed-create, quarantined, archived, closed — means no process is
# doing the work, whatever the session record remembers about having started.
#
# `gc session list` is a live read, not a stored one: it asks the runtime
# provider whether the session is really running and downgrades a stale
# `active` to `asleep` when it is not. That is what makes it the authority here
# rather than another thing to distrust.
WORKER_LIVE_STATES='active awake start-pending creating'

# worker_is_live <package-id> — true when Gas City reports a running session for
# this package's worker agent.
#
# One agent per package is the dispatch design, so the agent IS the ownership
# link: a live session for worker-<id> is a worker holding that package's work.
worker_is_live() {
  local agent="worker-$1" json
  json="$(gcx session list --json 2>> "$EVIDENCE/session-list.err" || true)"
  # A session list that cannot be read is not an answer, and the caller treats
  # "no answer" as "no worker" — which routes a bead that may already have one.
  # That is the safe direction to be wrong in, and it is said out loud rather
  # than swallowed, because a run that silently duplicates work looks identical
  # to one that correctly recovered it.
  [ -n "$json" ] || {
    say "$1: Gas City could not be asked which sessions are running; treating the worker as absent"
    return 1
  }
  jq -e --arg a "$agent" --arg live "$WORKER_LIVE_STATES" '
      ($live | split(" ")) as $ok
      | (.sessions // [])
      | map(select(
          ((.template // "") == $a)
          or ((.template // "") | endswith("/" + $a))
          or ((.agent_name // "") == $a)))
      | map(select((.closed // false) | not))
      | map(select((.state // "") as $s | ($ok | index($s)) != null))
      | length > 0
    ' <<<"$json" >/dev/null 2>&1
}

# recover_worker <package-id> — put a worker back on work that still needs one.
#
# The three cases, decided from re-read state and nothing else:
#
#   bead closed                    the work is done; replaying it would hand a
#                                  worker a bead it cannot act on
#   bead open, worker live         someone is on it; a second sling would be a
#                                  duplicate
#   bead open, no worker           the orphan case — route it again, through the
#                                  same sling dispatch used in the first place
#
# A package whose upstreams have not merged is a fourth case that is not an
# orphan: it has no worktree because it has no base yet, and publish cuts one
# and starts its worker then. Routing it here would put a worker somewhere that
# does not exist.
recover_worker() {
  local id="$1" bead wt
  bead="$(rt_get "bead.$id")"
  [ -n "$bead" ] || return 0

  if bead_is_closed "$bead"; then
    return 0
  fi

  wt="$(rt_get "wt.$id")"
  if [ -z "$wt" ] || [ ! -d "$wt" ]; then
    say "$id: no worktree yet; its worker starts when its upstreams have merged"
    return 0
  fi

  if worker_is_live "$id"; then
    say "$id: a live worker still holds bead $bead"
    return 0
  fi

  # A fifth case, and the same rule as the fourth: a tree cut from a base the
  # default branch has moved past is not somewhere this work can be done, so
  # routing a worker into it would start someone with no project to work on.
  # ensure_package_worktree re-cuts it and routes it there, exactly as it does
  # for a package reaching its base for the first time — and routing here as
  # well would sling the same bead twice.
  if worktree_base_is_stale "$id" "$wt"; then
    say "$id: its tree was cut from a base the default branch has moved past; it is re-cut and routed when its stage begins"
    return 0
  fi

  say "$id: bead $bead is open and no worker holds it; routing it again"
  sa_ledger_note "recovering $id: re-routing open bead $bead with no live worker"
  route_bead "$id" "$bead"
}

# ===========================================================================
# STAGE dispatch
# ===========================================================================

# The lifecycle sentence every worker is given. It states the two things a
# bounded worker must know and cannot discover: that it has no git, and that
# claiming `shipped` is not available to it.
worker_lifecycle() {
  printf '%s' 'Verify your change with the project'"'"'s own gates before closing. You cannot run git; the controller publishes. Close the assigned bead with gc.work_outcome=blocked plus a gc.work_blocked_reason; do NOT claim shipped.'
}

# mk_bead <title> [description]
#
# The title is a LABEL and the description carries the work. A real work
# package's objective is several sentences — it has to be, since the worker
# reading it has no other context — and bead titles are capped at 500
# characters. Putting the objective in the title made the cap a limit on how
# precisely work could be described, which is exactly backwards.
mk_bead() {
  local title="$1" description="${2:-}" out rc
  if [ "${#title}" -gt 500 ]; then
    printf 'driver: bead title is %s chars, over the 500 limit\n' "${#title}" >&2
    return 1
  fi
  if [ -n "$description" ]; then
    out="$(gcx bd create "$title" --description "$description" --silent 2>&1)"; rc=$?
  else
    out="$(gcx bd create "$title" --silent 2>&1)"; rc=$?
  fi
  [ "$rc" -eq 0 ] || { printf 'driver: creating bead: %s\n' "$(tail -1 <<<"$out")" >&2; return 1; }
  tail -1 <<<"$out"
}

# stamp_bead applies a controller stamp that must actually land.
#
# THE SUPPRESSION THIS REPLACES. Every stamp was written with its output and its
# exit status both discarded. Publication is adjudicated against these stamps —
# the authorized scope, the required artifact, the work directory a worker is
# started in — so a stamp that silently did not land is a package the run
# believes it described and did not. The refusal is invisible at the moment it
# happens and surfaces an hour later as a publication that cannot find what it
# is meant to check, or as a worker with nowhere to work.
stamp_bead() {
  local bead="$1" what="$2"
  shift 2
  gcx bd update "$bead" "$@" >> "$EVIDENCE/stamp-$bead.txt" 2>&1 \
    || die "stamping $what on bead $bead: $(tail -2 "$EVIDENCE/stamp-$bead.txt" | tr '\n' ' ')"
}

# wire_dep makes one bead block another, and refuses to carry on if it did not.
#
# The dependency edge is what makes a dependent package wait for repository
# state instead of for a sibling worker's filesystem. An edge that was silently
# not wired is a worker started against a base its upstream has not merged into,
# which is the one thing the whole dependency design exists to prevent.
wire_dep() {
  local blocker="$1" blocked="$2" what="$3"
  gcx bd dep "$blocker" --blocks "$blocked" >> "$EVIDENCE/deps.txt" 2>&1 \
    || die "wiring $what ($blocker blocks $blocked): $(tail -2 "$EVIDENCE/deps.txt" | tr '\n' ' ')"
}

# route_bead hands a package's work to its worker.
#
# THE SUPPRESSION THIS REPLACES. A routing failure was reported with `say` and
# the stage carried on to report success. Routing is the only thing that starts
# a worker, so a package that was not routed has nobody doing its work — and the
# next stage waits out its entire deadline for a worker that was never asked to
# exist. A failure here is a failure of the stage.
route_bead() {
  local id="$1" bead="$2"
  gcx sling "$RIG_NAME/worker-$id" "$bead" --no-formula --no-convoy \
    >> "$EVIDENCE/route-$id.txt" 2>&1 \
    || die "routing $id to its worker: $(tail -2 "$EVIDENCE/route-$id.txt" | tr '\n' ' ')"
}

stage_dispatch() {
  [ -n "$CITY" ] || die 'no city; city-up has not run'
  cd "$CITY" || die "cannot enter $CITY"

  # Already dispatched means the beads exist, their dependencies are wired and
  # their scope is stamped — none of which is created twice. It does NOT mean
  # the work is still being done. This is the stage a resumed run re-enters, so
  # it is where reality is re-derived: every package that is still open with no
  # worker holding it gets one, through the routing that put it there first.
  #
  # DISPATCH IS PER PACKAGE, NOT ALL-OR-NOTHING. It used to short-circuit on a
  # single `dispatched` flag, which was right while a plan could never grow.
  # A remediation adds corrective work to a delivery that has already
  # dispatched, and the flag sent this stage straight past it: the remedial
  # package got no work bead, and its publication died an hour later on "no work
  # bead for wp-3-fix" — in a stage that had done nothing wrong.
  #
  # So the question is asked of each package: one with a bead is re-derived, one
  # without is created. The flag stays, because it still records that routing
  # happened once, and `wire_dep` and the scope stamps are still applied exactly
  # once per package — the three passes below run over $fresh, which is the
  # packages that have nothing yet.
  local pkg fresh=''
  for pkg in $(packages); do
    # A VERIFICATION PACKAGE IS NOT DISPATCHED, BECAUSE THERE IS NOBODY TO
    # DISPATCH IT TO. It checks evidence already on the authoritative branch: no
    # work bead, because no work is being asked for; no merge bead, because
    # nothing will be merged; no worktree here, because its own stage cuts a
    # detached one at the exact commit it verifies; and no worker, because a
    # worker's job is to change files and this package may change none.
    #
    # Creating them anyway would be the dishonest shape this whole mechanism
    # exists to avoid — a bead nobody can close by doing anything, waiting for a
    # publication that has nothing to publish.
    if pkg_is_verification "$pkg"; then
      say "$pkg verifies existing evidence; it is not routed to a worker"
      continue
    fi
    if [ -n "$(rt_get "bead.$pkg")" ]; then
      recover_worker "$pkg"
    else
      fresh="$fresh $pkg"
    fi
  done
  if [ -n "$(rt_get dispatched)" ] && [ -z "${fresh// /}" ]; then
    say 'work was already dispatched; re-derived which of it still needs a worker'
    return 0
  fi
  if [ -n "$(rt_get dispatched)" ]; then
    say "dispatching corrective work added since:${fresh}"
  fi

  # THE BASE IS READ NOW, NOT REMEMBERED FROM WHEN THE DELIVERY BEGAN.
  #
  # `baseSha` is what the default branch was when city-up cloned the rig. On a
  # first run that IS the merged head, so cutting from either produces the same
  # tree — which is why the difference stayed invisible for as long as a plan
  # could only be dispatched once.
  #
  # It stops being the same the moment a plan grows. Corrective work authorized
  # after a delivery completed has no upstreams, so it takes the branch below
  # that cuts immediately — and cutting it from `baseSha` hands it the repository
  # as it stood BEFORE any of the delivery's own work merged. On
  # scorm-course-studio that was 9 files against main's 177: no package.json, no
  # src, and none of the merged evidence the criterion had been disproved
  # against. The package's declared gates could not run at all
  # (`npm error enoent Could not read package.json`) and its required artifact
  # could not be produced, so two authorized remedial packages failed for a
  # reason that had nothing to do with the repair they were asked to make.
  #
  # A criterion is invalidated against the evidence of the CURRENT merged branch,
  # so the work that repairs it must be based there too. Reading the head at the
  # moment of the cut is the rule ensure_package_worktree already states, and it
  # needs no conditional to tell a first run from a resumed one: on a first run
  # it resolves to exactly what `baseSha` records. `baseSha` stays recorded as
  # the historical fact it is, and stops being mistaken for a current one.
  local base; base="$(merged_head)"
  [ -n "$base" ] || die 'cannot read the authoritative merged branch to cut work from'
  local id bead mergeBead objective artifact paths wt branch dep

  # Pass 1: create every work bead and its controller merge bead, and stamp the
  # scope each one authorizes. Publication is adjudicated against these stamps,
  # so they exist before any worker starts.
  for id in $fresh; do
    objective="$(pkg_field "$id" objective)"
    artifact="$(pkg_field "$id" artifact)"
    paths="$(pkg_paths_csv "$id")"

    bead="$(mk_bead "$(pkg_field "$id" title)" "$objective

$(worker_lifecycle)")" || die "creating the work bead for $id"
    mergeBead="$(mk_bead "Controller publishes and merges $id ($bead)")" \
      || die "creating the merge bead for $id"

    stamp_bead "$bead" 'the required artifact' --set-metadata "gc.required_artifact=$artifact"
    stamp_bead "$bead" 'the authorized paths' --set-metadata "gc.authorised_paths=$paths"
    stamp_bead "$bead" 'the delivery package' --set-metadata "gc.delivery_package=$id"
    stamp_bead "$mergeBead" "the controller's ownership" -a 'corsolv-controller' -s in_progress

    rt_set "bead.$id" "$bead"
    rt_set "merge.$id" "$mergeBead"
    say "$id -> work bead $bead, merge bead $mergeBead"
  done

  # Pass 2: dependencies. A package depends on its upstreams being MERGED, not
  # merely closed, so the edge runs from the upstream's MERGE bead. That is what
  # makes a dependent package wait for repository state rather than for a
  # sibling worker's filesystem.
  for id in $fresh; do
    bead="$(rt_get "bead.$id")"
    mergeBead="$(rt_get "merge.$id")"
    wire_dep "$bead" "$mergeBead" "$id's work before its publication"
    for dep in $(pkg_deps "$id"); do
      wire_dep "$(rt_get "merge.$dep")" "$bead" "$dep's merge before $id's work"
      say "$id waits for $dep to merge"
    done
  done

  # Pass 3: worktrees and routing. A package with upstreams gets no worktree
  # yet — its base does not exist until those upstreams merge — and the publish
  # stage cuts it then.
  for id in $fresh; do
    bead="$(rt_get "bead.$id")"
    branch="$(branch_for "$id")"
    wt="$CITY/.gc/worktrees/$RIG_NAME/worker-$id"

    if [ -z "$(pkg_deps "$id")" ]; then
      wt_add "$RIG_PATH" "$wt" "$branch" "$base" || die "creating the worktree for $id"
      wt_is_registered "$RIG_PATH" "$wt" || die "the worktree for $id is not registered"
      prepare_worktree "$wt" "$id"
    fi
    stamp_bead "$bead" 'the work directory' --set-metadata "work_dir=$wt"
    rt_set "wt.$id" "$wt"
    rt_set "branch.$id" "$branch"
  done

  # Pass 4: route, and only what has somewhere to work.
  #
  # THE DEFECT THIS EXISTS FOR. Routing every package here routed the ones whose
  # upstreams had not merged — packages that deliberately have no worktree yet.
  # A routed bead is Gas City's to act on, so when the upstream merged and the
  # bead opened, Gas City cut a worktree OF ITS OWN, on a branch of its own
  # naming, and woke a worker in it. The controller's own preparation — this
  # package's gate grant, and its dependency install — is done when the
  # controller cuts the tree, and the tree already existed, so none of it ever
  # reached that worker. It was left deny-by-default with an instruction to
  # verify itself, could run neither `npm` nor `node`, and closed `blocked`
  # having written two of its four files and proved nothing. Which is the right
  # answer to an impossible instruction, and a lost package.
  #
  # `recover_worker` has stated the rule all along — a package with no worktree
  # is waiting, not orphaned, and routing it "would put a worker somewhere that
  # does not exist". Dispatch is where that rule was not applied.
  # `ensure_package_worktree` routes each of these the moment it cuts and
  # prepares its tree, so nothing is left unrouted, only routed later.
  #
  # And only what this dispatch CREATED. A package that already had a bead was
  # re-derived by `recover_worker` above, which asks whether its work is still
  # open before it routes anything; routing it here as well would sling a closed
  # bead — finished work handed back to a worker because a later dispatch added
  # a package somewhere else in the plan.
  for id in $fresh; do
    if [ -n "$(pkg_deps "$id")" ]; then
      say "$id has no base yet; it is routed when its upstreams merge and its tree is cut"
      continue
    fi
    route_bead "$id" "$(rt_get "bead.$id")"
  done

  rt_set dispatched "$(date -u +%FT%TZ)"
  say 'dispatch complete'
}

# prepare_worktree installs whatever the project needs before a worker can run
# its gates.
#
# The detection is the ENGINE's judgement about this host's toolchains, not the
# portal's and not the plan's — which is why it lives here and not in anything
# either of them can write. A project whose manifest is not recognized simply
# gets no preparation, and its gate authority is the required CI run.
prepare_worktree() {
  local wt="$1" id="$2"
  install_package_gates "$wt" "$id"
  if [ -f "$wt/package.json" ]; then
    run_project_command "$wt" npm ci --silent > "$EVIDENCE/prepare-$id.txt" 2>&1 \
      || run_project_command "$wt" npm install --silent >> "$EVIDENCE/prepare-$id.txt" 2>&1 \
      || say "dependency install for $id reported a problem; see prepare-$id.txt"
  fi
}

# install_package_gates grants this package's worker exactly the verification
# commands its package declared, and nothing else.
#
# THE DEFECT THIS EXISTS FOR. A bounded worker is deny-by-default: its launch
# carries an explicit allowlist and `--permission-mode dontAsk`, so anything not
# on the list is refused with "Permission to use Bash has been denied". The
# scaffold package told its worker to prove itself with `npm install && npm run
# verify`; neither was on the list, and the worker — correctly — closed
# `blocked` rather than claiming work it could not verify. An instruction a
# worker is structurally forbidden from obeying is not a gate.
#
# WHY HERE AND NOT IN THE PERMISSION MODE. `bounded-project` is a fleet-wide
# constant, and widening it would grant every worker on every project whatever
# one package needed. This grant lives in the worker's OWN worktree, which is
# the one directory that belongs to exactly one package, so one package's gates
# are not another's and nothing is widened for anyone else. Claude Code composes
# a project-local allow list with the launch allowlist, which is what makes a
# narrower, per-package grant possible at all.
#
# The content comes from the validated plan through jq rather than through the
# shell: `handoff.ValidateGate` has already refused anything that is not a bare
# project runner, and nothing here re-interprets it.
install_package_gates() {
  local wt="$1" id="$2"
  local settings="$wt/.claude/settings.local.json"
  mkdir -p "$wt/.claude" || die "creating the permission directory for $id"
  jq --arg id "$id" \
    '{permissions: {allow: [.packages[] | select(.id == $id) | .gates[]? | "Bash(" + . + ":*)"]}}' \
    < "$PLAN" > "$settings" || die "granting the declared gates for $id"
  cp -f "$settings" "$EVIDENCE/gates-$id.json" 2>/dev/null || true
  local granted; granted="$(pkg_gates "$id" | tr '\n' ',' | sed 's/,$//')"
  if [ -n "$granted" ]; then
    say "$id may run its declared gates: $granted"
  else
    say "$id declared no gates; its worker may run none"
  fi
}

# ensure_package_worktree gives a package the working tree its worker needs,
# cut from the base as it stands NOW that its upstreams have merged.
#
# A package with upstreams gets no worktree at dispatch, because the base it
# must build on does not exist yet: its upstreams' work reaches it through
# repository state, never by reading a sibling worker's files. So the cut
# happens at the moment the package becomes workable, which is when the stage
# that waits for it begins — and the bead is re-slung there, because an agent
# whose work directory did not exist when it was first routed has nowhere to
# have started.
# merged_head — the authoritative default branch AS IT STANDS NOW.
#
# Asked of the remote every time rather than cached, because the whole point of
# it is to be current: between one stage and the next this run merges its own
# work, and a package cut after that must be cut on top of it.
merged_head() {
  git -C "$RIG_PATH" fetch -q origin "$DEFAULT_BRANCH" 2>/dev/null
  git -C "$RIG_PATH" rev-parse "refs/remotes/origin/$DEFAULT_BRANCH" 2>/dev/null
}

# worktree_base_is_stale <id> <worktree> — true when this tree was cut from a
# base the default branch has since moved past, AND re-cutting it would destroy
# nothing.
#
# Every clause is a refusal to touch something that matters:
#
#   the bead is closed      finished work was based correctly when it was done,
#                           and its tree is not this stage's business any more.
#   a worker is live        a running worker's base is never moved underneath
#                           it. Liveness is asked of Gas City, not remembered.
#   the tip is not merged   anything this branch carries that the default branch
#                           does not is WORK, and work is never discarded. Only
#                           a branch that has contributed nothing may be re-cut.
#
# What is left is the case this exists for: an open package, nobody working it,
# on a branch that is exactly some older state of the default branch.
worktree_base_is_stale() {
  local id="$1" wt="$2" bead tip head
  bead="$(rt_get "bead.$id")"
  if [ -n "$bead" ] && bead_is_closed "$bead" 2>/dev/null; then
    return 1
  fi
  if worker_is_live "$id"; then
    return 1
  fi
  tip="$(git -C "$wt" rev-parse HEAD 2>/dev/null)"
  head="$(merged_head)"
  [ -n "$tip" ] && [ -n "$head" ] || return 1
  [ "$tip" = "$head" ] && return 1
  git -C "$RIG_PATH" merge-base --is-ancestor "$tip" "$head" 2>/dev/null || return 1
  return 0
}

# recut_worktree <id> <worktree> <branch> <bead> — replace an unusable tree with
# one cut from the merged head, and route the bead at it.
#
# It is announced, never silent: what is thrown away is counted and named in the
# run's own log, because a tree cut from the wrong base can still have had a
# worker write files into it — and those files were written against a repository
# that was missing the project.
recut_worktree() {
  local id="$1" wt="$2" branch="$3" bead="$4" head loose
  head="$(merged_head)"
  [ -n "$head" ] || die "cannot read the authoritative merged branch to re-cut $id from"
  loose="$(git -C "$wt" status --porcelain 2>/dev/null | wc -l | tr -d ' ')"
  if [ "${loose:-0}" -gt 0 ]; then
    say "$id: discarding $loose uncommitted path(s) written against a base that had no project in it"
  fi
  git -C "$RIG_PATH" worktree remove --force "$wt" > "$EVIDENCE/recut-$id.txt" 2>&1 \
    || die_from "$EVIDENCE/recut-$id.txt" "removing the stale worktree for $id"
  git -C "$RIG_PATH" branch -D "$branch" >> "$EVIDENCE/recut-$id.txt" 2>&1 || true
  wt_add "$RIG_PATH" "$wt" "$branch" "$head" || die "re-cutting the worktree for $id"
  wt_is_registered "$RIG_PATH" "$wt" || die "the re-cut worktree for $id is not registered"
  prepare_worktree "$wt" "$id"
  say "re-cut $id from the merged base ${head:0:9} — its worker runs now"
  route_bead "$id" "$bead"
}

ensure_package_worktree() {
  local id="$1"
  local wt branch bead mergedBase
  wt="$(rt_get "wt.$id")"
  branch="$(rt_get "branch.$id")"
  bead="$(rt_get "bead.$id")"
  [ -n "$wt" ] && [ -n "$branch" ] || die "no worktree recorded for $id; dispatch has not run"

  # A tree that is already there is not necessarily a tree this controller cut.
  # Gas City cuts one for a routed bead it is asked to act on, and a run started
  # before routing waited for the base could arrive here with exactly that. The
  # preparation is what a worker cannot do for itself and is idempotent, so it
  # is applied to whatever tree is in hand rather than only to one this stage
  # created — otherwise the grant reaches every package except the ones that
  # lost the race for their own directory.
  if [ -d "$wt" ]; then
    # But a tree that is there is not necessarily a tree the work can be DONE
    # in. One cut from a base the default branch has moved past is missing the
    # project itself, and preparing it only installs gates its worker can never
    # run. That is reconciled here rather than discovered by the gate.
    if worktree_base_is_stale "$id" "$wt"; then
      recut_worktree "$id" "$wt" "$branch" "$bead"
      return 0
    fi
    prepare_worktree "$wt" "$id"
    return 0
  fi

  mergedBase="$(merged_head)"
  [ -n "$mergedBase" ] || die "cannot read the authoritative merged branch to cut $id from"
  wt_add "$RIG_PATH" "$wt" "$branch" "$mergedBase" || die "creating the worktree for $id"
  wt_is_registered "$RIG_PATH" "$wt" || die "the worktree for $id is not registered"
  prepare_worktree "$wt" "$id"
  say "cut $id from the merged base ${mergedBase:0:9} — its worker runs now"
  route_bead "$id" "$bead"
}

# ===========================================================================
# STAGE await
# ===========================================================================

# awaited_packages are the packages this stage is actually responsible for.
#
# A package whose upstreams have not merged has no worktree yet and its bead is
# correctly blocked — publish cuts its base and waits for it there. Counting it
# here made the deadline the NORMAL outcome for any delivery with a dependent
# package, which is precisely why expiring quietly looked survivable.
awaited_packages() {
  local id
  for id in $(packages); do
    [ -n "$(pkg_deps "$id")" ] || printf '%s\n' "$id"
  done
}

stage_await() {
  [ -n "$CITY" ] || die 'no city; city-up has not run'
  cd "$CITY" || die "cannot enter $CITY"

  local deadline=$(( $(date +%s) + DEADLINE ))
  local id bead remaining

  # WAIT FOR THE PACKAGE THIS STAGE IS FOR.
  #
  # Waiting for every package at once cannot succeed on a plan whose packages
  # depend on each other: a dependent package's work bead does not open until
  # its upstream's merge bead closes, and that happens in `publish`, which runs
  # after this stage. The compiler now schedules one await per package, so this
  # waits for that one — and a run given no package still waits for all of
  # them, which is the correct meaning for a single-package plan.
  local waitFor
  if [ -n "$PACKAGE" ]; then
    waitFor="$PACKAGE"
  else
    waitFor="$(awaited_packages)"
  fi

  # Waiting for a worker that has nowhere to work is how a stage burns its
  # whole deadline and reports the wrong thing. The tree comes first.
  for id in $waitFor; do
    ensure_package_worktree "$id"
  done

  while true; do
    remaining=''
    for id in $waitFor; do
      bead="$(rt_get "bead.$id")"
      [ -n "$bead" ] || continue
      bead_is_closed "$bead" || remaining="$remaining $id"
    done
    [ -z "$remaining" ] && { say "every work bead this stage waits for is closed:$(printf ' %s' $waitFor)"; return 0; }

    if [ "$(date +%s)" -ge "$deadline" ]; then
      # A deadline is not an outcome. Reporting success here said the wait had
      # succeeded when the work it waited for was still open, and publication
      # then refused on a missing artifact — naming the wrong thing, hours after
      # the real event, with the run's own record claiming the stage passed.
      #
      # Nor is it a failure. Nothing was proved wrong: the work simply is not
      # finished, which is the same shape as a harness stopping an agent at its
      # turn cap. Stated as CONTINUE it costs no retry budget, and the run
      # re-offers this stage under the bounded resume budget its task declared —
      # re-deriving on the way back in which packages still have no worker. A
      # stage that never converges is HELD for a person rather than failed,
      # because what to do about work that did not finish is a person's call.
      not_finished "the work this stage waits for is still open:$remaining"
      return 1
    fi
    sleep 15
  done
}

# send_back_to_worker returns a package whose pull request failed required CI.
#
# THE DEAD END THIS REPLACES. A package that reached the forge and failed the
# repository's required check ended the run. It was reported as FAILED, which is
# the one thing it is not: nothing about the platform went wrong, and nothing
# was proved impossible — a worker wrote code that does not pass the gate it is
# judged by, which is the ordinary condition of writing code.
#
# The dead end was total. The work bead was closed, so a resumed dispatch left
# it alone; the branch already carried the controller's commit, so a retried
# publication died at `git commit` with nothing to commit — reporting a commit
# problem for a CI failure, which is the wrong cause as well as the wrong stage.
# Every route back to a worker was shut, and the only remaining move was a
# person editing the project's source by hand. A controller that writes the code
# is forging the evidence it later checks, so that move is not available.
#
# The pilot found it on its first package: `wp-foundation` declared an npm
# `test` script for a directory a later package creates, passed its own declared
# gates, and failed the repository's CI on `Could not find 'test/'`.
#
# So the failure goes back where it can be acted on. The verdict is appended to
# the work bead — a worker reads its bead and nothing else, so a failure not
# written there is a failure it cannot see — the bead is reopened, and the stage
# says CONTINUE. That is already a state this driver knows: the re-offered
# publication finds an open bead, routes a worker to it through the same
# recovery an interrupted run uses, waits for it to close, and republishes.
#
# It is bounded by the run's own resume budget rather than by a counter here. A
# package that never converges is HELD for a person, which is the correct end
# for work that repeatedly cannot pass its gate.
send_back_to_worker() {
  local id="$1" bead="$2" prNum="$3"
  local why; why="$(tail -3 "$EVIDENCE/ci-$id.err" 2>/dev/null | tr '\n' ' ')"

  return_to_worker "$id" "$bead" \
    "Required CI failed on PR #$prNum for this package's head. \
The work is not accepted and this package is open again. Read the failing run on the pull request, \
fix the cause inside this package's authorized paths, and verify with this package's declared gates \
before finishing. ${why}" \
    "failed required CI on PR #$prNum" \
    "did not pass required CI on its exact head"
}

# return_to_worker reopens a package's work bead with a verdict written on it,
# and reports the stage unfinished so the run re-offers it.
#
# THE DEAD END THIS GENERALIZES. `send_back_to_worker` was written for one
# verdict — a red required check — because that is the one the first pilot hit.
# The dead end it describes was never specific to CI:
#
#   the run reports FAILED, which is the one thing it is not — a worker wrote
#   code that does not pass its gate, the ordinary condition of writing code;
#   the work bead is closed, so a resumed dispatch leaves it alone, since
#   closed work is finished work; and every route back to a worker is shut, so
#   the only remaining move is a person editing the project's source by hand.
#
# A package that fails the project's OWN gates reaches exactly that state, and
# reached it in this project: the controller re-ran the declared gates, refused
# to publish unproven work — correctly — and then `die`d, closing the run with a
# closed bead and no worker to send the verdict to. The refusal was right and
# the ending was the dead end.
#
# So the verdict goes where a worker can act on it. The reason is appended to
# the work bead — a worker reads its bead and nothing else, so a failure not
# written there is a failure it cannot see — the bead is reopened, and the stage
# says CONTINUE. The re-offered stage finds an open bead, routes a worker
# through the same recovery an interrupted run uses, waits for it to close, and
# tries again. It is bounded by the run's own resume budget, so a package that
# never converges is HELD for a person, which is the correct end for work that
# repeatedly cannot pass its gate.
return_to_worker() {
  local id="$1" bead="$2" note="$3" ledger="$4" unfinished="$5"

  gcx bd update "$bead" --append-notes "$note" >> "$EVIDENCE/sendback-$id.txt" 2>&1 \
    || say "$id: the verdict could not be appended to bead $bead; it is reopened without it"

  gcx bd update "$bead" -s open >> "$EVIDENCE/sendback-$id.txt" 2>&1 \
    || die_from "$EVIDENCE/sendback-$id.txt" \
      "$id $unfinished and its work bead $bead could not be reopened, so no worker can be sent back to it"

  sa_ledger_note "$id $ledger; reopened bead $bead for a worker"
  not_finished "$id $unfinished; its work bead $bead is open again for a worker to fix"
}

# ===========================================================================
# STAGE publish
# ===========================================================================

# adopt_committed_work prints the commit an earlier attempt of this stage made,
# when there is nothing left to commit because that attempt already made it.
#
# It refuses on either of the two things that would make adoption a lie: a tree
# that still holds authorized changes, so the earlier attempt did not commit
# everything; or a HEAD already contained in the authoritative branch, so there
# is nothing unpublished and the empty commit means no work was produced.
adopt_committed_work() {
  local wt="$1"; shift
  if ! git -C "$wt" diff --quiet HEAD -- "$@"; then
    printf '%s\n' "adopt_committed_work: the tree still holds uncommitted authorized changes" >&2
    return 1
  fi
  local head
  head="$(git -C "$wt" rev-parse HEAD)" || return 1
  if git -C "$wt" merge-base --is-ancestor "$head" "refs/remotes/origin/$DEFAULT_BRANCH" 2>/dev/null; then
    printf '%s\n' "adopt_committed_work: HEAD ${head:0:9} is already on $DEFAULT_BRANCH, so nothing was produced to publish" >&2
    return 1
  fi
  printf '%s\n' "$head"
}

stage_publish() {
  [ -n "$PACKAGE" ] || die 'publish needs -package'
  [ -n "$CITY" ] || die 'no city; city-up has not run'
  cd "$CITY" || die "cannot enter $CITY"

  if [ -n "$(rt_get "published.$PACKAGE")" ]; then
    say "$PACKAGE is already published"
    return 0
  fi

  local bead mergeBead wt branch artifact paths
  bead="$(rt_get "bead.$PACKAGE")"
  mergeBead="$(rt_get "merge.$PACKAGE")"
  wt="$(rt_get "wt.$PACKAGE")"
  branch="$(rt_get "branch.$PACKAGE")"
  artifact="$(pkg_field "$PACKAGE" artifact)"
  paths="$(pkg_paths_csv "$PACKAGE")"
  [ -n "$bead" ] || die "no work bead for $PACKAGE"

  # Normally the await stage for this package already cut the worktree and its
  # worker has finished. This stays as the fallback for a publication reached
  # without one — a single-package plan resumed straight into publish, say —
  # and is a no-op when the work is already done.
  #
  # Whether the tree already existed decides who is allowed to start the worker.
  # ensure_package_worktree cuts and routes a package that is reaching its base
  # for the FIRST time; recover_worker routes one whose tree was already there
  # and whose worker is gone. Letting both act on the same package would sling
  # the same bead twice within a second of itself.
  local hadWorktree='no'
  [ -d "$wt" ] && hadWorktree='yes'
  ensure_package_worktree "$PACKAGE"

  # An open bead here means the work is not done, and the worktree existing says
  # nothing about whether anyone is doing it. Gating the worker on the WORKTREE
  # being absent conflated "there is nowhere to work" with "someone is working":
  # an interrupted run whose worktree survived arrived here with an open bead, no
  # worker, and nothing left in the run that would ever start one.
  if ! bead_is_closed "$bead"; then
    [ "$hadWorktree" = 'yes' ] && recover_worker "$PACKAGE"
    local deadline=$(( $(date +%s) + DEADLINE ))
    while ! bead_is_closed "$bead"; do
      if [ "$(date +%s)" -ge "$deadline" ]; then
        # The same statement the await stage makes, for the same reason: work
        # that has not finished has not failed, and the run re-offers this stage
        # under its bounded resume budget rather than spending a retry on it.
        not_finished "$PACKAGE has not finished; its work bead $bead is still open"
        return 1
      fi
      sleep 15
    done
  fi

  # The final-state re-read is EVIDENCE OF A CLOSURE, and it reports whether it
  # actually observed one. That verdict was discarded, which is exactly what
  # sa-lib warns against: a caller that stores a non-final record as final. The
  # rest of this stage publishes on the strength of that record, so a record
  # that does not show a closed bead has to stop it.
  capture_final_bead_state "$bead" "$EVIDENCE" >/dev/null \
    || die "$PACKAGE: re-reading bead $bead after it closed did not observe a closed bead, so there is no final record to publish against"

  # REALITY OVER MEMORY, for the branch as much as for the worker.
  #
  # A package routed before its base exists has no worktree of the controller's
  # yet, and Gas City creates one of its own — on a branch of its own naming —
  # so the agent it was asked to start has somewhere to work. When publish later
  # reaches that package, ensure_package_worktree finds the directory already
  # there and returns, and the branch the ledger remembers was never created.
  # Pushing the remembered name pushed a ref that does not exist — `src refspec
  # … does not match any` — over a worktree holding finished, gated work.
  #
  # So the branch is re-read from the worktree, exactly as worker liveness is
  # re-derived rather than remembered, and the ledger is corrected to what is
  # there, because every later step names this branch — the pull request most
  # of all.
  local onBranch
  onBranch="$(git -C "$wt" symbolic-ref --quiet --short HEAD 2>/dev/null || true)"
  if [ -n "$onBranch" ] && [ "$onBranch" != "$branch" ]; then
    say "$PACKAGE is on branch $onBranch, not the recorded $branch; publishing the branch that exists"
    branch="$onBranch"
    rt_set "branch.$PACKAGE" "$onBranch"
  fi

  # THE BOUNDARY: what actually changed. bounded-project grants Write/Edit, so
  # the permission list buys scope clarity rather than containment. What
  # contains the worker is that only the controller publishes — and for that to
  # mean anything the controller must look at the change set first.
  #
  # First, take back what the worker could not. bounded-project grants Write and
  # Edit and nothing that removes a file, so a transient probe the worker wrote
  # to verify its own configuration is a file it cannot delete — and an
  # undeletable stray would block this package permanently. The controller moves
  # untracked out-of-scope files into evidence before it gates anything, so what
  # it verifies and what it commits are the same authorised tree. A TRACKED file
  # changed out of scope is not touched and still refuses below.
  local quarantined
  quarantined="$(quarantine_untracked_out_of_scope "$wt" "$paths" "$EVIDENCE/quarantined-$PACKAGE")"
  if [ -n "$quarantined" ]; then
    say "$PACKAGE left untracked files outside its scope; quarantined to evidence: $(tr '\n' ' ' <<<"$quarantined")"
    printf '%s\n' "$quarantined" > "$EVIDENCE/quarantined-$PACKAGE.txt"
  fi

  local violations
  violations="$(publication_scope_violations "$wt" "$paths")"
  if [ -n "$violations" ]; then
    die "$PACKAGE changed paths it was not authorized to: $(tr '\n' ' ' <<<"$violations")"
  fi
  [ -f "$wt/$artifact" ] || die "$PACKAGE did not produce its required artifact $artifact"
  say "$PACKAGE produced $artifact within its authorized scope"

  # A package that fails its own declared gates is not published — and is not a
  # failed RUN either. Nothing about the platform went wrong and nothing was
  # proved impossible; the worker wrote code that does not pass the gate it is
  # judged by. `die` here closed the run with a closed bead and no route back,
  # which is the dead end return_to_worker exists to end. The refusal stands;
  # the verdict now reaches somebody who can act on it.
  if ! run_project_gates "$wt" "$PACKAGE"; then
    local gateWhy
    gateWhy="$(tail -3 "$(ls -t "$EVIDENCE"/gate-*-"$PACKAGE".txt 2>/dev/null | head -1)" 2>/dev/null | tr '\n' ' ')"
    return_to_worker "$PACKAGE" "$bead" \
      "This package failed the project's own gates when the controller re-ran them, so the work is not \
accepted and this package is open again. The controller runs exactly the gates this package declared, so \
what failed for it fails for you. Fix the cause inside this package's authorized paths and verify with \
those same gates before finishing. ${gateWhy}" \
      "failed the project's own gates" \
      "failed the project's own gates"
    return 1
  fi

  local -a pathList=()
  IFS=',' read -r -a pathList <<< "$paths"

  # A republication with nothing new is a fact about the WORKER, not about git.
  #
  # `git commit` exits non-zero on an empty index, so a publication resumed
  # after its pull request failed CI — where the controller's commit is already
  # on the branch — died reporting "committing wp-x". That named the wrong
  # cause, at the wrong stage, for a package whose real problem was a red check.
  # Said plainly here, it is the one thing a person needs to know: the worker
  # was sent back and returned the same tree, so republishing would present the
  # head that already failed.
  git -C "$wt" add -- "${pathList[@]}" > "$EVIDENCE/stage-$PACKAGE.txt" 2>&1 \
    || die_from "$EVIDENCE/stage-$PACKAGE.txt" "staging $PACKAGE's authorized paths"
  if git -C "$wt" diff --cached --quiet && [ -n "$(rt_get "head.$PACKAGE")" ]; then
    stop_human "$PACKAGE was sent back after failing required CI and has returned no change, so republishing would present the same head that already failed; a person needs to decide what this package should do differently"
  fi

  local commit
  if commit="$(controller_commit "$wt" "feat($PACKAGE): $artifact

Published by the controller. The worker produced and verified this change
under bounded-project and is denied git by policy." "${pathList[@]}")"; then
    say "$PACKAGE committed ${commit:0:9}"
  else
    # THE UNRECOVERABLE STATE THIS EXISTS TO PREVENT. Commit and push are two
    # steps and a push fails on its own — a refspec, a credential, a network.
    # The retry then finds nothing left to commit, because the work is already
    # on the branch, and a stage that read an empty commit as a failure spent
    # all three attempts refusing to publish work it had committed itself. The
    # package became permanently unpublishable, and nothing short of a person
    # editing the branch by hand could undo it.
    #
    # An empty commit is benign only when the tree really is clean AND the
    # branch really holds something the authoritative branch does not. Either
    # half missing is the ordinary failure and still stops publication.
    commit="$(adopt_committed_work "$wt" "${pathList[@]}")" || die "committing $PACKAGE"
    say "$PACKAGE was already committed as ${commit:0:9} by an attempt that could not push; publishing that"
  fi

  git -C "$wt" push -q -u origin "$branch" > "$EVIDENCE/push-$PACKAGE.txt" 2>&1 \
    || die_from "$EVIDENCE/push-$PACKAGE.txt" "pushing $branch: $(tail -2 "$EVIDENCE/push-$PACKAGE.txt")"

  # The `|| true` stays, and it is not suppression: a pull request that already
  # exists for this branch is a legitimate non-zero exit from a resumed
  # publication, and the verdict is taken below from whether a pull request
  # exists rather than from this command's status. What the refusal in this file
  # DOES decide is why it does not exist, when it does not.
  "$GH" pr create --repo "$REPO_SLUG" --base "$DEFAULT_BRANCH" --head "$branch" \
    --title "feat($PACKAGE): $(pkg_field "$PACKAGE" title)" \
    --body "Managed delivery via Gas City. The worker produced this change under bounded-project; the controller validated, committed, pushed and opened this pull request. Package \`$PACKAGE\`, bead \`$bead\`." \
    > "$EVIDENCE/pr-$PACKAGE.txt" 2>&1 || true

  local prNum prHead
  prNum="$("$GH" pr list --repo "$REPO_SLUG" --head "$branch" --json number --jq '.[0].number' 2>> "$EVIDENCE/pr-$PACKAGE.txt")"
  [ -n "$prNum" ] || die_from "$EVIDENCE/pr-$PACKAGE.txt" \
    "no pull request for $branch: $(tail -2 "$EVIDENCE/pr-$PACKAGE.txt" | tr '\n' ' ')"
  prHead="$("$GH" pr view "$prNum" --repo "$REPO_SLUG" --json headRefOid --jq '.headRefOid' 2>> "$EVIDENCE/pr-$PACKAGE.txt")"
  [ "$prHead" = "$commit" ] || die_from "$EVIDENCE/pr-$PACKAGE.txt" \
    "PR #$prNum head ${prHead:-unknown} is not the controller commit $commit"
  say "$PACKAGE opened PR #$prNum at ${prHead:0:9}"
  rt_set "pr.$PACKAGE" "$prNum"
  rt_set "head.$PACKAGE" "$prHead"

  if [ "$NEED_CHECKS" = 'true' ]; then
    await_required_ci "$PACKAGE" "$prHead" || { send_back_to_worker "$PACKAGE" "$bead" "$prNum"; return 1; }
  fi

  if [ "$NEED_MERGE" != 'true' ]; then
    say "$PACKAGE stops at an open pull request; merge authority was withheld"
    rt_set "published.$PACKAGE" "pr-open"
    return 0
  fi

  merge_pr "$PACKAGE" "$prNum" \
    || die_from "$EVIDENCE/merge-$PACKAGE.txt" \
      "$PACKAGE was not merged: $(tail -2 "$EVIDENCE/merge-$PACKAGE.txt" | tr '\n' ' ')"
  rt_set "published.$PACKAGE" "merged"

  # Close the controller's merge bead with a typed shipped record, against a
  # local ref that has actually been moved to what GitHub did — the work-record
  # gate verifies reachability, and claiming it against a stale ref is a claim
  # that cannot be verified.
  # THE SUPPRESSION THIS REPLACES, AND WHY IT MATTERS MOST HERE. All four
  # commands wrote to /dev/null and discarded their status. Closing this bead is
  # what UNBLOCKS every package that depends on this one — the dependency edge
  # runs from the merge bead — and the work-record gate can legitimately refuse
  # the close when the shipped claim names a commit the local ref cannot reach.
  # So a silent refusal here left a merged package looking merged while every
  # dependent package's work bead stayed shut, and the run waited out deadline
  # after deadline for workers that were never eligible to start.
  git -C "$RIG_PATH" fetch -q origin "$DEFAULT_BRANCH" > "$EVIDENCE/merge-ref-$PACKAGE.txt" 2>&1 \
    || die_from "$EVIDENCE/merge-ref-$PACKAGE.txt" \
      "$PACKAGE merged, but its merge commit could not be fetched, so the shipped claim cannot be verified: $(tail -2 "$EVIDENCE/merge-ref-$PACKAGE.txt" | tr '\n' ' ')"
  git -C "$RIG_PATH" update-ref "refs/heads/$DEFAULT_BRANCH" "refs/remotes/origin/$DEFAULT_BRANCH" \
    >> "$EVIDENCE/merge-ref-$PACKAGE.txt" 2>&1 \
    || die "$PACKAGE merged, but $DEFAULT_BRANCH could not be moved to what the forge did, so the shipped claim would name an unreachable commit"
  local mergedSha; mergedSha="$(rt_get "merged.$PACKAGE")"
  stamp_bead "$mergeBead" 'the shipped work record' \
    --set-metadata 'gc.work_outcome=shipped' \
    --set-metadata "gc.work_commit=$mergedSha" \
    --set-metadata "gc.work_branch=$DEFAULT_BRANCH"
  gcx bd close "$mergeBead" --reason 'controller published, CI-verified and merged this package' \
    >> "$EVIDENCE/stamp-$mergeBead.txt" 2>&1 \
    || die "closing the merge bead $mergeBead for $PACKAGE, which is what unblocks the packages that depend on it: $(tail -2 "$EVIDENCE/stamp-$mergeBead.txt" | tr '\n' ' ')"
  capture_final_bead_state "$mergeBead" "$EVIDENCE" >/dev/null \
    || die "$PACKAGE: the merge bead $mergeBead was closed and the re-read did not observe a closed bead"
  say "$PACKAGE merged as ${mergedSha:0:9}"
}

# run_project_gates re-runs the project's own checks under controller identity.
#
# The worker already ran them; the controller runs them again because a worker
# reporting its own success is a claim, and the thing being published must be
# checked by whoever publishes it. A project with no recognized manifest has no
# local gate — its authority is the required CI run, which is stronger anyway.
run_project_gates() {
  local wt="$1" id="$2" ok=0

  # The gate the package declared is the gate the controller re-runs. Anything
  # else would judge the work against a different standard than the one the
  # worker was given and permitted, which is how "the worker verified it" and
  # "the controller verified it" come to mean two different things.
  # The gates are read into an array BEFORE any of them runs. Read one at a
  # time from a here-string, the first gate that touched stdin drained the list
  # feeding the loop — a real `npm install` does — and the loop ended after it.
  # Every package in the pilot that found this was published on the strength of
  # exactly one of its declared gates, while the run's own account said it had
  # re-run them all. A list that is already in hand cannot be consumed by what
  # it is a list of.
  local gate n=0
  local -a gateList=()
  while IFS= read -r gate; do
    [ -n "$gate" ] || continue
    gateList+=("$gate")
  done <<< "$(pkg_gates "$id")"

  for gate in ${gateList+"${gateList[@]}"}; do
    n=$((n + 1))
    # shellcheck disable=SC2086 # a validated gate is a bare command, by contract
    run_project_command "$wt" $gate > "$EVIDENCE/gate-$n-$id.txt" 2>&1 \
      || { say "$id failed its declared gate '$gate'; see gate-$n-$id.txt"; ok=1; }
  done
  if [ "$n" -gt 0 ]; then
    say "$id: the controller re-ran all $n declared gate(s)"
    return $ok
  fi

  if [ -f "$wt/package.json" ]; then
    local script
    for script in typecheck test; do
      if jq -e --arg s "$script" '.scripts[$s] // empty' < "$wt/package.json" >/dev/null 2>&1; then
        run_project_command "$wt" npm run "$script" --silent > "$EVIDENCE/gate-$script-$id.txt" 2>&1 \
          || { say "$id failed npm run $script; see gate-$script-$id.txt"; ok=1; }
      fi
    done
  elif [ -f "$wt/go.mod" ]; then
    run_project_command "$wt" sh -c 'go build ./... && go test ./...' > "$EVIDENCE/gate-go-$id.txt" 2>&1 \
      || { say "$id failed the Go gates; see gate-go-$id.txt"; ok=1; }
  else
    say "$id has no recognized manifest; the required CI run is its gate"
  fi
  return $ok
}

# await_required_ci proves the workflow ran on the EXACT head, not merely that
# some run for the branch went green.
await_required_ci() {
  local id="$1" head="$2"
  local deadline=$(( $(date +%s) + ${DELIVERY_CI_DEADLINE:-2700} ))
  local runId='' concl='' runHead=''
  # The forge's own refusals are kept rather than dropped down /dev/null. A
  # credential this run cannot use and a workflow that has not started yet look
  # identical from here — both produce no run id — and reporting the first as
  # the second sends a person looking for a CI problem that does not exist.
  local errors="$EVIDENCE/ci-$id.err"

  while [ "$(date +%s)" -lt "$deadline" ]; do
    runId="$("$GH" api "repos/$REPO_SLUG/actions/runs?head_sha=$head&event=pull_request" \
      --jq '[.workflow_runs[]] | sort_by(.id) | last | .id' 2>> "$errors")"
    if [ -n "$runId" ] && [ "$runId" != 'null' ]; then
      concl="$("$GH" api "repos/$REPO_SLUG/actions/runs/$runId" --jq '.conclusion' 2>> "$errors")"
      [ -n "$concl" ] && [ "$concl" != 'null' ] && break
    fi
    sleep 15
  done

  if [ -z "$runId" ] || [ "$runId" = 'null' ]; then
    say "$id: no workflow run was found for head $head$( [ -s "$errors" ] && printf ': %s' "$(tail -2 "$errors" | tr '\n' ' ')")"
    return 1
  fi
  runHead="$("$GH" api "repos/$REPO_SLUG/actions/runs/$runId" --jq '.head_sha' 2>> "$errors")"
  "$GH" api "repos/$REPO_SLUG/actions/runs/$runId" > "$EVIDENCE/ci-$id.json" 2>> "$errors" || true
  [ "$runHead" = "$head" ] || { say "$id: run $runId tested $runHead, not the PR head $head"; return 1; }
  [ "$concl" = 'success' ] || { say "$id: run $runId concluded '$concl'"; return 1; }
  say "$id: required CI passed on the exact head (run $runId)"
  rt_set "ci.$id" "$runId"
  return 0
}

merge_pr() {
  local id="$1" prNum="$2" state mergedSha
  "$GH" pr merge "$prNum" --repo "$REPO_SLUG" --squash --delete-branch=false \
    > "$EVIDENCE/merge-$id.txt" 2>&1 \
    || "$GH" pr merge "$prNum" --repo "$REPO_SLUG" --merge --delete-branch=false \
      >> "$EVIDENCE/merge-$id.txt" 2>&1 \
    || { say "$id: merging PR #$prNum: $(tail -2 "$EVIDENCE/merge-$id.txt")"; return 1; }

  state="$("$GH" pr view "$prNum" --repo "$REPO_SLUG" --json state --jq '.state' 2>/dev/null)"
  mergedSha="$("$GH" pr view "$prNum" --repo "$REPO_SLUG" --json mergeCommit --jq '.mergeCommit.oid' 2>/dev/null)"
  [ "$state" = 'MERGED' ] && [ -n "$mergedSha" ] || {
    say "$id: PR #$prNum state='$state' mergeCommit='${mergedSha:-none}'"; return 1;
  }
  rt_set "merged.$id" "$mergedSha"
  return 0
}

# ===========================================================================
# STAGE verify — check evidence that is already on the authoritative branch
# ===========================================================================

# WHAT THIS STAGE IS FOR, AND WHY IT IS NOT PUBLISH.
#
# A criterion reported met can be disproved later, and the repair is usually
# work. Sometimes it is not: the evidence the criterion now needs is already on
# the authoritative branch, and what was missing was the checking. Asked to
# repair such a criterion, the ordinary lifecycle cannot — a worker handed a tree
# that already contains the evidence produces no diff, and publish correctly
# refuses that nothing was produced. The only way through would be to manufacture
# a change nobody needs and merge it so the shape fits, which is a lie told to a
# state machine.
#
# So this stage does the honest thing instead. It creates no bead, starts no
# worker, cuts no branch, opens no pull request and merges nothing, because it
# changes nothing. It cuts a DETACHED tree at the exact commit the package names,
# proves that commit is really on the authoritative branch, proves the evidence
# is really there, and runs the package's declared gates against it.
#
# Each of those three is recorded as its own durable fact, and the projection's
# completion gate is derived from them the same way a published package's is
# derived from its own three. A verification that could not prove one of them
# records what it did prove and leaves the criterion unmet — which is the whole
# reason the checks are separate facts rather than one boolean.
stage_verify() {
  [ -n "$CITY" ] || die 'no city; city-up has not run'
  [ -n "$PACKAGE" ] || die 'verify needs -package'

  local sha artifact wt
  sha="$(pkg_verifies_sha "$PACKAGE")"
  artifact="$(pkg_field "$PACKAGE" artifact)"
  [ -n "$sha" ] || die "$PACKAGE is not a verification package; it names no merged commit to verify"

  # Already answered. Re-running a stage that has done its work is how every
  # other stage behaves, and re-running gates against an immutable commit could
  # only produce the same answer at more cost.
  if [ -n "$(rt_get "verified.$PACKAGE")" ]; then
    say "$PACKAGE was already verified at ${sha:0:9}"
    return 0
  fi

  # CONTROL 1 — the commit is really on the authoritative branch.
  #
  # Asked of the remote, not of the local ref: a sha that exists in the rig
  # proves only that something once fetched it. What the criterion rests on is
  # that this commit is part of the branch the project is judged by.
  git -C "$RIG_PATH" fetch -q origin "$DEFAULT_BRANCH" 2>/dev/null
  local head; head="$(git -C "$RIG_PATH" rev-parse "refs/remotes/origin/$DEFAULT_BRANCH" 2>/dev/null)"
  [ -n "$head" ] || die "cannot read the authoritative branch to verify $PACKAGE against"
  if ! git -C "$RIG_PATH" merge-base --is-ancestor "$sha" "$head" 2>/dev/null; then
    say "$PACKAGE names $sha, which is not on origin/$DEFAULT_BRANCH (head ${head:0:9})"
    die "$PACKAGE verifies a commit that is not on the authoritative branch, so nothing it proved would be about the delivered product"
  fi
  rt_set "verifiedCommit.$PACKAGE" "$sha"
  say "$PACKAGE: ${sha:0:9} is on origin/$DEFAULT_BRANCH"

  # A DETACHED tree, deliberately. There is no branch because there is nothing
  # to put on one, and a branch would be a thing a later step could try to push.
  wt="$CITY/.gc/worktrees/$RIG_NAME/verify-$PACKAGE"
  if [ -d "$wt" ]; then
    git -C "$RIG_PATH" worktree remove --force "$wt" > "$EVIDENCE/verify-recut-$PACKAGE.txt" 2>&1 || true
  fi
  mkdir -p "$(dirname "$wt")"
  GIT_LFS_SKIP_SMUDGE=1 git -C "$RIG_PATH" worktree add -q --detach "$wt" "$sha" \
    > "$EVIDENCE/verify-worktree-$PACKAGE.txt" 2>&1 \
    || die_from "$EVIDENCE/verify-worktree-$PACKAGE.txt" "cutting a clean tree at $sha to verify $PACKAGE"
  rt_set "wt.$PACKAGE" "$wt"

  # CONTROL 2 — the evidence the criterion needs is really there.
  #
  # Asked of the COMMIT rather than of the directory. A file on disk could have
  # arrived any number of ways; what the criterion rests on is that the tree the
  # project merged carries it.
  if ! git -C "$RIG_PATH" cat-file -e "$sha:$artifact" 2>/dev/null; then
    die "$PACKAGE requires $artifact at ${sha:0:9}, and that commit does not carry it"
  fi
  rt_set "verifiedEvidence.$PACKAGE" "$artifact"
  say "$PACKAGE: $artifact is present at ${sha:0:9}"

  # CONTROL 3 — the declared gates pass against that exact tree.
  prepare_worktree "$wt" "$PACKAGE"
  if ! run_project_gates "$wt" "$PACKAGE"; then
    die "$PACKAGE failed its declared verification gates at ${sha:0:9}; the criterion it repairs stays unmet"
  fi
  rt_set "verifiedGates.$PACKAGE" "$sha"

  # Only now, and only past all three.
  rt_set "verified.$PACKAGE" "$sha"
  say "$PACKAGE verified at ${sha:0:9}: on the branch, evidence present, declared gates passed"
  return 0
}

# ===========================================================================
# STAGE project — render the delivery projection
# ===========================================================================

# run_progression_refusal is the run's own answer to whether this packet may
# progress, read from where the run publishes it.
#
# It is READ and never derived. The run evaluates its mandatory gates against
# evidence bound to a revision (internal/unattended/qa.go) and applies that
# decision as a ceiling to its own projection; this stage renders the OTHER
# projection — the delivery one an acceptance assessment reads — and a ceiling
# on one document but not the other is two accounts of the same run, of which a
# reader gets the more reassuring.
run_progression_refusal() {
  local dir
  dir="$(cr_state_dir)"
  [ -n "$dir" ] || return 0
  cr_progression_refusal "$dir/heartbeat.json"
}

stage_project() {
  [ -n "$CITY" ] || die 'no city; city-up has not run'

  PROGRESSION_REFUSAL="$(run_progression_refusal)"
  [ -z "$PROGRESSION_REFUSAL" ] || say "the projection is capped: $PROGRESSION_REFUSAL"

  local facts="$STATE/facts.json"
  local out="$STATE/PROJECT-STATE.yml"
  local cursor="$STATE/projector-cursor.json"

  # The facts document is assembled from what actually happened — the runtime
  # ledger and the forge — and never from what was planned. A package with no
  # merge recorded projects as whatever it actually reached, not as merged.
  build_facts > "$facts" || die 'assembling the projection facts'
  build_controls || die 'assembling the run control ledger'

  local gen="$STATE/projector-gen"
  if [ ! -x "$gen" ]; then
    ( cd "$SOURCE_REPO" && go build -o "$gen" ./corsolv/projector-gen ) > "$EVIDENCE/projector-build.txt" 2>&1 \
      || die "building projector-gen; see $EVIDENCE/projector-build.txt"
  fi

  "$gen" -city "$CITY" -evidence "$EVIDENCE" -facts "$facts" -out "$out" -cursor "$cursor" \
    > "$EVIDENCE/projector.txt" 2>&1 || die "rendering the projection; see $EVIDENCE/projector.txt"
  say "projection rendered at $out"
}

# build_facts emits the reconciliation facts the projector consumes.
#
# Status is DERIVED from the ledger, per package:
#   merged   the forge reports a merge commit
#   pr-open  a pull request exists and no merge is recorded
#   blocked  the work bead closed without producing a publication
#   planned  nothing has happened yet
build_facts() {
  local mainSha
  mainSha="$(git -C "$RIG_PATH" rev-parse "refs/remotes/origin/$DEFAULT_BRANCH" 2>/dev/null || true)"
  # Only claim an accepted main SHA when something was actually merged into it.
  local anyMerged=''
  local id
  for id in $(packages); do
    [ -n "$(rt_get "merged.$id")" ] && anyMerged=1
  done
  [ -n "$anyMerged" ] || mainSha=''

  {
    printf '{\n'
    printf '  "project": {\n'
    printf '    "projectId": %s,\n' "$(jq -Rn --arg v "$PROJECT" '$v')"
    printf '    "strategy": %s,\n' "$(jq -Rn --arg v "$(jqi '.objective')" '$v')"
    printf '    "authoritativeRef": %s,\n' "$(jq -Rn --arg v "origin/$DEFAULT_BRANCH" '$v')"
    printf '    "currentPhase": %s,\n' "$(jq -Rn --arg v "$(jqi '.lifecycle[0]')" '$v')"
    printf '    "currentMilestone": "managed delivery",\n'
    printf '    "overallRag": %s,\n' "$(jq -Rn --arg v "$(overall_rag)" '$v')"
    printf '    "overallRagReason": %s,\n' "$(jq -Rn --arg v "$(overall_rag_reason)" '$v')"
    printf '    "latestAcceptedMainSha": %s\n' "$(jq -Rn --arg v "$mainSha" '$v')"
    printf '  },\n'
    printf '  "runId": %s,\n' "$(jq -Rn --arg v "$RUN_TAG" '$v')"
    printf '  "deliverables": %s,\n' "$(deliverable_facts)"
    printf '  "tasks": [\n'
    local first=1
    for id in $(packages); do
      [ "$first" -eq 1 ] || printf ',\n'
      first=0
      emit_task_facts "$id"
    done
    printf '\n  ]\n}\n'
  }
}

# deliverable_facts joins what the project must deliver to what claimed it.
#
# WHAT THE PROJECTION WAS MISSING. It carried work packages and nothing else, so
# a portal reading it could say four packages merged and could not say which of
# seven deliverables were finished — different taxonomies, and one cannot stand
# for the other. The pilot watched a project sit at "0 of 7" with its first
# package merged and its deliverable evidenced, because the document that
# reaches the portal had no word for the thing the project agreed to produce.
#
# Both halves here are FACTS, taken from the two validated documents and nothing
# else: the acceptance criteria the intent carries, and the `satisfies` each
# package declared in the plan. No verdict is formed. Whether a deliverable is
# met is derived by the projector from the task rows, under the same two
# conditions handoff.Assess applies when it reads the document back.
deliverable_facts() {
  # THE THIRD DOCUMENT. A criterion reserved for a person is claimed by no work
  # package, so the plan says nothing about it and the task rows never will
  # either — and the projector, deriving `met` from claiming packages, could
  # never report one as done however many people accepted it. The pilot proved
  # that the hard way: a real acceptance was recorded, the engine's own state
  # turned `completed`, and the document the dashboard reads went on saying 6 of
  # 7 with nothing anywhere to say which was right.
  #
  # So the durable record joins the intent and the plan here. It is read as
  # FACTS like the other two — who accepted, and when — and forms no verdict;
  # whether that makes the deliverable met stays the projector's to decide.
  # AND THE FINDINGS. A criterion can be reported met, the project can complete
  # on the strength of it, and later evidence can prove it was never satisfied.
  # The task rows this document carries cannot say so — they say the packages
  # merged and their gates passed, which is exactly what happened and exactly
  # what the finding disproves — so the record's finding travels here too.
  #
  # `satisfiedBy` stays what the ORIGINAL plan claimed; the corrective work is
  # carried separately as `remediatedBy`, from the remediation documents. Both
  # are facts, and which of them makes a deliverable met is the projector's to
  # derive under the same two conditions it applies to everything else.
  #
  # The LATEST finding against a criterion is carried, answered or not, with the
  # packages authorized to answer it beside it — so a reader of this document
  # can see the sequence (met, disproved, repaired) rather than a bare verdict.
  # Earlier findings against the same criterion stay in the record, which is the
  # place a full history belongs; this document reports the state now.
  local record="$RECORD"
  [ -f "$record" ] || record='/dev/null'
  local rems=() f
  for f in "$STATE"/remediation-[0-9][0-9][0-9].json; do
    [ -f "$f" ] && rems+=("$f")
  done
  local remsJson='[]'
  if [ "${#rems[@]}" -gt 0 ]; then
    remsJson="$(jq -s '.' "${rems[@]}")" || die 'reading the authorized remediations'
  fi

  # The BASE plan, not the effective one: `satisfiedBy` is what the original
  # plan claimed, and a remedial package appearing in both lists would be
  # counted as an original claimant of the criterion it exists to repair.
  jq -c --slurpfile plan "$BASE_PLAN" --slurpfile record "$record" --argjson rems "$remsJson" '
    [ .acceptance[]
      | . as $c
      | ($record[0].acceptances // [] | map(select(.criterionId == $c.id)) | first) as $a
      | ([ $record[0].invalidations // [] | .[] | select(.criterionId == $c.id) ] | last) as $inv
      | ([ $rems[]
           | select(any(.repairs[]?; .criterionId == $c.id and ($inv != null and .invalidation == $inv.seq)))
           | .packages[]
           | select(any(.satisfies[]?; . == $c.id))
           | .id ]) as $repaired
      | { id: $c.id,
          statement: $c.statement,
          acceptedBy: ($a.by // ""),
          acceptedAt: ($a.at // ""),
          satisfiedBy: [ $plan[0].packages[]
                         | select(any(.satisfies[]?; . == $c.id))
                         | .id ],
          remediatedBy: $repaired,
          invalidated: $inv }
    ]' < "$INTENT"
}

emit_task_facts() {
  local id="$1" status prNum mergedSha gateKind=''
  prNum="$(rt_get "pr.$id")"
  mergedSha="$(rt_get "merged.$id")"
  status="$(package_status "$id")"
  if pkg_is_verification "$id"; then
    gateKind='verification'
    # The commit this package VERIFIED is the commit that implements what the
    # criterion rests on, so it is the implementation sha — the same field, and
    # the same meaning, reached a different way.
    mergedSha="$(rt_get "verifiedCommit.$id")"
  fi

  printf '    {\n'
  printf '      "taskId": %s,\n' "$(jq -Rn --arg v "$id" '$v')"
  printf '      "beadId": %s,\n' "$(jq -Rn --arg v "$(rt_get "bead.$id")" '$v')"
  printf '      "title": %s,\n' "$(jq -Rn --arg v "$(pkg_field "$id" title)" '$v')"
  printf '      "phase": %s,\n' "$(jq -Rn --arg v "$(pkg_field "$id" phase)" '$v')"
  printf '      "taskType": "code",\n'
  printf '      "status": %s,\n' "$(jq -Rn --arg v "$status" '$v')"
  printf '      "priority": "high",\n'
  printf '      "ownerType": "agent",\n'
  printf '      "branch": %s,\n' "$(jq -Rn --arg v "$(rt_get "branch.$id")" '$v')"
  printf '      "pullRequest": %s,\n' "${prNum:-0}"
  printf '      "dependencies": %s,\n' "$(jq -c --arg id "$id" '[.packages[] | select(.id == $id) | .dependsOn[]?]' < "$PLAN")"
  printf '      "parallelGroup": %s,\n' "$(jq -Rn --arg v "$id" '$v')"
  printf '      "criticalPath": true,\n'
  printf '      "implementationSha": %s,\n' "$(jq -Rn --arg v "$mergedSha" '$v')"
  printf '      "agentSession": %s,\n' "$(jq -Rn --arg v "worker-$id" '$v')"
  printf '      "worktreePath": %s,\n' "$(jq -Rn --arg v "$(rt_get "wt.$id")" '$v')"
  printf '      "gateLabel": %s,\n' "$(jq -Rn --arg v "$id" '$v')"
  printf '      "gateKind": %s\n' "$(jq -Rn --arg v "$gateKind" '$v')"
  printf '    }'
}

# build_controls writes the run's control ledger — one row per control the
# controller actually adjudicated, in the two-column shape projector-gen parses.
#
# The projection's completion gate is DERIVED from these rows and never from a
# task's status, because "merged" says a pull request landed while these say the
# exact head was tested by required CI, that the controller re-derived the result
# itself before merging, and that the merge went through the repository's own
# governance. A merge without them is publication without acceptance.
#
# The driver never wrote one, so every delivery run reached `project` and died
# reading a file that had never existed — after all four packages had merged.
#
# It is derived from the runtime ledger rather than appended during publication,
# so a RESUMED run produces the same ledger as an uninterrupted one. A run that
# re-enters publish for an already-published package returns early and would
# otherwise record nothing, and the projection would score verified work as
# unverified. Every fact here is durable and was written when it was adjudicated:
#
#   ci.<id>         the workflow run id await_required_ci accepted on the exact head
#   published.<id>  set only past `run_project_gates ... || die`, so the
#                   controller's own re-run of the package's declared gates
#                   passed; there is no path to this key without it
#   merged.<id>     the commit `gh pr merge` produced on the authoritative branch
#
# A control with no durable fact behind it is omitted, never invented: the
# projector reads an absent row as not-met, which is the answer that keeps an
# unverified package from scoring as an accepted one.
# THE CEILING THE RUN'S OWN GATES PUT ON THIS LEDGER.
#
# A control row is a CLAIM that something was verified, and the projection's
# completion gate is derived from the rows that claim PASS. When the run's
# mandatory QA gates refuse this packet — a gate that failed, never ran, or ran
# against different code — no claim of verification is available to make, and a
# ledger that made one anyway would license the exact false completion QA-001
# exists to prevent, from the one document an acceptance assessment reads.
#
# The FACTS are not erased; they are recorded with the claim withheld. The
# controller really did re-run the gates and the forge really did report that
# run — losing that would make a refused packet indistinguishable from one that
# never got anywhere. What changes is the status column, which is the column
# that certifies.
build_controls() {
  local ledger="$EVIDENCE/controls.tsv" id v
  local status='PASS' withheld=''
  if [ -n "$PROGRESSION_REFUSAL" ]; then
    status='BLOCKED'
    withheld=" — the claim is withheld: $PROGRESSION_REFUSAL"
  fi
  printf 'control\tstatus\treason\n' > "$ledger"
  for id in $(packages); do
    v="$(rt_get "ci.$id")"
    [ -n "$v" ] && printf '%s\t%s\t%s\n' \
      "$id required CI passed on the exact pull-request head" "$status" "run $v$withheld" >> "$ledger"
    [ -n "$(rt_get "published.$id")" ] && printf '%s\t%s\t%s\n' \
      "$id independent assurance passed" "$status" \
      "the controller re-ran the package's declared gates$withheld" >> "$ledger"
    v="$(rt_get "merged.$id")"
    [ -n "$v" ] && printf '%s\t%s\t%s\n' \
      "$id merged through repository governance" "$status" "$v$withheld" >> "$ledger"

    # A VERIFICATION'S THREE, DERIVED THE SAME WAY AND FROM FACTS AS DURABLE.
    #
    # It has no pull request and no merge of its own, so the three rows above
    # are not merely absent from its ledger — they could never be there, and a
    # gate that demanded them would report every honest verification as unmet.
    # What it does have is a named commit that was already merged through the
    # governance those rows describe, and three separate checks against it. Each
    # is written only past the step that established it, so there is no path to
    # any of these rows without the thing it claims.
    v="$(rt_get "verifiedCommit.$id")"
    [ -n "$v" ] && printf '%s\t%s\t%s\n' \
      "$id verified commit is on the authoritative branch" "$status" "$v$withheld" >> "$ledger"
    v="$(rt_get "verifiedEvidence.$id")"
    [ -n "$v" ] && printf '%s\t%s\t%s\n' \
      "$id required evidence present at the verified commit" "$status" "$v$withheld" >> "$ledger"
    v="$(rt_get "verifiedGates.$id")"
    [ -n "$v" ] && printf '%s\t%s\t%s\n' \
      "$id declared verification gates passed at the verified commit" "$status" \
      "the controller ran the package's declared gates against $v$withheld" >> "$ledger"
  done
  say "control ledger: $(( $(wc -l < "$ledger") - 1 )) adjudicated control(s), recorded $status"
}

package_status() {
  local id="$1"
  # A verification reports `verified`, never `merged`. Both count toward
  # completion, and the difference is the record: this package reconciled a
  # criterion by checking evidence that was already there, and a reader who
  # cannot tell that from a merge has been told the wrong thing.
  if [ -n "$(rt_get "verified.$id")" ]; then
    [ -z "$PROGRESSION_REFUSAL" ] || { printf 'blocked'; return; }
    printf 'verified'
    return
  fi
  if pkg_is_verification "$id"; then
    # It is not planned-and-waiting for a worker; it simply has not been checked
    # yet, and nothing else in this function's vocabulary would be true of it.
    printf 'planned'
    return
  fi
  if [ -n "$(rt_get "merged.$id")" ]; then
    # The same ceiling the run applies to its own projection, applied here for
    # the same reason: a merge is publication, and publication is not
    # acceptance. A package the run's mandatory gates refuse is reported as
    # blocked on that refusal, never as delivered.
    [ -z "$PROGRESSION_REFUSAL" ] || { printf 'blocked'; return; }
    printf 'merged'
    return
  fi
  [ -n "$(rt_get "pr.$id")" ] && { printf 'pr-open'; return; }
  local bead; bead="$(rt_get "bead.$id")"
  if [ -n "$bead" ] && bead_is_closed "$bead" 2>/dev/null; then printf 'blocked'; return; fi
  printf 'planned'
}

overall_rag() {
  local id
  for id in $(packages); do
    [ "$(package_status "$id")" = 'merged' ] || { printf 'amber'; return; }
  done
  printf 'green'
}

overall_rag_reason() {
  local done=0 total=0 id
  for id in $(packages); do
    total=$((total + 1))
    [ "$(package_status "$id")" = 'merged' ] && done=$((done + 1))
  done
  printf '%d of %d work packages merged through repository governance.' "$done" "$total"
  [ -z "$PROGRESSION_REFUSAL" ] || printf ' %s.' "$PROGRESSION_REFUSAL"
}

# ===========================================================================
# STAGE publish-projection
# ===========================================================================

stage_publish_projection() {
  local projection="$STATE/PROJECT-STATE.yml"
  [ -f "$projection" ] || die 'no projection to publish; the project stage has not run'

  local target='delivery/gascity/PROJECT-STATE.yml'
  local pub="$STATE/publish-clone"

  rm -rf "$pub"
  git -c credential.helper="$GIT_CRED_HELPER" clone -q --depth 1 --branch "$DEFAULT_BRANCH" \
    "$REPO_ORIGIN" "$pub" > "$EVIDENCE/publish-clone.txt" 2>&1 \
    || die_from "$EVIDENCE/publish-clone.txt" "cloning to publish the projection: $(tail -2 "$EVIDENCE/publish-clone.txt")"
  git -C "$pub" config user.name 'Gas City Controller'
  git -C "$pub" config user.email 'support@corsolv.com'
  git -C "$pub" config credential.helper "$GIT_CRED_HELPER"

  # The publisher refuses any target it did not author, so a document a project
  # maintains by hand cannot be overwritten from here.
  local runner="$STATE/unattended-run"
  if [ ! -x "$runner" ]; then
    ( cd "$SOURCE_REPO" && go build -o "$runner" ./corsolv/unattended-run ) > "$EVIDENCE/runner-build.txt" 2>&1 \
      || die "building unattended-run; see $EVIDENCE/runner-build.txt"
  fi
  "$runner" publish -state "$STATE" -repo "$pub" -target "$target" \
    > "$EVIDENCE/publish-projection.txt" 2>&1 \
    || die "publishing the projection: $(tail -3 "$EVIDENCE/publish-projection.txt")"

  if [ -z "$(git -C "$pub" status --porcelain)" ]; then
    say 'the published projection is unchanged'
    return 0
  fi
  git -C "$pub" add -- "$target"
  git -C "$pub" commit -q -m "chore(delivery): publish the Gas City execution projection

Generated by the delivery engine's projector from this run's own evidence.
Hand edits are overwritten and are not a source of truth." \
    || die 'committing the projection'
  git -C "$pub" push -q origin "$DEFAULT_BRANCH" > "$EVIDENCE/publish-push.txt" 2>&1 \
    || die_from "$EVIDENCE/publish-push.txt" "pushing the projection: $(tail -2 "$EVIDENCE/publish-push.txt")"
  say "projection published to $REPO_SLUG:$target"
}

# ===========================================================================

case "$STAGE" in
  city-up)            stage_city_up ;;
  dispatch)           stage_dispatch ;;
  await)              stage_await ;;
  publish)            stage_publish ;;
  verify)             stage_verify ;;
  project)            stage_project ;;
  publish-projection) stage_publish_projection ;;
esac
