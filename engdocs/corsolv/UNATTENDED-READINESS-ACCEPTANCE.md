# Gas City unattended execution readiness — acceptance contract

This document is written **before** the implementation and is the standard the
implementation is judged against. The `RESULT` column starts at `NOT REACHED`
for every row. A row moves to `PASS` only when the named acceptance test exists,
has been executed, and passed. `NOT REACHED` is never reported as `PASS`.

## Why this milestone exists

The first-runner programme reached technical acceptance, but every long session
in it died the same way: a blocker that was *knowable before execution started*
was discovered serially, mid-run, after work had already begun. The observed
population:

| Observed failure | Class |
| --- | --- |
| two writers mutating one worktree | concurrency |
| a branch switched underneath a live writer | concurrency |
| machine-specific absolute paths | environment |
| dirty worktree at run start | repository |
| stale server/build state from an earlier run | environment |
| GitHub merge/permission boundary found at merge time | governance |
| a session allocated to the wrong repository | ownership |
| a run ending at its first dependency instead of continuing | scheduling |

Each is mechanically detectable. None of them is a judgement call. That is
exactly the population a control layer can eliminate, and this milestone builds
that layer.

## Starting state (executable evidence, 2026-08-11)

| Fact | Value |
| --- | --- |
| Branch | `corsolv/p2-gascity-main-reconcile` |
| HEAD | `f7bdf5b8ea50a519d37640b6595b71ce78eeaccf` |
| Origin | `https://github.com/CorsolvSolutions/corsolv-delivery-engine.git` (fork, `viewerPermission: ADMIN`) |
| Upstream | `https://github.com/gastownhall/gascity.git` |
| Working tree | clean |
| Worktrees | one — the primary checkout |
| vs `origin/main` | 25 ahead, 0 behind |
| Open PR | #2, base `main`, `MERGEABLE` / `CLEAN`, full upstream CI green at exact HEAD |
| Execution host | WSL2 Ubuntu (`go1.26.5`, `bd 1.1.0`, `gc 1.4.1`, `dolt`, `tmux`, `jq`); `gh` on Windows only |

Live process evidence collected at the same moment — this is the "stale state"
failure class, observed, not hypothesised:

| Observation | Age |
| --- | --- |
| `gc supervisor run` (pid 388) | ~10.5 h |
| `dolt sql-server` rooted in the **source checkout** `cmd/gc/.gc/runtime/packs/dolt` | ~10.4 h |
| 4 leaked `tmux -L test-city` servers from `TestCmdNudgeStatusJSON` | ~10 h |
| ~28 Windows `claude` processes, oldest ~21 h | — |

## Scope boundaries

- `D:\Development\claude-project-management` — read-only compatibility checks only.
- `D:\Development\guk-bpm-platform` — **not mutated**. Readiness assessment only (§19).
- The Dashboard is the accepted control plane and is not modified unless an
  integration defect is proven.
- No `git reset --hard`, no `git clean`, no force push.

## The contract

`CURRENT IMPLEMENTATION` records what exists in the tree **today**, before this
milestone's code.

### A. One writer per worktree

- **Capability.** A mutable worktree admits exactly one writer at a time, and
  exclusion is enforced by the operating system rather than by convention.
- **Current implementation.** None. `internal/config/repo_cache_lock_{unix,windows}.go`
  and `internal/packregistry/file_lock_{unix,windows}.go` are *scoped, blocking*
  locks around a callback; neither models a lock **held** across a long run by a
  separate process, and neither carries owner evidence. Worker separation in
  `corsolv/p2-smoke` rests on session naming and per-task worktree paths only.
- **Gap.** Everything: try-acquire semantics, held ownership, owner evidence,
  stale recovery.
- **Acceptance test.** `TestClaimIsExclusive`, `TestClaimAcrossProcessesHasOneWinner`
  (real second OS process), and acceptance scenario TEST 1.
- **Result.** NOT REACHED

### B. Session / repository ownership

- **Capability.** A worker proves project ID, repository, worktree, branch and
  role *before* mutating anything; launched against the wrong repository it
  fails closed.
- **Current implementation.** None mechanical. `sa-preflight.sh` hardcodes
  `SOURCE_REPO=/mnt/d/Development/corsolv-delivery-engine` with an env override
  and never verifies the directory is the repository it believes.
- **Gap.** A declared, durable ownership assertion checked before mutation.
- **Acceptance test.** `TestOwnershipRejectsWrongRepository`,
  `TestOwnershipRejectsWrongBranch`, acceptance scenario TEST 2.
- **Result.** NOT REACHED

### C. Pre-run repository validation

- **Capability.** One consolidated preflight validates the repository before
  useful work starts: directory exists, is a git worktree, expected origin,
  expected branch, known HEAD, no unfinished merge/rebase/cherry-pick, no
  competing writer.
- **Current implementation.** Partial and run-specific. `sa-preflight.sh` probes
  a *route*, not an environment, and explicitly tolerates a dirty source tree.
