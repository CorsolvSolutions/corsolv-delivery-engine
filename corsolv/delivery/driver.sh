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
#
# Exit: 0 the stage completed  1 the stage failed  2 the invocation was wrong

set -uo pipefail

SOURCE_REPO="${CORSOLV_ENGINE_REPO:-/mnt/d/Development/corsolv-delivery-engine}"
# shellcheck source=../p2-smoke/lib/sa-lib.sh
. "$SOURCE_REPO/corsolv/p2-smoke/lib/sa-lib.sh"

CONTROLLER_ID='Gas City Controller <support@corsolv.com>'

# ---------------------------------------------------------------------------
# Invocation.
# ---------------------------------------------------------------------------

STAGE="${1:-}"; shift || true
PACKAGE=''
PROJECT=''
STATE=''

FORGE=''

while [ $# -gt 0 ]; do
  case "$1" in
    -package) PACKAGE="${2:-}"; shift 2 ;;
    -project) PROJECT="${2:-}"; shift 2 ;;
    -state)   STATE="${2:-}"; shift 2 ;;
    -gh)      FORGE="${2:-}"; shift 2 ;;
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
  city-up|dispatch|await|publish|project|publish-projection) ;;
  *) printf 'driver: unknown stage %s\n' "$STAGE" >&2; exit 2 ;;
esac

# The forge CLI comes from the run's host profile, passed in on the command
# line. It is not defaulted to `gh` on PATH, because on this host the engine
# runs under WSL while the only authenticated gh is a Windows install — and a
# driver that silently fell back to a `gh` that is not there fails at the clone
# with an authentication error that names nothing.
GH="${FORGE:-${GH:-gh}}"

INTENT="$STATE/intent.json"
PLAN="$STATE/plan.json"
RUNTIME="$STATE/runtime.json"
EVIDENCE="$STATE/evidence"
mkdir -p "$EVIDENCE"

die() { printf 'driver[%s]: %s\n' "$STAGE" "$1" >&2; exit 1; }
say() { printf 'driver[%s] %s\n' "$STAGE" "$1" >&2; }

[ -f "$INTENT" ] || die "no delivery intent at $INTENT"
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
  jq --arg k "$1" --arg v "$2" '.[$k] = $v' < "$RUNTIME" > "$tmp" && mv -f "$tmp" "$RUNTIME"
}

CITY="$(rt_get city)"
RIG_PATH="$(rt_get rigPath)"
RIG_NAME="$(rt_get rigName)"
RUN_TAG="$(rt_get runTag)"

export SA_CITY="$CITY"
export SA_RIG="$RIG_NAME"
export GC_WORK_RECORD_ENFORCE=1
sa_ledger_init "$EVIDENCE/gc-commands.log" 2>/dev/null || true

# Git credentials without writing a token to disk: the helper stores a COMMAND
# that asks the forge CLI for a token at the moment git needs one, so the
# credential stays where it already lives.
GIT_CRED_HELPER="!f() { echo username=x-access-token; echo \"password=\$(\"$GH\" auth token)\"; }; f"

packages() { jqp '.packages[].id'; }
pkg_field() { jq -r --arg id "$1" --arg f "$2" '.packages[] | select(.id == $id) | .[$f]' < "$PLAN"; }
pkg_paths_csv() { jq -r --arg id "$1" '[.packages[] | select(.id == $id) | .authorizedPaths[]] | join(",")' < "$PLAN"; }
pkg_deps() { jq -r --arg id "$1" '.packages[] | select(.id == $id) | .dependsOn[]?' < "$PLAN"; }

branch_for() { printf 'delivery/%s/%s' "$RUN_TAG" "$1"; }

# ===========================================================================
# STAGE city-up
# ===========================================================================

