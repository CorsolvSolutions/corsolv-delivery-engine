# Corsolv S-A — Local Controlled First-Runner Acceptance

S-A OVERALL: PASS

| Adjudication | Count |
| --- | --- |
| Mandatory PASS | 72 |
| Mandatory FAIL | 0 |
| Mandatory NOT REACHED | 0 |

## Criterion 10 across the two stages

Criterion 10 of the POC brief — *"W3 started automatically only after W1 and W2
merged"* — spans both stages of this programme, and S-A may claim only its local
half.

| Half | Meaning | Status here |
| --- | --- | --- |
| Local (S-A) | the controller integrates validated A/B commits into the run base, and C is released, claimed and started with no operator command | PASS |
| Remote (S-B) | GitHub merge after PR + exact-head CI + independent assurance | **DEFERRED** |
| Full criterion | both halves | **INCOMPLETE** |

Per the POC brief, NOT REACHED is never reported as PASS. The remote half is not
attempted here and is not counted as a failure of S-A; it is the subject of S-B.

## Foundation

| Item | Value |
| --- | --- |
| Run ID | `sa-20260810T142219Z` |
| Source SHA | `28dc3ab1516829fdeb3c36b8c733e472fc401d2a` |
| Source branch | `corsolv/p2-gascity-main-reconcile` |
| Binary SHA256 | `f059df45dfad0e1cb28eeed129f015c423ac63707bc379549c96bf2a897e5a2c` |
| City | `/home/corsolvtech/corsolv-p2/sa-city-20260810T142219Z` |
| Rig | `/home/corsolvtech/corsolv-p2/sa-rig-20260810T142219Z` |
| Work beads | A=`sr2-u2h` B=`sr2-2t0` C=`sr2-691` |
| Integration beads | A-int=`sr2-blz` B-int=`sr2-cla` |

## The graph

```
A (sr2-u2h) ──> A-int (sr2-blz) ──┐
                            ├──> C (sr2-691)
B (sr2-2t0) ──> B-int (sr2-cla) ──┘
```

C gates on INTEGRATION, not on close. That is what makes autonomous
continuation and per-task worktree isolation compatible: C only becomes ready
once a base containing both upstream results exists.

## Worktree ownership

| Task | Agent | Worktree | Base |
| --- | --- | --- | --- |
| A | `sa-rig-20260810T142219Z/worker-a` | `/home/corsolvtech/corsolv-p2/sa-city-20260810T142219Z/.gc/worktrees/sa-rig-20260810T142219Z/worker-a` | `d7f2a9fc99c371074273d7d087e20896f943fa53` |
| B | `sa-rig-20260810T142219Z/worker-b` | `/home/corsolvtech/corsolv-p2/sa-city-20260810T142219Z/.gc/worktrees/sa-rig-20260810T142219Z/worker-b` | `d7f2a9fc99c371074273d7d087e20896f943fa53` |
| C | `sa-rig-20260810T142219Z/worker-c` | `/home/corsolvtech/corsolv-p2/sa-city-20260810T142219Z/.gc/worktrees/sa-rig-20260810T142219Z/worker-c` | `5c7809510e212867e848ca807afd81435ef0f59f` (integrated) |

Canonical `gc.work_dir` per bead:

```
sr2-u2h=/home/corsolvtech/corsolv-p2/sa-city-20260810T142219Z/.gc/worktrees/sa-rig-20260810T142219Z/worker-a
sr2-2t0=/home/corsolvtech/corsolv-p2/sa-city-20260810T142219Z/.gc/worktrees/sa-rig-20260810T142219Z/worker-b
sr2-691=/home/corsolvtech/corsolv-p2/sa-city-20260810T142219Z/.gc/worktrees/sa-rig-20260810T142219Z/worker-c

```

## Integration

