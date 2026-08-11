//go:build integration

package unattended

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The real cross-OS regression: it fails against the defective worktree
// strategy and passes against the repaired one.
//
// The same-OS prune test in worktree_test.go does NOT discriminate. Measured:
// a worktree with absolute pointers survives `git worktree prune` run by the
// git that created it, because that git can resolve its own paths perfectly
// well. Only a prune run by the *other* OS deletes it. So a same-OS test would
// have passed against the very strategy that lost the pilot and D8 worktrees,
// and inspecting the recorded pointer — while a useful preflight guard — is not
// evidence that the failure mode is gone.
//
// This test therefore drives both gits. It needs a Windows git reachable
// through WSL interop and a location both namespaces can see, so it is
// integration-tagged and skips cleanly where that is not the case.

const (
	windowsGit     = "/mnt/c/Program Files/Git/cmd/git.exe"
	sharedTestRoot = "/mnt/d/Development/worktrees"
)

// winPath converts a /mnt/<drive>/... path to the Windows form that git.exe
// needs. The whole defect is that these two spellings do not interchange, so
// the conversion is explicit rather than assumed.
func winPath(p string) string {
	rest, ok := strings.CutPrefix(p, "/mnt/")
	if !ok || len(rest) < 2 || rest[1] != '/' {
		return p
	}
	drive := strings.ToUpper(rest[:1])
	return drive + ":\\" + strings.ReplaceAll(rest[2:], "/", "\\")
}

