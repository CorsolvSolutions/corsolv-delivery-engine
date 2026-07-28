# Release gate: session wedges in 'creating' forever when Runtime.Observed=false

**Deploy bead:** `ga-5hdwl6`
**Build bead:** `ga-uco1ol`
**Review bead:** `ga-uco1ol` (same bead — no separate review bead was created; the
review verdict is recorded directly in this bead's own notes)
**Reviewed commit:** `df22e72dcbddb10713a39f400d8972d7f815c4c1`
**Round 1 retried commit:** `eb87748d3c30e55f8edca958a91a435185f5c838` (superseded — see Round 2)
**Round 2 commits (new, not yet code-reviewed):** `0fcb4406cb0aa517a7631a9cc538f6bb19303ab9`
(red), `8eb3b4a11f3b65a7ff57f5fb0f016d1beb42964d` (green) — CLI age-gate rework
**Base checked:** `origin/main` at `679e6e46316aa50226ecf58e4f2df739dabcaf21`
(round 1 rebase target); round 2 re-rebased onto `08bba7a3a63faaece48cf88976e11c51727fb4e6`
(see Round 2 section below)
**Isolated branch:** `builder/ga-5hdwl6-session-creating-wedge`
**Round 1 verdict:** **PASS** (superseded by mayor REQUEST-CHANGES on PR #4772 —
see Round 2)
**Round 2 verdict:** **TECHNICAL GATE PASS — code-review criteria (1, 4) pending
mayor re-review of the new commits.** This document's technical criteria
(build/vet/tests/clean branch/clean divergence) are builder-self-certified, as
in round 1. The code-review criteria are NOT builder-self-certifiable: round 1's
`ga-uco1ol` PASS review covers only the patch-id-identical projection-fix
portion of the diff (confirmed unchanged below); the round 2 CLI age-gate
rework is new code submitted here for the first time and has not yet been
reviewed by anyone. Do not treat this document as full deploy clearance until
mayor re-reviews PR #4772's new commits.

## Why this is a retry (round 1)

`ga-5hdwl6`'s prior gate attempt FAILed on `make test-fast-parallel` due to
`TestCityRuntimeReloadDrainBoundedByTimeout` flaking under shard-parallel host
load — a pre-existing, unrelated timing issue tracked separately in
`ga-ajmj0y`, not a defect in this diff. That flake's fix (PR #4730, commit
`7a5bdeeee5c240663964916cea4c8f72dd91c1f4`, widening CI-jitter tolerance from
500ms to 3s) merged to `origin/main` at `2026-07-27T23:33:13Z`, confirmed via
`gh pr view 4730 --json state,mergedAt,mergeCommit`. This gate rebases the
reviewed commit onto a main that includes that fix and re-verifies from
scratch.

