# Release gate: session wedges in 'creating' forever when Runtime.Observed=false

**Deploy bead:** `ga-5hdwl6`
**Build bead:** `ga-uco1ol`
**Review bead:** `ga-uco1ol` (same bead — no separate review bead was created; the
review verdict is recorded directly in this bead's own notes)
**Reviewed commit:** `df22e72dcbddb10713a39f400d8972d7f815c4c1`
**Retried commit (this gate):** `eb87748d3c30e55f8edca958a91a435185f5c838`
**Base checked:** `origin/main` at `679e6e46316aa50226ecf58e4f2df739dabcaf21`
(rebase target and full test-suite base); re-confirmed clean against current
tip `5fd0545f0` (see criterion 6)
**Isolated branch:** `builder/ga-5hdwl6-session-creating-wedge`
**Verdict:** **PASS**

## Why this is a retry

`ga-5hdwl6`'s prior gate attempt FAILed on `make test-fast-parallel` due to
`TestCityRuntimeReloadDrainBoundedByTimeout` flaking under shard-parallel host
load — a pre-existing, unrelated timing issue tracked separately in
`ga-ajmj0y`, not a defect in this diff. That flake's fix (PR #4730, commit
`7a5bdeeee5c240663964916cea4c8f72dd91c1f4`, widening CI-jitter tolerance from
500ms to 3s) merged to `origin/main` at `2026-07-27T23:33:13Z`, confirmed via
`gh pr view 4730 --json state,mergedAt,mergeCommit`. This gate rebases the
reviewed commit onto a main that includes that fix and re-verifies from
scratch.

## Gate criteria

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | PASS | `ga-uco1ol`'s notes contain `VERDICT: PASS` from a full independent re-review dated 2026-07-28, reviewed at `df22e72d`. `git patch-id --stable` on `origin/builder/ga-uco1ol~2..origin/builder/ga-uco1ol` (the reviewed range) and on this branch's `HEAD~2..HEAD` (the rebased range) both produce `42a9356eedb69bf3304a3c0ba11988280e0389b8` — the diff content is byte-identical, only the base commit changed. The PASS verdict transfers unchanged. |
| 2 | Acceptance criteria met | PASS | Per `ga-uco1ol`'s SPEC COMPLIANCE finding: `projectRuntimeProjection` (`internal/session/lifecycle_projection.go`) no longer returns `RuntimeProjectionUnknown` unconditionally when `Runtime.Observed` is false — it now checks `creatingUnobservedExpired` first when `base == BaseStateCreating`, closing the short-circuit-before-staleness-heal gap. `creatingStateIsStale`'s `StaleCreatingAfter<=0` branch (the second gap, on the wake path) now falls back to `creatingUnobservedExpired` instead of hardcoding false. `cmd_session_wake.go`'s new switch arm makes `gc session wake` fail loudly instead of silently no-oping on a wedged session. The GREEN-phase zero-`Now` regression guard (`creatingUnobservedExpired` treats a zero `input.Now` as "cannot assess", not `time.Now()`) is present. |
| 3 | Tests pass | PASS | See Test evidence below. Covers unit build/vet, the feature's own new tests, the originally-flaky test in isolation, the full fast-parallel lane, a skip-hazard double-check on the CI-required `cmd_gc_process` filter's `TestTutorial01` path (`GC_FAST_UNIT=0`), and the full `make test-cmd-gc-process-parallel` run — the last of these was explicitly *not* run by the original reviewer (judged a narrower scoped double-check proportionate instead); this retry runs it in full per `engdocs/contributors/release-gate-criteria-conventions.md`. |
| 4 | No high-severity review findings open | PASS | Zero HIGH findings in `ga-uco1ol`'s OWASP-style security walk. One non-blocking finding: `cmd_session_wake.go`'s new loud-failure path (`sessionWakeStuckInFlightInfo` switch arm) has no direct test coverage; tracked separately as `ga-feuu02` (P3, non-blocking fast-follow), not required for this gate. |
| 5 | Final branch is clean | PASS | `git status --porcelain` is empty after committing this gate file on `builder/ga-5hdwl6-session-creating-wedge`. |
| 6 | Branch diverges cleanly from main | PASS | `git merge-tree --write-tree origin/main HEAD` succeeded with tree `57d58705330f2b4323f61ab90a885f0190da003c` against current `origin/main` tip `5fd0545f0` (one unrelated commit ahead: a push-ownership-guard deploy-gate fix, `ga-anwmtr` / #4761). No conflicts; no further rebase needed. |
| 7 | Single feature theme | PASS | Two commits: RED (failing repro test) then GREEN (fix) for one session-lifecycle defect. Patch-id-identical to the originally reviewed diff (criterion 1), so this holds unchanged from review: `cmd/gc/cmd_session_wake.go`, `internal/session/lifecycle_projection.go`, `internal/session/wedge_repro_test.go` — no unrelated changes. |

## Reviewed history

```text
eb87748d3 feat: green — Fix: sessions wedge in 'creating' forever when Runtime.Observed=false short-circuits the staleness heal (refs ga-uco1ol)
3504c2362 test(feat): red — Fix: sessions wedge in 'creating' forever when Runtime.Observed=false short-circuits the staleness heal (refs ga-uco1ol)
679e6e463 test(gc): migrate controller hang deadlines to shared wait helpers (#4745)
```

The commit set touches three files: `cmd/gc/cmd_session_wake.go`,
`internal/session/lifecycle_projection.go`, and the new
`internal/session/wedge_repro_test.go`. It does not change configuration,
HTTP/API schemas, generated assets, dashboard code, or CI workflow files.

## Test evidence

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
