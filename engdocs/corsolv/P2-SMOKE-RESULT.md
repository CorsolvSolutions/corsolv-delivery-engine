# Corsolv Gas City Phase 2.1 Smoke Result

OVERALL: PASS

## Purpose

Prove that the Corsolv-controlled Gas City build can launch, supervise and
complete a real Claude-managed coding task without a PowerShell scheduler
performing the orchestration.

## Foundation

- Gas City version: 1.4.1
- Corsolv source SHA: dceb6dbf4960ed5aea0d5802982b8c9911c277ce
- Claude version: 2.1.226 (Claude Code)
- Store provider: file
- GC_HOME: /home/corsolvtech/.gc-corsolv-p2
- City: /home/corsolvtech/corsolv-p2/city-20260809T210923Z
- Rig path: /home/corsolvtech/corsolv-p2/rig-20260809T210923Z
- Rig name: rig-20260809T210923Z
- Work ID: r2-b9m

## Coding target

- Initial Git SHA: d503dac91653f7dd02b938bade5c20712de5e10a
- Git SHA after worker: 93884a38d1cae246dcdd58254200ce50e1c29010
- Required artifact: /home/corsolvtech/corsolv-p2/rig-20260809T210923Z/CORSOLV_GASCITY_SMOKE.txt
- Required marker: CORSOLV_GASCITY_MANAGED_CLAUDE_PASS

## Result

Gas City work state: CLOSED

Expected file exists: YES

Expected marker verified independently: YES

No PowerShell process launched Claude directly for this coding task.

The work was dispatched using Gas City:

gc sling rig-20260809T210923Z/claude "<task>"

## Git working-tree evidence

```
 M .beads/config.yaml
 M .beads/interactions.jsonl
 M .beads/metadata.json
 M .gitignore
?? .beads/identity.toml
?? .claude/
?? .gc/
?? CORSOLV_GASCITY_SMOKE.txt
```

## Gas City work evidence

```
2026/08/09 22:12:46 WARN native_store_unavailable gate=version_compat reason="bd version differs from linked beads library version" scope=/home/corsolvtech/corsolv-p2/rig-20260809T210923Z
✓ r2-b9m · Create the file CORSOLV_GASCITY_SMOKE.txt in the repository root. The file must contain exactly this single line: CORSOLV_GASCITY_MANAGED_CLAUDE_PASS. Do not merely describe the change. Make the filesystem change, verify the exact file contents, and then mark the assigned Gas City work complete.   [● P2 · CLOSED]
Owner: Corsolv Autonomy POC · Type: task
Created: 2026-08-09 · Updated: 2026-08-09

DESCRIPTION
  (none)

NOTES

  Done: created CORSOLV_GASCITY_SMOKE.txt at repo root containing exactly one 
  line, CORSOLV_GASCITY_MANAGED_CLAUDE_PASS. Verification: od -c shows 36     
  bytes ending in a single newline and no other content; wc -l reports 1 line;
  git status --short shows the file as the only task-related change. Commit:  
  NONE - no gc.work_commit SHA recorded because this session runs in Claude   
  Code don't-ask permission mode and every git write is denied (git add and   
  git commit both refused; read-only git status is allowed). The artifact is  
  present and verified in the working tree but is UNCOMMITTED and still       
  untracked. Escalation by mail also failed: gc mail send is denied by the    
  same policy, so this note is the only channel. Follow-up needed: allow git  
  add and git commit for pool workers in this rig, or have an operator commit 
  the file. Unrelated dirty paths left alone: .beads/ churn, .gitignore,      
  .claude/, .gc/.                                                             



METADATA
  gc.execution_routed_to: rig-20260809T210923Z/claude
  gc.last_heartbeat_at: 2026-08-09T21:10:25Z
  gc.outcome: pass
  gc.work_branch: main
  gc.work_outcome: shipped
  gc.work_verification: od -c CORSOLV_GASCITY_SMOKE.txt; wc -l CORSOLV_GASCITY_SMOKE.txt; git status --short

BLOCKS
  ← ✓ r2-ggx: input convoy for r2-b9m ● P2
```

## Gas City status

```
city-20260809T210923Z  /home/corsolvtech/corsolv-p2/city-20260809T210923Z
  Controller: supervisor-managed (PID 463288)
  API:        http://127.0.0.1:8372
  Authority: supervisor process PID 463288
  Suspended:  no

Agents:
  bd.dog                  scaled (min=0, max=2)
    bd.dog-1              running
    bd.dog-2              stopped
  core.control-dispatcher  stopped
  rig-20260809T210923Z/core.control-dispatcher  running
  claude                  scaled (min=0, max=unlimited)
    claude-c2-jmm         running

3/5 agents running

Named sessions:
  mayor                   awake (always)

Rigs:
  rig-20260809T210923Z    /home/corsolvtech/corsolv-p2/rig-20260809T210923Z

Store health:
  Path:        /home/corsolvtech/corsolv-p2/city-20260809T210923Z/.beads/dolt
  Size:        8.9 MB
  Live rows:   45
  Ratio:       0.2 MB/row  (threshold 1.0 MB/row)

Sessions: 4 active, 0 suspended
```

## Rig status

```

Rigs in /home/corsolvtech/corsolv-p2/city-20260809T210923Z:

  city-20260809T210923Z (HQ):
    Prefix: c2
    Beads:  initialized

  rig-20260809T210923Z:
    Path:   /home/corsolvtech/corsolv-p2/rig-20260809T210923Z
    Prefix: r2
    Default branch: main
    Beads:  initialized
```

## Acceptance

- Gas City binary launched: PASS
- City created: PASS
- Rig registered: PASS
- Real Claude worker dispatched by Gas City: PASS
- Work reached CLOSED: PASS
- Required filesystem artifact exists: PASS
- Artifact contents independently verified: PASS
- No human continuation command during dispatched task: PASS

## Next gate

Reproduce the complete Corsolv W1/W2/W3 autonomous-delivery acceptance POC
under Gas City, including parallel work, dependency release, GitHub PR/CI,
independent assurance and merge governance.