## Round 1 gate criteria

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | PASS | `ga-uco1ol`'s notes contain `VERDICT: PASS` from a full independent re-review dated 2026-07-28, reviewed at `df22e72d`. `git patch-id --stable` on `origin/builder/ga-uco1ol~2..origin/builder/ga-uco1ol` (the reviewed range) and on this branch's `HEAD~2..HEAD` (the rebased range) both produce `42a9356eedb69bf3304a3c0ba11988280e0389b8` — the diff content is byte-identical, only the base commit changed. The PASS verdict transfers unchanged. |
| 2 | Acceptance criteria met | PASS | Per `ga-uco1ol`'s SPEC COMPLIANCE finding: `projectRuntimeProjection` (`internal/session/lifecycle_projection.go`) no longer returns `RuntimeProjectionUnknown` unconditionally when `Runtime.Observed` is false — it now checks `creatingUnobservedExpired` first when `base == BaseStateCreating`, closing the short-circuit-before-staleness-heal gap. `creatingStateIsStale`'s `StaleCreatingAfter<=0` branch (the second gap, on the wake path) now falls back to `creatingUnobservedExpired` instead of hardcoding false. `cmd_session_wake.go`'s new switch arm makes `gc session wake` fail loudly instead of silently no-oping on a wedged session. The GREEN-phase zero-`Now` regression guard (`creatingUnobservedExpired` treats a zero `input.Now` as "cannot assess", not `time.Now()`) is present. |
| 3 | Tests pass | PASS | See Test evidence below. Covers unit build/vet, the feature's own new tests, the originally-flaky test in isolation, the full fast-parallel lane, a skip-hazard double-check on the CI-required `cmd_gc_process` filter's `TestTutorial01` path (`GC_FAST_UNIT=0`), and the full `make test-cmd-gc-process-parallel` run — the last of these was explicitly *not* run by the original reviewer (judged a narrower scoped double-check proportionate instead); this retry runs it in full per `engdocs/contributors/release-gate-criteria-conventions.md`. |
| 4 | No high-severity review findings open | PASS | Zero HIGH findings in `ga-uco1ol`'s OWASP-style security walk. One non-blocking finding: `cmd_session_wake.go`'s new loud-failure path (`sessionWakeStuckInFlightInfo` switch arm) has no direct test coverage; tracked separately as `ga-feuu02` (P3, non-blocking fast-follow), not required for this gate. |
| 5 | Final branch is clean | PASS | `git status --porcelain` is empty after committing this gate file on `builder/ga-5hdwl6-session-creating-wedge`. |
| 6 | Branch diverges cleanly from main | PASS | `git merge-tree --write-tree origin/main HEAD` succeeded with tree `57d58705330f2b4323f61ab90a885f0190da003c` against current `origin/main` tip `5fd0545f0` (one unrelated commit ahead: a push-ownership-guard deploy-gate fix, `ga-anwmtr` / #4761). No conflicts; no further rebase needed. |
| 7 | Single feature theme | PASS | Two commits: RED (failing repro test) then GREEN (fix) for one session-lifecycle defect. Patch-id-identical to the originally reviewed diff (criterion 1), so this holds unchanged from review: `cmd/gc/cmd_session_wake.go`, `internal/session/lifecycle_projection.go`, `internal/session/wedge_repro_test.go` — no unrelated changes. |

## Round 1 reviewed history

```text
eb87748d3 feat: green — Fix: sessions wedge in 'creating' forever when Runtime.Observed=false short-circuits the staleness heal (refs ga-uco1ol)
3504c2362 test(feat): red — Fix: sessions wedge in 'creating' forever when Runtime.Observed=false short-circuits the staleness heal (refs ga-uco1ol)
679e6e463 test(gc): migrate controller hang deadlines to shared wait helpers (#4745)
```

The commit set touches three files: `cmd/gc/cmd_session_wake.go`,
`internal/session/lifecycle_projection.go`, and the new
`internal/session/wedge_repro_test.go`. It does not change configuration,
HTTP/API schemas, generated assets, dashboard code, or CI workflow files.

## Round 1 test evidence

```text
go build ./...
PASS

go vet ./...
PASS

go test ./internal/session/... -run 'TestCreatingWedgesWhenRuntimeUnobserved|TestCreatingStaleDetectionIgnoresPendingCreateClaim|TestStaleCreatingAfterUnsetNeverGoesStale' -v -count=1
--- PASS: TestCreatingWedgesWhenRuntimeUnobserved (...)
    --- PASS: .../observed_dead_runtime_heals_to_asleep
    --- PASS: .../unobserved_runtime_stays_creating_forever
    --- PASS: .../young_unobserved_runtime_still_projects_creating
--- PASS: TestCreatingStaleDetectionIgnoresPendingCreateClaim (...)
--- PASS: TestStaleCreatingAfterUnsetNeverGoesStale (...)
PASS

go test ./internal/session/... -run 'TestCityRuntimeReloadDrainBoundedByTimeout' -count=4 -v
PASS (4/4 reps, 1.1-1.13s each, no timing variance)

make test-fast-parallel
Running 9 fast job(s) with LOCAL_TEST_JOBS=16
[fsys-darwin-compile] ok
[push-gate-lock-selftest] ok
[unit-core] ok
[unit-cmd-gc-1-of-6] ok   <- shard containing the originally-flaky test
[unit-cmd-gc-2-of-6] ok
[unit-cmd-gc-3-of-6] ok
[unit-cmd-gc-4-of-6] ok
[unit-cmd-gc-5-of-6] ok
[unit-cmd-gc-6-of-6] ok
All fast jobs passed
EXIT:0

go test ./cmd/gc/... -run 'Wake|Creating' -v -count=1        (GC_FAST_UNIT unset)
262 PASS, 0 FAIL, 0 SKIP

GC_FAST_UNIT=0 go test ./cmd/gc/... -run 'Wake|Creating' -v -count=1
262 PASS, 0 FAIL, 0 SKIP

make test-cmd-gc-process-parallel        (GC_FAST_UNIT=0, includes TestTutorial01)
Running 7 cmd-gc-process job(s) with LOCAL_TEST_JOBS=14
[cmd-gc-process-1-of-6] ok
[cmd-gc-process-2-of-6] ok
[cmd-gc-process-3-of-6] ok
[cmd-gc-process-4-of-6] ok
[cmd-gc-process-5-of-6] ok
[cmd-gc-process-6-of-6] ok
[productmetrics-testhook] ok
All cmd-gc-process jobs passed
EXIT:0
```

The unset-vs-`GC_FAST_UNIT=0` comparison on the `Wake|Creating` scope
reproduces the reviewer's own skip-hazard double-check (precedent
`ga-7jmqyx`/`ga-5bjrbd`) fresh on the rebased commit: identical PASS/FAIL/SKIP
counts either way confirm no test in that scope is silently skipped. The
`make test-cmd-gc-process-parallel` run additionally exercises `TestTutorial01`
under the real CI-required `cmd_gc_process` filter conditions — the coverage
gap `engdocs/contributors/release-gate-criteria-conventions.md` was written to
close, and one the original review explicitly chose not to run (judging the
narrower double-check proportionate). Both now pass on this diff.

