# Corsolv P2.1 — Current-Main Reconciliation Evidence

INTEGRATION LANE: PASS (pending exact-SHA CI)

This is the integration proof on current `main`. The historical evidence lives
in `P2-FINAL-GATE-RESULT.md` and PR #1, which is deliberately **not** rebased —
its binary and acceptance run are pinned to the exact source they executed
against, and rebasing 362 commits would break that relationship.

## Foundation

| Item | Value |
| --- | --- |
| Branch | `corsolv/p2-gascity-main-reconcile` |
| Base | `7b00ef944d02ce67e279aa0a47bca49ef332d7a4` (exact `origin/main`) |
| Git SHA | `dceb6dbf4960ed5aea0d5802982b8c9911c277ce` |
| Binary SHA256 | `75d5a898e47669c5ece061aee96eacfab9c84bd651f0d236f595ead224fa782d` |
| `gc version` | `1.4.1` |
| Claude Code | 2.1.226 |

Nothing below is inherited from the historical run; every result was produced
against this tree.

## How the reconciliation resolved

Upstream moved while P2.1 was being proven, and the newer behaviour is kept.

Main had already fixed the invalid legacy permission values flagged during
P2.1 (GH#4602): `auto-edit` now maps to `acceptEdits` and `full-auto` to
`dontAsk`. Rather than redefine `full-auto` for anyone already selecting it,
the reconciliation adds a distinct **`bounded-auto`** choice — `full-auto`'s
`dontAsk` plus the enumerated pool-worker lifecycle grants — and makes that the
autonomous default in place of `unrestricted`.

| Upstream behaviour | Disposition |
| --- | --- |
| `auto-edit` → `acceptEdits` | preserved unchanged |
| `full-auto` → `dontAsk` | preserved unchanged |
| `plan`, `unrestricted` | preserved unchanged |
| model choices incl. canonical ids | preserved unchanged |
| default `permission_mode = unrestricted` | **changed** to `bounded-auto` |
| — | **added** `bounded-auto` choice |

Files upstream had also touched, and how each was handled:

| File | Upstream commits | Resolution |
| --- | --- | --- |
| `internal/worker/builtin/profiles.go` | 4 | replayed onto the new spec; upstream's mappings kept |
| `internal/config/provider_test.go` | 2 | expectations re-derived |
| `internal/config/{resolve,options}_test.go` | 1 each | expectations re-derived |
| `cmd/gc/{cmd_session,template_resolve_phase2,worker_handle}_test.go` | 1 each | expectations re-derived |
| `corsolv/`, `engdocs/corsolv/` | 0 | carried over verbatim |

## Final policy on this tree

```
claude --permission-mode dontAsk \
  '--allowedTools=Read,Write,Edit,Glob,Grep,Bash(gc hook --claim:*),Bash(gc bd show:*),Bash(gc bd mol current:*),Bash(gc bd mol progress:*),Bash(gc bd heartbeat:*),Bash(gc bd update:*),Bash(gc bd close:*),Bash(gc convoy status:*),Bash(gc runtime drain-ack:*)' \
  --effort max --settings <path>
```

## Revalidation (all re-run on this source)

| Check | Result |
| --- | --- |
| 20-test startup-contract gate | **20/20 PASS**, exit 0 |
| Live allow/deny matrix | **18/18 PASS** |
| Mutation matrix | **18/18 as required** |
| `go build ./...` | clean |
| `go vet` (config, builtin, cmd/gc, api) | clean |
| `go vet -tags acceptance_c ./test/acceptance/...` | clean |
| `internal/config`, `internal/worker/...`, `internal/api` | pass |
| `cmd/gc` | all tests pass; see leak-guard note |

**Leak-guard note.** `cmd/gc` fails its `TestMain` dolt leak guard on this
machine. No individual test fails. The identical failure reproduces on pristine
`origin/main` in a clean `git worktree`, so it is pre-existing and unrelated to
this change:

```
cmd/gc test dolt leak guard: leaked 1 dolt sql-server process(es) under
  .../TestDoltLeakGuardedTestingMFinalSnapshotRunsBeforeRegistryReap.../gct12345-current
```

## Fresh acceptance on this source

City `city-20260809T210923Z`, rig `rig-20260809T210923Z`, work item **`r2-b9m`**
(fresh — not `r2-gsl`, `r2-cyh`, or `r2-40f`).

### Supervisor runs the fingerprinted build

```
expected: 75d5a898e47669c5ece061aee96eacfab9c84bd651f0d236f595ead224fa782d
actual:   75d5a898e47669c5ece061aee96eacfab9c84bd651f0d236f595ead224fa782d
SUPERVISOR BINARY: PASS
```

Fingerprinted from `/proc/<pid>/exe` — the inode the process is running, not
PATH and not a version string.

### Live process security proof (both live claude processes)

```
permission mode is dontAsk                          PASS
Read,Write,Edit,Glob,Grep present                   PASS
all mandatory lifecycle grants present              PASS
no Bash grant outside the approved lifecycle set    PASS
no --dangerously-skip-permissions                   PASS
```

Global `Bash` absent, `Bash(gc:*)` absent, git grants absent, bypass absent.

### Worker lifecycle, from its own transcript

```
gc hook --claim --drain-ack --json     claimed its own work
gc bd show r2-64v                      read the assigned step
gc convoy status r2-ggx --json         derived the work bead
gc bd show r2-b9m                      read the work item
gc bd heartbeat r2-b9m                 heartbeat
Write CORSOLV_GASCITY_SMOKE.txt        created the artifact
git status/add/commit                  ATTEMPTED → DENIED by policy
gc mail send mayor                     ATTEMPTED → DENIED by policy
gc bd update r2-b9m ...                recorded verification + outcome
gc bd update r2-64v --status=closed    closed the step
gc runtime drain-ack                   drained; session released
```

Bead `r2-b9m`: **CLOSED**, `gc.outcome: pass`, `gc.work_outcome: shipped`,
`gc.last_heartbeat_at` stamped. Zero human continuation.

The denied `git` and `gc mail send` attempts are the policy actively enforcing,
not merely unexercised.

### Independent assurance — PASS (read-only, pre-commit)

Artifact exact (36 bytes, one line, `od` byte-comparison); worker-owned diff is
exactly the artifact; transcript shows exactly one file write; bead closed with
heartbeat; worker session drained; no live worker process; no policy
regression; worker did not commit.

### Controller publication

Rig commit `66018f8aac496e62fcb45c6ebc315d02d71e938b` — exactly one file staged.

## Assurance-script defects found and fixed during this run

1. `.claude/` was counted as worker-owned. It holds
   `.claude/skills/.gc-skill-ownership.json`, written by Gas City at rig setup
   (22:09:48) — 37s before the artifact (22:10:25). Now classified as
   infrastructure, with attribution proved by the worker transcript rather than
   assumed.
2. The drain check matched a worker session by template while ignoring the
   state column, so a session correctly sitting in `draining` was reported as
   "still active". It now fails only on `active`, and corroborates from the
   process table that no worker process survives.
