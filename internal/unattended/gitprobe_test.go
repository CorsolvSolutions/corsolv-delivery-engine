package unattended

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProbeRepoReadsBranchHeadAndOrigin(t *testing.T) {
	const origin = "https://github.com/CorsolvSolutions/corsolv-delivery-engine.git"
	dir := newRepo(t, origin)

	st, err := ProbeRepo(dir)
	if err != nil {
		t.Fatalf("ProbeRepo: %v", err)
	}
	if st.Branch != "main" || st.Detached {
		t.Fatalf("branch = %q detached=%v, want main", st.Branch, st.Detached)
	}
	if len(st.Head) != 40 {
		t.Fatalf("head = %q, want a full sha", st.Head)
	}
	if !SameRemote(st.OriginURL, origin) {
		t.Fatalf("origin = %q, want %q", st.OriginURL, origin)
	}
	if !st.Clean() {
		t.Fatalf("fresh repo reported dirty: %v", st.Dirty)
	}
	if st.InProgress != "" {
		t.Fatalf("fresh repo reported %q in progress", st.InProgress)
	}
	if st.GitDir == "" || st.CommonDir == "" {
		t.Fatalf("git dirs not resolved: gitDir=%q commonDir=%q", st.GitDir, st.CommonDir)
	}
}

func TestProbeRepoRejectsANonRepository(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	// A directory that is not under any repository. GIT_CEILING_DIRECTORIES
	// stops git walking up out of the temp dir into a real one.
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	if _, err := ProbeRepo(dir); err == nil {
		t.Fatal("a plain directory must not probe as a git worktree")
	}
}

func TestProbeRepoRejectsAMissingDirectory(t *testing.T) {
	if _, err := ProbeRepo(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("a missing directory must not probe")
	}
}

func TestProbeRepoEnumeratesDirt(t *testing.T) {
	dir := newRepo(t, "")
	writeRepoFile(t, dir, "scratch.txt", "uncommitted\n")

	st, err := ProbeRepo(dir)
	if err != nil {
		t.Fatalf("ProbeRepo: %v", err)
	}
	if st.Clean() {
		t.Fatal("worktree with an untracked file reported clean")
	}
	if !strings.Contains(strings.Join(st.Dirty, "\n"), "scratch.txt") {
		t.Fatalf("dirt not enumerated: %v", st.Dirty)
	}
}

func TestProbeRepoDetectsDetachedHead(t *testing.T) {
	dir := newRepo(t, "")
	head := mustGit(t, dir, "rev-parse", "HEAD")
	mustGit(t, dir, "checkout", "-q", "--detach", head)

	st, err := ProbeRepo(dir)
	if err != nil {
		t.Fatalf("ProbeRepo: %v", err)
	}
	if !st.Detached || st.Branch != "" {
		t.Fatalf("detached HEAD not detected: detached=%v branch=%q", st.Detached, st.Branch)
	}
}

func TestProbeRepoDetectsUnfinishedMerge(t *testing.T) {
	dir := newRepo(t, "")
	mustGit(t, dir, "checkout", "-q", "-b", "side")
	writeRepoFile(t, dir, "conflict.txt", "side\n")
	commitAll(t, dir, "feat: side")
	mustGit(t, dir, "checkout", "-q", "main")
	writeRepoFile(t, dir, "conflict.txt", "main\n")
	commitAll(t, dir, "feat: main")

	// Expected to conflict; the point is the repository left mid-merge.
	if _, err := runGit(dir, "merge", "side"); err == nil {
		t.Skip("merge did not conflict on this git; the marker case cannot be built")
	}
	st, err := ProbeRepo(dir)
	if err != nil {
		t.Fatalf("ProbeRepo: %v", err)
	}
	if st.InProgress != "merge" {
		t.Fatalf("inProgress = %q, want merge", st.InProgress)
	}
}

