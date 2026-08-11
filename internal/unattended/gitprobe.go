package unattended

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// gitTimeout bounds every git invocation. A git command that hangs — a
// credential helper waiting on a terminal, a filesystem that stopped answering
// — is the failure mode that turns "the session stopped after nine minutes"
// into "the session stopped and nobody knows where", so nothing here can wait
// forever.
const gitTimeout = 30 * time.Second

// RepoState is everything about a git worktree that the control layer needs to
// know before it is willing to let work begin.
type RepoState struct {
	// Dir is the path that was probed, as given.
	Dir string `json:"dir"`
	// Root is the worktree's top level.
	Root string `json:"root"`
	// GitDir is this worktree's own git directory. For a linked worktree it is
	// under the main repository's .git/worktrees/<name>, which makes it the
	// natural per-worktree home for the writer lock.
	GitDir string `json:"gitDir"`
	// CommonDir is the shared git directory across all worktrees of the repo.
	CommonDir string `json:"commonDir"`

	Branch   string `json:"branch"`
	Detached bool   `json:"detached"`
	Head     string `json:"head"`

	OriginURL string `json:"originUrl"`

	// Dirty holds `git status --porcelain` lines, so a report can name the dirt
	// rather than merely assert it.
	Dirty []string `json:"dirty,omitempty"`

	// InProgress names an unfinished merge, rebase, cherry-pick, revert or
	// bisect. An unattended run must never start on top of one: the repository
	// is mid-operation and any mutation compounds a state a human was going to
	// resolve.
	InProgress string `json:"inProgress,omitempty"`

	// Worktrees lists every worktree registered against the repository.
	Worktrees []string `json:"worktrees,omitempty"`
}

// Clean reports whether the worktree has no uncommitted change.
func (s RepoState) Clean() bool { return len(s.Dirty) == 0 }

func runGit(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// A credential prompt in an unattended run is a hang, not a question.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		var ee *exec.ExitError
		if ok := asExitError(err, &ee); ok {
			stderr = strings.TrimSpace(string(ee.Stderr))
		}
		if ctx.Err() != nil {
			return "", fmt.Errorf("git %s in %q timed out after %s", strings.Join(args, " "), dir, gitTimeout)
		}
		if stderr != "" {
			return "", fmt.Errorf("git %s in %q: %w: %s", strings.Join(args, " "), dir, err, stderr)
		}
		return "", fmt.Errorf("git %s in %q: %w", strings.Join(args, " "), dir, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func asExitError(err error, target **exec.ExitError) bool {
	ee := &exec.ExitError{}
	if errors.As(err, &ee) {
		*target = ee
		return true
	}
	return false
}

// ProbeRepo reads the state of a git worktree.
//
// It answers only from git. Nothing here infers a repository identity from a
// directory name, a shell window title or an environment variable, because
// every one of those has already been observed pointing at the wrong project.
func ProbeRepo(dir string) (RepoState, error) {
	st := RepoState{Dir: dir}
	if fi, err := os.Stat(dir); err != nil {
		return st, fmt.Errorf("probing %q: %w", dir, err)
	} else if !fi.IsDir() {
		return st, fmt.Errorf("probing %q: not a directory", dir)
	}

	root, err := runGit(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return st, fmt.Errorf("%q is not inside a git worktree: %w", dir, err)
	}
	st.Root = filepath.Clean(root)

	if st.GitDir, err = runGit(dir, "rev-parse", "--absolute-git-dir"); err != nil {
		return st, err
	}
	common, err := runGit(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return st, err
	}
	st.CommonDir = filepath.Clean(common)

	branch, err := runGit(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return st, err
	}
	if branch == "HEAD" {
		st.Detached = true
		st.Branch = ""
	} else {
		st.Branch = branch
	}

	// An unborn branch has no HEAD commit. That is a legitimate state for a
	// freshly initialized repository, so it is recorded as an empty HEAD rather
	// than failing the probe.
	if head, err := runGit(dir, "rev-parse", "HEAD"); err == nil {
		st.Head = head
	}

	if origin, err := runGit(dir, "remote", "get-url", "origin"); err == nil {
		st.OriginURL = origin
	}

	status, err := runGit(dir, "status", "--porcelain")
	if err != nil {
		return st, err
	}
	for _, line := range strings.Split(status, "\n") {
		if strings.TrimSpace(line) != "" {
			st.Dirty = append(st.Dirty, line)
		}
	}

	st.InProgress = inProgressOperation(st.GitDir)

	if list, err := runGit(dir, "worktree", "list", "--porcelain"); err == nil {
		for _, line := range strings.Split(list, "\n") {
			if p, ok := strings.CutPrefix(line, "worktree "); ok {
				st.Worktrees = append(st.Worktrees, filepath.Clean(p))
			}
		}
	}
	return st, nil
}

// inProgressOperation names an unfinished git operation from the marker files
// git itself uses, in the order git resolves them.
func inProgressOperation(gitDir string) string {
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(gitDir, name))
		return err == nil
	}
	switch {
	case exists("rebase-merge"), exists("rebase-apply"):
		return "rebase"
	case exists("MERGE_HEAD"):
		return "merge"
	case exists("CHERRY_PICK_HEAD"):
		return "cherry-pick"
	case exists("REVERT_HEAD"):
		return "revert"
	case exists("BISECT_LOG"):
		return "bisect"
	default:
		return ""
	}
}

// normalizeRemoteURL reduces a remote URL to a comparable identity.
//
// The same repository is written `https://github.com/o/r.git`,
// `git@github.com:o/r`, and `ssh://git@github.com/o/r/` depending on who
// cloned it. Comparing the raw strings would make a correctly configured
// machine fail preflight, which trains people to disable the check.
func normalizeRemoteURL(raw string) string {
	u := strings.TrimSpace(raw)
	if u == "" {
		return ""
	}
	u = strings.TrimSuffix(u, "/")
	for _, scheme := range []string{"https://", "http://", "ssh://", "git://"} {
		u = strings.TrimPrefix(u, scheme)
	}
	if at := strings.LastIndex(u, "@"); at >= 0 && !strings.Contains(u[:at], "/") {
		u = u[at+1:]
	}
	u = strings.Replace(u, ":", "/", 1)
	u = strings.TrimSuffix(u, ".git")
	return strings.ToLower(strings.TrimSuffix(u, "/"))
}

// SameRemote reports whether two remote URLs name the same repository.
func SameRemote(a, b string) bool {
	na, nb := normalizeRemoteURL(a), normalizeRemoteURL(b)
	return na != "" && na == nb
}

// WriterLockDir returns the directory a worktree's writer lock lives in.
//
// It is the worktree's own git directory: per-worktree by construction, never
// tracked by git so it cannot dirty a status, and removed with the worktree so
// a disposed worktree cannot leave a lock behind to confuse the next run.
func WriterLockDir(state RepoState) string {
	return filepath.Join(state.GitDir, "gc-unattended")
}
