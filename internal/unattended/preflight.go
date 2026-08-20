package unattended

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Report is the single consolidated answer to "may this run start".
type Report struct {
	RunID     string    `json:"runId"`
	ProjectID string    `json:"projectId"`
	Session   string    `json:"session"`
	StartedAt time.Time `json:"startedAt"`
	Duration  string    `json:"duration"`

	Readiness Readiness `json:"readiness"`
	Checks    Checks    `json:"checks"`

	Repo   RepoState    `json:"repo"`
	GitHub *GitHubProbe `json:"github,omitempty"`
}

// PermitsUnattendedRun reports whether the run may begin.
func (r *Report) PermitsUnattendedRun() bool { return r.Readiness.PermitsUnattendedRun() }

// Check returns a named check from the report.
func (r *Report) Check(id string) (Check, bool) {
	for _, c := range r.Checks {
		if c.ID == id {
			return c, true
		}
	}
	return Check{}, false
}

// Boundaries maps each boundary check's ID to the human action that clears it.
// The queue holds tasks that require these, rather than attempting work known
// in advance to be un-completable.
func (r *Report) Boundaries() map[string]string {
	out := map[string]string{}
	for _, c := range r.Checks.Boundaries() {
		out[c.ID] = c.Boundary
	}
	return out
}

// Blocking returns the checks that make the run unsafe to start.
func (r *Report) Blocking() Checks { return r.Checks.Failures() }

// String renders the report the way a person reads it after the fact.
func (r *Report) String() string {
	var b strings.Builder
	b.WriteString("============================================================\n")
	fmt.Fprintf(&b, "UNATTENDED PREFLIGHT — %s\n", r.ProjectID)
	b.WriteString("============================================================\n")
	fmt.Fprintf(&b, "run:      %s\n", orNone(r.RunID))
	fmt.Fprintf(&b, "session:  %s\n", orNone(r.Session))
	fmt.Fprintf(&b, "worktree: %s\n", orNone(r.Repo.Root))
	if r.Repo.Branch != "" {
		fmt.Fprintf(&b, "position: %s@%s\n", r.Repo.Branch, shortSHA(r.Repo.Head))
	}
	b.WriteString(r.Checks.String())
	b.WriteString("\n============================================================\n")
	fmt.Fprintf(&b, "VERDICT: %s (%d checks in %s)\n", r.Readiness, len(r.Checks), r.Duration)

	if bs := r.Checks.Boundaries(); len(bs) > 0 {
		b.WriteString("\nKnown human boundaries — plan around these, do not wait on them:\n")
		for _, c := range bs {
			fmt.Fprintf(&b, "  - %s: %s\n", c.ID, c.Boundary)
		}
	}
	if fs := r.Blocking(); len(fs) > 0 {
		b.WriteString("\nBlocking:\n")
		for _, c := range fs {
			fmt.Fprintf(&b, "  - %s (%s): expected %s, observed %s\n",
				c.ID, c.Outcome, orNone(c.Expected), orNone(c.Observed))
			if c.Remedy != "" {
				fmt.Fprintf(&b, "      remedy: %s\n", c.Remedy)
			}
			if c.Detail != "" {
				fmt.Fprintf(&b, "      detail: %s\n", c.Detail)
			}
		}
	}
	return b.String()
}

// JSON renders the report for durable evidence.
func (r *Report) JSON() ([]byte, error) {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding preflight report: %w", err)
	}
	return append(data, '\n'), nil
}

// ErrNotReady is returned when a run is asked to start from a NOT-READY state.
var ErrNotReady = errors.New("unattended: the preflight verdict does not permit an unattended run")

// Preflight runs every declared check once, in one place, before any work.
//
// The consolidation is the whole point. Each of these questions used to be
// asked implicitly — by doing the thing and seeing whether it worked — spread
// across the length of a run, so a run could spend forty minutes on useful work
// and stop at a credential that had been missing the entire time. Asking all of
// them first costs seconds and converts every one from a mid-run surprise into a
// fact known before the first mutation.
//
// A nil plan is not an empty plan: it produces NOT REACHED for the work-queue
// checks, because a run whose plan could not be read has not been shown to have
// anywhere to go.
func Preflight(ctx context.Context, spec Spec, plan *Plan) *Report {
	started := time.Now().UTC()
	r := &Report{
		ProjectID: spec.ProjectID,
		Session:   spec.Ownership.Session,
		StartedAt: started,
	}

	state, ownershipChecks := AssertOwnership(spec.Ownership)
	r.Repo = state
	r.Checks = append(r.Checks, ownershipChecks...)
	r.Checks = append(r.Checks, competingWriterCheck(state))
	if state.Root != "" {
		r.Checks = append(r.Checks, CheckWorktreeCrossOSDurable(state.Root))
	}

	if spec.GitHub != nil {
		probe := ProbeGitHub(ctx, *spec.GitHub)
		r.GitHub = &probe
		r.Checks = append(r.Checks, GitHubChecks(*spec.GitHub, probe)...)
	}

	r.Checks = append(r.Checks, ToolChecks(ctx, spec.Tools)...)
	r.Checks = append(r.Checks, PathChecks(spec.Paths)...)
	r.Checks = append(r.Checks, EnvChecks(spec.Env)...)
	r.Checks = append(r.Checks, PortChecks(spec.Ports)...)
	r.Checks = append(r.Checks, CommandChecks(ctx, spec.Commands)...)
	r.Checks = append(r.Checks, CredentialChecks(ctx, spec.Credentials)...)
	r.Checks = append(r.Checks, stateChecks(spec)...)
	r.Checks = append(r.Checks, DeclaredBoundaryChecks(spec.Boundaries)...)

	switch plan {
	case nil:
		r.Checks = append(r.Checks,
			notReached("plan.work", CategoryProject, "the run has declared work", "no work plan was supplied to preflight"),
			notReached("plan.fallback", CategoryProject, "useful work exists below the primary band", "no work plan was supplied to preflight"),
		)
	default:
		r.RunID = plan.RunID
		r.Checks = append(r.Checks, PlanChecks(*plan)...)
	}

	r.Readiness = r.Checks.Readiness()
	r.Duration = time.Since(started).Round(time.Millisecond).String()
	return r
}

