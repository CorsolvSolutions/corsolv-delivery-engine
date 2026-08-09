# Corsolv P2 / P2.1 — Final Gate and Acceptance Evidence

GATE: PASS (20/20, exit 0)
P2.1 ACCEPTANCE (worker path): PASS
P2.1 OVERALL: **NOT COMPLETE** — blocked on GitHub CI, see "Remaining blockers"

## Foundation

| Item | Value |
| --- | --- |
| Branch | `corsolv/p2-gascity-smoke` |
| Git SHA (fingerprinted build) | `bb90712f81ce5271de56339327b4a5429d1f6e56` |
| Binary SHA256 | `0ab81b06b7cf83de9c1626169827cf82d0b89a04dcdb0885d6c051045aa5fc20` |
| `gc version` | `1.4.1` |
| Base SHA | `a7297c511d637a3609947386f3389d76ddb2f23b` |
| Claude Code | 2.1.226 |
| Go | 1.26.5 (linux/amd64, WSL2 Ubuntu) |

Scripts: `corsolv/p2-smoke/{final-gate,policy-matrix,policy-mutations,run-smoke,verify-live-process,verify-supervisor-binary,verify-independent,controller-publish}.sh`

## Final lifecycle permission policy

```
claude --permission-mode dontAsk \
  '--allowedTools=Read,Write,Edit,Glob,Grep,Bash(gc hook --claim:*),Bash(gc bd show:*),Bash(gc bd mol current:*),Bash(gc bd mol progress:*),Bash(gc bd heartbeat:*),Bash(gc bd update:*),Bash(gc bd close:*),Bash(gc convoy status:*),Bash(gc runtime drain-ack:*)' \
  --effort max --settings <path>
```

Derived from the checked-in contract, not guessed: `pool-worker.md` (claim →
show → molecule steps → close → drain-ack) and `mol-do-work.toml` (convoy
status → bd show → heartbeat → bd update --status=closed → drain-ack), checked
against the Cobra command tree.

**Excluded as optional, not lifecycle:** `gc mail inbox`, `gc mail send`
(escalation), `gc runtime request-restart` (context exhaustion).
**Excluded as controller:** `gc sling`, `gc rig`, `gc agent`, `gc supervisor`,
`gc status`, `gc session`, `gc init`, `gc stop`.

Two rules are scoped tighter than their family because the family is an
escalation:

- `gc hook run -- <gc args...>` re-executes the binary with arbitrary
  arguments, so `Bash(gc hook:*)` is `Bash(gc:*)` by another name → scoped to
  `gc hook --claim`.
- `gc runtime` also carries controller-side `drain` / `undrain` → scoped to
  `gc runtime drain-ack`.

The approval text said `drain-acc`; the real command is `drain-ack`, and a
pattern for `drain-acc` matches nothing. Corrected, same scope.

### Live allow/deny matrix — 18/18 (`policy-matrix.sh`)

Evaluated by the real Claude permission engine with shim `gc`/`git` on PATH.

| Allowed (9) | Denied (9) |
| --- | --- |
| `gc hook --claim --drain-ack --json` | `gc hook run -- sling` |
| `gc bd show` | `gc runtime drain` |
| `gc bd mol current` | `gc runtime undrain` |
| `gc bd mol progress` | `gc sling` |
| `gc bd heartbeat` | `gc rig add` |
| `gc bd update --status=closed` | `gc supervisor stop` |
| `gc bd close` | `gc mail send mayor` |
| `gc convoy status --json` | `git commit` |
| `gc runtime drain-ack` | `git push` |

### Mutation matrix — 18/18 (`policy-mutations.sh`)

Every mutation must turn the suite red; all did.

| Mutation | Result |
| --- | --- |
| baseline / restored | PASS |
| adding global `Bash` | FAIL as required |
| broadening to `Bash(gc:*)` | FAIL as required |
| adding `Bash(git:*)` | FAIL as required |
| broadening `gc hook` to the family | FAIL as required |
| broadening `gc runtime` to the family | FAIL as required |
| removing each of the 9 lifecycle rules (9 cases) | FAIL as required |
| reverting to the unsafe variadic encoding | FAIL as required |
| reinstating `--dangerously-skip-permissions` as default | FAIL as required |

Expectations are written literally, never referencing the production constant.
An earlier revision did reference it and was provably toothless: a mutation
widening the grant to `Bash` still passed.

## 20/20 gate

