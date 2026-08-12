# Defect: the test harness deletes tmux socket roots without stopping the servers

**Status:** root cause FIXED and guarded by regression; historical debris NOT
yet reclaimed (a separate, human-gated action).
**Found by:** the process census taken while validating PR #5.
**Severity:** operational, cumulative, and silent. Nothing fails; the machine
just fills up.

## Identifier

This fork has **no beads workspace**. `bd where` reports no active workspace,
and the only `.beads` directory present (`cmd/gc/.beads`) is empty, unconfigured
and gitignored — test scaffolding, not a tracker. A `ga-` identifier could not
be minted without inventing one, so this git-tracked record is the durable
defect. Give it a real bead ID if a workspace is ever initialised here.

## What was measured

A census of this development host found:

| Resource | Count | Age |
| --- | --- | --- |
| orphaned tmux servers | 135 | up to 27h |
| — of those, unreachable (deleted `TMUX_TMPDIR`) | 134 | |
| `gc convoy control --serve` dispatchers under `/var/tmp/gc-integration-*` | ~129 | 13–14h |
| `dolt sql-server` | 1 | 25h+ |

Two full-suite runs moved the tmux count 132 → 135, so it grows roughly one
server per `cmd/gc` run and never shrinks.

**PR #5 did not cause this.** Its isolated before/after delta was zero on every
counter. That is a separate claim from "the test estate is leak-free", which is
false and is what this record is about.

## Root cause

`tmux` keeps its socket at `$TMUX_TMPDIR/tmux-<uid>/<name>`. The harness creates
a per-run socket parent — `/tmp/gct-<pid>-<random>/tmux` — and at the end of the
run removes that directory.

**Removing the directory does not stop the server.** The process keeps running
with its `TMUX_TMPDIR` gone. It is then:

- invisible to `tmux list-sessions`, because the socket it advertises is deleted;
- unreachable by any later sweep, because every sweep works by finding
  directories, and the directory is what was just removed;
- immortal for practical purposes — nothing short of a targeted kill or a reboot
  will reclaim it.

So the cleanup that was supposed to tidy up is precisely what makes the leak
permanent. It converts a findable orphan into an unfindable one.

Confirmed directly: every orphan's recorded `TMUX_TMPDIR` is a
`/tmp/gct-<pid>-*/tmux` path — the harness's own socket parent.

Three call sites deleted before stopping:

| Site | Path |
| --- | --- |
| `cmd/gc` normal exit | `cleanupTestingM.Run()` → `os.RemoveAll` |
| `cmd/gc` setup panic | `TestMain`'s deferred `os.RemoveAll` |
| `test/integration` exit, skip and panic paths | `os.RemoveAll(tmuxSocketParent)` |

`SweepOrphanPIDPrefixedDirs` had the same shape: it removed a dead sibling's
socket parent without stopping the servers inside, turning a reclaimable orphan
into a permanent one.

The signal path was a fourth gap. `go test -timeout` raises SIGQUIT, and the
sweeper it installs called only `KillAllTestSessions`, which matches `gctest-*`
socket names — not the per-run socket root where these sessions actually live.

## The fix

`tmuxtest.ShutdownSocketRoot(root, diagnostics)` stops every tmux server whose
socket lives under `root`; `CleanupSocketRoot` does that and then removes the
directory. Kill first, then delete. Every site above now uses it, including the
sweep and the signal handler.

Scope is the whole safety argument: candidates are found **by path, inside a
root the caller owns**. There is no matching on process name, socket name, or
user, so it cannot reach an operator's tmux server. `pkill tmux` is exactly what
this must never become — this machine runs both test debris and real sessions.

It reuses the existing `killTestSocketPath` call site, so the all-source
resource census is unchanged. No pinned baseline was raised.

## Regression

`test/tmuxtest/socket_root_leak_test.go`. These spawn real tmux servers and
assert on real process liveness, because the defect is that the filesystem
looked clean while the process table did not.

| Criterion | Test |
| --- | --- |
| Deleting the root alone leaks the server (pins the defect) | `TestRemovingASocketRootAloneLeaksTheServer` |
| Cleanup returns the process census to baseline | `TestCleanupSocketRootLeavesNoServerBehind` |
| Two consecutive cycles, no cumulative growth | `TestCleanupIsRepeatableWithNoCumulativeGrowth` |
| A server outside the root is never touched | `TestCleanupNeverTouchesAServerOutsideItsRoot` |
| The orphan sweep stops before it removes | `TestSweepStopsServersBeforeRemovingAnOrphanedDir` |

**Verified by mutation**, which is what makes them regressions rather than
decoration. With cleanup reverted to delete-only, four of the five fail and name
the surviving PID:

```
--- FAIL: TestCleanupSocketRootLeavesNoServerBehind
    tmux server 2424578 survived CleanupSocketRoot: the harness leaked it
--- FAIL: TestSweepStopsServersBeforeRemovingAnOrphanedDir
    sweep removed the directory but left server 2427377 running: a permanent orphan
```

## Historical debris

The fix prevents new leaks. It does not reclaim the 135 servers already running,
and deliberately so: they belong to processes this repository no longer tracks,
and terminating them is destructive.

`corsolv/reclaim-orphaned-tmux.sh` inventories them. It is **dry-run by default**
and only acts under `--reclaim`. A server qualifies only if all of the following
are proven: it is a tmux server; its recorded `TMUX_TMPDIR` is non-empty and no
longer exists; it cannot list its own sessions; its socket is not `default`; it
is not the caller's own `$TMUX`; and it is older than the age guard. Anything
unproven is kept. Liveness and the deleted-directory condition are re-proven
immediately before signalling, because PIDs are reused.

Current inventory: 136 examined, **135 provably orphaned, 1 preserved** (the
`default` socket).

## Not covered here

The ~129 `gc convoy control --serve` dispatchers and the stale `dolt sql-server`
are a **different mechanism** — they are children of `test/integration` runs
under `/var/tmp/gc-integration-*`, not tmux servers, and their own cleanup path
is `cleanupIntegrationDoltSQLServersUnderRoot` plus the supervisor stop. This
record does not claim to have fixed them. `cmd/gc/path_helpers_test.go` already
notes that "test cleanup paths do not call `shutdownBeadsProvider`", which is
the documented shape of the dolt half. That deserves its own investigation with
the same standard of evidence applied here.
