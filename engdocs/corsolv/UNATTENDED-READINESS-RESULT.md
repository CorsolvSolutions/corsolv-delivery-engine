# Gas City unattended execution readiness — result

Judged against `UNATTENDED-READINESS-ACCEPTANCE.md`, which was written before any
of the implementation and has received exactly one edit since: a pointer to this
document. Its RESULT columns are deliberately left at their pre-implementation
values, so the standard cannot drift toward what was built. `NOT REACHED` is
never reported as `PASS`.

## Verdict

**GAS CITY UNATTENDED EXECUTION READINESS: PASS**, with two named human
boundaries that the runs discovered up front rather than collided with.

## What was built

`internal/unattended` — a fork-owned control layer — plus `corsolv/unattended-run`,
the command that drives it, and the Delivery Engine's own run configuration under
`corsolv/unattended/`.

| Concern | Where |
| --- | --- |
| Writer lock | `lock.go`, `lock_unix.go`, `lock_windows.go` |
| Repository probing | `gitprobe.go` |
| Session/repository ownership | `ownership.go` |
| Branch and HEAD fence | `fence.go` |
| Verdict algebra | `verdict.go`, `check.go` |
| Declarative run spec | `spec.go`, `plan.go` |
| Preflight | `preflight.go`, `probes.go`, `github.go`, `version.go` |
| Failure classes and retry | `failure.go` |
| Work queue and fallback | `queue.go` |
| Durable journal and resume | `journal.go` |
| Progress and completion events | `progress.go` |
| Run loop | `runner.go`, `session.go` |
| Dashboard publication | `publish.go` |

## The capability contract

| # | Capability | Result | Proof |
| --- | --- | --- | --- |
| A | One writer per worktree | **PASS** | `TestAcquireIsExclusive`, `TestConcurrentGoroutinesElectExactlyOneWriter`, and across real OS processes `TestAcquireAcrossProcessesIsDenied`, `TestConcurrentProcessesElectExactlyOneWriter`. Proved live: a second `unattended-run` against the running one was refused, naming run, session and pid. |
| B | Session / repository ownership | **PASS** | `TestOwnershipRejectsWrongRepository`, `...WrongBranch`, `...DetachedHead`, `TestWrongRepositoryIsRefusedBeforeAnyMutation` |
| C | Pre-run repository validation | **PASS** | `TestProbeRepo*`, `TestOwnershipAcceptsTheDeclaredRepository` |
| D | Clean / known worktree state | **PASS** | `TestWorktreeCleanlinessGate`. Proved live: the first real preflight refused the tree while it was dirty. |
| E | Expected branch / HEAD / remote | **PASS** | `TestOwnershipRejectsWrongBranch`, `TestNormalizeRemoteURLTreatsEquivalentFormsAsOne` |
| F | Branch movement detection | **PASS** | `TestFenceDetectsBranchChange`, `...UnauthorisedHeadMove`, `TestAuthorisedAdvanceCannotLaunderABranchChange`, `TestRunStopsWhenTheBranchMovesUnderneathIt` |
| G | Auth / permission readiness | **PASS** | `TestClassifyCredential` (11 cases), `TestCredentialTroubleIsABoundaryWhenAHumanActionIsNamed`, `TestRedactRemovesCredentialMaterial` |
| H | GitHub push / PR / CI / merge readiness | **PASS** | `TestGitHub*` over recorded probes, plus a live probe: authenticated as `Corsolv`, `ADMIN`, push/PR/checks all readable, merge held as a declared boundary |
| I | Required local tool availability | **PASS** | `TestToolChecks`, `TestToolChecksRefuseAnUnreadableVersion`, `TestVersionAtLeast` |
| J | Machine-path / config readiness | **PASS** | `TestPathChecks`, `TestPathChecksDoNotConjureProjectDirectories`, `TestWithinPath` |
| K | Required server / port readiness | **PASS** | `TestPortChecks` against a real listener |
| L | External dependency readiness | **PASS** | `TestCommandChecks` |
| M | Long-run continuation | **PASS** | `TestRunContinuesPastAnOrdinaryTestFailure`, `TestRunFallsBackWhenThePrimaryPathExhaustsItsAttempts`; both real runs continued past failures |
| N | Fallback useful work | **PASS** | `TestQueueSelectsFallbackWhenThePrimaryPathIsBlocked`, `TestQueueNeverInventsWork` |
| O | Failure classification | **PASS** | `TestClassifyRecognizesTheObservedFailures` (14 cases), `TestEveryFailureClassHasAPolicy` |
| P | Recovery / retry | **PASS** | `TestRetryIsAlwaysBounded`, `TestBackoffGrowsAndIsCapped`, `TestRetryKeepsTheTaskPendingUntilItsBudgetIsSpent` |
| Q | Human-boundary classification | **PASS** | `TestRunHoldsWorkBehindAHumanBoundaryAndReportsIt`, `TestAnAuthFailureIsNotRetried` |
| R | Durable execution evidence | **PASS** | `TestJournalToleratesATruncatedTailRecord`, `TestJournalRefusesCorruptionInTheMiddle`, `TestReplay*`, `TestFailureOutputIsCapturedWhereAPersonCanReadIt` |
| S | Dashboard state publication | **PASS** | `TestPublish*` — renders through the existing projector, refuses a status outside its vocabulary, never claims a status the work did not earn |
| T | No false pass / no not-reached inflation | **PASS** | `TestNotReachedNeverPasses`, `TestWorstOutcomeIsTheWorstConstituent`, `TestUnknownOutcomeIsTreatedAsUnproven` |

## The ten acceptance scenarios