| Item | Value |
| --- | --- |
| A validated commit | `3b4d71c848b37a6a3418f9e72a6c384ed2bb6f1b` |
| B validated commit | `8b3a616028e98d46d31ebbdbd09a1cf03af5b07f` |
| Integrated base (C's base) | `5c7809510e212867e848ca807afd81435ef0f59f` |
| C validated commit | `a9c23e7f8d2d720280ab4a13d436c323cffc4afa` |
| Final run base | `227825cc10659e27433d692c682d0341d137e36e` |

## Autonomous continuation (D3)

| Item | Value |
| --- | --- |
| All three routed before execution | yes (route epoch 1786371785) |
| C blocked while routed | yes |
| Release moment (second integration bead closed) | 2026-08-10T14:24:26Z |
| C became ready | 2026-08-10T14:24:28Z |
| C worker started | 2026-08-10T14:24:34Z |
| Post-release directives naming C | none |

The command ledger (`gc-commands.log`) records every controller action with its
timestamp, so "no operator restarted C" is an artifact rather than an assertion.

## Execution

| Property | Evidence |
| --- | --- |
| Parallel work | max 2 concurrent workers ( 1556507 1557644) |
| A+B wall clock | 66s |
| C wall clock | 65s |
| Distinct sessions | 3 |
| Drain | settled in 7s |
| Live A/B posture | PASS |
| Live C posture | PASS |
| Independent assurance | PASS |

## Control ledger

Every mandatory assertion, with its own identity and result. Generated from
`controls.tsv`, not from the console.

| Control | Status | Reason | Subject |
| --- | --- | --- | --- |
| source tree is clean (corsolv/p2-gascity-main-reconcile @ 28dc3ab15) | PASS | — | — |
| supervisor runs the fingerprinted build (pid 1531038) | PASS | — | — |
| source sha | INFO | 28dc3ab1516829fdeb3c36b8c733e472fc401d2a | — |
| binary sha256 | INFO | f059df45dfad0e1cb28eeed129f015c423ac63707bc379549c96bf2a897e5a2c | — |
| gc version | INFO | 1.4.1 (dev is expected on an untagged branch) | — |
| disposable git target created (base d7f2a9fc9) | PASS | — | — |
| work-record enforcement delivered to managed sessions | PASS | — | — |
| rig beads store initialized | PASS | — | — |
| resolved the SDK pool-worker prompt template | PASS | — | — |
| three per-task worker agents are configured | PASS | — | — |
| three work beads created (A=sr2-u2h B=sr2-2t0 C=sr2-691) | PASS | — | — |
| two controller integration beads created and claimed (A-int=sr2-blz B-int=sr2-cla) | PASS | — | — |
| sr2-u2h declares its required artifact (ALPHA.md) | PASS | — | — |
| sr2-2t0 declares its required artifact (BETA.md) | PASS | — | — |
| sr2-691 declares its required artifact (INDEX.md) | PASS | — | — |
| closure predicate rejects free-text CLOSED (structured status only) | PASS | — | — |
| C is BLOCKED by both integration beads | PASS | — | — |
| C withheld from ready work before its dependencies are integrated | PASS | — | — |
| A and B are ready | PASS | — | — |
| sr2-u2h worktree created and legacy work_dir stamped before dispatch (worker-a) | PASS | — | — |
| sr2-2t0 worktree created and legacy work_dir stamped before dispatch (worker-b) | PASS | — | — |
| C worktree deliberately absent until the integrated base exists | PASS | — | — |
| A and B worktrees are pairwise distinct | PASS | — | — |
| task worktrees start isolated from each other | PASS | — | — |
| sr2-u2h routed before any worker started (sa-rig-20260810T142219Z/worker-a) | PASS | — | — |
| sr2-2t0 routed before any worker started (sa-rig-20260810T142219Z/worker-b) | PASS | — | — |
| sr2-691 routed before any worker started (sa-rig-20260810T142219Z/worker-c) | PASS | — | — |
| C remains blocked after being routed | PASS | — | — |
| live worker posture captured | INFO | PASS (pids: 1556507 1557644) | — |
| A and B both closed | PASS | — | — |
| A and B genuinely overlapped (2 concurrent workers: 1556507 1557644) | PASS | — | — |
| no C worker existed while C was blocked | PASS | — | — |
| A+B wall clock | INFO | 66s | — |
| sr2-u2h final authoritative record is closed | PASS | — | — |
| sr2-u2h typed work outcome is truthful (blocked) | PASS | — | — |
| sr2-u2h produced ALPHA.md inside its own worktree with exact content | PASS | — | — |
| sr2-u2h artifact did not leak into the shared rig checkout | PASS | — | — |
| sr2-u2h validated result committed (3b4d71c84) and integrated (46544d105) | PASS | — | — |
| sr2-blz integration bead closed by the controller with a typed shipped record | PASS | — | — |
| sr2-2t0 final authoritative record is closed | PASS | — | — |
| sr2-2t0 typed work outcome is truthful (blocked) | PASS | — | — |
| sr2-2t0 produced BETA.md inside its own worktree with exact content | PASS | — | — |
| sr2-2t0 artifact did not leak into the shared rig checkout | PASS | — | — |
| sr2-2t0 validated result committed (8b3a61602) and integrated (5c7809510) | PASS | — | — |
| C worktree created from the integrated base (5c7809510) | PASS | — | — |
| C worktree carries both upstream artifacts via repository state | PASS | — | — |
| sr2-cla integration bead closed by the controller with a typed shipped record | PASS | — | — |
| integrated base sha | INFO | 5c7809510e212867e848ca807afd81435ef0f59f | — |
| C base sha | INFO | 5c7809510e212867e848ca807afd81435ef0f59f | — |
| C base SHA equals the controller-integrated base | PASS | — | — |
| integrated base descends from both validated commits | PASS | — | — |
| readiness projection exposed C automatically at 2026-08-10T14:24:28Z | PASS | — | — |
| C worker started autonomously | INFO | 2026-08-10T14:24:34Z (pids: 1592678) | — |
| live C worker posture captured | INFO | PASS | — |
| C closed | PASS | — | — |
| C wall clock | INFO | 65s | — |
| normal controller demand claimed and started C with no operator command (2026-08-10T14:24:34Z) | PASS | — | — |
| zero post-release directives naming C (release 2026-08-10T14:24:26Z) | PASS | — | — |
| sr2-691 final authoritative record is closed | PASS | — | — |
| sr2-691 typed work outcome is truthful (blocked) | PASS | — | — |
| sr2-691 produced INDEX.md inside its own worktree with exact content | PASS | — | — |
| sr2-691 artifact did not leak into the shared rig checkout | PASS | — | — |
| handoff: INDEX.md carries both upstream results | PASS | — | — |
| sr2-u2h required artifact present and inside its own worktree (ALPHA.md) | PASS | — | — |
| sr2-2t0 required artifact present and inside its own worktree (BETA.md) | PASS | — | — |
| sr2-691 required artifact present and inside its own worktree (INDEX.md) | PASS | — | — |
| sr2-u2h records the agent and session that executed it (sa-rig-20260810T142219Z/worker-a / sc2-q1b) | PASS | — | — |
| sr2-u2h heartbeat | INFO | not stamped on the raw-bead route | — |
| sr2-u2h canonical gc.work_dir mirrored from the controller stamp (/home/corsolvtech/corsolv-p2/sa-city-20260810T142219Z/.gc/worktrees/sa-rig-20260810T142219Z/worker-a) | PASS | — | — |
| sr2-2t0 records the agent and session that executed it (sa-rig-20260810T142219Z/worker-b / sc2-341) | PASS | — | — |
| sr2-2t0 heartbeat | INFO | not stamped on the raw-bead route | — |
| sr2-2t0 canonical gc.work_dir mirrored from the controller stamp (/home/corsolvtech/corsolv-p2/sa-city-20260810T142219Z/.gc/worktrees/sa-rig-20260810T142219Z/worker-b) | PASS | — | — |
| sr2-691 records the agent and session that executed it (sa-rig-20260810T142219Z/worker-c / sc2-kei) | PASS | — | — |
| sr2-691 heartbeat | INFO | not stamped on the raw-bead route | — |
| sr2-691 canonical gc.work_dir mirrored from the controller stamp (/home/corsolvtech/corsolv-p2/sa-city-20260810T142219Z/.gc/worktrees/sa-rig-20260810T142219Z/worker-c) | PASS | — | — |
| distinct worktree ownership per task (three distinct gc.work_dir values) | PASS | — | — |
| three distinct sessions executed the three tasks (no duplicate ownership) | PASS | — | — |
| every commit in the run base is controller-authored (Corsolv Autonomy POC,Gas City Controller,) | PASS | — | — |
| controller performed both local integrations; no worker published | PASS | — | — |
| controller integrated C into the run base (227825cc1) | PASS | — | — |
| live A/B worker posture proved while those workers were alive | PASS | — | — |
| live C worker posture proved while that worker was alive | PASS | — | — |
| managed workers drained (settled in 7s) | PASS | — | — |
| no worker session left in the active state | PASS | — | — |
| independent assurance passed | PASS | — | — |

### Failures and unreached controls

None. Every mandatory control passed.

## Evidence directory

`/home/corsolvtech/corsolv-p2/sa-evidence-20260810T142219Z`

- `controls.tsv` — the control ledger above, machine-readable
- `gc-commands.log` — every controller command with its timestamp (the D3 evidence)
- `parallelism.result` — max concurrent workers, with pids and observation time
- `live-process.result`, `live-process-c.result` — live permission posture, captured pre-drain
- `dep-tree.txt`, `ready-before.txt`, `ready-after-route.txt`, `ready-after-integration.txt` — the dependency gate
- `final-*.txt` / `final-*.json` — per-bead TERMINAL state, re-read after closure
- `independent.txt` — the independent assurance transcript
