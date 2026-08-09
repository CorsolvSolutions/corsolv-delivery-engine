# Corsolv P2 Final Gate — Result

OVERALL: PASS (20/20, exit 0)

## Foundation

- Branch: `corsolv/p2-gascity-smoke`
- SHA: `db90818a83e9501b4daf8e70d06450f3306d1b48`
- Base SHA: `a7297c511d637a3609947386f3389d76ddb2f23b`
- Claude Code: 2.1.226
- Go: 1.26.5 (linux/amd64, WSL2 Ubuntu)
- Gate script: `corsolv/p2-smoke/final-gate.sh`

## Why the previous run stopped at 2/20

`TestPhase2StartupMaterialization/claude/tmux-cli/WC-START-001` asserted the
launch command began with the prefix:

```
claude --permission-mode dontAsk --effort max
```

The bounded permission mode had since gained a tool allowlist, which
materializes between those two flags:

```
claude --permission-mode dontAsk --allowedTools ... --effort max --settings ...
```

### Classification: stale assertion, plus one real defect found while verifying

The adjacency the assertion required is **not** a Claude CLI contract. Verified
against the installed CLI (2.1.226), not inferred:

| Fact | Evidence |
| --- | --- |
| `dontAsk` is a real permission mode | `--permission-mode <mode>` choices are `acceptEdits, auto, bypassPermissions, manual, dontAsk, plan` |
| `--allowedTools` accepts a tool list | `--allowedTools, --allowed-tools <tools...>` — "Comma or space-separated list of tool names to allow" |
| The generated command parses end to end | `--permission-mode dontAsk --allowedTools ... --effort max --settings <path>` reached settings validation, failing only on a deliberately nonexistent path |
| Flag order is otherwise irrelevant | `--allowedTools A B C --permission-mode bogus` errors on `--permission-mode`, proving the variadic stops at the next flag |

So the assertion was stale. **But** verifying it surfaced a genuine latent
defect, which is fixed rather than papered over.

## The real defect: a variadic flag that could eat the prompt

`--allowedTools <tools...>` is variadic. In the space-separated form it
consumes every following non-flag token. Claude's `prompt_mode` is `"arg"`, so
the startup prompt is appended as a bare positional. Empirically, against
claude 2.1.226:

```
claude ... --allowedTools Read Write Edit Glob Grep MYPROMPT
  -> "Input must be provided ..."   (MYPROMPT absorbed as a 6th tool name)

claude ... --allowedTools=Read,Write,Edit,Glob,Grep MYPROMPT
  -> prompt received
```

This was reachable: `--settings` is appended only when the settings file exists
(`ProviderSettingsSource`), and a city can configure the `effort` option away —
leaving the allowlist trailing.

**Fix:** emit the allowlist as one `=`-bound token
(`internal/worker/builtin/profiles.go`, `ClaudeDontAskAllowedToolsArg`). Same
five tools, same policy, no positional hazard, order now genuinely irrelevant.
The safety control was not weakened.

## Test changes

The prefix assertion was replaced with a **full token-for-token argument-vector
assertion**, not a substring check:

```go
wantCommandArgv: []string{
    "claude",
    "--permission-mode", "dontAsk",
    "--allowedTools=Read,Write,Edit,Glob,Grep",
    "--effort", "max",
    "--settings", phase2SettingsPathToken,
}
```

Exact-vector equality proves, in one assertion: the `claude` executable,
`--permission-mode dontAsk`, the exact approved tool set, the absence of any
global Bash grant, `--effort max`, a `--settings` path (suffix-checked
separately), and the absence of duplicate or conflicting permission arguments.

Five sibling expectations in `internal/config` had been left on the
pre-allowlist vector and were failing for the same reason; all now assert the
materialized vector.

Expectations spell the allowlist out **literally** rather than referencing the
production constant. An earlier revision referenced the constant and was proven
toothless: a mutation widening the grant to `Bash` still passed, because the
expectation moved with it.

### Mutation-verified

| Mutation | Result |
| --- | --- |
| Widen allowlist to include `Bash` | 3 tests fail, including gate check 2 |
| Revert allowlist to space-separated tokens | swallow-hazard test fails with a direct diagnostic |

## Gate result

```
TEST 01/20  resume command carries settings + defaults                 PASS
TEST 02/20  Phase 2 startup materialization (WC-START-001/002)         PASS
TEST 03/20  resolved session command: defaults + settings              PASS
TEST 04/20  resolved session command: overrides beat defaults          PASS
TEST 05/20  worker runtime uses provider launch command                PASS
TEST 06/20  pool resume preserves launch flags                         PASS
TEST 07/20  API resume preserves stored resolved command               PASS
TEST 08/20  API resume rebuilds bare pool command                      PASS
TEST 09/20  launch command: defaults + settings file                   PASS
TEST 10/20  launch command: initial_message is not a flag              PASS
TEST 11/20  launch command: explicit option overrides                  PASS
TEST 12/20  claude schema resolves default args                        PASS
TEST 13/20  claude has nil Args + option defaults                      PASS
TEST 14/20  builtin claude provider shape                              PASS
TEST 15/20  claude CommandString + default args                        PASS
TEST 16/20  agent-level provider resolution                            PASS
TEST 17/20  bounded permission mode, no bypass flag                    PASS
TEST 18/20  safe tool allowlist, Bash absent                           PASS
TEST 19/20  bounded mode survives into launch command                  PASS
TEST 20/20  allowlist cannot swallow positional prompt                 PASS

RESULT: 20/20 passed, 0 failed
```