## Why round 2: mayor REQUEST-CHANGES on PR #4772

Mayor reviewed PR #4772 directly in the checkout (2026-07-28) and returned
**DO NOT DEPLOY**, routing `ga-5hdwl6` back to builder. Full verdict recorded
verbatim on `ga-5hdwl6`'s comment thread. Three findings:

1. The projection fix (round 1) targets a mechanism that cannot occur in
   production: both production `RuntimeFacts{...}` construction sites
   (`cmd/gc/session_reconcile.go:887`, `cmd/gc/session_sleep.go:144-145`)
   hardcode `Observed: true`, so the new `!input.Runtime.Observed` branch in
   `projectRuntimeProjection` is unreachable from production, and every new
   test drives `Runtime{Observed: false}` — an input shape no production
   caller constructs. Mayor judged this **harmless defense-in-depth**, not a
   blocker: "The projection half is harmless defense-in-depth and can stay."
2. Round 1's CLI change (`cmd_session_wake.go`'s new switch arm) is a
   **confirmed regression**: `gc session wake` exits 1 on a *healthy*
   in-flight create, with no age check and no runtime probe, worsened by
   `sessionWakeHasRunnableTemplateInfo` returning `true` whenever
   `cfg == nil`. Zero test coverage existed for the new arm. **This is the
   blocking finding this round's commits address.**
3. The real root cause of the original wedge (`ga-pofwv9`) is still
   unconfirmed; the most plausible candidate — `PendingCreateClaim &&
   LastWokeAt == ""` racing ahead of any staleness check on the
   `Observed: true` path — was not actually excluded by
   `TestCreatingStaleDetectionIgnoresPendingCreateClaim`, which only drives
   `Observed: false` and returns before that branch is ever reached. Recorded
   on `ga-uco1ol` (the parent investigation bead) as an informational
   cross-reference; explicitly out of scope for this rework — mayor's WHAT TO
   DO list does not ask for root-cause work here, and item 1 above directs
   leaving the projection half untouched.

Mayor's WHAT TO DO list (verbatim, four items) and this round's disposition:

