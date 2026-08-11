package unattended

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The failure that removed the GUK BPM pilot and D8 worktrees mid-program,
// made executable.
//
// The real event needed two operating systems, which a test cannot have. What
// it does not need is two operating systems: git decides a worktree is stale by
// resolving the pointer it recorded, so a pointer only one namespace can resolve
// is the whole mechanism. These tests exercise that mechanism directly, and the
// absolute/relative distinction they assert is exactly what the two gits
// disagreed about.

func TestIsAbsoluteAnyOSSeesBothNamespaces(t *testing.T) {
	// filepath.IsAbs answers for the OS this binary was built for, and the
	// entire defect is a path written by the other one.
	absolute := []string{
		"/home/corsolvtech/guk-bpm-d8",                         // what WSL wrote
		"/mnt/d/Development/guk-bpm-platform/.git/worktrees/x", // also WSL
		"D:/Development/guk-bpm-platform/.git/worktrees/x",     // what Windows wrote
		`D:\Development\guk-bpm-platform\.git`,
		"c:/repo/.git",
		`\\server\share\repo`,
	}
	for _, p := range absolute {
		if !IsAbsoluteAnyOS(p) {
			t.Errorf("IsAbsoluteAnyOS(%q) = false, want true", p)
		}
	}
	relative := []string{
		"../../../guk-bpm-platform/.git/worktrees/verify-main",
		"../../.git/worktrees/x",
		".git",
		"",
	}
	for _, p := range relative {
		if IsAbsoluteAnyOS(p) {
			t.Errorf("IsAbsoluteAnyOS(%q) = true, want false", p)
		}
	}
}

func TestAMainWorktreeHasNothingToBreak(t *testing.T) {
	// The first version of this check read .git as a file and failed with "is a
	// directory" for every main worktree, which took the whole preflight suite
	// down with it. A main worktree holds no cross-repository pointer, so there
	// is nothing here that a prune on the other side could invalidate.
	repo := newRepo(t, testOrigin)
	c := CheckWorktreeCrossOSDurable(repo)
	if c.Outcome != OutcomePass {
		t.Fatalf("main worktree = %s (%s), want pass", c.Outcome, c.Observed)
	}
	if !strings.Contains(c.Observed, "main worktree") {
		t.Fatalf("the check must say why it passed, got %q", c.Observed)
	}
}

func TestAWorktreeWithAbsolutePointersIsReportedNotDurable(t *testing.T) {
	// This is the pilot/D8 shape: git recorded an absolute path that only one
	// OS can resolve, and the other one pruned it.
	repo := newRepo(t, testOrigin)
	linked := filepath.Join(t.TempDir(), "linked")
	mustGit(t, repo, "worktree", "add", "-q", "-b", "linked-branch", linked)

	// Confirm the fixture really is the failing shape before asserting on it —
	// otherwise this test could pass because git changed its default.
	wp, err := ReadWorktreePointers(linked)
	if err != nil {
		t.Fatalf("ReadWorktreePointers: %v", err)
	}
	if !IsAbsoluteAnyOS(wp.DotGit) {
		t.Skipf("this git already writes relative worktree pointers (%q); the failing shape cannot be built", wp.DotGit)
	}

	c := CheckWorktreeCrossOSDurable(linked)
	if c.Outcome != OutcomeFail {
		t.Fatalf("absolute-pointer worktree = %s, want fail", c.Outcome)
	}
	if !strings.Contains(c.Remedy, "--relative-paths") {
		t.Fatalf("the remedy must name the fix, got %q", c.Remedy)
	}
	if !strings.Contains(c.Detail, "prune") {
		t.Fatalf("the detail must name the mechanism that deleted the registration, got %q", c.Detail)
	}
}

func TestAWorktreeWithRelativePointersIsDurable(t *testing.T) {
	repo := newRepo(t, testOrigin)
	// Sibling of the repo, so a relative pointer is expressible — which is also
	// why the real strategy puts worktrees under a shared parent directory.
	linked := filepath.Join(filepath.Dir(repo), "linked-relative")
	t.Cleanup(func() { os.RemoveAll(linked) }) //nolint:errcheck

	args := append(CrossOSWorktreeArgs(linked, "durable-branch", "HEAD"), "-q")
	if _, err := runGit(repo, args...); err != nil {
		t.Skipf("this git does not support --relative-paths: %v", err)
	}

	wp, err := ReadWorktreePointers(linked)
	if err != nil {
		t.Fatalf("ReadWorktreePointers: %v", err)
	}
	if IsAbsoluteAnyOS(wp.DotGit) {
		t.Fatalf(".git pointer is absolute despite --relative-paths: %q", wp.DotGit)
	}
	if wp.GitDirBack != "" && IsAbsoluteAnyOS(wp.GitDirBack) {
		t.Fatalf("back-pointer is absolute despite --relative-paths: %q", wp.GitDirBack)
	}

	if c := CheckWorktreeCrossOSDurable(linked); c.Outcome != OutcomePass {
		t.Fatalf("relative-pointer worktree = %s (%s), want pass", c.Outcome, c.Observed)
	}
}

