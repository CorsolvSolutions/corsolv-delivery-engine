# Corsolv Shadow Run — Engine Coordination Properties (PRE-FLIGHT)

OVERALL: FAIL (2 check(s))

**This is not first-runner acceptance.** It is a pre-flight coordination proof
on a disposable local target, and it is deliberately narrower than the standard
it feeds.

## Scope against the authoritative standard

The acceptance standard is the 14 numbered criteria in
`D:\Development\corsolv-autonomy-poc\POC-BRIEF.md`, recorded as passing in
that repo's `artifacts/POC-RESULT.md` against real PRs (#1/#2/#3), exact-SHA
CI runs and a final main SHA. W1/W2/W3 are defined there (add, multiply,
calculator — W3 dependent on both being **merged**).

| Criteria | Status here |
| --- | --- |
| 1, 3, 6, 14 | proved by this run |
| 2, 10 | proved in shape only — distinct sessions not worktrees; release on CLOSE, not MERGED |
| 4, 5, 7, 8, 9, 11, 12, 13 | **NOT REACHED** — require the promoted run against the real repository |

Per the POC brief, NOT REACHED is never reported as PASS.

P2.1 proved one bead end-to-end, which cannot show whether the engine holds
project state. This uses a three-bead graph where C depends on A and B.

## Foundation

| Item | Value |
| --- | --- |
| Source SHA | `7c9940360219e2d504e3fa38bdae3df59606ccae` |
| Binary SHA256 | `29c0f641ba1099a7e19ec42196b89e39f5c7eb3f53894198de6333838f855e54` |
| City | `/home/corsolvtech/corsolv-p2/shadow-city-20260810T085118Z` |
| Rig | `/home/corsolvtech/corsolv-p2/shadow-rig-20260810T085118Z` |
| Work beads | A=`sr2-5cd` B=`sr2-zc8` C=`sr2-sxh` |

## Properties

| Property | Evidence |
| --- | --- |
| Parallel work | max 2 concurrent managed-claude workers ( 3585586 3594055) |
| Dependency release | C withheld from `bd ready` while A/B open; released after |
| Handoff | INDEX.md carries both upstream results — unobtainable without reading them |
| Ownership | every bead records `gc.execution_routed_to` and a heartbeat |
| Timings | A+B 106s, C 83s, recorded by the engine |
| Merge governance | no worker commit; controller published `6e3a5d9ca93967245bd07719f46d1848b25b7f0a` |

## Dependency gate

Before dispatch:

```
2026/08/10 09:54:02 WARN native_store_unavailable gate=version_compat reason="bd version differs from linked beads library version" scope=/home/corsolvtech/corsolv-p2/shadow-rig-20260810T085118Z

🌲 Dependency tree for sr2-sxh:

sr2-sxh: Read ALPHA.md and BETA.md in the repository root, then create INDEX.md containing exactly two lines: the line from ALPHA.md, then the line from BETA.md. Do not invent the contents; read both files. Make the change, verify the exact contents, then close the assigned bead. You cannot run git; the controller publishes. Close with gc.work_outcome=blocked plus a gc.work_blocked_reason; do NOT claim shipped. [P2] (open) [1m[BLOCKED][m
    ├── sr2-zc8: Create BETA.md in the repository root containing exactly this single line: BETA_OK. Make the change, verify the exact contents, then close the assigned bead. You cannot run git; the controller publishes. Close with gc.work_outcome=blocked plus a gc.work_blocked_reason; do NOT claim shipped. [P2] (open) [blocks]
    └── sr2-5cd: Create ALPHA.md in the repository root containing exactly this single line: ALPHA_OK. Make the change, verify the exact contents, then close the assigned bead. You cannot run git; the controller publishes. Close with gc.work_outcome=blocked plus a gc.work_blocked_reason; do NOT claim shipped. [P2] (open) [blocks]
```

Ready work before A and B closed (C absent):

```
○ sr2-zc8 ● P2 Create BETA.md in the repository root containing exactly this single line: BETA_OK. Make the change, verify the exact contents, then close the assigned bead. You cannot run git; the controller publishes. Close with gc.work_outcome=blocked plus a gc.work_blocked_reason; do NOT claim shipped.
○ sr2-5cd ● P2 Create ALPHA.md in the repository root containing exactly this single line: ALPHA_OK. Make the change, verify the exact contents, then close the assigned bead. You cannot run git; the controller publishes. Close with gc.work_outcome=blocked plus a gc.work_blocked_reason; do NOT claim shipped.

--------------------------------------------------------------------------------
Ready: 2 issues with no active blockers

Status: ○ open  ◐ in_progress  ● blocked  ✓ closed  ❄ deferred
```

Ready work after both closed (C present):

```
○ sr2-2z1 ● P2 input convoy for sr2-zc8
○ sr2-c8w ● P2 input convoy for sr2-5cd
○ sr2-sxh ● P2 Read ALPHA.md and BETA.md in the repository root, then create INDEX.md containing exactly two lines: the line from ALPHA.md, then the line from BETA.md. Do not invent the contents; read both files. Make the change, verify the exact contents, then close the assigned bead. You cannot run git; the controller publishes. Close with gc.work_outcome=blocked plus a gc.work_blocked_reason; do NOT claim shipped.

--------------------------------------------------------------------------------
Ready: 3 issues with no active blockers

Status: ○ open  ◐ in_progress  ● blocked  ✓ closed  ❄ deferred
```

## Sessions

```
ID       TEMPLATE                                             STATE   REASON          TARGET                                               TITLE                           WORKDIR                                                                        AGE  LAST ACTIVE  LAST NUDGE
sc2-as5  shadow-rig-20260810T085118Z/claude                   active  session         shadow-rig-20260810T085118Z/claude-1                 shadow-rig-20260810T085118Z...  /home/corsolvtech/corsolv-p2/shadow-rig-20260810T085118Z                       34s  0s ago       -
sc2-99g  shadow-rig-20260810T085118Z/claude                   active  session         shadow-rig-20260810T085118Z/claude-2                 shadow-rig-20260810T085118Z...  /home/corsolvtech/corsolv-p2/shadow-rig-20260810T085118Z                       34s  0s ago       -
sc2-bjj  shadow-rig-20260810T085118Z/claude                   active  session         shadow-rig-20260810T085118Z/claude-3                 shadow-rig-20260810T085118Z...  /home/corsolvtech/corsolv-p2/shadow-rig-20260810T085118Z                       1m   0s ago       -
sc2-tmp  shadow-rig-20260810T085118Z/core.control-dispatcher  active  session         shadow-rig-20260810T085118Z/core.control-dispatcher  shadow-rig-20260810T085118Z...  /home/corsolvtech/corsolv-p2/shadow-rig-20260810T085118Z                       3m   3m ago       -
sc2-rf7  bd.dog                                               active  session,config  bd.dog-1                                             bd.dog-1                        /home/corsolvtech/corsolv-p2/shadow-city-20260810T085118Z/.gc/agents/bd.dog-1  3m   1m ago       -
sc2-b61  mayor                                                active  session,config  mayor                                                mayor                           /home/corsolvtech/corsolv-p2/shadow-city-20260810T085118Z                      3m   1m ago       -
```

## Evidence directory

`/home/corsolvtech/corsolv-p2/shadow-evidence-20260810T085118Z`
