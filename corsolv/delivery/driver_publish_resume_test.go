//go:build integration

// Publication as two steps, resumed.
//
// Both defects here were found by a live pilot delivery, on the same package,
// in the same minute. A commit succeeded, its push named a branch nothing had
// created, and the three retries that followed could not commit again because
// the work was already committed — so a finished, gated package became
// permanently unpublishable and the run reported a commit problem.
//
// Like the recovery tests these spawn bash and git, so they carry the
// integration tag.

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitOut runs git in the test's own environment and returns trimmed stdout.
func gitOut(t *testing.T, e *recoveryEnv, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = e.scrubbedEnv(nil)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// THE REFSPEC THAT DID NOT EXIST.
//
// A package routed before its base exists gets no worktree from the controller;
// Gas City makes one, on a branch of its own naming, so the agent it was told to
// start has somewhere to work. The controller's ledger still remembers the
// branch it intended, and publication pushed that — a ref nothing ever created.
// The failure reads `src refspec … does not match any`, over a worktree holding
// finished, gated, committed work.
func TestPublicationPushesTheBranchTheWorktreeIsActuallyOn(t *testing.T) {
	s := newSendbackEnv(t)

	// What Gas City's own naming looks like beside the controller's.
	const actual = "gc-worker-wp-one"
	gitOut(t, s.recoveryEnv, s.worktree, "branch", "-m", actual)

	code, out := s.runPublish()

	if strings.Contains(out, "does not match any") {
		t.Fatalf("publication pushed a branch that does not exist:\n%s", out)
	}
	if !strings.Contains(out, actual) {
		t.Errorf("the driver must say which branch it found, got:\n%s", out)
	}

	origin := filepath.Join(s.root, "origin.git")
	if refs := gitOut(t, s.recoveryEnv, origin, "branch", "--list", actual); refs == "" {
		t.Errorf("the branch holding the work was never pushed to origin (exit %d):\n%s", code, out)
	}

	// The ledger is corrected, because every later step names this branch.
	if got := gitOut(t, s.recoveryEnv, s.worktree, "rev-parse", "--abbrev-ref", "HEAD"); got != actual {
		t.Errorf("HEAD = %q, want %q", got, actual)
	}
}

// THE UNRECOVERABLE STATE.
//
// Commit and push are two steps, and a push fails on its own. The retry finds
// nothing left to commit — the work is already on the branch — and reading that
// empty commit as a failure spent every attempt refusing to publish work the
// controller had committed itself. Nothing but a person editing the branch could
// undo it.
func TestPublicationResumesAfterAnAttemptThatCommittedAndCouldNotPush(t *testing.T) {
	s := newSendbackEnv(t)

	// Exactly what the attempt that could not push left behind: the authorized
	// work committed on the branch, and nothing pushed.
	gitOut(t, s.recoveryEnv, s.worktree, "add", "--", "src/one.ts")
	gitOut(t, s.recoveryEnv, s.worktree,
		"-c", "user.name=Gas City Controller", "-c", "user.email=support@corsolv.com",
		"commit", "-q", "-m", "feat(wp-one): src/one.ts")
	committed := gitOut(t, s.recoveryEnv, s.worktree, "rev-parse", "HEAD")

	code, out := s.runPublish()

	if strings.Contains(out, "committing wp-one") {
		t.Fatalf("work already committed by an earlier attempt must not be reported as a commit failure:\n%s", out)
	}
	if !strings.Contains(out, "already committed") {
		t.Errorf("the driver must say it adopted the earlier attempt's commit, got:\n%s", out)
	}

	origin := filepath.Join(s.root, "origin.git")
	if got := gitOut(t, s.recoveryEnv, origin, "rev-parse", "refs/heads/delivery/20260814T164300Z/wp-one"); got != committed {
		t.Errorf("origin holds %q, want the adopted commit %q (exit %d):\n%s", got, committed, code, out)
	}
}

// The other half of the same rule. Adoption is only honest when the branch holds
// something the authoritative branch does not; a clean tree over a HEAD already
// on main means the worker produced nothing, and that still stops publication.
func TestPublicationRefusesToAdoptWhenNothingWasProduced(t *testing.T) {
	s := newSendbackEnv(t)

	// Remove the worker's output, so the tree is clean against a HEAD that is
	// still exactly the base commit. It was never committed, so it goes from the
	// filesystem rather than from the index.
	if err := os.Remove(filepath.Join(s.worktree, "src", "one.ts")); err != nil {
		t.Fatal(err)
	}

	code, out := s.runPublish()

	if code == 0 {
		t.Fatalf("a package that produced nothing must not publish:\n%s", out)
	}
	if strings.Contains(out, "already committed") {
		t.Errorf("nothing was produced, so there is no earlier commit to adopt:\n%s", out)
	}
}
