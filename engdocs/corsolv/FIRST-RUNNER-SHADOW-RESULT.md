# Corsolv Shadow Run — Engine Coordination Properties (PRE-FLIGHT)

OVERALL: FAIL (3 check(s))

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
| Source SHA | `cd1875cd3afd988c526dd1f0393f0eba13116af3` |
| Binary SHA256 | `29c0f641ba1099a7e19ec42196b89e39f5c7eb3f53894198de6333838f855e54` |
| City | `/home/corsolvtech/corsolv-p2/shadow-city-20260810T073926Z` |
| Rig | `/home/corsolvtech/corsolv-p2/shadow-rig-20260810T073926Z` |
| Work beads | A=`sr2-2gq` B=`sr2-tl4` C=`sr2-g6j` |

## Properties

| Property | Evidence |
| --- | --- |
| Parallel work | max 2 concurrent managed-claude workers ( 1437987 1442716) |
| Dependency release | C withheld from `bd ready` while A/B open; released after |
| Handoff | INDEX.md carries both upstream results — unobtainable without reading them |
| Ownership | every bead records `gc.execution_routed_to` and a heartbeat |
| Timings | A+B 115s, C 113s, recorded by the engine |
| Merge governance | no worker commit; controller published `290765bfc3a1dff79f1bc6fd2d9b6f0cba38cf5f` |

## Dependency gate

Before dispatch:

```
2026/08/10 08:41:01 WARN native_store_unavailable gate=version_compat reason="bd version differs from linked beads library version" scope=/home/corsolvtech/corsolv-p2/shadow-rig-20260810T073926Z

🌲 Dependency tree for sr2-g6j:

sr2-g6j: Read ALPHA.md and Read BETA.md in the repository root. Create INDEX.md containing exactly two lines: the single line from ALPHA.md, then the single line from BETA.md. Do not invent the contents; read both files. Make the filesystem change, verify the exact file contents, then mark the assigned Gas City work complete. You are not permitted to run git; the controller publishes. Record the outcome truthfully -- do not report work as shipped if you did not commit it. [P2] (open) [1m[BLOCKED][m
    ├── sr2-tl4: Create the file BETA.md in the repository root containing exactly this single line: BETA_OK. Make the filesystem change, verify the exact file contents, then mark the assigned Gas City work complete. You are not permitted to run git; the controller publishes. Record the outcome truthfully -- do not report work as shipped if you did not commit it. [P2] (open) [blocks]
    └── sr2-2gq: Create the file ALPHA.md in the repository root containing exactly this single line: ALPHA_OK. Make the filesystem change, verify the exact file contents, then mark the assigned Gas City work complete. You are not permitted to run git; the controller publishes. Record the outcome truthfully -- do not report work as shipped if you did not commit it. [P2] (open) [blocks]
```

Ready work before A and B closed (C absent):

```
○ sr2-2gq ● P2 Create the file ALPHA.md in the repository root containing exactly this single line: ALPHA_OK. Make the filesystem change, verify the exact file contents, then mark the assigned Gas City work complete. You are not permitted to run git; the controller publishes. Record the outcome truthfully -- do not report work as shipped if you did not commit it.
○ sr2-tl4 ● P2 Create the file BETA.md in the repository root containing exactly this single line: BETA_OK. Make the filesystem change, verify the exact file contents, then mark the assigned Gas City work complete. You are not permitted to run git; the controller publishes. Record the outcome truthfully -- do not report work as shipped if you did not commit it.

--------------------------------------------------------------------------------
Ready: 2 issues with no active blockers

Status: ○ open  ◐ in_progress  ● blocked  ✓ closed  ❄ deferred

💡 Tip: Install the beads plugin for automatic workflow context, or run 'bd setup claude' for CLI-only mode
```

Ready work after both closed (C present):

```
○ sr2-gae ● P2 input convoy for sr2-tl4
○ sr2-tpw ● P2 input convoy for sr2-2gq
○ sr2-g6j ● P2 Read ALPHA.md and Read BETA.md in the repository root. Create INDEX.md containing exactly two lines: the single line from ALPHA.md, then the single line from BETA.md. Do not invent the contents; read both files. Make the filesystem change, verify the exact file contents, then mark the assigned Gas City work complete. You are not permitted to run git; the controller publishes. Record the outcome truthfully -- do not report work as shipped if you did not commit it.

--------------------------------------------------------------------------------
Ready: 3 issues with no active blockers

Status: ○ open  ◐ in_progress  ● blocked  ✓ closed  ❄ deferred
```

## Sessions

```
ID       TEMPLATE                                             STATE   REASON          TARGET                                               TITLE                           WORKDIR                                                                        AGE  LAST ACTIVE  LAST NUDGE
sc2-8x3  shadow-rig-20260810T073926Z/claude                   active  session         shadow-rig-20260810T073926Z/claude-2                 shadow-rig-20260810T073926Z...  /home/corsolvtech/corsolv-p2/shadow-rig-20260810T073926Z                       5s   0s ago       -
sc2-pdx  shadow-rig-20260810T073926Z/claude                   active  session         shadow-rig-20260810T073926Z/claude-1                 shadow-rig-20260810T073926Z...  /home/corsolvtech/corsolv-p2/shadow-rig-20260810T073926Z                       15s  0s ago       -
sc2-8zv  shadow-rig-20260810T073926Z/claude                   active  session         shadow-rig-20260810T073926Z/claude-3                 shadow-rig-20260810T073926Z...  /home/corsolvtech/corsolv-p2/shadow-rig-20260810T073926Z                       1m   0s ago       -
sc2-9id  shadow-rig-20260810T073926Z/core.control-dispatcher  active  session         shadow-rig-20260810T073926Z/core.control-dispatcher  shadow-rig-20260810T073926Z...  /home/corsolvtech/corsolv-p2/shadow-rig-20260810T073926Z                       3m   3m ago       -
sc2-rjx  bd.dog                                               active  session,config  bd.dog-1                                             bd.dog-1                        /home/corsolvtech/corsolv-p2/shadow-city-20260810T073926Z/.gc/agents/bd.dog-1  4m   0s ago       -
sc2-h5a  mayor                                                active  session,config  mayor                                                mayor                           /home/corsolvtech/corsolv-p2/shadow-city-20260810T073926Z                      4m   1m ago       -
```

## Evidence directory

`/home/corsolvtech/corsolv-p2/shadow-evidence-20260810T073926Z`
