package unattended

import (
	"testing"
)

func fencedRepo(t *testing.T) (dir string, fence *Fence) {
	t.Helper()
	dir = newRepo(t, testOrigin)
	state, err := ProbeRepo(dir)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	lockDir := WriterLockDir(state)
	lock, err := Acquire(lockDir, Owner{
		RunID: "run-fence", ProjectID: "p", Session: "s", Worktree: dir, Role: RoleWriter,
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	t.Cleanup(func() { lock.Release() }) //nolint:errcheck
	return dir, TakeFence(state, lockDir, lock.Owner())
}

func TestFenceIsIntactWhenNothingMoved(t *testing.T) {
	_, f := fencedRepo(t)
	if res := f.Check(); !res.Intact() {
		t.Fatalf("fence = %s, want intact (%v)", res.Status, res.Error())
	}
}

func TestFenceDetectsBranchChange(t *testing.T) {
	// The exact observed failure: something else checks out another branch in
	// the worktree while this run holds it.
	dir, f := fencedRepo(t)
	mustGit(t, dir, "checkout", "-q", "-b", "someone-elses-branch")

	res := f.Check()
	if res.Status != FenceBranchChanged {
		t.Fatalf("fence = %s, want branch-changed", res.Status)
	}
	if res.Expected != "main" || res.Observed != "someone-elses-branch" {
		t.Fatalf("violation must record both sides: expected=%q observed=%q", res.Expected, res.Observed)
	}
	if res.Error() == nil {
		t.Fatal("a violated fence must produce an error so callers fail closed")
	}
	if res.Evidence == "" {
		t.Fatal("a violation must carry external-change evidence")
	}
}

func TestFenceDetectsDetachOfTheOwnedBranch(t *testing.T) {
	dir, f := fencedRepo(t)
	mustGit(t, dir, "checkout", "-q", "--detach", mustGit(t, dir, "rev-parse", "HEAD"))

	if res := f.Check(); res.Status != FenceBranchChanged {
		t.Fatalf("fence = %s, want branch-changed", res.Status)
	}
}

func TestFenceDetectsUnauthorisedHeadMove(t *testing.T) {
	dir, f := fencedRepo(t)
	writeRepoFile(t, dir, "outside.txt", "someone else committed here\n")
	commitAll(t, dir, "chore: not this run")

	res := f.Check()
	if res.Status != FenceHeadMoved {
		t.Fatalf("fence = %s, want head-moved", res.Status)
	}
	if res.Expected == res.Observed {
		t.Fatal("violation must record differing SHAs")
	}
}

func TestFenceAcceptsAnAuthorisedAdvance(t *testing.T) {
	dir, f := fencedRepo(t)
	before := f.Head

	writeRepoFile(t, dir, "produced.txt", "this run produced this\n")
	after := commitAll(t, dir, "feat: work this run did")

	if res, err := f.RecordAuthorisedAdvance("controller published the task artifact"); err != nil {
		t.Fatalf("authorized advance rejected: %v (%s)", err, res.Status)
	}
	if f.Head != after {
		t.Fatalf("fence head = %s, want %s", f.Head, after)
	}
	if len(f.Advances) != 1 || f.Advances[0].From != before || f.Advances[0].To != after {
		t.Fatalf("advance not recorded: %+v", f.Advances)
	}
	if f.Advances[0].Reason == "" {
		t.Fatal("an advance must record why it was authorized")
	}
	if res := f.Check(); !res.Intact() {
		t.Fatalf("fence after authorized advance = %s, want intact", res.Status)
	}
}

func TestAuthorisedAdvanceCannotLaunderABranchChange(t *testing.T) {
	dir, f := fencedRepo(t)
	mustGit(t, dir, "checkout", "-q", "-b", "someone-elses-branch")
	writeRepoFile(t, dir, "elsewhere.txt", "x\n")
	commitAll(t, dir, "chore: elsewhere")

	res, err := f.RecordAuthorisedAdvance("pretending this was mine")
	if err == nil {
		t.Fatal("an advance on a branch the run does not own must be refused")
	}
	if res.Status != FenceBranchChanged {
		t.Fatalf("status = %s, want branch-changed", res.Status)
	}
	if f.Branch != "main" || len(f.Advances) != 0 {
		t.Fatalf("a refused advance must leave the fence untouched: branch=%q advances=%v", f.Branch, f.Advances)
	}
}

func TestAuthorisedAdvanceRequiresAReason(t *testing.T) {
	_, f := fencedRepo(t)
	if _, err := f.RecordAuthorisedAdvance(""); err == nil {
		t.Fatal("an unexplained advance must be refused")
	}
}

func TestFenceDetectsLostOwnership(t *testing.T) {
	_, f := fencedRepo(t)
	if err := ForceClearOwner(f.LockDir, "test: simulate a governed takeover", "test"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	res := f.Check()
	if res.Status != FenceOwnerLost {
		t.Fatalf("fence = %s, want owner-lost", res.Status)
	}
	if res.Error() == nil {
		t.Fatal("lost ownership must fail closed")
	}
}

func TestFenceReportsOwnershipLossBeforeBranchMovement(t *testing.T) {
	// When both are wrong, ownership is the true cause and must be the reported
	// one; a branch-changed report would send a human looking at the wrong
	// thing.
	dir, f := fencedRepo(t)
	mustGit(t, dir, "checkout", "-q", "-b", "someone-elses-branch")
	if err := ForceClearOwner(f.LockDir, "test", "test"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if res := f.Check(); res.Status != FenceOwnerLost {
		t.Fatalf("fence = %s, want owner-lost", res.Status)
	}
}

func TestFenceDetectsAnUnreadableWorktree(t *testing.T) {
	_, f := fencedRepo(t)
	f.Worktree += "-removed"
	if res := f.Check(); res.Status != FenceUnreadable {
		t.Fatalf("fence = %s, want unreadable", res.Status)
	}
}
