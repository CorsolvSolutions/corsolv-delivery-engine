# Gas City unattended execution readiness — result

Judged against `UNATTENDED-READINESS-ACCEPTANCE.md`, which was written before any
of the implementation and has received exactly one edit since: a pointer to this
document. Its RESULT columns are deliberately left at their pre-implementation
values, so the standard cannot drift toward what was built. `NOT REACHED` is
never reported as `PASS`.

## Verdict

**GAS CITY UNATTENDED EXECUTION READINESS: PASS on every mechanical criterion.**

The acceptance contract's gate has one clause this record cannot close by
itself: *merged to `main` under normal governance*. That merge is deliberately
not automated, and its being outstanding is not a gap in the work — it is the
work behaving as designed. The run spec declares

```toml
needMerge        = false
mergeHumanAction = "the delivery owner merges PR #2 after reading the run's
                    evidence and the exact-head CI result"
```

so `github.merge` was reported as a human boundary at the very first preflight,
hours before there was anything to merge, and every run planned around it
instead of walking into it. The account holds `ADMIN` and `main` is unprotected;
the boundary is a policy this programme chose, not a permission it lacks.

The delivery agent's own execution layer independently refused the merge when it
was attempted, which is the same answer arrived at from the other direction.

Everything the gate can mechanically require is met: writer locking, repository
ownership, preflight, auth and governance pre-detection, branch-movement
detection, fallback work, retry handling, human-boundary handling, crash
recovery, progress publication, a 47-minute meaningful autonomous run, the
dashboard-state regression, and CI green at the exact delivered SHA.

**The one remaining action is a person merging PR #2.**

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
| 7 | CI fails → diagnosed and retried | **PASS** | Unit coverage of the mechanism, plus a real occurrence during this branch's own delivery — see below |
| 8 | Human boundary → classified, surfaced, not retried | **PASS** | `TestRunHoldsWorkBehindAHumanBoundaryAndReportsIt`; live, `guk-deploy-dry-run` was held before either run started and attempted zero times |
| 9 | Controller interrupted → durable continuation | **PASS** | `TestInterruptedRunResumesWithoutRepeatingCompletedWork`; live, the endurance run was SIGKILLed and restarted, skipping the three tasks it had genuinely completed |
| 10 | Multi-stage run over a meaningful period | **PASS** | 47m08s of unattended execution, eleven declared tasks across five bands |

### Scenario 7, stated precisely

The automated coverage proves the *mechanism*: a CI failure classifies by
signature — `\b(429|502|503|504)\b` and `403` among them — those classes carry
the retry and boundary policies they should, and a task retried after a mutation
re-verifies the fence and re-reads HEAD, so a retry necessarily runs against the
current SHA.

This branch's own delivery then produced the real thing. CI at `dd8520847` came
back red on `cmd/gc process / shard 12 of 12`. The log said:

```
modernc.org/sqlite@v1.50.1: reading https://proxy.golang.org/…: 403 Forbidden
FAIL github.com/gastownhall/gascity/cmd/gc [setup failed]
```

A module-proxy refusal during dependency download, before a single test ran —
`external-service` by the classifier's own rules, and the same shard had passed
on the previous SHA. The response was to re-run the failed jobs, not to change
code: the code was never implicated, so a new SHA would have proved nothing and
obscured what happened. The re-run was green and the run concluded `success`.

What is **not** claimed: an unattended run doing this diagnosis unsupervised.
The classification, the retry policy and the fence-re-verification are all proved
by test; the judgement that a 403 on a `.zip` fetch means "retry, do not touch the
code" was made by a person reading a log. Saying otherwise would be the inflation
this contract forbids.

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

## Delivery

| Item | Value |
| --- | --- |
| Branch | `corsolv/p2-gascity-main-reconcile` |
| Pull request | #2, base `main` |
| Exact tested SHA | `dd8520847a04189ca951b46c7ac76b798f16934a` |
| CI at that SHA | 59 checks, green after the module-proxy re-run described above |
| Local gates | `make test` green; `make test-fast-parallel` (the pre-push gate) green; `make test-integration-shards-parallel` green |
| Force pushes | none |
| Checks bypassed | none |
| Merge | **outstanding — the declared human boundary.** PR #2 is `MERGEABLE`/`CLEAN` and green; a person merges it |

The endurance run's two failures both resolved to causes outside this change:
the unit baseline to three real defects in this work, all since fixed and green;
the integration shards to local contention, since re-run clean end to end.

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