Check 2 covers WC-START-001 and WC-START-002 across all seven profiles
(claude, codex, gemini, kimi, opencode, mimocode, antigravity). The six
non-claude providers were untouched and pass unchanged.

## Wider verification beyond the gate

- `go build ./...` — clean
- `go vet` on `cmd/gc`, `internal/api`, `internal/config`, `internal/worker/builtin` — clean
- `go vet -tags acceptance_c ./test/acceptance/...` — clean
- Full package runs: `internal/config`, `internal/worker/...`, `internal/api`, `cmd/gc` — all pass
- `golangci-lint` via pre-commit on all changed packages — 0 issues

Live fresh-spawn is not in this gate: those tests are behind `//go:build
acceptance_c` and need real inference. That path is proven by the P2.1
managed-Claude acceptance run (`corsolv/p2-smoke/run-smoke.sh`).

## P2.1 acceptance: BLOCKED (not by this change)

The gate is green, but the managed-Claude acceptance run does **not** pass. It
is blocked by the permission policy itself, and the block is a human decision
to resolve — not something to fix by weakening a control unilaterally.

### What happened

A fresh city was created, the rig registered, and work dispatched successfully:

```
Created r2-40f — "Create the file CORSOLV_GASCITY_SMOKE.txt ..."
Attached workflow r2-5dm (formula "mol-do-work")
```

The worker spawned and stayed up, but after 11 minutes the bead was still
`OPEN` and no artifact existed. The worker's own session says why:

> I have not drained. `gc runtime drain-ack` never ran ...
> To unblock me, either: 1. Switch permission mode ... or 2. Allowlist the gc
> binary specifically — I can walk you through adding `Bash(gc:*)` ...

Its launch command:

```
claude --permission-mode dontAsk --allowedTools=Read,Write,Edit,Glob,Grep --effort max --settings <path>
```

A Gas City worker has to shell out to `gc` (claim, close, drain-ack) and to
`git`. With no Bash grant it can read the repo and nothing else.

### Root cause, measured

`dontAsk` is **deny-by-default**, not "permit without prompting". Measured
against claude 2.1.226, one trivial shell command per case:

| Configuration | Bash |
| --- | --- |
| `--dangerously-skip-permissions` | RAN |
| `--permission-mode acceptEdits` | RAN |
| `--permission-mode auto` | RAN |
| `--permission-mode dontAsk`, no allowlist | **BLOCKED** |
| `dontAsk` + the 5-tool allowlist | **BLOCKED** |
| `dontAsk` + allowlist including `Bash` | RAN |
| `--dangerously-skip-permissions` + 5-tool allowlist | RAN |

Two consequences:

1. **Removing the allowlist would not fix this.** `dontAsk` on its own already
   blocks Bash. The blocking default is `permission_mode = "dontAsk"`, which
   replaced `unrestricted`; the allowlist is the *grant* mechanism, not the
   restriction.
2. **The encoding fix in this commit is not implicated.** It changed how the
   same five tools are spelled on the command line, not which tools are
   granted. The block reproduces identically with the space-separated form.

### Scoped Bash is viable

"No global Bash allow" is still achievable — scoped grants are enforced
correctly:

| Allowlist | Command | Result |
| --- | --- | --- |
| `...,Bash(touch *)` | `touch made.txt` | RAN |
| `...,Bash(git *)` | `touch made.txt` | BLOCKED (correctly) |

So the shape of a working policy is `dontAsk` plus an allowlist carrying
narrowly scoped Bash patterns for the commands workers actually issue (`gc`,
`git`), rather than either a blanket bypass or no Bash at all. Note that
patterns are matched against the whole command string: a scoped grant did not
cover an invocation containing a shell redirect, so the patterns need tuning
against the real command set.

### Decision needed

Three options, in the order I would recommend them:

1. **Scoped Bash in the allowlist** — keeps `dontAsk`, keeps "no global Bash",
   unblocks workers. Requires enumerating the command patterns Gas City issues.
2. **`--permission-mode auto` or `acceptEdits`** — workers run, no blanket
   bypass flag, but no tool allowlist enforcement either.
3. **Revert to `unrestricted`** — the pre-change behaviour; restores autonomy
   at the cost of the bounded-execution goal.

Option 1 preserves the stated safety posture; the other two trade it away. This
changes the security policy that was specified, so it is not mine to pick.

### Harness defects fixed along the way

`corsolv/p2-smoke/run-smoke.sh` could not have reported a pass even on a
healthy city:

- It exported `GC_BEADS=file`, which makes every `gc bd show` fail with "only
  supported for bd-backed beads providers". The wait loop could never observe
  `CLOSED`, so any run would burn to the 1200s deadline and exit 51.
- `gc rig add` returns before the rig's beads store is usable; slinging inside
  that window put the bead in the city scope and dispatch failed. Now it waits
  for the store.
- A failing `gc sling` died under `set -e` with its stderr discarded, which is
  what made the first failure unreadable. Now captured and reported.

## Note for follow-up (not part of this gate)

The builtin claude `PermissionModes` table and options schema still offer
`auto-edit` and `full-auto`. Neither is a valid `--permission-mode` value in
claude 2.1.226 (`acceptEdits, auto, bypassPermissions, manual, dontAsk, plan`).
They predate this change and are not on the autonomous default path, so they
were left alone — but selecting either would produce a command the CLI rejects.
