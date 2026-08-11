# Unattended run evidence — `delivery-engine-endurance-2026-08-11`

Written by the run itself, from its own durable journal. Every fact below
comes from a record the control layer wrote at the moment the thing
happened; nothing here is reconstructed after the fact or inferred.

| Item | Value |
| --- | --- |
| Run ID | `delivery-engine-endurance-2026-08-11` |
| Started | `2026-08-11T05:42:59.345887477Z` |
| Journal records at the time of writing | 66 |
| Worktree | `/mnt/d/Development/corsolv-delivery-engine` |
| Branch | `corsolv/p2-gascity-main-reconcile` |
| HEAD before this commit | `be05c295dbdcf6de41f09043bdd1f06aac2b8a8a` |
| State directory | `/home/corsolvtech/corsolv-unattended/delivery-engine` |

## Task outcomes

| Task | Attempt | Kind | Duration (ms) |
| --- | --- | --- | --- |
| `build-all` | 1 | task-succeeded | 36065 |
| `test-control-layer` | 1 | task-succeeded | 5265 |
| `vet-fork` | 1 | task-succeeded | 444 |
| `cross-compile-windows` | 1 | task-failed | 35902 |
| `cross-compile-windows` | 1 | task-retry-scheduled | — |
| `cross-compile-windows` | 2 | task-failed | 21917 |
| `cross-compile-windows` | 2 | task-retry-scheduled | — |
| `cross-compile-windows` | 3 | task-failed | 22230 |
| `cross-compile-darwin` | 1 | task-succeeded | 47888 |
| `regress-projector` | 1 | task-succeeded | 202 |
| `regress-config-beads` | 1 | task-succeeded | 24819 |
| `regress-orchestration` | 1 | task-succeeded | 5060 |
| `regress-session-worker` | 1 | task-succeeded | 17040 |
| `regress-events-convoy` | 1 | task-succeeded | 29729 |
| `regress-api` | 1 | task-succeeded | 73204 |
| `write-evidence` | 1 | task-succeeded | 143946 |
| `guk-readiness-probe` | 1 | task-succeeded | 14008 |
| `build-binary` | 1 | task-succeeded | 17793 |
| `race-control-layer` | 1 | task-succeeded | 12309 |
| `vet-tree` | 1 | task-succeeded | 22320 |
| `unit-baseline` | 2 | task-failed | 429129 |
| `cross-compile-fork` | 1 | task-succeeded | 5425 |
| `process-shards` | 1 | task-succeeded | 136438 |
| `integration-shards` | 1 | task-failed | 2068994 |
| `regress-projector` | 1 | task-succeeded | 1643 |

## Fence

Every mutating stage re-verified branch, HEAD and lock ownership before it ran.

```
{"seq":2,"at":"2026-08-11T05:42:59.348007606Z","kind":"fence-taken","runId":"delivery-engine-readiness-2026-08-11","detail":"corsolv/p2-gascity-main-reconcile@80967f440"}
{"seq":33,"at":"2026-08-11T05:48:24.051197597Z","kind":"fence-verified","runId":"delivery-engine-readiness-2026-08-11","taskId":"write-evidence","detail":"corsolv/p2-gascity-main-reconcile@80967f440"}
{"seq":36,"at":"2026-08-11T05:50:49.808196345Z","kind":"fence-advanced","runId":"delivery-engine-readiness-2026-08-11","taskId":"write-evidence","detail":"922a7cf77"}
{"seq":41,"at":"2026-08-11T05:58:21.30791503Z","kind":"fence-taken","runId":"delivery-engine-endurance-2026-08-11","detail":"corsolv/p2-gascity-main-reconcile@00a699d21"}
{"seq":52,"at":"2026-08-11T06:03:34.484615992Z","kind":"fence-taken","runId":"delivery-engine-endurance-2026-08-11","detail":"corsolv/p2-gascity-main-reconcile@be05c295d"}
{"seq":65,"at":"2026-08-11T06:47:38.418123902Z","kind":"fence-verified","runId":"delivery-engine-endurance-2026-08-11","taskId":"write-evidence","detail":"corsolv/p2-gascity-main-reconcile@be05c295d"}
```

## Live progress at the moment this was written

```json
{
  "runId": "delivery-engine-endurance-2026-08-11",
  "projectId": "corsolv-delivery-engine",
  "session": "gascity-unattended-readiness",
  "stage": "running",
  "currentTask": "write-evidence",
  "currentBand": "evidence",
  "startedAt": "2026-08-11T06:03:34.487225314Z",
  "updatedAt": "2026-08-11T06:47:36.538999201Z",
  "elapsed": "44m2s",
  "lastMilestone": "regress-projector succeeded",
  "activeBlocker": "guk-deploy-dry-run: requires a capability behind a human boundary: credential.guk-deployment (obtain a GUK BPM deployment credential before the pilot's deploy stage; nothing in this run needs it)",
  "usingFallback": false,
  "nextAction": "write-evidence (evidence)",
  "writerOwner": "delivery-engine-endurance-2026-08-11",
  "writerPid": 1002935,
  "worktree": "/mnt/d/Development/corsolv-delivery-engine",
  "branch": "corsolv/p2-gascity-main-reconcile",
  "head": "be05c295dbdcf6de41f09043bdd1f06aac2b8a8a",
  "tasks": {
    "failed": 2,
    "held": 1,
    "pending": 2,
    "succeeded": 6
  },
  "attempts": 5,
  "boundaries": [
    "guk-deploy-dry-run: requires a capability behind a human boundary: credential.guk-deployment (obtain a GUK BPM deployment credential before the pilot's deploy stage; nothing in this run needs it)"
  ]
}
```

The complete journal is at `/home/corsolvtech/corsolv-unattended/delivery-engine/run-journal.jsonl` on the execution host. It is
append-only and synced per record, so it is the authority for what
happened, and this document is a projection of it.
