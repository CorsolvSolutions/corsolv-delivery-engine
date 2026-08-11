package unattended

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// This file exists because of a defect the GUK BPM pilot and the D8 delivery
// both hit, and which neither noticed until afterwards.
//
// Both runs created their worktree at a WSL-native path — /home/corsolvtech/… —
// of a repository hosted on the Windows D: drive. Git records a worktree with
// two absolute pointers: a `.git` file in the worktree holding `gitdir: <path>`,
// and a `gitdir` file under .git/worktrees/<name> pointing back. Written by WSL
// git those are Linux paths, which Windows git cannot resolve; it therefore
// regards the worktree as gone and `git worktree prune` deletes its metadata.
//
// That is exactly what happened: a Windows-side prune removed both runs'
// worktree registrations mid-program. Nothing was lost — the commits and
// branches were already on origin — but "the work was safe because it had been
// pushed" is not a durability property. It is luck about timing. A run that had
// not yet pushed would have been left holding a worktree git no longer knew
// about, and a resumed run would have found its worktree missing rather than
// its work finished.
//
// The reverse fails too: a Windows-created worktree records `gitdir: D:/…`,
// which WSL git reads as a relative path and cannot resolve, so a WSL-side run
// cannot use it either. The two namespaces simply do not agree on absolute
// paths.
//
// The fix is to stop recording absolute paths. Git can write both pointers
// relative to the repository, and a relative path contains no drive letter and
// no /mnt prefix, so it resolves identically from either side. That, plus
// placing the worktree somewhere both namespaces can actually reach, is the
// whole of it.

// ErrWorktreeNotCrossOSDurable is returned when a worktree's registration would
// not survive being seen from the other operating system.
var ErrWorktreeNotCrossOSDurable = errors.New("unattended: worktree registration is not durable across the Windows/WSL boundary")

// WorktreePointers are the two files git uses to link a worktree to its
// repository. Both must be relative for the link to survive a prune run from
// the other side of the boundary.
type WorktreePointers struct {
	// DotGit is the `gitdir: …` value inside the worktree's .git file.
	DotGit string
	// GitDirBack is the contents of .git/worktrees/<name>/gitdir.
	GitDirBack string
}

// IsAbsoluteAnyOS reports whether a recorded pointer is absolute in either
// namespace.
//
// It cannot use filepath.IsAbs: that answers for the OS this binary was
// compiled for, and the entire problem is a path written by the *other* one. A
// Linux binary asked about `D:/repo/.git` would say "relative" and be wrong in
// the way that matters.
func IsAbsoluteAnyOS(p string) bool {
	p = strings.TrimSpace(p)
	if p == "" {
		return false
	}
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return true // POSIX absolute, or a Windows UNC/rooted path
	}
	// A drive-letter prefix: C:/…, D:\…
	if len(p) >= 2 && p[1] == ':' {
		c := p[0]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			return true
		}
	}
	return false
}

// ReadWorktreePointers reads both pointers for a linked worktree.
func ReadWorktreePointers(worktree string) (WorktreePointers, error) {
	var wp WorktreePointers

	dotGit := filepath.Join(worktree, ".git")

	// A main worktree has a .git *directory*, not a pointer file. That shape is
	// durable by construction — there is no cross-repository pointer to break —
	// so it reports nothing to check rather than an error. Reading it as a file
	// would fail with "is a directory", which is how the first version of this
	// wrongly failed every main worktree in the suite.
	info, err := os.Stat(dotGit)
	if err != nil {
		return wp, fmt.Errorf("reading %q: %w", dotGit, err)
	}
	if info.IsDir() {
		return wp, nil
	}

	raw, err := os.ReadFile(dotGit)
	if err != nil {
		return wp, fmt.Errorf("reading %q: %w", dotGit, err)
	}
	value, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir:")
	if !ok {
		return wp, nil
	}
	wp.DotGit = strings.TrimSpace(value)

	// The back-pointer lives beside the gitdir the .git file names. Resolving it
	// relative to the worktree is what a git on this side would do, so if that
	// resolution fails the pointer is already unusable here.
	adminDir := wp.DotGit
	if !IsAbsoluteAnyOS(adminDir) {
		adminDir = filepath.Join(worktree, adminDir)
	}
	back, err := os.ReadFile(filepath.Join(adminDir, "gitdir"))
	if err != nil {
		// Unreadable from this side is itself the symptom; report what was
		// found rather than failing, so the caller can say why.
		return wp, nil
	}
	wp.GitDirBack = strings.TrimSpace(string(back))
	return wp, nil
}

// CheckWorktreeCrossOSDurable reports whether a worktree's registration will
// survive a prune run from the other operating system.
//
// It checks the recorded pointers rather than trying the other OS, because the
// other OS is not available to ask, and because the pointers are the whole of
// what git consults when it decides a worktree is stale.
func CheckWorktreeCrossOSDurable(worktree string) Check {
	const (
		id    = "worktree.crossOsDurable"
		title = "worktree registration survives a prune from the other OS"
	)

	wp, err := ReadWorktreePointers(worktree)
	if err != nil {
		return fail(id, CategoryRepository, title, "readable worktree pointers", err.Error(),
			"check the worktree exists and is a git worktree")
	}
	if wp.DotGit == "" {
		return pass(id, CategoryRepository, title, "main worktree — no cross-repository pointer to break")
	}

	var offenders []string
	if IsAbsoluteAnyOS(wp.DotGit) {
		offenders = append(offenders, fmt.Sprintf(".git → %s", wp.DotGit))
	}
	if wp.GitDirBack != "" && IsAbsoluteAnyOS(wp.GitDirBack) {
		offenders = append(offenders, fmt.Sprintf("worktrees/…/gitdir → %s", wp.GitDirBack))
	}
	if len(offenders) == 0 {
		return pass(id, CategoryRepository, title, "both pointers are relative")
	}

	return Check{
		ID: id, Category: CategoryRepository, Title: title, Outcome: OutcomeFail,
		Expected: "relative pointers, resolvable from Windows and WSL alike",
		Observed: strings.Join(offenders, "; "),
		Detail: "an absolute pointer names a path only one OS can resolve; git on the other side treats the worktree as gone, " +
			"and `git worktree prune` there deletes its registration — which is what removed the pilot and D8 worktrees mid-program",
		Remedy: "re-create the worktree with `git worktree add --relative-paths`, or set worktree.useRelativePaths=true, " +
			"and place it where both namespaces can reach it (a Windows-hosted path, reached from WSL through /mnt)",
	}
}

// CrossOSWorktreeArgs returns the `git worktree add` arguments that produce a
// durable registration.
//
// It is a function rather than a documented convention because a convention is
// something the next run can forget, and this one was forgotten twice.
func CrossOSWorktreeArgs(path, branch, baseRef string) []string {
	args := []string{"worktree", "add", "--relative-paths"}
	if branch != "" {
		args = append(args, "-b", branch)
	} else {
		args = append(args, "--detach")
	}
	args = append(args, path)
	if baseRef != "" {
		args = append(args, baseRef)
	}
	return args
}