```
TEST 01 resume command carries settings + defaults              PASS
TEST 02 Phase 2 startup materialization (WC-START-001/002)      PASS
TEST 03 resolved session command: defaults + settings           PASS
TEST 04 resolved session command: overrides beat defaults       PASS
TEST 05 worker runtime uses provider launch command             PASS
TEST 06 pool resume preserves launch flags                      PASS
TEST 07 API resume preserves stored resolved command            PASS
TEST 08 API resume rebuilds bare pool command                   PASS
TEST 09 launch command: defaults + settings file                PASS
TEST 10 launch command: initial_message is not a flag           PASS
TEST 11 launch command: explicit option overrides               PASS
TEST 12 claude schema resolves default args                     PASS
TEST 13 claude has nil Args + option defaults                   PASS
TEST 14 builtin claude provider shape                           PASS
TEST 15 claude CommandString + default args                     PASS
TEST 16 agent-level provider resolution                         PASS
TEST 17 bounded permission mode, no bypass flag                 PASS
TEST 18 allowlist exact: lifecycle grants only, no global Bash  PASS
TEST 19 bounded mode survives into launch command               PASS
TEST 20 allowlist cannot swallow positional prompt              PASS

RESULT: 20/20 passed, 0 failed
```

Test 02 covers WC-START-001 and WC-START-002 across all seven profiles. The six
non-claude providers were untouched and pass unchanged.

## P2.1 acceptance

City `city-20260809T111917Z`, rig `rig-20260809T111917Z`, work item **`r2-gsl`**
(fresh — not `r2-cyh` or `r2-40f`).

### Supervisor runs the fingerprinted build

```
supervisor pid: 2793127
running image:  /home/corsolvtech/.local/bin/gc
expected:       0ab81b06b7cf83de9c1626169827cf82d0b89a04dcdb0885d6c051045aa5fc20
actual:         0ab81b06b7cf83de9c1626169827cf82d0b89a04dcdb0885d6c051045aa5fc20
SUPERVISOR BINARY: PASS
```

Fingerprinted via `/proc/<pid>/exe`, the inode the process is actually running
— not PATH and not a version string.

### Live process security proof (pid 2795414, from `/proc/<pid>/cmdline`)

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
gc hook --claim --drain-ack --json        claimed its own work
gc bd show r2-i2g                         read the assigned step
gc convoy status r2-lrq --json            derived the work bead
gc bd show r2-gsl                         read the work item
gc bd heartbeat r2-gsl                    heartbeat
Write CORSOLV_GASCITY_SMOKE.txt           created the artifact
git status --short / add / commit         ATTEMPTED → DENIED by policy
gc mail send mayor                        ATTEMPTED → DENIED by policy
gc bd update r2-gsl  ...                  recorded outcome
gc bd update r2-i2g --status=closed       closed the step
gc runtime drain-ack                      drained; session released
```

The denied `git` and `gc mail send` attempts are the policy actively enforcing,
not merely unused. Zero human continuation was required.

Bead `r2-gsl`: **CLOSED**, `gc.outcome: pass`, `gc.last_heartbeat_at` stamped.
Convoy `r2-lrq`: CLOSED. Worker session gone (drained); only `mayor` remains.

`gc.work_outcome: blocked` is **INFO**, not failure: the worker chose it because
it could not commit. Its note records the reason verbatim. The artifact is
correct and byte-verified; the controller performs publication.

### Independent assurance — PASS (read-only, pre-commit)

Artifact exists, content matches exactly, one line, 36 bytes; byte-for-byte
`od` comparison; worker-owned diff is exactly `CORSOLV_GASCITY_SMOKE.txt`;
transcript shows exactly one file write; bead closed with heartbeat; worker
drained; live policy has no regression; worker did not commit.

`.beads/`, `.gc/`, `.gitignore` are `gc rig add` infrastructure (written
12:19:37–42, with its own `bd init` commit) — the artifact was written 12:20:18.
Reported as INFO, attributed by the worker's transcript rather than assumed.

### Controller publication

Rig commit `956a57dd0b30cf4bff4e4064eb1f12a80d54c93f` — exactly one file staged.

```
956a57d feat: add CORSOLV_GASCITY_SMOKE.txt from Gas City work r2-gsl
 CORSOLV_GASCITY_SMOKE.txt | 1 +