// competingWriterCheck reports whether another session already owns the
// worktree — without taking the lock.
//
// Preflight observes; it does not claim. Taking the lock here would mean
// preflight and the run competing for the same lock, and a preflight that has
// to release before the run can acquire opens a window for exactly the race it
// is checking for. The run takes the lock once, and holds it for its lifetime.
func competingWriterCheck(state RepoState) Check {
	const (
		id    = "concurrency.writer"
		title = "no competing writer on this worktree"
	)
	if state.Root == "" {
		return notReached(id, CategoryConcurrency, title,
			"the worktree could not be probed, so its lock could not be read")
	}
	owner, recorded, live, err := ProbeOwner(WriterLockDir(state))
	switch {
	case err != nil:
		return fail(id, CategoryConcurrency, title, "a readable lock", err.Error(),
			"inspect the lock directory by hand")
	case live:
		return Check{
			ID: id, Category: CategoryConcurrency, Title: title, Outcome: OutcomeFail,
			Expected: "no live owner",
			Observed: describeOwner(owner, recorded),
			Detail:   "the lock is held right now, so another session is genuinely working in this worktree",
			Remedy:   "wait for the other session to finish; do not break a live lock",
		}
	case recorded:
		// A record with no lock behind it is a previous run that died. Saying so
		// is the whole point: treating the record as authority would make every
		// crashed run unrestartable until a person deleted a file.
		return Check{
			ID: id, Category: CategoryConcurrency, Title: title, Outcome: OutcomePass,
			Observed: "no live owner; a stale record from " + describeOwner(owner, true),
			Detail:   "the previous owner's record outlived its lock, so it will be archived when this run acquires",
		}
	default:
		return pass(id, CategoryConcurrency, title, "no owner recorded and the lock is free")
	}
}

func describeOwner(o Owner, recorded bool) string {
	if !recorded {
		return "an unrecorded holder"
	}
	return fmt.Sprintf("run %q (session %q, pid %d on %s) since %s",
		o.RunID, o.Session, o.PID, o.Host, o.AcquiredAt.UTC().Format(time.RFC3339))
}

// stateChecks verify the run has somewhere durable to keep its own evidence.
func stateChecks(spec Spec) Checks {
	var cs Checks

	if strings.TrimSpace(spec.StateDir) == "" {
		cs = append(cs, fail("state.dir", CategoryProject, "durable run state has somewhere to go",
			"stateDir", "unset",
			"set stateDir — a run whose journal has nowhere to go cannot be resumed or audited"))
		return cs
	}
	// The state directory must not live inside the mutable worktree: a branch
	// switch, a cleanliness check or a checkout would otherwise touch the run's
	// own record of what it was doing.
	if spec.Ownership.Worktree != "" && withinPath(spec.Ownership.Worktree, spec.StateDir) {
		cs = append(cs, fail("state.dir", CategoryProject, "durable run state has somewhere to go",
			"a directory outside the mutable worktree", spec.StateDir,
			"move stateDir outside "+spec.Ownership.Worktree+" so a checkout cannot disturb the run journal"))
		return cs
	}
	if err := probeStateDir(spec.StateDir); err != nil {
		cs = append(cs, fail("state.dir", CategoryProject, "durable run state has somewhere to go",
			"a writable directory", err.Error(),
			"create "+spec.StateDir+" and make it writable"))
		return cs
	}
	cs = append(cs, pass("state.dir", CategoryProject, "durable run state has somewhere to go", spec.StateDir))

	if strings.TrimSpace(spec.PublishPath) == "" {
		cs = append(cs, Check{
			ID: "state.publish", Category: CategoryProject,
			Title: "run progress is published where the dashboard reads it", Outcome: OutcomePass,
			Observed: "this run publishes nothing",
			Detail:   "publishPath is unset, so no delivery projection is written; the report says so rather than implying publication happened",
		})
	} else {
		cs = append(cs, pass("state.publish", CategoryProject,
			"run progress is published where the dashboard reads it", spec.PublishPath))
	}
	return cs
}

func probeStateDir(dir string) error {
	if err := osMkdirAll(dir); err != nil {
		return err
	}
	return probeWritable(dir, true)
}
