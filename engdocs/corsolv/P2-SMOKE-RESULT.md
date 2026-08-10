# Corsolv Gas City Phase 2.1 Smoke Result

OVERALL: PASS

## Purpose

Prove that the Corsolv-controlled Gas City build can launch, supervise and
complete a real Claude-managed coding task without a PowerShell scheduler
performing the orchestration.

## Foundation

- Gas City version: dev
- Corsolv source SHA: 9e3ca3657c06728dd79e2ad5f6d7ddf6ba164216
- Claude version: 2.1.226 (Claude Code)
- Store provider: file
- GC_HOME: /home/corsolvtech/.gc-corsolv-p2
- City: /home/corsolvtech/corsolv-p2/city-20260810T054550Z
- Rig path: /home/corsolvtech/corsolv-p2/rig-20260810T054550Z
- Rig name: rig-20260810T054550Z
- Work ID: r2-xm1

## Coding target

- Initial Git SHA: 36c22ed0c650115e0cca43e415e79378f05cda11
- Git SHA after worker: 6ba5fa0d3c38810d324b2a1f2097e50412867b98
- Required artifact: /home/corsolvtech/corsolv-p2/rig-20260810T054550Z/CORSOLV_GASCITY_SMOKE.txt
- Required marker: CORSOLV_GASCITY_MANAGED_CLAUDE_PASS

## Result

Gas City work state: CLOSED

Expected file exists: YES

Expected marker verified independently: YES

No PowerShell process launched Claude directly for this coding task.

The work was dispatched using Gas City:

gc sling rig-20260810T054550Z/claude "<task>"

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
2026/08/10 06:48:51 WARN native_store_unavailable gate=version_compat reason="bd version differs from linked beads library version" scope=/home/corsolvtech/corsolv-p2/rig-20260810T054550Z
✓ r2-xm1 · Create the file CORSOLV_GASCITY_SMOKE.txt in the repository root. The file must contain exactly this single line: CORSOLV_GASCITY_MANAGED_CLAUDE_PASS. Do not merely describe the change. Make the filesystem change, verify the exact file contents, and then mark the assigned Gas City work complete.   [● P2 · CLOSED]
Owner: Corsolv Autonomy POC · Type: task
Created: 2026-08-10 · Updated: 2026-08-10

DESCRIPTION
  (none)

NOTES

  Created CORSOLV_GASCITY_SMOKE.txt in repo root containing exactly           
  CORSOLV_GASCITY_MANAGED_CLAUDE_PASS plus trailing newline. Verified byte-   
  exact with od -c. NOT COMMITTED: this session runs in don't-ask permission  
  mode and git add/git commit were denied by the harness, as was gc mail send.
  File is present and uncommitted in the working tree on branch main; a       
  permitted actor must commit it. Remaining dirty paths are unrelated         
  bead/runtime churn (.beads/, .gc/, .claude/, .gitignore).                   



METADATA
  gc.execution_routed_to: rig-20260810T054550Z/claude
  gc.last_heartbeat_at: 2026-08-10T05:47:07Z
  gc.outcome: pass
  gc.work_branch: main
  gc.work_outcome: shipped
  gc.work_verification: od -c CORSOLV_GASCITY_SMOKE.txt; git status --short

BLOCKS
  ← ○ r2-5f6: input convoy for r2-xm1 ● P2
```

## Gas City status

```
city-20260810T054550Z  /home/corsolvtech/corsolv-p2/city-20260810T054550Z
  Controller: supervisor-managed (PID 1266515)
  API:        http://127.0.0.1:8372
  Authority: supervisor process PID 1266515
  Suspended:  no

Agents:
  bd.dog                  scaled (min=0, max=2)
    bd.dog-1              running
    bd.dog-2              stopped
  core.control-dispatcher  stopped
  rig-20260810T054550Z/core.control-dispatcher  running
  claude                  scaled (min=0, max=unlimited)
    claude-c2-1v5         running

3/5 agents running

Named sessions:
  mayor                   awake (always)

Rigs:
  rig-20260810T054550Z    /home/corsolvtech/corsolv-p2/rig-20260810T054550Z

Store health:
  Path:        /home/corsolvtech/corsolv-p2/city-20260810T054550Z/.beads/dolt
  Size:        8.5 MB
  Live rows:   43
  Ratio:       0.2 MB/row  (threshold 1.0 MB/row)

Sessions: 4 active, 0 suspended
```

## Rig status

```

Rigs in /home/corsolvtech/corsolv-p2/city-20260810T054550Z:

  city-20260810T054550Z (HQ):
    Prefix: c2
    Beads:  initialized

  rig-20260810T054550Z:
    Path:   /home/corsolvtech/corsolv-p2/rig-20260810T054550Z
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
