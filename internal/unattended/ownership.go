package unattended

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Ownership is a session's declaration of what it is allowed to touch.
//
// It is declared up front and proved before mutation. The failure it exists to
// stop — "session allocated to the wrong repository" — is invisible to every
// signal a session normally trusts: the shell window title, the working
// directory it was launched in, and an environment variable all say what
// someone intended, not what is actually under the cursor.
type Ownership struct {
	ProjectID string `json:"projectId"`
	// Worktree is the exact directory this session may mutate.
	Worktree string `json:"worktree"`
	// ExpectedOrigin is the origin remote the worktree must have. Empty means
	// the session declares no expectation, which is only correct for a target
	// with no remote at all.
	ExpectedOrigin string `json:"expectedOrigin,omitempty"`
	// ExpectedBranch is the branch the worktree must be on. Empty means any
	// branch is acceptable, which a mutating role should almost never say.
	ExpectedBranch string `json:"expectedBranch,omitempty"`
	Role           Role   `json:"role"`
	Session        string `json:"session"`
	// AllowDirtyWorktree makes an unclean tree acceptable. The dirt is still
	// enumerated in the report either way; this only decides whether it votes.
	AllowDirtyWorktree bool `json:"allowDirtyWorktree,omitempty"`
}

// ErrOwnershipIncomplete is returned when a declaration omits something without
// which nothing can be proved.
var ErrOwnershipIncomplete = errors.New("unattended: ownership declaration is incomplete")

// Validate refuses a declaration that cannot be checked.
func (o Ownership) Validate() error {
	var missing []string
	if strings.TrimSpace(o.ProjectID) == "" {
		missing = append(missing, "projectId")
	}
	if strings.TrimSpace(o.Worktree) == "" {
		missing = append(missing, "worktree")
	}
	if strings.TrimSpace(o.Session) == "" {
		missing = append(missing, "session")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: missing %s", ErrOwnershipIncomplete, strings.Join(missing, ", "))
	}
	if !o.Role.Valid() {
		return fmt.Errorf("%w: role %q is not one of writer, controller, read-only", ErrOwnershipIncomplete, string(o.Role))
	}
	if o.Role.Exclusive() && strings.TrimSpace(o.ExpectedBranch) == "" {
		return fmt.Errorf("%w: a %s must declare expectedBranch — a mutating session with no branch expectation cannot detect the branch moving underneath it",
			ErrOwnershipIncomplete, string(o.Role))
	}
	return nil
}

// samePath reports whether two paths denote the same location, resolving
// symlinks where possible and honoring the platform's case rules.
//
// Symlink resolution matters more than it looks: a worktree reached through
// one path and declared through another is the same worktree, and refusing it
// would push people towards disabling the ownership check entirely.
func samePath(a, b string) bool {
	norm := func(p string) string {
		p = filepath.Clean(p)
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
		if runtime.GOOS == "windows" {
			return strings.ToLower(p)
		}
		return p
	}
	return norm(a) == norm(b)
}