| Item | Instruction | Disposition |
|------|-------------|-------------|
| 1 | Age-gate the CLI arm using the *existing* `isStaleCreatingInfo` helper (`cmd/gc/city_runtime.go:2927`), not a new one — "keeps the CLI and the sweep agreeing" | Done: `isStaleCreatingInfo(res.Info)` added as a third conjunct on the `hasRunnableTemplate && sessionWakeStuckInFlightInfo(...)` switch case. `sessionWakeStuckInFlightInfo` itself is untouched. |
| 2 | Add `cmd_session_wake_test.go` coverage for the new arm, landing *with* this change, not deferred | Done: `TestDoSessionWake_StuckInFlightAgeGate` (4 subtests: fresh creating/start-pending wake normally; stale creating/start-pending reject). `ga-feuu02` (the P3 bead that had deferred this) closed as done-elsewhere. |
| 3 | Reword the failure message to state only what was checked — "has been in state X since TIMESTAMP without completing its create" — not "no live runtime", which the path never probes | Done: message now reads `session %s has been in state %q since %s without completing its create`, sourced from new helper `stuckCreatingSinceInfo` (mirrors `isStaleCreatingInfo`'s anchor preference — `PendingCreateStartedAt` else `CreatedAt` — purely to pick what to print, not to re-decide staleness). |
| 4 | The projection half is harmless defense-in-depth and can stay | Done (by omission): `internal/session/lifecycle_projection.go` is untouched this round. |

## Round 2 gate criteria

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | **PARTIAL** | The patch-id-identical projection-fix portion retains `ga-uco1ol`'s round-1 PASS (verified unchanged below). The new CLI age-gate rework (`0fcb4406c`, `8eb3b4a11`) is submitted here for the first time and has not been reviewed by anyone yet — that is the purpose of pushing this candidate to PR #4772. Not builder-self-certifiable. |
| 2 | Acceptance criteria met | PASS | All four WHAT TO DO items addressed — see table above. |
| 3 | Tests pass | PASS | See Round 2 test evidence below: build/vet, the new test in isolation, full `cmd/gc`+`internal/session` suite, `Wake\|Creating` scope with and without `GC_FAST_UNIT=0` (skip-hazard double-check), `make test-fast-parallel` (10/10), `make test-cmd-gc-process-parallel` (7/7, includes `TestTutorial01`). |
| 4 | No high-severity review findings open | **PARTIAL** | Mayor's finding 2 (the blocking one) is addressed this round. Finding 3 (root-cause uncertainty) is informational, tracked on `ga-uco1ol`, explicitly out of scope per mayor's own item 4. No new HIGH findings self-identified. Full disposition is mayor's call on re-review, not builder's. |
| 5 | Final branch is clean | PASS | `git status --porcelain` empty after each commit on `builder/ga-5hdwl6-session-creating-wedge`. |
| 6 | Branch diverges cleanly from main | PASS | Rebased onto `origin/main` @ `08bba7a3a63faaece48cf88976e11c51727fb4e6` with zero conflicts (`Successfully rebased and updated refs/heads/builder/ga-5hdwl6-session-creating-wedge`). `git merge-tree --write-tree origin/main HEAD` succeeds with tree `5445643ced265e6cdccb26e3068de9ceafaed0ed`. |
| 7 | Single feature theme | PASS | Two new commits, RED then GREEN, touching exactly `cmd/gc/cmd_session_wake.go` and `cmd/gc/cmd_session_wake_test.go` — the CLI-arm regression mayor flagged, nothing else. The carried-forward round-1 diff remains patch-id-identical (criterion below), so no unrelated drift was introduced by the rebase. |

**Patch-id continuity check:** `git patch-id --stable` on the round-1
reviewed range (`2b191d9aa~1..7bb30ddf8` post-rebase) reproduces
`42a9356eedb69bf3304a3c0ba11988280e0389b8` — identical to the value recorded
in this file's criterion 1 (round 1) before this round's rebase. Only the
base commit changed; the reviewed content did not.

## Round 2 history

```text
8eb3b4a11 fix: green — age-gate gc session wake's stuck-in-flight rejection via isStaleCreatingInfo (refs ga-5hdwl6)
0fcb4406c test(fix): red — gc session wake exits 1 on a healthy in-flight create with no age check (refs ga-5hdwl6)
74505eb6e docs(release-gates): add retry gate evidence for ga-5hdwl6
7bb30ddf8 feat: green — Fix: sessions wedge in 'creating' forever when Runtime.Observed=false short-circuits the staleness heal (refs ga-uco1ol)
2b191d9aa test(feat): red — Fix: sessions wedge in 'creating' forever when Runtime.Observed=false short-circuits the staleness heal (refs ga-uco1ol)
08bba7a3a fix(paths): route discovery + store-scope through the single path normalizer (#4695)
```

Two new commits on top of the unchanged round-1 pair, touching only
`cmd/gc/cmd_session_wake.go` and `cmd/gc/cmd_session_wake_test.go`. No
configuration, HTTP/API schema, generated asset, dashboard, or CI workflow
files touched.

## Round 2 test evidence

```text
go build ./...
BUILD OK

go vet ./...
VET OK

go test ./cmd/gc/... -run 'TestDoSessionWake_StuckInFlightAgeGate' -v -count=1
--- PASS: TestDoSessionWake_StuckInFlightAgeGate (0.00s)
    --- PASS: .../fresh_creating_wakes_normally (0.00s)
    --- PASS: .../fresh_start-pending_wakes_normally (0.00s)
    --- PASS: .../stale_creating_rejects_wake (0.00s)
    --- PASS: .../stale_start-pending_rejects_wake (0.00s)
PASS
ok  	github.com/gastownhall/gascity/cmd/gc	0.252s

go test ./cmd/gc/... ./internal/session/... -count=1
ok  	github.com/gastownhall/gascity/cmd/gc	329.384s
ok  	github.com/gastownhall/gascity/internal/session	0.680s
ok  	github.com/gastownhall/gascity/internal/session/sessiontest	0.019s

go test ./cmd/gc/... -run 'Wake|Creating' -v -count=1        (GC_FAST_UNIT unset)
263 PASS, 0 FAIL, 0 SKIP

GC_FAST_UNIT=0 go test ./cmd/gc/... -run 'Wake|Creating' -v -count=1
263 PASS, 0 FAIL, 0 SKIP

make test-fast-parallel
Running 10 fast job(s) with LOCAL_TEST_JOBS=15 inner_p=1
[fsys-darwin-compile] ok
[unit-core] ok
[push-gate-lock-selftest] ok
[local-concurrency-selftest] ok
[unit-cmd-gc-1-of-6] ok
[unit-cmd-gc-2-of-6] ok
[unit-cmd-gc-3-of-6] ok
[unit-cmd-gc-4-of-6] ok
[unit-cmd-gc-5-of-6] ok
[unit-cmd-gc-6-of-6] ok
All fast jobs passed
EXIT:0

make test-cmd-gc-process-parallel        (includes TestTutorial01)
Running 7 cmd-gc-process job(s) with LOCAL_TEST_JOBS=2 inner_p=1
[cmd-gc-process-1-of-6] ok
[cmd-gc-process-2-of-6] ok
[cmd-gc-process-3-of-6] ok
[cmd-gc-process-4-of-6] ok
[cmd-gc-process-5-of-6] ok
[cmd-gc-process-6-of-6] ok
[productmetrics-testhook] ok
All cmd-gc-process jobs passed
EXIT:0
```

263 vs round 1's 262 on the `Wake\|Creating` scope reflects exactly the one
new top-level test (`TestDoSessionWake_StuckInFlightAgeGate`); its four
subtests are indented `--- PASS` lines and are not double-counted by the
`^--- (PASS|FAIL|SKIP)` anchor used for this count, consistent with round 1's
methodology. Identical counts with `GC_FAST_UNIT` unset vs `=0` again confirm
no skip-hazard in this scope. `local-concurrency-selftest` in the fast-parallel
output is a job added to `main` since round 1 (unrelated to this diff) — not
a regression in this branch.

**Not yet done (this round):** pushing the branch and updating PR #4772; that
is the action this evidence gates.