stage_city_up() {
  if [ -n "$CITY" ] && [ -f "$CITY/city.toml" ]; then
    say "city already built at $CITY"
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
    > "$EVIDENCE/clone.txt" 2>&1 || die "cloning $REPO_SLUG: $(tail -2 "$EVIDENCE/clone.txt")"
  git -C "$RIG_PATH" config user.name 'Gas City Controller'
  git -C "$RIG_PATH" config user.email 'support@corsolv.com'
  git -C "$RIG_PATH" config credential.helper "$GIT_CRED_HELPER"

  say "initializing the city at $CITY"
  command gc init "$CITY" --provider claude --yes > "$EVIDENCE/init.txt" 2>&1
  # The exit code is not the verdict here, and treating it as one is wrong on
  # exactly the machines this is meant to run on. `gc init` also tries to start
  # the machine-wide supervisor, and on a host that already has one it reports a
  # non-zero exit for a condition that is not merely benign but correct — only
  # one supervisor may run per machine, and the city is registered with the one
  # already there. So the verdict is the state on disk, not the status code.
  if [ ! -f "$CITY/city.toml" ]; then
    die "the city was not created; see $EVIDENCE/init.txt"
  fi
  if grep -q 'supervisor did not' "$EVIDENCE/init.txt" 2>/dev/null; then
    if pgrep -f 'gc supervisor run' >/dev/null 2>&1; then
      say 'a machine-wide supervisor is already running; this city is registered with it'
    else
      die "the city was created but no supervisor is running; see $EVIDENCE/init.txt"
    fi
  fi

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

  # One single-capacity, rig-scoped agent per work package. Distinct agents are
  # the only configuration that yields distinct worktrees: the work_dir template
  # surface carries no per-slot variable, so an unbounded pool would resolve
  # every concurrent slot to one directory. They remain pure configuration —
  # no role name appears in Go.
  local prompt; prompt="$(sa_pool_worker_prompt)"
  local id agent wt
  for id in $(packages); do
    agent="worker-$id"
    wt="$CITY/.gc/worktrees/$RIG_NAME/$agent"
    sa_declare_worker_agent "$CITY" "$RIG_NAME" "$RIG_PATH" "$agent" "$wt" "$prompt"
    # bounded-project is the opt-in that gives a worker Read/Write/Edit and the
    # project's named gates, and denies it git, gh and the shell family.
    # Publication authority stays with the controller.
    printf '\n[option_defaults]\npermission_mode = "bounded-project"\n' >> "$CITY/agents/$agent/agent.toml"
  done

  gcx config show > "$EVIDENCE/config-show.txt" 2>&1 || true
  for id in $(packages); do
    grep -qE "^name = \"worker-$id\"$" "$EVIDENCE/config-show.txt" \
      || die "agent worker-$id did not load; see $EVIDENCE/config-show.txt"
  done
  grep -q 'bounded-project' "$EVIDENCE/config-show.txt" \
    || die 'the bounded-project selection did not survive config resolution'

  rt_set city "$CITY"
  rt_set rigPath "$RIG_PATH"
  rt_set rigName "$RIG_NAME"
  rt_set runTag "$RUN_TAG"
  rt_set baseSha "$(git -C "$RIG_PATH" rev-parse "$DEFAULT_BRANCH")"
  say "city up: $(packages | wc -w) worker agent(s) declared"
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

stage_dispatch() {
  [ -n "$CITY" ] || die 'no city; city-up has not run'
  cd "$CITY" || die "cannot enter $CITY"

  if [ -n "$(rt_get dispatched)" ]; then
    say 'work already dispatched'
    return 0
  fi

  local base; base="$(rt_get baseSha)"
  local id bead mergeBead objective artifact paths wt branch dep

  # Pass 1: create every work bead and its controller merge bead, and stamp the
  # scope each one authorizes. Publication is adjudicated against these stamps,
  # so they exist before any worker starts.
  for id in $(packages); do
    objective="$(pkg_field "$id" objective)"
    artifact="$(pkg_field "$id" artifact)"
    paths="$(pkg_paths_csv "$id")"

    bead="$(mk_bead "$(pkg_field "$id" title)" "$objective

$(worker_lifecycle)")" || die "creating the work bead for $id"
    mergeBead="$(mk_bead "Controller publishes and merges $id ($bead)")" \
      || die "creating the merge bead for $id"

    gcx bd update "$bead" --set-metadata "gc.required_artifact=$artifact" >/dev/null 2>&1
    gcx bd update "$bead" --set-metadata "gc.authorised_paths=$paths" >/dev/null 2>&1
    gcx bd update "$bead" --set-metadata "gc.delivery_package=$id" >/dev/null 2>&1
    gcx bd update "$mergeBead" -a 'corsolv-controller' -s in_progress >/dev/null 2>&1

    rt_set "bead.$id" "$bead"
    rt_set "merge.$id" "$mergeBead"
    say "$id -> work bead $bead, merge bead $mergeBead"
  done

  # Pass 2: dependencies. A package depends on its upstreams being MERGED, not
  # merely closed, so the edge runs from the upstream's MERGE bead. That is what
  # makes a dependent package wait for repository state rather than for a
  # sibling worker's filesystem.
  for id in $(packages); do
    bead="$(rt_get "bead.$id")"
    mergeBead="$(rt_get "merge.$id")"
    gcx bd dep "$bead" --blocks "$mergeBead" >/dev/null 2>&1
    for dep in $(pkg_deps "$id"); do
      gcx bd dep "$(rt_get "merge.$dep")" --blocks "$bead" >/dev/null 2>&1
      say "$id waits for $dep to merge"
    done
  done

  # Pass 3: worktrees and routing. A package with upstreams gets no worktree
  # yet — its base does not exist until those upstreams merge — and the publish
  # stage cuts it then.
  for id in $(packages); do
    bead="$(rt_get "bead.$id")"
    branch="$(branch_for "$id")"
    wt="$CITY/.gc/worktrees/$RIG_NAME/worker-$id"

    if [ -z "$(pkg_deps "$id")" ]; then
      wt_add "$RIG_PATH" "$wt" "$branch" "$base" || die "creating the worktree for $id"
      wt_is_registered "$RIG_PATH" "$wt" || die "the worktree for $id is not registered"
      prepare_worktree "$wt" "$id"
    fi
    gcx bd update "$bead" --set-metadata "work_dir=$wt" >/dev/null 2>&1
    rt_set "wt.$id" "$wt"
    rt_set "branch.$id" "$branch"
  done

  for id in $(packages); do
    gcx sling "$RIG_NAME/worker-$id" "$(rt_get "bead.$id")" --no-formula --no-convoy \
      > "$EVIDENCE/route-$id.txt" 2>&1 || say "routing $id reported a problem; see route-$id.txt"
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
  if [ -f "$wt/package.json" ]; then
    ( cd "$wt" && npm ci --silent ) > "$EVIDENCE/prepare-$id.txt" 2>&1 \
      || ( cd "$wt" && npm install --silent ) >> "$EVIDENCE/prepare-$id.txt" 2>&1 \
      || say "dependency install for $id reported a problem; see prepare-$id.txt"
  fi
}

# ===========================================================================
# STAGE await
# ===========================================================================

stage_await() {
  [ -n "$CITY" ] || die 'no city; city-up has not run'
  cd "$CITY" || die "cannot enter $CITY"

  local deadline=$(( $(date +%s) + ${DELIVERY_WORK_DEADLINE:-5400} ))
  local id bead remaining

  while true; do
    remaining=''
    for id in $(packages); do
      bead="$(rt_get "bead.$id")"
      [ -n "$bead" ] || continue
      # A package whose upstreams have not merged has no worktree yet and its
      # bead is correctly blocked; it is not something to wait for here.
      bead_is_closed "$bead" || remaining="$remaining $id"
    done
    [ -z "$remaining" ] && { say 'every work bead is closed'; return 0; }

    if [ "$(date +%s)" -ge "$deadline" ]; then
      say "deadline reached with work outstanding:$remaining"
      # NOT a failure of this stage. The packages that did finish are still
      # publishable, and reporting a hard failure here would throw away real
      # completed work because a sibling was slow.
      return 0
    fi
    sleep 15
  done
}

# ===========================================================================
# STAGE publish
# ===========================================================================

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

  # A dependent package's worktree is cut here, from the base as it stands now
  # that its upstreams have merged — so it consumes them through repository
  # state rather than by reading a sibling's working tree.
  if [ ! -d "$wt" ]; then
    git -C "$RIG_PATH" fetch -q origin "$DEFAULT_BRANCH"
    local mergedBase; mergedBase="$(git -C "$RIG_PATH" rev-parse "refs/remotes/origin/$DEFAULT_BRANCH")"
    wt_add "$RIG_PATH" "$wt" "$branch" "$mergedBase" || die "creating the worktree for $PACKAGE"
    prepare_worktree "$wt" "$PACKAGE"
    say "cut $PACKAGE from the merged base ${mergedBase:0:9} — its worker runs now"
    gcx sling "$RIG_NAME/worker-$PACKAGE" "$bead" --no-formula --no-convoy >/dev/null 2>&1
    local deadline=$(( $(date +%s) + ${DELIVERY_WORK_DEADLINE:-5400} ))
    while ! bead_is_closed "$bead"; do
      [ "$(date +%s)" -ge "$deadline" ] && die "$PACKAGE did not finish before its deadline"
      sleep 15
    done
  fi

  capture_final_bead_state "$bead" "$EVIDENCE" >/dev/null || true

  # THE BOUNDARY: what actually changed. bounded-project grants Write/Edit, so
  # the permission list buys scope clarity rather than containment. What
  # contains the worker is that only the controller publishes — and for that to
  # mean anything the controller must look at the change set first.
  local violations
  violations="$(publication_scope_violations "$wt" "$paths")"
  if [ -n "$violations" ]; then
    die "$PACKAGE changed paths it was not authorized to: $(tr '\n' ' ' <<<"$violations")"
  fi
  [ -f "$wt/$artifact" ] || die "$PACKAGE did not produce its required artifact $artifact"
  say "$PACKAGE produced $artifact within its authorized scope"

  run_project_gates "$wt" "$PACKAGE" || die "$PACKAGE failed the project's own gates"

  local -a pathList=()
  IFS=',' read -r -a pathList <<< "$paths"
  local commit
  commit="$(controller_commit "$wt" "feat($PACKAGE): $artifact

Published by the controller. The worker produced and verified this change
under bounded-project and is denied git by policy." "${pathList[@]}")" \
    || die "committing $PACKAGE"
  say "$PACKAGE committed ${commit:0:9}"

  git -C "$wt" push -q -u origin "$branch" > "$EVIDENCE/push-$PACKAGE.txt" 2>&1 \
    || die "pushing $branch: $(tail -2 "$EVIDENCE/push-$PACKAGE.txt")"

  "$GH" pr create --repo "$REPO_SLUG" --base "$DEFAULT_BRANCH" --head "$branch" \
    --title "feat($PACKAGE): $(pkg_field "$PACKAGE" title)" \
    --body "Managed delivery via Gas City. The worker produced this change under bounded-project; the controller validated, committed, pushed and opened this pull request. Package \`$PACKAGE\`, bead \`$bead\`." \
    > "$EVIDENCE/pr-$PACKAGE.txt" 2>&1 || true

  local prNum prHead
  prNum="$("$GH" pr list --repo "$REPO_SLUG" --head "$branch" --json number --jq '.[0].number' 2>/dev/null)"
  [ -n "$prNum" ] || die "no pull request for $branch: $(tail -2 "$EVIDENCE/pr-$PACKAGE.txt")"
  prHead="$("$GH" pr view "$prNum" --repo "$REPO_SLUG" --json headRefOid --jq '.headRefOid' 2>/dev/null)"
  [ "$prHead" = "$commit" ] || die "PR #$prNum head $prHead is not the controller commit $commit"
  say "$PACKAGE opened PR #$prNum at ${prHead:0:9}"
  rt_set "pr.$PACKAGE" "$prNum"
  rt_set "head.$PACKAGE" "$prHead"

  if [ "$NEED_CHECKS" = 'true' ]; then
    await_required_ci "$PACKAGE" "$prHead" || die "$PACKAGE did not pass required CI on its exact head"
  fi

  if [ "$NEED_MERGE" != 'true' ]; then
    say "$PACKAGE stops at an open pull request; merge authority was withheld"
    rt_set "published.$PACKAGE" "pr-open"
    return 0
  fi

  merge_pr "$PACKAGE" "$prNum" || die "$PACKAGE was not merged"
  rt_set "published.$PACKAGE" "merged"

  # Close the controller's merge bead with a typed shipped record, against a
  # local ref that has actually been moved to what GitHub did — the work-record
  # gate verifies reachability, and claiming it against a stale ref is a claim
  # that cannot be verified.
  git -C "$RIG_PATH" fetch -q origin "$DEFAULT_BRANCH" 2>/dev/null
  git -C "$RIG_PATH" update-ref "refs/heads/$DEFAULT_BRANCH" "refs/remotes/origin/$DEFAULT_BRANCH" 2>/dev/null
  local mergedSha; mergedSha="$(rt_get "merged.$PACKAGE")"
  gcx bd update "$mergeBead" \
    --set-metadata 'gc.work_outcome=shipped' \
    --set-metadata "gc.work_commit=$mergedSha" \
    --set-metadata "gc.work_branch=$DEFAULT_BRANCH" >/dev/null 2>&1
  gcx bd close "$mergeBead" --reason 'controller published, CI-verified and merged this package' >/dev/null 2>&1
  capture_final_bead_state "$mergeBead" "$EVIDENCE" >/dev/null || true
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
  if [ -f "$wt/package.json" ]; then
    local script
    for script in typecheck test; do
      if jq -e --arg s "$script" '.scripts[$s] // empty' < "$wt/package.json" >/dev/null 2>&1; then
        ( cd "$wt" && npm run "$script" --silent ) > "$EVIDENCE/gate-$script-$id.txt" 2>&1 \
          || { say "$id failed npm run $script; see gate-$script-$id.txt"; ok=1; }
      fi
    done
  elif [ -f "$wt/go.mod" ]; then
    ( cd "$wt" && go build ./... && go test ./... ) > "$EVIDENCE/gate-go-$id.txt" 2>&1 \
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
  local runId concl runHead

  while [ "$(date +%s)" -lt "$deadline" ]; do
    runId="$("$GH" api "repos/$REPO_SLUG/actions/runs?head_sha=$head&event=pull_request" \
      --jq '[.workflow_runs[]] | sort_by(.id) | last | .id' 2>/dev/null)"
    if [ -n "$runId" ] && [ "$runId" != 'null' ]; then
      concl="$("$GH" api "repos/$REPO_SLUG/actions/runs/$runId" --jq '.conclusion' 2>/dev/null)"
      [ -n "$concl" ] && [ "$concl" != 'null' ] && break
    fi
    sleep 15
  done

  if [ -z "$runId" ] || [ "$runId" = 'null' ]; then
    say "$id: no workflow run was found for head $head"
    return 1
  fi
  runHead="$("$GH" api "repos/$REPO_SLUG/actions/runs/$runId" --jq '.head_sha' 2>/dev/null)"
  "$GH" api "repos/$REPO_SLUG/actions/runs/$runId" > "$EVIDENCE/ci-$id.json" 2>/dev/null || true
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
# STAGE project — render the delivery projection
# ===========================================================================

stage_project() {
  [ -n "$CITY" ] || die 'no city; city-up has not run'

  local facts="$STATE/facts.json"
  local out="$STATE/PROJECT-STATE.yml"
  local cursor="$STATE/projector-cursor.json"

  # The facts document is assembled from what actually happened — the runtime
  # ledger and the forge — and never from what was planned. A package with no
  # merge recorded projects as whatever it actually reached, not as merged.
  build_facts > "$facts" || die 'assembling the projection facts'

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

emit_task_facts() {
  local id="$1" status prNum mergedSha
  prNum="$(rt_get "pr.$id")"
  mergedSha="$(rt_get "merged.$id")"
  status="$(package_status "$id")"

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
  printf '      "gateLabel": %s\n' "$(jq -Rn --arg v "$id" '$v')"
  printf '    }'
}

package_status() {
  local id="$1"
  [ -n "$(rt_get "merged.$id")" ] && { printf 'merged'; return; }
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
    || die "cloning to publish the projection: $(tail -2 "$EVIDENCE/publish-clone.txt")"
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
    || die "pushing the projection: $(tail -2 "$EVIDENCE/publish-push.txt")"
  say "projection published to $REPO_SLUG:$target"
}

# ===========================================================================

case "$STAGE" in
  city-up)            stage_city_up ;;
  dispatch)           stage_dispatch ;;
  await)              stage_await ;;
  publish)            stage_publish ;;
  project)            stage_project ;;
  publish-projection) stage_publish_projection ;;
esac
