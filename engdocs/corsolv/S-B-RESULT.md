# Corsolv S-B — Promoted Run (Remote Acceptance Half)

S-B OVERALL: PASS

| Adjudication | Count |
| --- | --- |
| Mandatory PASS | 98 |
| Mandatory FAIL | 0 |
| Mandatory NOT REACHED | 0 |

## What this stage closes

S-A proved the local coordination properties and claimed only the LOCAL half of
criterion 10. This stage runs the same W1/W2/W3 graph against the real GitHub
repository, so the dependent workstream is released only after its upstreams are
**merged remotely**.

| Half | Meaning | Status |
| --- | --- | --- |
| Local (S-A) | controller integrates validated commits into the run base | PASS (run `sa-20260810T142219Z`) |
| Remote (S-B) | GitHub merge after PR + exact-head CI + independent assurance | PASS |
| Full criterion 10 | both halves | COMPLETE |

## Scope decisions, recorded

**The rig is a working clone.** `gc rig add` writes a bead store, runtime state
and a .gitignore into its rig and makes its own commit; pointing that at the
authoritative checkout would mutate the repository holding the POC's own
acceptance record. PRs, CI runs and merges are real and land on
`CorsolvSolutions/corsolv-autonomy-poc`.

**The integration target is `sb/20260810T171804Z/base`, not `main`.** W1/W2/W3 are already
merged into main (PRs #1/#2/#3) by the original PowerShell controller, so the
same three tasks cannot be re-expressed as a diff against main. The graph is
replayed from the POC's own pre-workstream base `8c4f7c7`. Criterion 11
("all successful work entered main through PRs") is already satisfied on main by
the original run and is **not** re-proved here against main.

## Foundation

| Item | Value |
| --- | --- |
| Run ID | `sb-20260810T171804Z` |
| Delivery-engine source SHA | `006bcad884d79a7af3929857e674a9215be5b2f7` |
| Binary SHA256 | `23a3a3a8eac5ec29efe0e6330aee74eb5cff2eb70f9305ad783d6f8963950124` |
| Target repository | `CorsolvSolutions/corsolv-autonomy-poc` |
| Replay base | `8c4f7c7028c5baa878bbeefe17446398849f409a` |
| S-B base branch | `sb/20260810T171804Z/base` @ `2aa79b28f61957e131f688c5c25513767908b417` |
| Final S-B base | `941d2a1d95c7e38719084b7baac2d16d75df2f50` |
| Local Node | `v24.19.0` (CI pins Node 24) |
| Work beads | W1=`sr2-5yl` W2=`sr2-7wy` W3=`sr2-dx3` |
| Merge beads | W1-int=`sr2-1gi` W2-int=`sr2-gke` |

## Remote evidence

| Item | Value |
| --- | --- |
| Pull requests | w1=#13 w2=#14 w3=#15  |
| PR head SHAs | w1=d75f28e57c8c8063d10667ceeb6571b09850d7af w2=1747c5cebf4648e60b4a190160d4b3663eb3230c w3=1121c07c97ddd7f6b89d9c7486e23d996e714fdf  |
| CI runs (run=head) | w1=31413560248(d75f28e57c8c8063d10667ceeb6571b09850d7af) w2=31413700897(1747c5cebf4648e60b4a190160d4b3663eb3230c) w3=31414024036(1121c07c97ddd7f6b89d9c7486e23d996e714fdf)  |
| Merge commits | w1=3365da401bf5782bf923270735f9571d28451e83 w2=a100b3b2ce71f25d21dc1c8490f8a2e00415c6ca w3=941d2a1d95c7e38719084b7baac2d16d75df2f50  |
| Release moment (both upstreams merged) | 2026-08-10T17:23:14Z |
| W3 became ready | 2026-08-10T17:23:16Z |
| W3 worker started | 2026-08-10T17:23:38Z |
| Post-release directives naming W3 | none |

## Worker posture

Workers ran `bounded-project`: Read/Write/Edit/Glob/Grep, the pool-worker
lifecycle, and three named project gates (typecheck/build/test). No git, no gh,
no shell family. Publication was gated on the changed-file set matching each
bead's `gc.authorised_paths`.

| Property | Evidence |
| --- | --- |
| Parallel work | max 2 concurrent workers ( 2390346 2391828) |
| W1+W2 wall clock | 103s |
| W3 wall clock | 177s |
| Live W1/W2 posture | PASS |
| Live W3 posture | PASS |
| Drain | settled in 1s |

## Control ledger

| Control | Status | Reason | Subject |
| --- | --- | --- | --- |
| delivery-engine source tree is clean (corsolv/p2-gascity-main-reconcile @ 006bcad88) | PASS | — | — |
| supervisor runs the fingerprinted build (pid 2276487) | PASS | — | — |
| source sha | INFO | 006bcad884d79a7af3929857e674a9215be5b2f7 | — |
| binary sha256 | INFO | 23a3a3a8eac5ec29efe0e6330aee74eb5cff2eb70f9305ad783d6f8963950124 | — |
| local Node matches the CI major (v24.19.0) | PASS | — | — |
| npm version | INFO | 11.17.0 | — |
| controller holds GitHub authentication | PASS | — | — |
| gh token scopes | INFO | Token scopes: 'gist', 'read:org', 'repo', 'workflow' | — |
| target repository reachable (CorsolvSolutions/corsolv-autonomy-poc, default branch main) | PASS | — | — |
| working clone created (main 931f94361) | PASS | — | — |
| replay base predates the workstreams (8c4f7c702; src: src/index.test.ts src/index.ts ) | PASS | — | — |
| workflow watches sb/20260810T171804Z/base (job steps unchanged) | PASS | — | — |
| S-B base pushed (2aa79b28f) | PASS | — | — |
| rig beads store initialized | PASS | — | — |
| resolved the SDK pool-worker prompt template | PASS | — | — |
| three bounded-project worker agents are configured | PASS | — | — |
| bounded-project selection survives config resolution | PASS | — | — |
| three work beads created (W1=sr2-5yl W2=sr2-7wy W3=sr2-dx3) | PASS | — | — |
| two controller merge beads created and claimed (W1-int=sr2-1gi W2-int=sr2-gke) | PASS | — | — |
| sr2-5yl declares its required artifact and authorised paths (src/add.ts) | PASS | — | — |
| sr2-7wy declares its required artifact and authorised paths (src/multiply.ts) | PASS | — | — |
| sr2-dx3 declares its required artifact and authorised paths (src/calculator.ts) | PASS | — | — |
| W3 is BLOCKED by both merge beads | PASS | — | — |
| W3 withheld before its upstreams merge | PASS | — | — |
| W1 and W2 are ready | PASS | — | — |
| sr2-5yl worktree dependencies installed by the controller | PASS | — | — |
| sr2-5yl worktree created and legacy work_dir stamped before dispatch (worker-w1) | PASS | — | — |
| sr2-7wy worktree dependencies installed by the controller | PASS | — | — |
| sr2-7wy worktree created and legacy work_dir stamped before dispatch (worker-w2) | PASS | — | — |
| W3 worktree deliberately absent until both upstreams merge | PASS | — | — |
| W1 and W2 worktrees are pairwise distinct | PASS | — | — |
| sr2-5yl routed before any worker started | PASS | — | — |
| sr2-7wy routed before any worker started | PASS | — | — |
| sr2-dx3 routed before any worker started | PASS | — | — |
| W3 remains blocked after being routed | PASS | — | — |
| live worker posture captured | INFO | PASS (pids: 2390346 2391828) | — |
| W1 and W2 both closed | PASS | — | — |
| W1 and W2 genuinely overlapped (2 concurrent workers: 2390346 2391828) | PASS | — | — |
| no W3 worker existed while W3 was blocked | PASS | — | — |
| W1+W2 wall clock | INFO | 103s | — |
| w1 publication scope is exactly what the bead authorised (src/add.ts,src/add.test.ts) | PASS | — | — |
| w1 produced its required artifact (src/add.ts) | PASS | — | — |
| w1 controller typecheck passes | PASS | — | — |
| w1 controller tests pass | PASS | — | — |
| w1 controller committed (d75f28e57) | PASS | — | — |
| w1 controller pushed sb/20260810T171804Z/w1-add | PASS | — | — |
| w1 pull request opened (#13) | PASS | — | — |
| w1 PR head SHA is the controller commit (d75f28e57c8c8063d10667ceeb6571b09850d7af) | PASS | — | — |
| w1 CI tested the exact PR head SHA (run 31413560248, head d75f28e57c8c8063d10667ceeb6571b09850d7af) | PASS | — | — |
| w1 required CI passed (run 31413560248) | PASS | — | — |
| w1 independent assurance passed | PASS | — | — |
| w1 merged through repository governance (PR #13, squash) | PASS | — | — |
| w1 merge state reconciled (MERGED, 3365da401) | PASS | — | — |
| w1 merge commit is reachable on the local base after reconciliation | PASS | — | — |
| w2 publication scope is exactly what the bead authorised (src/multiply.ts,src/multiply.test.ts) | PASS | — | — |
| w2 produced its required artifact (src/multiply.ts) | PASS | — | — |
| w2 controller typecheck passes | PASS | — | — |
| w2 controller tests pass | PASS | — | — |
| w2 controller committed (1747c5ceb) | PASS | — | — |
| w2 controller pushed sb/20260810T171804Z/w2-multiply | PASS | — | — |
| w2 pull request opened (#14) | PASS | — | — |
| w2 PR head SHA is the controller commit (1747c5cebf4648e60b4a190160d4b3663eb3230c) | PASS | — | — |
| w2 CI tested the exact PR head SHA (run 31413700897, head 1747c5cebf4648e60b4a190160d4b3663eb3230c) | PASS | — | — |
| w2 required CI passed (run 31413700897) | PASS | — | — |
| w2 independent assurance passed | PASS | — | — |
| w2 merged through repository governance (PR #14, squash) | PASS | — | — |
| w2 merge state reconciled (MERGED, a100b3b2c) | PASS | — | — |
| w2 merge commit is reachable on the local base after reconciliation | PASS | — | — |
| sr2-1gi merge bead closed by the controller with a typed shipped record | PASS | — | — |
| W3 worktree created from the MERGED base (a100b3b2c) | PASS | — | — |
| W3 worktree carries both merged upstreams via repository state | PASS | — | — |
| W3 worktree dependencies installed by the controller | PASS | — | — |
| sr2-gke merge bead closed by the controller with a typed shipped record | PASS | — | — |
| readiness projection exposed W3 automatically at 2026-08-10T17:23:16Z | PASS | — | — |
| W3 worker started autonomously | INFO | 2026-08-10T17:23:38Z (pids: 2444032) | — |
| live W3 worker posture captured | INFO | PASS | — |
| W3 closed | PASS | — | — |
| W3 wall clock | INFO | 177s | — |
| normal controller demand claimed and started W3 with no operator command (2026-08-10T17:23:38Z) | PASS | — | — |
| zero post-release directives naming W3 (release 2026-08-10T17:23:14Z) | PASS | — | — |
| w3 publication scope is exactly what the bead authorised (src/calculator.ts,src/calculator.test.ts) | PASS | — | — |
| w3 produced its required artifact (src/calculator.ts) | PASS | — | — |
| w3 controller typecheck passes | PASS | — | — |
| w3 controller tests pass | PASS | — | — |
| w3 controller committed (1121c07c9) | PASS | — | — |
| w3 controller pushed sb/20260810T171804Z/w3-calculator | PASS | — | — |
| w3 pull request opened (#15) | PASS | — | — |
| w3 PR head SHA is the controller commit (1121c07c97ddd7f6b89d9c7486e23d996e714fdf) | PASS | — | — |
| w3 CI tested the exact PR head SHA (run 31414024036, head 1121c07c97ddd7f6b89d9c7486e23d996e714fdf) | PASS | — | — |
| w3 required CI passed (run 31414024036) | PASS | — | — |
| w3 independent assurance passed | PASS | — | — |
| w3 merged through repository governance (PR #15, squash) | PASS | — | — |
| w3 merge state reconciled (MERGED, 941d2a1d9) | PASS | — | — |
| w3 merge commit is reachable on the local base after reconciliation | PASS | — | — |
| sr2-5yl typed work outcome is truthful (blocked) | PASS | — | — |
| sr2-5yl canonical gc.work_dir mirrored from the controller stamp | PASS | — | — |
| sr2-7wy typed work outcome is truthful (blocked) | PASS | — | — |
| sr2-7wy canonical gc.work_dir mirrored from the controller stamp | PASS | — | — |
| sr2-dx3 typed work outcome is truthful (blocked) | PASS | — | — |
| sr2-dx3 canonical gc.work_dir mirrored from the controller stamp | PASS | — | — |
| final S-B base sha | INFO | 941d2a1d95c7e38719084b7baac2d16d75df2f50 | — |
| final merged base checked out for verification (941d2a1d9) | PASS | — | — |
| final merged base typechecks | PASS | — | — |
| final merged base tests pass | PASS | — | — |
| every commit added by this run is controller-authored (Corsolv,Gas City Controller,) | PASS | — | — |
| live W1/W2 worker posture proved while those workers were alive | PASS | — | — |
| live W3 worker posture proved while that worker was alive | PASS | — | — |
| managed workers drained (settled in 1s) | PASS | — | — |

### Failures and unreached controls

None. Every mandatory control passed.

## Evidence directory

`/home/corsolvtech/corsolv-p2/sb-evidence-20260810T171804Z`