- **Gap.** A general, reusable, declarative preflight.
- **Acceptance test.** `TestRepositoryChecks*` table tests over real temporary
  git repositories.
- **Result.** NOT REACHED

### D. Clean / known worktree state

- **Capability.** Worktree cleanliness is a first-class, declared expectation —
  `clean`, or `dirty-allowed` with the dirt enumerated in the report.
- **Current implementation.** `sa-preflight.sh` calls `git status --porcelain`
  and emits an advisory note. No verdict effect.
- **Gap.** Verdict participation and enumeration.
- **Acceptance test.** `TestWorktreeCleanlinessGate`.
- **Result.** NOT REACHED

### E. Expected branch / HEAD / remote

- **Capability.** Expected branch, expected origin URL and the observed HEAD are
  captured and enforced at preflight and recorded for the fence.
- **Current implementation.** None.
- **Gap.** All.
- **Acceptance test.** `TestRepositoryChecksRejectsWrongOrigin`, `...WrongBranch`.
- **Result.** NOT REACHED

### F. Branch movement detection

- **Capability.** Before each material mutation stage the run re-verifies branch,
  HEAD and lock ownership against the fence taken at claim time. An unauthorised
  move fails closed, recording expected vs observed.
- **Current implementation.** None. This is the exact hole through which "a
  branch switched underneath a live writer" happened.
- **Gap.** All.
- **Acceptance test.** `TestFenceDetectsBranchChange`, `TestFenceDetectsHeadMove`,
  `TestFenceAcceptsAuthorisedAdvance`, acceptance scenario TEST 3.
- **Result.** NOT REACHED

### G. Auth / permission readiness

- **Capability.** Credential readiness is classified before the run as `ready`,
  `missing`, `expired` or `human-auth-required`, without exposing secret values.
- **Current implementation.** None.
- **Gap.** All.
- **Acceptance test.** `TestCredentialClassification`, `TestReportRedactsSecrets`,
  acceptance scenario TEST 4.
- **Result.** NOT REACHED

### H. GitHub push / PR / CI / merge readiness

- **Capability.** Preflight establishes gh authentication, repository
  readability, push authority, PR-creation authority, CI readability, and merge
  capability *or* a named human boundary — before the run.
- **Current implementation.** None. Merge authority was previously discovered at
  merge time.
- **Gap.** All.
- **Acceptance test.** `TestGitHubReadinessFromProbe` (table-driven over recorded
  probe outputs) plus a live probe in the acceptance run.
- **Result.** NOT REACHED

### I. Required local tool availability

- **Capability.** Required executables are resolved, and where a minimum version
  is declared, the version is parsed and compared.
- **Current implementation.** Implicit `PATH` assumptions; failures surface as
  command-not-found mid-run.
- **Gap.** All.
- **Acceptance test.** `TestToolChecks`, `TestVersionAtLeast`.
- **Result.** NOT REACHED

### J. Machine-path / config readiness

- **Capability.** Required paths and configuration files are declared and
  verified; machine-specific absolute paths are supplied as configuration, never
  embedded in logic.
- **Current implementation.** Hardcoded `/mnt/d/...` and `$HOME/corsolv-p2/...`
  throughout `corsolv/p2-smoke`.
- **Gap.** A declarative spec that carries the paths.
- **Acceptance test.** `TestPathChecks`, and the spec round-trip test.
- **Result.** NOT REACHED

### K. Required server / port readiness

- **Capability.** Declared TCP endpoints are probed for reachability before the
  run.
- **Current implementation.** None.
- **Gap.** All.
- **Acceptance test.** `TestPortCheckReachable` / `TestPortCheckUnreachable`
  against a real listener.
- **Result.** NOT REACHED

### L. External dependency readiness

- **Capability.** Declared external dependencies are probed by a declared command
  whose exit status is the verdict.
- **Current implementation.** None.
- **Gap.** All.
- **Acceptance test.** `TestCommandCheck`.
- **Result.** NOT REACHED

### M. Long-run continuation

- **Capability.** A run does not end at its first blocked task. Blocking a task
  moves the queue on rather than terminating the run.
- **Current implementation.** None — every harness script is a straight line that
  exits on first failure.
- **Gap.** All.
- **Acceptance test.** `TestQueueContinuesPastBlockedPrimary`, acceptance
  scenario TEST 5 and the long run (§15).
- **Result.** NOT REACHED

### N. Fallback useful work

- **Capability.** When the primary path is blocked the queue selects declared
  fallback work of a lower class — never invented work, never busy-work.
- **Current implementation.** None.
- **Gap.** All.
- **Acceptance test.** `TestQueueSelectsFallbackWhenPrimaryBlocked`,
  `TestQueueNeverInventsWork`.
- **Result.** NOT REACHED

### O. Failure classification