// Assert proves the declaration against observed repository state.
//
// It never mutates and never repairs. Its whole job is to answer, before a
// single byte is written, "am I where I said I would be".
func (o Ownership) Assert(state RepoState, probeErr error) Checks {
	var cs Checks

	if probeErr != nil {
		cs = append(cs, fail("ownership.worktree", CategoryOwnership,
			"declared worktree is a readable git worktree",
			o.Worktree, probeErr.Error(),
			"point the run at a real git worktree, or create it before starting"))
		// Nothing downstream can be evaluated against a repository that could
		// not be read, and saying so is the point: these are NOT REACHED, not
		// passes.
		const unprobed = "the declared worktree could not be probed"
		cs = append(cs,
			notReached("ownership.repository", CategoryOwnership, "worktree is the declared repository", unprobed+", so its identity is unknown"),
			notReached("ownership.origin", CategoryOwnership, "origin remote is the declared repository", unprobed+", so it has no readable remote"),
			notReached("ownership.branch", CategoryOwnership, "checked-out branch is the declared branch", unprobed+", so it has no readable branch"),
			notReached("repository.head", CategoryRepository, "HEAD is known", unprobed+", so HEAD is unknown"),
			notReached("repository.clean", CategoryRepository, "worktree state is known and acceptable", unprobed+", so its cleanliness is unknown"),
			notReached("repository.operation", CategoryRepository, "no unfinished merge, rebase or cherry-pick", unprobed+", so an unfinished operation cannot be ruled out"),
		)
		return cs
	}

	cs = append(cs, pass("ownership.worktree", CategoryOwnership,
		"declared worktree is a readable git worktree", state.Root))

	if samePath(state.Root, o.Worktree) {
		cs = append(cs, pass("ownership.repository", CategoryOwnership,
			"worktree is the declared repository", state.Root))
	} else {
		cs = append(cs, fail("ownership.repository", CategoryOwnership,
			"worktree is the declared repository",
			o.Worktree, state.Root,
			"this session is pointed at a different repository than it declared — stop before mutating anything"))
	}

	switch {
	case o.ExpectedOrigin == "":
		cs = append(cs, pass("ownership.origin", CategoryOwnership,
			"origin remote is the declared repository", "no origin expectation declared"))
	case SameRemote(state.OriginURL, o.ExpectedOrigin):
		cs = append(cs, pass("ownership.origin", CategoryOwnership,
			"origin remote is the declared repository", state.OriginURL))
	default:
		cs = append(cs, fail("ownership.origin", CategoryOwnership,
			"origin remote is the declared repository",
			o.ExpectedOrigin, orNone(state.OriginURL),
			"the directory is a git worktree but not of the declared project — do not push or open a PR from here"))
	}

	switch {
	case o.ExpectedBranch == "":
		cs = append(cs, pass("ownership.branch", CategoryOwnership,
			"checked-out branch is the declared branch", "no branch expectation declared"))
	case state.Detached:
		cs = append(cs, fail("ownership.branch", CategoryOwnership,
			"checked-out branch is the declared branch",
			o.ExpectedBranch, "detached HEAD at "+shortSHA(state.Head),
			"check out the declared branch before starting an unattended run"))
	case state.Branch == o.ExpectedBranch:
		cs = append(cs, pass("ownership.branch", CategoryOwnership,
			"checked-out branch is the declared branch", state.Branch))
	default:
		cs = append(cs, fail("ownership.branch", CategoryOwnership,
			"checked-out branch is the declared branch",
			o.ExpectedBranch, state.Branch,
			"another session may already have moved this worktree — do not mutate it"))
	}

	if state.Head != "" {
		cs = append(cs, pass("repository.head", CategoryRepository, "HEAD is known", shortSHA(state.Head)))
	} else {
		cs = append(cs, fail("repository.head", CategoryRepository, "HEAD is known",
			"a commit", "unborn branch — no commits",
			"make an initial commit; a run cannot fence a HEAD that does not exist"))
	}

	switch {
	case state.Clean():
		cs = append(cs, pass("repository.clean", CategoryRepository, "worktree state is known and acceptable", "clean"))
	case o.AllowDirtyWorktree:
		cs = append(cs, Check{
			ID: "repository.clean", Category: CategoryRepository,
			Title: "worktree state is known and acceptable", Outcome: OutcomePass,
			Observed: fmt.Sprintf("dirty, %d path(s), permitted by declaration", len(state.Dirty)),
			Detail:   strings.Join(state.Dirty, "; "),
		})
	default:
		cs = append(cs, Check{
			ID: "repository.clean", Category: CategoryRepository,
			Title: "worktree state is known and acceptable", Outcome: OutcomeFail,
			Expected: "clean", Observed: fmt.Sprintf("dirty, %d path(s)", len(state.Dirty)),
			Detail: strings.Join(state.Dirty, "; "),
			Remedy: "commit or stash the change, or declare allowDirtyWorktree if the dirt is expected",
		})
	}

	if state.InProgress == "" {
		cs = append(cs, pass("repository.operation", CategoryRepository,
			"no unfinished merge, rebase or cherry-pick", "none"))
	} else {
		cs = append(cs, fail("repository.operation", CategoryRepository,
			"no unfinished merge, rebase or cherry-pick",
			"no operation in progress", state.InProgress+" in progress",
			"finish or abort the "+state.InProgress+" by hand — an unattended run must not compound a state a human was resolving"))
	}

	return cs
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "none"
	}
	return s
}

func shortSHA(s string) string {
	if len(s) > 9 {
		return s[:9]
	}
	return s
}

// AssertOwnership probes the declared worktree and proves the declaration
// against it in one call.
func AssertOwnership(o Ownership) (RepoState, Checks) {
	if _, err := os.Stat(o.Worktree); err != nil {
		return RepoState{Dir: o.Worktree}, o.Assert(RepoState{Dir: o.Worktree}, err)
	}
	state, err := ProbeRepo(o.Worktree)
	return state, o.Assert(state, err)
}
