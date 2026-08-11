# Finding: cross-OS worktree durability

**Status:** FIXED — guarded by preflight and by regression tests.
**Found by:** the GUK BPM pilot and the D8 delivery, after both had completed.
**Severity:** operational. Nothing was lost, and that was timing rather than design.

## What happened

Both the pilot (`/home/corsolvtech/guk-bpm-pilot`) and the D8 delivery
(`/home/corsolvtech/guk-bpm-d8`) created their worktree at a **WSL-native path**
of a repository hosted on the **Windows D: drive**. Partway through the
programme both registrations vanished:

```
fatal: not a git repository:
  /mnt/d/Development/guk-bpm-platform/.git/worktrees/guk-bpm-d8
```

`git worktree list` no longer showed either. The working directories still
existed; git no longer knew about them.

## Root cause

Git links a worktree to its repository with **two absolute pointers**:

| File | Contents |
| --- | --- |
| `<worktree>/.git` | `gitdir: <path to .git/worktrees/<name>>` |
| `.git/worktrees/<name>/gitdir` | path back to `<worktree>/.git` |

Written by WSL git these are Linux paths. Windows git cannot resolve
`/home/corsolvtech/...`, so it regards the worktree as gone, and
**`git worktree prune` deletes its registration**. Another writer working
Windows-side ran exactly that.

The reverse fails identically. A Windows-created worktree records
`gitdir: D:/Development/...`; WSL git reads that as a *relative* path and
resolves it to nonsense:

```
fatal: not a git repository:
  /mnt/d/Development/worktrees/guk-bpm/verify-main/D:/Development/guk-bpm-platform/.git/worktrees/verify-main
```

Reproduced deliberately during this work. The two namespaces do not agree on
absolute paths, and git records absolute paths by default.

## Why branch safety is not enough

Both runs had already pushed, so no commit was lost. That is not a durability
property — it is luck about *when* the prune ran:

- A run pruned **before** pushing holds a worktree git no longer knows about;
  `git status`, `git commit` and `git push` all fail from inside it.
- **Crash recovery breaks.** A resumed run re-probes its declared worktree.
  After a prune that probe fails, so the run reports an unreadable worktree
  rather than resuming — the journal survives, the place to apply it does not.
- **The fence and the writer lock live under the admin directory.** Removing it
  removes the lock's home, so exclusivity evidence disappears with it.
- The failure is **silent and externally triggered**: nothing the run does
  causes it, and nothing in the run observes it until the next git command.

## The fix

Two parts, both required.

**1. Relative pointers.** `git worktree add --relative-paths` (git ≥ 2.48;
equivalently `worktree.useRelativePaths=true`) records:

```
gitdir: ../../../guk-bpm-platform/.git/worktrees/verify-main
```

No drive letter, no `/mnt` prefix — it resolves identically from both sides.
`CrossOSWorktreeArgs` in `internal/unattended/worktree.go` always passes it,
because a convention is something the next run can forget and this one was
forgotten twice.

**2. A path both namespaces can reach.** `D:\Development\worktrees\<project>\<task>`,
reached from WSL as `/mnt/d/Development/worktrees/<project>/<task>`. A
WSL-native `/home/...` path is invisible to Windows git whatever the pointers
say.

Proved live: one physical worktree at
`D:\Development\worktrees\guk-bpm\verify-main`, resolving to the same SHA and
the same toplevel from Windows git and WSL git.

## Guard

`CheckWorktreeCrossOSDurable` runs in every preflight as
`worktree.crossOsDurable`. It inspects the recorded pointers rather than trying
the other OS — the other OS is not available to ask, and the pointers are the
whole of what git consults when deciding a worktree is stale.

It cannot use `filepath.IsAbs`: that answers for the OS the binary was built
for, and the defect is a path written by the other one. A Linux binary asked
about `D:/repo/.git` would say "relative" and be wrong in the way that matters.
`IsAbsoluteAnyOS` recognises POSIX roots, drive letters and UNC prefixes alike.

## Acceptance

The regression drives **both gits**: WSL git at `/usr/bin/git` creates the
worktree, Windows git at `/mnt/c/Program Files/Git/cmd/git.exe` runs the prune.
It is integration-tagged and skips where either git or a shared location is
absent.

| Criterion | Test |
| --- | --- |
| **The defect reproduces: a Windows prune destroys the WSL-native worktree** | `TestTheDefectiveStrategyLosesItsWorktreeToAWindowsPrune` |
| **The repair holds: the same prune leaves the shared worktree registered** | `TestTheRepairedStrategySurvivesAWindowsPrune` |
| Both gits resolve the same worktree to the same HEAD | same test |
| Branch and HEAD unchanged across the prune | same test |
| **The writer lock survives the prune** | same test |
| A resumed run can still re-establish ownership | same test |
| Cleanup stays deterministic | same test, and `TestSanctionedRemovalStillWorksOnADurableWorktree` |
| Both namespaces recognised as absolute | `TestIsAbsoluteAnyOSSeesBothNamespaces` |
| The failing shape is reported, with the remedy | `TestAWorktreeWithAbsolutePointersIsReportedNotDurable` |
| Relative pointers are produced and pass | `TestAWorktreeWithRelativePointersIsDurable` |
| An ordinary clone is not flagged | `TestAMainWorktreeHasNothingToBreak` |
| The convention cannot be forgotten | `TestCrossOSWorktreeArgsAlwaysRequestRelativePaths` |

### The regression discriminates, and that was measured

An earlier version of this section credited a **same-OS** prune test as the
regression. It was not one. Measured directly:

```
ABSOLUTE pointers (the defective strategy), same-OS prune  -> SURVIVED
RELATIVE pointers (the repaired strategy),  same-OS prune  -> SURVIVED
```

The git that recorded an absolute path resolves it perfectly well, so a same-OS
prune never removes anything and the test passed against the defective strategy
too. It proved nothing about the fix, and the only thing still distinguishing
the two strategies was the pointer inspection in `CheckWorktreeCrossOSDurable` —
a useful preflight guard, but an assertion about a string, not evidence that the
failure mode is gone. That test is now named
`TestARelativeWorktreeIsNotDisturbedBySameOSHousekeeping` and claims only what it
earns.

The cross-OS regression was then verified by mutation — the check the acceptance
actually rests on. With `--relative-paths` removed from `CrossOSWorktreeArgs`:

```
--- FAIL: TestTheRepairedStrategySurvivesAWindowsPrune
    windows git cannot read the shared worktree: exit status 128
    (fatal: not a git repository: (NULL))
```

and with the fix restored it passes. Windows git's own view names the defect
before the prune even runs:

```
D:/Development/.../repo         24595ab [main]
/home/corsolvtech/...-wt        24595ab [defective] prunable   <-- about to be deleted
```

After that prune the worktree is unusable from WSL with the identical error the
incident produced — `fatal: not a git repository: .../worktrees/<name>`.