- **Capability.** Every failure carries one of the declared classes:
  `retryable`, `code-defect`, `environment`, `auth`, `governance`, `dependency`,
  `concurrent-writer`, `external-service`, `human-decision`,
  `irreversible-boundary`.
- **Current implementation.** None — failures are exit codes.
- **Gap.** All.
- **Acceptance test.** `TestClassify*` table tests, `TestEveryClassHasAPolicy`.
- **Result.** NOT REACHED

### P. Recovery / retry

- **Capability.** Retry policy is per-class, bounded, with backoff; attempt
  history is persisted; no infinite retry.
- **Current implementation.** None.
- **Gap.** All.
- **Acceptance test.** `TestRetryPolicyIsBounded`, `TestBackoffIsMonotonic`,
  `TestAttemptHistoryPersists`, acceptance scenarios TEST 6 and TEST 7.
- **Result.** NOT REACHED

### Q. Human-boundary classification

- **Capability.** A human-only action is classified as such, surfaced, and never
  retried as if it were a transient fault.
- **Current implementation.** None.
- **Gap.** All.
- **Acceptance test.** `TestHumanBoundaryIsNotRetried`, acceptance scenario TEST 8.
- **Result.** NOT REACHED

### R. Durable execution evidence

- **Capability.** Run state is a durable append-only journal: crash-safe,
  resumable, no duplicate publication, no lost completion evidence.
- **Current implementation.** `internal/projector` is durable and resumable for
  the *delivery projection*. There is no journal for the *run*.
- **Gap.** The run journal.
- **Acceptance test.** `TestJournalReplayIsIdempotent`,
  `TestJournalSurvivesTruncatedTailRecord`, `TestResumeSkipsCompletedTasks`,
  acceptance scenario TEST 9.
- **Result.** NOT REACHED

### S. Dashboard state publication

- **Capability.** Enough durable state is published for the Dashboard to show
  run, worker/session, worktree, task, progress, attempts, blockers, completion
  gates and real timings — through the **existing** projector contract, without
  creating a second authority.
- **Current implementation.** `internal/projector` emits the consumer schema for
  a *completed* run. Nothing publishes *in-flight* progress.
- **Gap.** Heartbeat publication reusing the projector schema.
- **Acceptance test.** `TestHeartbeatRendersProjectorSchema`,
  `TestHeartbeatDoesNotInventAuthority`, plus §16 regression.
- **Result.** NOT REACHED

### T. No false pass / no not-reached inflation

- **Capability.** A check that did not execute is reported `NOT REACHED`, never
  `PASS`; the overall verdict is the worst constituent verdict.
- **Current implementation.** The `p2-smoke` harness already honours this in
  prose. Not mechanised.
- **Gap.** Mechanised in the verdict type.
- **Acceptance test.** `TestVerdictIsWorstOfConstituents`,
  `TestNotReachedNeverPasses`.
- **Result.** NOT REACHED

## Acceptance scenarios

Ten scenarios from the milestone brief, run against disposable fixtures. GUK BPM
is never used as a destructive fixture.

| # | Scenario | Result |
| --- | --- | --- |
| 1 | Two writers compete for one worktree → exactly one wins | NOT REACHED |
| 2 | Worker launched from the wrong repository → fails before mutation | NOT REACHED |
| 3 | Branch changes externally during execution → detected, stopped safely | NOT REACHED |
| 4 | GitHub auth missing → preflight catches it before the long run | NOT REACHED |
| 5 | Primary task blocked, fallback exists → useful work continues | NOT REACHED |
| 6 | Ordinary test failure → retried, not a stop condition | NOT REACHED |
| 7 | CI fails → diagnosed and retried against a new SHA | NOT REACHED |
| 8 | Human-only boundary → classified and surfaced, not retried | NOT REACHED |
| 9 | Controller interrupted and restarted → safe durable continuation | NOT REACHED |
| 10 | Multi-stage run executes for a meaningful period unattended | NOT REACHED |

## Stop conditions

The run stops **only** for: security approval; an irreversible or destructive
action; human authentication or MFA; a missing mandatory secret; genuine
governance approval; an ambiguous product or business decision; an externally
imposed service failure that prevents all useful work; or an exhausted safe work
queue.

The run does **not** stop for: ordinary test failure, lint failure, build
failure, merge conflict, a safe retryable CI failure, a known dependency with
other useful work available, or ordinary coding defects.

## Gate

`GAS CITY UNATTENDED EXECUTION READINESS: PASS` is recorded only when every row
above reads `PASS`, all ten scenarios pass, the long run (§15) has run
unattended for a meaningful multi-stage period, CI is green at the exact
delivered SHA, and the change is merged to `main` under normal governance.

## Task-tracking note

`AGENTS.md` directs all task tracking to `bd`. This repository has no beads
store (`bd list` → "no beads database found"), and creating one would add
untracked runtime state to a checkout that is kept mergeable with upstream. The
tracking authority for this milestone is therefore this document, which is
itself a required durable deliverable.