func requireCrossOS(t *testing.T) string {
	t.Helper()
	if _, err := os.Stat(windowsGit); err != nil {
		t.Skipf("no Windows git at %s; cross-OS evidence needs both gits", windowsGit)
	}
	if _, err := os.Stat(sharedTestRoot); err != nil {
		t.Skipf("no shared location at %s visible to both namespaces", sharedTestRoot)
	}
	base, err := os.MkdirTemp(sharedTestRoot, "gc-xos-")
	if err != nil {
		t.Skipf("cannot create a shared test root: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(base) }) //nolint:errcheck
	return base
}

// winGit runs the Windows git against a Windows path. The second result
// reports whether git exited zero; a non-zero exit is an answer here, not a
// failure to run.
//
// This goes through runProbe rather than exec.Command directly. The all-source
// census ratchets subprocess call sites, and reusing the existing one keeps a
// test that must spawn a second git from spending ledger budget — the same
// reason lock_process_test.go does it. runProbe is also the better tool: it
// supervises the process group, bounds the run, and refuses to let a prompt
// hang the suite.
func winGit(t *testing.T, repoWinPath string, args ...string) (string, bool) {
	t.Helper()
	argv := append([]string{windowsGit, "-C", repoWinPath}, args...)
	out, ok, err := runProbe(context.Background(), 60*time.Second, "", argv)
	if err != nil {
		t.Fatalf("running windows git %v: %v", args, err)
	}
	return out, ok
}

// seedSharedRepo makes a repository both gits can reach.
func seedSharedRepo(t *testing.T, base string) string {
	t.Helper()
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "test@example.invalid"},
		{"config", "user.name", "Cross OS Test"},
		{"config", "commit.gpgsign", "false"},
	} {
		if _, err := runGit(repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	writeRepoFile(t, repo, "README.md", "base\n")
	if _, err := runGit(repo, "add", "README.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(repo, "commit", "-qm", "chore: base"); err != nil {
		t.Fatal(err)
	}
	return repo
}

func registered(t *testing.T, repo, needle string) bool {
	t.Helper()
	list, err := runGit(repo, "worktree", "list", "--porcelain")
	if err != nil {
		t.Fatalf("worktree list: %v", err)
	}
	return strings.Contains(list, needle)
}

// TestTheDefectiveStrategyLosesItsWorktreeToAWindowsPrune reproduces the
// incident that removed the GUK BPM pilot and D8 worktrees.
//
// It asserts the FAILURE, so it is the half that would go red if someone
// reverted the fix and kept using WSL-native paths.
func TestTheDefectiveStrategyLosesItsWorktreeToAWindowsPrune(t *testing.T) {
	base := requireCrossOS(t)
	repo := seedSharedRepo(t, base)

	// A WSL-native path, exactly as the pilot and D8 used.
	native, err := os.MkdirTemp("/home/corsolvtech", "gc-xos-native-")
	if err != nil {
		t.Skipf("cannot create a WSL-native path: %v", err)
	}
	os.RemoveAll(native)                       //nolint:errcheck — git wants to create the leaf itself
	t.Cleanup(func() { os.RemoveAll(native) }) //nolint:errcheck

	if _, err := runGit(repo, "worktree", "add", "-q", "-b", "defective-branch", native); err != nil {
		t.Fatalf("creating the WSL-native worktree: %v", err)
	}
	if !registered(t, repo, native) {
		t.Fatal("the worktree was not registered to begin with")
	}

	// Windows git should already regard it as prunable — it cannot resolve a
	// /home path, so as far as it is concerned the worktree is gone.
	list, _ := winGit(t, winPath(repo), "worktree", "list")
	if !strings.Contains(list, "prunable") {
		t.Skipf("this Windows git does not mark the WSL worktree prunable; the incident cannot be reproduced here:\n%s", list)
	}

	if _, ok := winGit(t, winPath(repo), "worktree", "prune"); !ok {
		t.Fatal("windows prune exited non-zero")
	}

	if registered(t, repo, native) {
		t.Fatal("the defective strategy survived a Windows prune; this test no longer reproduces the incident")
	}
	// And the worktree is now unusable — the exact failure seen on guk-bpm-d8.
	if _, err := ProbeRepo(native); err == nil {
		t.Fatal("the pruned worktree is still usable; the incident is not reproduced")
	}
}

// TestTheRepairedStrategySurvivesAWindowsPrune is the same event against the
// strategy the engine now uses.
func TestTheRepairedStrategySurvivesAWindowsPrune(t *testing.T) {
	base := requireCrossOS(t)
	repo := seedSharedRepo(t, base)
	shared := filepath.Join(base, "task-worktree")

	args := append(CrossOSWorktreeArgs(shared, "repaired-branch", "HEAD"), "-q")
	if _, err := runGit(repo, args...); err != nil {
		t.Skipf("this git does not support --relative-paths: %v", err)
	}

	before, err := ProbeRepo(shared)
	if err != nil {
		t.Fatalf("probing the shared worktree: %v", err)
	}

	// Both namespaces must resolve the same physical worktree before the prune
	// is even interesting.
	winHead, ok := winGit(t, winPath(shared), "rev-parse", "HEAD")
	if !ok {
		t.Fatalf("windows git cannot read the shared worktree: %s", winHead)
	}
	if winHead != before.Head {
		t.Fatalf("the two gits disagree about HEAD: windows=%s wsl=%s", winHead, before.Head)
	}

	// A held writer lock must survive too: the lock lives under the admin
	// directory a prune would remove.
	lock, err := Acquire(WriterLockDir(before), Owner{
		RunID: "xos-run", ProjectID: "p", Session: "s", Worktree: shared, Role: RoleWriter,
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer lock.Release() //nolint:errcheck

	if _, ok := winGit(t, winPath(repo), "worktree", "prune"); !ok {
		t.Fatal("windows prune exited non-zero")
	}

	if !registered(t, repo, "task-worktree") {
		t.Fatal("a Windows prune removed the repaired worktree's registration")
	}
	after, err := ProbeRepo(shared)
	if err != nil {
		t.Fatalf("the worktree is unusable after a Windows prune: %v", err)
	}
	if after.Head != before.Head || after.Branch != before.Branch {
		t.Fatalf("identity changed: %s@%s -> %s@%s",
			before.Branch, shortSHA(before.Head), after.Branch, shortSHA(after.Head))
	}
	owner, recorded, live, err := ProbeOwner(WriterLockDir(after))
	if err != nil || !recorded || !live || owner.RunID != "xos-run" {
		t.Fatalf("the writer lock did not survive: recorded=%v live=%v owner=%q err=%v", recorded, live, owner.RunID, err)
	}

	// Crash/resume must be able to rediscover it by the declared path alone.
	state, ownership := AssertOwnership(Ownership{
		ProjectID: "p", Worktree: shared, ExpectedBranch: "repaired-branch",
		Role: RoleWriter, Session: "s",
	})
	if ownership.Readiness() == NotReady {
		t.Fatalf("a resumed run could not re-establish ownership:\n%s", ownership)
	}
	if state.Head != before.Head {
		t.Fatalf("rediscovered HEAD %s, want %s", shortSHA(state.Head), shortSHA(before.Head))
	}

	// Cleanup must still be deterministic.
	if err := lock.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := runGit(repo, "worktree", "remove", "--force", shared); err != nil {
		t.Fatalf("sanctioned removal: %v", err)
	}
	if registered(t, repo, "task-worktree") {
		t.Fatal("removal left the registration behind")
	}
}
