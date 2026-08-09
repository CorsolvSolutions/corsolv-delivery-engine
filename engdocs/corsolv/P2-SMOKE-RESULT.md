# Corsolv Gas City Phase 2.1 Smoke Result

OVERALL: PASS

## Purpose

Prove that the Corsolv-controlled Gas City build can launch, supervise and
complete a real Claude-managed coding task without a PowerShell scheduler
performing the orchestration.

## Foundation

- Gas City version: 1.4.1
- Corsolv source SHA: bb90712f81ce5271de56339327b4a5429d1f6e56
- Claude version: 2.1.226 (Claude Code)
- Store provider: file
- GC_HOME: /home/corsolvtech/.gc-corsolv-p2
- City: /home/corsolvtech/corsolv-p2/city-20260809T111917Z
- Rig path: /home/corsolvtech/corsolv-p2/rig-20260809T111917Z
- Rig name: rig-20260809T111917Z
- Work ID: r2-gsl

## Coding target

- Initial Git SHA: d61ead6c44d2de04ba5a793f5895a4d4b660597e
- Git SHA after worker: 2fcdc200f74443432b83565b15e3b8bc5fa891e0
- Required artifact: /home/corsolvtech/corsolv-p2/rig-20260809T111917Z/CORSOLV_GASCITY_SMOKE.txt
- Required marker: CORSOLV_GASCITY_MANAGED_CLAUDE_PASS

## Result

Gas City work state: CLOSED

Expected file exists: YES

Expected marker verified independently: YES

No PowerShell process launched Claude directly for this coding task.

The work was dispatched using Gas City:

gc sling rig-20260809T111917Z/claude "<task>"

## Git working-tree evidence

```
 M .beads/interactions.jsonl
 M .beads/metadata.json
 M .gitignore
?? .beads/identity.toml
?? .gc/
?? CORSOLV_GASCITY_SMOKE.txt
```

## Gas City work evidence

```
✓ r2-gsl · Create the file CORSOLV_GASCITY_SMOKE.txt in the repository root. The file must contain exactly this single line: CORSOLV_GASCITY_MANAGED_CLAUDE_PASS. Do not merely describe the change. Make the filesystem change, verify the exact file contents, and then mark the assigned Gas City work complete.   [● P2 · CLOSED]
Type: task
Created: 2026-08-09 · Updated: 2026-08-09

DESCRIPTION
  (none)

NOTES

  File CORSOLV_GASCITY_SMOKE.txt created in repo root and byte-verified with  
  od -c: exactly 'CORSOLV_GASCITY_MANAGED_CLAUDE_PASS' + trailing newline (36 
  bytes, single line). COMMIT BLOCKED: 'git add' and 'git commit' are denied  
  by the Claude Code don't-ask permission mode, so the file remains untracked 
  on disk. 'gc mail send' is also denied, so Witness/mayor could not be       
  mailed. Needs git write commands allowlisted for rig pool workers, then the 
  commit can be made. No commit SHA recorded.                                 



METADATA
  gc.last_heartbeat_at: 2026-08-09T11:20:14Z
  gc.outcome: pass
  gc.work_branch: main
  gc.work_outcome: blocked
  gc.work_verification: od -c CORSOLV_GASCITY_SMOKE.txt; git status --short

BLOCKS
  ← ○ r2-lrq: input convoy for r2-gsl ● P2
```

## Gas City status

```
city-20260809T111917Z  /home/corsolvtech/corsolv-p2/city-20260809T111917Z
  Controller: supervisor-managed (PID 2793127)
  API:        http://127.0.0.1:8372
  Authority: supervisor process PID 2793127
  Suspended:  no

Agents:
  bd.dog                  scaled (min=0, max=2)
    bd.dog-1              stopped
    bd.dog-2              stopped
  core.control-dispatcher stopped
  rig-20260809T111917Z/core.control-dispatcherrunning
  claude                  scaled (min=0, max=unlimited)
    claude-c2-7ga         running

2/5 agents running

Named sessions:
  mayor                   awake (always)

Rigs:
  rig-20260809T111917Z    /home/corsolvtech/corsolv-p2/rig-20260809T111917Z

Store health:
  Path:        /home/corsolvtech/corsolv-p2/city-20260809T111917Z/.beads/dolt
  Size:        6.6 MB
  Live rows:   37
  Ratio:       0.2 MB/row  (threshold 1.0 MB/row)

Sessions: 3 active, 0 suspended
```

## Rig status

```

Rigs in /home/corsolvtech/corsolv-p2/city-20260809T111917Z:

  city-20260809T111917Z (HQ):
    Prefix: c2
    Beads:  initialized

  rig-20260809T111917Z:
    Path:   /home/corsolvtech/corsolv-p2/rig-20260809T111917Z
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