| # | Scenario | Result | Where proved |
| --- | --- | --- | --- |
| 1 | Two writers compete → exactly one wins | **PASS** | Unit, cross-process, and live against the real repository mid-run |
| 2 | Wrong repository → fails before mutation | **PASS** | `TestWrongRepositoryIsRefusedBeforeAnyMutation` |
| 3 | Branch changes externally → detected, stopped | **PASS** | `TestRunStopsWhenTheBranchMovesUnderneathIt` |
| 4 | GitHub auth missing → caught at preflight | **PASS** | `TestGitHubUnauthenticatedIsABoundaryAndStopsTheRestBeingClaimed`; observed live before the gh path was declared in the spec |
| 5 | Primary blocked, fallback exists → work continues | **PASS** | `TestRunFallsBackWhenThePrimaryPathExhaustsItsAttempts`; both real runs did it |
| 6 | Ordinary test failure → retried, not a stop | **PASS** | `TestRunContinuesPastAnOrdinaryTestFailure`; live, `cross-compile-windows` retried twice and the run continued through eleven more tasks |
| 7 | CI fails → diagnosed and retried | **PASS (in part)** — see below | Retry-against-new-SHA is proved by the delivery cycle, not by an automated scenario |
| 8 | Human boundary → classified, surfaced, not retried | **PASS** | `TestRunHoldsWorkBehindAHumanBoundaryAndReportsIt`; live, `guk-deploy-dry-run` was held before either run started and attempted zero times |
| 9 | Controller interrupted → durable continuation | **PASS** | `TestInterruptedRunResumesWithoutRepeatingCompletedWork`; live, the endurance run was SIGKILLed and restarted, skipping the three tasks it had genuinely completed |
| 10 | Multi-stage run over a meaningful period | **PASS** | 47m08s of unattended execution, eleven declared tasks across five bands |

### Scenario 7, stated precisely

The automated coverage proves the *mechanism*: a CI failure classifies as
`external-service` or `governance` by signature, those classes carry the retry
and boundary policies they should, and a task retried after a mutation
re-verifies the fence and re-reads HEAD, so a retry necessarily runs against the
new SHA. What is **not** claimed is an end-to-end scenario in which a red CI run
was diagnosed and fixed by an unattended run without a person present. The
delivery of this branch exercised the human-in-the-loop version of that cycle.
Reporting it as fully proved would be the kind of inflation this contract forbids.

## The two real runs

| | Run 1 — mechanism | Run 2 — endurance |
| --- | --- | --- |
| Run ID | `delivery-engine-readiness-2026-08-11` | `delivery-engine-endurance-2026-08-11` |
| Duration | 8m04s | **47m08s** |
| Declared tasks | 14 across 5 bands | 11 across 5 bands |
| Succeeded | 12 | 8 |
| Failed | 1 | 2 |
| Held at a boundary | 1 | 1 |
| Attempts | 15 | — |
| Retries taken | 2 | 2 |
| Human interventions during the run | **0** | **0** |
| Fence | taken `80967f440`, verified before the mutation, advanced to `922a7cf77` | taken, verified, advanced |
| Mutating commits made by the run | 1 (`922a7cf77`) | 1 |

Neither run was touched while it held the worktree.

## What the runs found

The runs were not a demonstration. They found five real defects, four of them in
the control layer itself, and every one was diagnosed from the run's own durable
evidence without re-running anything.

1. **Orphaned grandchildren could hold a run open past its timeout.** A task
   declaring a one-second timeout around a sixty-second sleep ran the full sixty:
   cancelling killed the shell, but the `sleep` it spawned inherited the output
   pipes. Commands now run in their own process group with a wait delay. The
   package's own suite went from 63s to 4.6s.
2. **The cross-compile task asked for something this repository never
   supported.** `go build ./...` for Windows fails on upstream packages that call
   `syscall.Kill` and `syscall.Flock` directly. Pre-existing, not a regression;
   the check is now scoped to what the fork owns.
3. **The heartbeat called ordinary work a fallback.** `UsingFallback` was
   computed from the band alone, so validation work reported "the primary path is
   blocked" while every primary task had already succeeded.
4. **Resume credited another run's history.** The endurance run shared a state
   directory with the run before it, three task IDs matched, and it began by
   reporting `succeeded=3` for work it had never done.
5. **`status` answered a question about the live run with a fact about a dead
   one.** A previous run's completion record masked the running one.

Two further gaps were closed as a result: a failing task's output is now written
to `<stateDir>/failures/`, redacted, with the journal pointing at it — because
the journal deliberately keeps one line per record and had nowhere to put it; and
a spec classification rule that matched the word `GOCACHE` in an echoed command
line was mis-filing genuine test failures as environment faults.

The endurance run also failed the project's own unit baseline, which turned out
to be three real defects in this work: a missing `testenv_import_test.go`, a
resource-census ratchet violation (the census forbids untagged subprocess, sleep
and listener growth — the cross-process and socket cases now carry the
integration tag, and one sleep became a channel barrier), and a test of this
package's own that asserted the wrong thing about credential prompts. All three
are fixed and `make test` is green.

## Standing limitations

- **The writer lock does not compose across OS boundaries.** A Windows
  byte-range lock and a WSL `flock` on the same DrvFs path do not see each other.
  Where the recorded owner names another host or OS family, `Acquire` refuses
  outright rather than guessing, and `ForceClearOwner` is the governed way
  through. Within one OS the lock is authoritative.
- **The Windows lock path is compiled and vetted, not executed.** No Go
  toolchain is installed on the Windows side of this host.
- **The delivery projection only carries tasks that declare a delivery status.**
  That is deliberate — most of a run is internal machinery — but it means the
  dashboard sees a subset of a run by design.