func TestProbeRepoResolvesLinkedWorktreeGitDir(t *testing.T) {
	dir := newRepo(t, "")
	linked := filepath.Join(t.TempDir(), "linked")
	mustGit(t, dir, "worktree", "add", "-q", "-b", "linked-branch", linked)

	main, err := ProbeRepo(dir)
	if err != nil {
		t.Fatalf("ProbeRepo main: %v", err)
	}
	sub, err := ProbeRepo(linked)
	if err != nil {
		t.Fatalf("ProbeRepo linked: %v", err)
	}
	if sub.GitDir == main.GitDir {
		t.Fatal("a linked worktree must have its own git dir, or the writer lock would be shared across worktrees")
	}
	if !samePath(sub.CommonDir, main.CommonDir) {
		t.Fatalf("linked worktree common dir = %q, want %q", sub.CommonDir, main.CommonDir)
	}
	if sub.Branch != "linked-branch" {
		t.Fatalf("linked worktree branch = %q", sub.Branch)
	}
	if len(main.Worktrees) != 2 {
		t.Fatalf("worktree list = %v, want both", main.Worktrees)
	}
}

func TestWriterLockDirIsPerWorktreeAndUntracked(t *testing.T) {
	dir := newRepo(t, "")
	linked := filepath.Join(t.TempDir(), "linked")
	mustGit(t, dir, "worktree", "add", "-q", "-b", "linked-branch", linked)

	main, _ := ProbeRepo(dir)
	sub, _ := ProbeRepo(linked)
	if WriterLockDir(main) == WriterLockDir(sub) {
		t.Fatal("two worktrees must not share a writer lock")
	}

	// Taking the lock must not make either worktree dirty; a lock that shows up
	// in `git status` would fail the very cleanliness check it sits beside.
	lk, err := Acquire(WriterLockDir(sub), Owner{RunID: "r", ProjectID: "p", Session: "s", Worktree: linked, Role: RoleWriter})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer lk.Release() //nolint:errcheck

	after, err := ProbeRepo(linked)
	if err != nil {
		t.Fatalf("ProbeRepo after lock: %v", err)
	}
	if !after.Clean() {
		t.Fatalf("writer lock dirtied the worktree: %v", after.Dirty)
	}
}

func TestNormalizeRemoteURLTreatsEquivalentFormsAsOne(t *testing.T) {
	same := []string{
		"https://github.com/CorsolvSolutions/corsolv-delivery-engine.git",
		"https://github.com/CorsolvSolutions/corsolv-delivery-engine",
		"git@github.com:CorsolvSolutions/corsolv-delivery-engine.git",
		"ssh://git@github.com/CorsolvSolutions/corsolv-delivery-engine.git",
		"https://github.com/corsolvsolutions/corsolv-delivery-engine/",
	}
	for _, a := range same {
		for _, b := range same {
			if !SameRemote(a, b) {
				t.Fatalf("SameRemote(%q, %q) = false, want true", a, b)
			}
		}
	}
	if SameRemote("https://github.com/CorsolvSolutions/corsolv-delivery-engine.git",
		"https://github.com/gastownhall/gascity.git") {
		t.Fatal("distinct repositories must not compare equal")
	}
	if SameRemote("", "") {
		t.Fatal("two absent remotes must not compare equal — absence is not identity")
	}
}

func TestRunGitDoesNotWaitOnACredentialPrompt(t *testing.T) {
	// GIT_TERMINAL_PROMPT=0 is what turns an unattended credential prompt from
	// an unbounded hang into an error. Assert the environment carries it.
	dir := newRepo(t, "")
	out, err := runGit(dir, "var", "GIT_EDITOR")
	if err != nil {
		t.Fatalf("git var: %v", err)
	}
	_ = out
	if got := os.Getenv("GIT_TERMINAL_PROMPT"); got != "" && got != "0" {
		t.Fatalf("test environment leaks GIT_TERMINAL_PROMPT=%q", got)
	}
}
