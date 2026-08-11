package unattended

import (
	"fmt"
	"time"
)

// FenceStatus is the outcome of re-verifying that the ground has not moved.
type FenceStatus string

// The fence outcomes.
const (
	// FenceIntact — branch, HEAD and lock ownership are all as the fence
	// recorded them.
	FenceIntact FenceStatus = "intact"
	// FenceBranchChanged — the worktree is on a different branch than the one
	// the run claimed. This is the exact shape of the observed failure where
	// one session checked out another branch underneath a live writer.
	FenceBranchChanged FenceStatus = "branch-changed"
	// FenceHeadMoved — the branch is right but HEAD advanced or rewound without
	// an authorized advance being recorded, so something else committed here.
	FenceHeadMoved FenceStatus = "head-moved"
	// FenceOwnerLost — the writer lock no longer records this run as owner.
	FenceOwnerLost FenceStatus = "owner-lost"
	// FenceUnreadable — the worktree could not be probed at all.
	FenceUnreadable FenceStatus = "unreadable"
)

// FenceResult records one fence verification, in enough detail to be evidence.
type FenceResult struct {
	Status    FenceStatus `json:"status"`
	Expected  string      `json:"expected,omitempty"`
	Observed  string      `json:"observed,omitempty"`
	Evidence  string      `json:"evidence,omitempty"`
	CheckedAt time.Time   `json:"checkedAt"`
}

// Intact reports whether work may proceed.
func (r FenceResult) Intact() bool { return r.Status == FenceIntact }

// Error renders a violation as an error suitable for failing closed.
func (r FenceResult) Error() error {
	if r.Intact() {
		return nil
	}
	return fmt.Errorf("unattended: fence %s — expected %q, observed %q%s",
		r.Status, r.Expected, r.Observed, evidenceSuffix(r.Evidence))
}

func evidenceSuffix(e string) string {
	if e == "" {
		return ""
	}
	return " (" + e + ")"
}

// FenceAdvance is one authorized movement of HEAD, recorded by the run that
// caused it.
type FenceAdvance struct {
	At     time.Time `json:"at"`
	From   string    `json:"from"`
	To     string    `json:"to"`
	Reason string    `json:"reason"`
}

// Fence is the run's claim on the exact repository position it is working from.
//
// HEAD legitimately moves during a run — the run itself commits — so a fence
// that simply forbade movement would be useless. What it forbids is
// *unrecorded* movement: the run declares its own advances, and anything else
// is an external change.
type Fence struct {
	Worktree string `json:"worktree"`
	LockDir  string `json:"lockDir"`
	RunID    string `json:"runId"`
	PID      int    `json:"pid"`

	Branch string `json:"branch"`
	Head   string `json:"head"`

	TakenAt  time.Time      `json:"takenAt"`
	Advances []FenceAdvance `json:"advances,omitempty"`
}

// TakeFence records the position a run starts from.
func TakeFence(state RepoState, lockDir string, owner Owner) *Fence {
	return &Fence{
		Worktree: state.Root,
		LockDir:  lockDir,
		RunID:    owner.RunID,
		PID:      owner.PID,
		Branch:   state.Branch,
		Head:     state.Head,
		TakenAt:  time.Now().UTC(),
	}
}

// Check re-verifies the fence. It is called before every material mutation
// stage, and its answer is obeyed: a violation stops the stage.
//
// Ownership is verified first. If the lock record no longer names this run,
// then branch and HEAD are somebody else's business and comparing them would
// report the wrong violation.
func (f *Fence) Check() FenceResult {
	now := time.Now().UTC()

	owner, found, err := ReadOwner(f.LockDir)
	switch {
	case err != nil:
		return FenceResult{
			Status: FenceOwnerLost, CheckedAt: now,
			Expected: f.RunID, Observed: "unreadable", Evidence: err.Error(),
		}
	case !found:
		return FenceResult{
			Status: FenceOwnerLost, CheckedAt: now,
			Expected: f.RunID, Observed: "no owner recorded",
			Evidence: "the writer lock record was removed while this run held it",
		}
	case owner.RunID != f.RunID || owner.PID != f.PID:
		return FenceResult{
			Status: FenceOwnerLost, CheckedAt: now,
			Expected: fmt.Sprintf("%s (pid %d)", f.RunID, f.PID),
			Observed: fmt.Sprintf("%s (pid %d)", owner.RunID, owner.PID),
			Evidence: "another session took ownership of this worktree",
		}
	}

	state, err := ProbeRepo(f.Worktree)
	if err != nil {
		return FenceResult{
			Status: FenceUnreadable, CheckedAt: now,
			Expected: f.Worktree, Observed: "unreadable", Evidence: err.Error(),
		}
	}
	if state.Branch != f.Branch {
		observed := state.Branch
		if state.Detached {
			observed = "detached HEAD at " + shortSHA(state.Head)
		}
		return FenceResult{
			Status: FenceBranchChanged, CheckedAt: now,
			Expected: f.Branch, Observed: observed,
			Evidence: "the worktree was moved to a different ref outside this run",
		}
	}
	if state.Head != f.Head {
		return FenceResult{
			Status: FenceHeadMoved, CheckedAt: now,
			Expected: shortSHA(f.Head), Observed: shortSHA(state.Head),
			Evidence: "HEAD moved without an authorized advance being recorded by this run",
		}
	}
	return FenceResult{
		Status: FenceIntact, CheckedAt: now,
		Expected: fmt.Sprintf("%s@%s", f.Branch, shortSHA(f.Head)), Observed: "unchanged",
	}
}

// RecordAuthorisedAdvance moves the fence to the repository's current HEAD
// after the run itself committed.
//
// It re-verifies first, so an advance can never launder an external change into
// an authorized one: if the branch moved or ownership was lost, the violation is
// returned and the fence is left exactly where it was.
func (f *Fence) RecordAuthorisedAdvance(reason string) (FenceResult, error) {
	if reason == "" {
		return FenceResult{}, fmt.Errorf("unattended: an authorized advance must state its reason")
	}

	owner, found, err := ReadOwner(f.LockDir)
	if err != nil || !found || owner.RunID != f.RunID || owner.PID != f.PID {
		res := f.Check()
		return res, res.Error()
	}
	state, err := ProbeRepo(f.Worktree)
	if err != nil {
		res := FenceResult{
			Status: FenceUnreadable, CheckedAt: time.Now().UTC(),
			Expected: f.Worktree, Observed: "unreadable", Evidence: err.Error(),
		}
		return res, res.Error()
	}
	if state.Branch != f.Branch {
		res := FenceResult{
			Status: FenceBranchChanged, CheckedAt: time.Now().UTC(),
			Expected: f.Branch, Observed: state.Branch,
			Evidence: "an advance was claimed on a branch this run does not own",
		}
		return res, res.Error()
	}

	if state.Head != f.Head {
		f.Advances = append(f.Advances, FenceAdvance{
			At: time.Now().UTC(), From: f.Head, To: state.Head, Reason: reason,
		})
		f.Head = state.Head
	}
	return FenceResult{
		Status: FenceIntact, CheckedAt: time.Now().UTC(),
		Expected: fmt.Sprintf("%s@%s", f.Branch, shortSHA(f.Head)), Observed: "advanced by this run",
	}, nil
}
