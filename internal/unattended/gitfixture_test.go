package unattended

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gitFixture builds real git repositories for the repository, ownership and
// fence tests.
//
// These tests shell out to the real git rather than to an interface with a fake
// behind it. The bugs this layer exists to catch — a detached HEAD, an
// unfinished rebase, a linked worktree's git directory, a remote written in ssh
// form — all live in git's actual behavior, and a fake would only ever
// reproduce the behavior that was already understood.

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := runGit(dir, args...)
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return out
}

// newRepo creates an initialized repository on `main` with one commit and an
// origin remote.
func newRepo(t *testing.T, origin string) string {
	t.Helper()
	requireGit(t)
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q", "-b", "main")
	mustGit(t, dir, "config", "user.name", "Unattended Test")
	mustGit(t, dir, "config", "user.email", "test@example.invalid")
	mustGit(t, dir, "config", "commit.gpgsign", "false")
	writeRepoFile(t, dir, "README.md", "base\n")
	mustGit(t, dir, "add", "README.md")
	mustGit(t, dir, "commit", "-qm", "chore: base")
	if origin != "" {
		mustGit(t, dir, "remote", "add", "origin", origin)
	}
	return dir
}

func writeRepoFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func commitAll(t *testing.T, dir, message string) string {
	t.Helper()
	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "commit", "-qm", message)
	return mustGit(t, dir, "rev-parse", "HEAD")
}