committed content: CORSOLV_GASCITY_MANAGED_CLAUDE_PASS
```

The worker was never granted git to achieve this.

## GitHub publication

| Item | Value |
| --- | --- |
| Branch pushed | `corsolv/p2-gascity-smoke` |
| Pushed SHA | `83414f92e56becf32dddd89e710bfab8d34b1f43` (remote == local) |
| Pre-push suite | PASS — "All fast jobs passed" |
| PR | https://github.com/CorsolvSolutions/corsolv-delivery-engine/pull/1 |
| PR head | `83414f92e56becf32dddd89e710bfab8d34b1f43` (exact match) |
| CI against that SHA | **NONE — cannot run** |

### Why the push looked "in flight" for over an hour

WSL git has **no credential helper** (`credential.helper` unset locally and
globally). Anonymous `git ls-remote` succeeds, so the remote appeared
reachable, but `push` needs auth: `git-remote-https` slept indefinitely waiting
for a prompt a non-interactive shell can never answer. Two pushes sat in
`State: S (sleeping)`, emitting no error — which is exactly why it read as
progress. Both were killed; neither wrote anything to the remote.

Resolved without new credentials by passing the already-authorised `gh` token
into WSL via `WSLENV` and an in-memory credential helper, so it never reaches
argv, the reflog, or on-disk config. No `--no-verify`; the hook ran and passed.

## Remaining blockers

### 1. GitHub Actions has never been activated on this fork

```
repos/.../actions/permissions        -> {"enabled":true,"allowed_actions":"all"}
repos/.../actions/workflows          -> total_count: 0
repos/.../actions/runs               -> total_count: 0   (none, ever)
commits/83414f92e.../check-runs      -> total_count: 0
commits/83414f92e.../status          -> pending
```

The workflow files are present on both `main` and the branch (`ci.yml` and 20+
others), and `ci.yml` triggers on `pull_request` (`opened`, `reopened`,
`synchronize`, `ready_for_review`). A fresh `reopened` event was fired by
closing and reopening PR #1 — still 0 runs.

`enabled: true` on the permissions endpoint is not the same as workflows being
registered; `total_count: 0` proves none are. GitHub disables Actions on forks
until a repo admin clicks through the one-time confirmation in the Actions tab
("I understand my workflows, go ahead and enable them"). There is no REST API
for that step, so it cannot be done from here.

**Action required:** a repo admin enables Actions at
https://github.com/CorsolvSolutions/corsolv-delivery-engine/actions, then
re-fires the PR event. Nothing in the branch needs to change.

### 2. PR #1 conflicts with `main`

`mergeable: CONFLICTING`, `mergeStateStatus: DIRTY`. The branch is based on
`a7297c511` (upstream v1.4.0), which **is** an ancestor of `origin/main`, but
`origin/main` is **362 commits ahead** while this branch carries 6.

Rebasing onto `origin/main` is not a mechanical step to take unasked: upstream
almost certainly touched `internal/worker/builtin/profiles.go` and the config
tests over those 362 commits, and any rebase invalidates the fingerprinted
binary, the 20/20 gate run, and the acceptance evidence recorded here — all of
which would need re-running against the rebased tree.

## Defects found and fixed

1. **Prompt-swallowing argv encoding.** `--allowedTools <tools...>` is variadic;
   in space-separated form it consumes the positional prompt that
   `prompt_mode = "arg"` appends. Reachable because `--settings` is only added
   when the settings file exists and `effort` can be configured away. Fixed by
   binding the tools with `=` as one token.
2. **`dontAsk` is deny-by-default.** Not "permit without prompting". A worker
   with edit-only tools could not run `gc` at all, so work was never claimed.
3. **`GC_BEADS=file` broke `gc bd show`** in the harness, so the wait loop could
   never observe CLOSED and every run burned to the 1200s deadline.
4. **`gc rig add` races its own beads store.** Slinging in that window put the
   bead in the city scope and dispatch failed. Now waits.
5. **Failing `gc sling` discarded stderr** under `set -e`, making the first
   failure unreadable. Now captured with its exit code.
6. **A stale supervisor silently proves the previous build.** The supervisor,
   not the script, materializes launch commands; installing a new binary does
   not restart it. An acceptance run against a freshly built `gc` observed the
   old allowlist. `run-smoke.sh` now retires it and refuses to continue if one
   survives.
7. **Two escalation vectors in the proposed policy** — `gc hook run` and the
   `gc runtime` family — found by reading the Cobra tree and closed by scoping.
8. **A tautological expectation** referencing the production constant, which
   let a `Bash`-widening mutation pass. Expectations are now literal.
9. **A false-negative probe detector** keyed on "did the shim run at all",
   which reported every command as allowed because Claude Code makes its own
   incidental `git` calls. Now keyed on a per-probe token.

## Note for follow-up (not part of this gate)

The builtin claude options schema still offers `auto-edit` and `full-auto`.
Neither is a valid `--permission-mode` value in claude 2.1.226 (`acceptEdits,
auto, bypassPermissions, manual, dontAsk, plan`). They predate this work and are
off the autonomous default path, but selecting either produces a command the CLI
rejects.