func TestARelativeWorktreeSurvivesAPruneAndKeepsItsIdentity(t *testing.T) {
	// The regression proper. `git worktree prune` is precisely the command that
	// removed the pilot and D8 registrations, so it is the one run here.
	repo := newRepo(t, testOrigin)
	linked := filepath.Join(filepath.Dir(repo), "linked-prune")
	t.Cleanup(func() { os.RemoveAll(linked) }) //nolint:errcheck

	args := append(CrossOSWorktreeArgs(linked, "prune-branch", "HEAD"), "-q")
	if _, err := runGit(repo, args...); err != nil {
		t.Skipf("this git does not support --relative-paths: %v", err)
	}

	before, err := ProbeRepo(linked)
	if err != nil {
		t.Fatalf("probing before prune: %v", err)
	}

	// A writer lock taken in the worktree must also survive: a prune that
	// removed the admin directory would take the lock's home with it.
	lock, err := Acquire(WriterLockDir(before), Owner{
		RunID: "prune-run", ProjectID: "p", Session: "s", Worktree: linked, Role: RoleWriter,
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer lock.Release() //nolint:errcheck

	if _, err := runGit(repo, "worktree", "prune"); err != nil {
		t.Fatalf("prune: %v", err)
	}

	list, err := runGit(repo, "worktree", "list", "--porcelain")
	if err != nil {
		t.Fatalf("worktree list: %v", err)
	}
	if !strings.Contains(list, filepath.Base(linked)) {
		t.Fatalf("prune removed a live worktree's registration:\n%s", list)
	}

	after, err := ProbeRepo(linked)
	if err != nil {
		t.Fatalf("the worktree is unusable after prune: %v", err)
	}
	if after.Head != before.Head || after.Branch != before.Branch {
		t.Fatalf("identity changed across prune: %s@%s -> %s@%s",
			before.Branch, shortSHA(before.Head), after.Branch, shortSHA(after.Head))
	}

	owner, recorded, live, err := ProbeOwner(WriterLockDir(after))
	if err != nil || !recorded || !live {
		t.Fatalf("the writer lock did not survive the prune: recorded=%v live=%v err=%v", recorded, live, err)
	}
	if owner.RunID != "prune-run" {
		t.Fatalf("lock owner changed across prune: %q", owner.RunID)
	}
}

func TestSanctionedRemovalStillWorksOnADurableWorktree(t *testing.T) {
	// Durability must not mean undeletable. Cleanup has to stay deterministic,
	// or the fix trades one operational problem for another.
	repo := newRepo(t, testOrigin)
	linked := filepath.Join(filepath.Dir(repo), "linked-removable")
	t.Cleanup(func() { os.RemoveAll(linked) }) //nolint:errcheck

	args := append(CrossOSWorktreeArgs(linked, "removable-branch", "HEAD"), "-q")
	if _, err := runGit(repo, args...); err != nil {
		t.Skipf("this git does not support --relative-paths: %v", err)
	}
	if _, err := runGit(repo, "worktree", "remove", "--force", linked); err != nil {
		t.Fatalf("sanctioned removal failed: %v", err)
	}
	list, _ := runGit(repo, "worktree", "list", "--porcelain")
	if strings.Contains(list, "linked-removable") {
		t.Fatalf("removal left the registration behind:\n%s", list)
	}
	if _, err := os.Stat(linked); !os.IsNotExist(err) {
		t.Fatal("removal left the directory behind")
	}
}

func TestCrossOSWorktreeArgsAlwaysRequestRelativePaths(t *testing.T) {
	// The convention was forgotten twice. Encoding it in a function is the point.
	for _, tc := range []struct{ branch, base string }{
		{"a-branch", "origin/main"},
		{"", "origin/main"},
		{"a-branch", ""},
	} {
		args := CrossOSWorktreeArgs("/some/path", tc.branch, tc.base)
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--relative-paths") {
			t.Fatalf("args %q omit --relative-paths", joined)
		}
		if tc.branch == "" && !strings.Contains(joined, "--detach") {
			t.Fatalf("args %q should detach when no branch is named", joined)
		}
	}
}
