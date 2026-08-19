package handoff

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DeliveryState is the canonical vocabulary the portal renders.
//
// It is derived, never stored. Every value below is computed from an authority
// that already exists — the run's heartbeat, the run's completion event, and
// the delivery projection published into the project's repository — so there is
// no state field anywhere that can drift out of agreement with what actually
// happened.
type DeliveryState string

// The canonical states.
const (
	// StateNotStarted — no delivery record. The normal state of a project.
	StateNotStarted DeliveryState = "not-started"
	// StatePlanning — delivery is admitted and the plan does not exist yet.
	StatePlanning DeliveryState = "planning"
	// StateQueued — a validated plan exists and no run is executing it.
	StateQueued DeliveryState = "queued"
	// StateRunning — a run is executing and is not waiting on a person.
	StateRunning DeliveryState = "running"
	// StateBlocked — progress needs something this system may not do itself.
	StateBlocked DeliveryState = "blocked"
	// StateCompleted — the evidence gate is met. Nothing else produces it.
	StateCompleted DeliveryState = "completed"
	// StateFailed — the run stopped on something it could not work around.
	StateFailed DeliveryState = "failed"
)

// Status is the whole answer to "where is this project's delivery".
type Status struct {
	ProjectID string        `json:"projectId"`
	State     DeliveryState `json:"state"`
	// Detail is the one sentence a person reads first.
	Detail string `json:"detail"`

	// RunID is the run this status came from, when there is one.
	RunID string `json:"runId,omitempty"`
	// Live is true when a run currently holds the delivery's worktree.
	Live bool `json:"live"`

	// Packages counts the plan, so the portal can show progress without
	// inventing a percentage of its own.
	PackagesTotal    int `json:"packagesTotal"`
	PackagesComplete int `json:"packagesComplete"`

	// Boundaries are the human actions delivery already knows it cannot take.
	Boundaries []string `json:"boundaries,omitempty"`

	// Evidence is the completion assessment, always present so the portal can
	// show WHY a project is not yet complete.
	Evidence Evidence `json:"evidence"`

	ObservedAt time.Time `json:"observedAt"`
}

// Evidence is the evidence-based completion assessment.
//
// Completion in this system is never "the tasks are ticked". A task is a claim;
// this is the check on it. The distinction is load-bearing: an agent can close
// its own bead, and if closing beads were enough then delivery would be
// complete exactly when the thing doing the work said so.
type Evidence struct {
	// Met is the verdict. True only when every clause below holds.
	Met bool `json:"met"`

	RequiredPackages    []string `json:"requiredPackages"`
	CompletePackages    []string `json:"completePackages"`
	OutstandingPackages []string `json:"outstandingPackages,omitempty"`

	// GateNotMet names packages that reached the forge but whose completion
	// gate — required CI on the exact head, independent assurance, governed
	// merge — did not pass. These are the dangerous ones: merged and unproven.
	GateNotMet []string `json:"gateNotMet,omitempty"`

	AcceptanceMet         []string `json:"acceptanceMet"`
	AcceptanceOutstanding []string `json:"acceptanceOutstanding,omitempty"`

	// AwaitingHuman names the criteria only a person may accept and nobody has
	// yet. They are held apart from AcceptanceOutstanding on purpose: one is
	// work delivery has not finished, the other is an answer delivery is not
	// entitled to give, and reporting the second as the first turns a boundary
	// into a failure.
	AwaitingHuman []string `json:"awaitingHuman,omitempty"`

	// BlockingTasks are tasks the projection reports as blocked.
	BlockingTasks []string `json:"blockingTasks,omitempty"`

	// MergedMainSha is the accepted commit on the authoritative branch. Empty
	// means nothing this delivery produced has been accepted there.
	MergedMainSha string `json:"mergedMainSha,omitempty"`

	// Reasons says, in order, what is still missing. Empty exactly when Met.
	Reasons []string `json:"reasons,omitempty"`
}

// projection is the slice of the published PROJECT-STATE.yml this assessment
// reads. It is deliberately a subset: the document is the dashboard's contract
// and this engine must not become a second full reader of it.
type projection struct {
	SchemaVersion int `yaml:"schemaVersion"`
	Project       struct {
		ProjectID             string `yaml:"projectId"`
		LatestAcceptedMainSha string `yaml:"latestAcceptedMainSha"`
	} `yaml:"project"`
	ActiveTasks []struct {
		TaskID               string `yaml:"taskId"`
		Status               string `yaml:"status"`
		CompletionGateStatus string `yaml:"completionGateStatus"`
	} `yaml:"activeTasks"`
}

// ErrProjectionUnreadable is returned when a projection exists but cannot be
// parsed. It is never treated as "no work done": a document that cannot be read
// is unknown, and unknown is not the same as empty.
var ErrProjectionUnreadable = errors.New("handoff: delivery projection is unreadable")

// readProjection loads the published projection, if there is one.
func readProjection(path string) (projection, bool, error) {
	var p projection
	data, err := os.ReadFile(path) //nolint:gosec // a path this process composed
	if os.IsNotExist(err) {
		return p, false, nil
	}
	if err != nil {
		return p, false, fmt.Errorf("reading delivery projection %q: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &p); err != nil {
		return p, false, fmt.Errorf("%w: %s: %w", ErrProjectionUnreadable, path, err)
	}
	if p.SchemaVersion != 1 {
		return p, false, fmt.Errorf("%w: %s: schemaVersion %d, this engine reads 1",
			ErrProjectionUnreadable, path, p.SchemaVersion)
	}
	return p, true, nil
}

// acceptedStatuses are the task statuses that can contribute to completion.
//
// `merged` is included because it is where this engine's delivery ends, but on
// its own it is never sufficient — the gate check below still has to pass. That
// pairing is the whole point: merged says a PR landed, the gate says it was
// earned.
var acceptedStatuses = map[string]bool{
	"merged": true, "verified": true, "complete": true,
}

// Assess computes the completion evidence for a plan against a projection.
//
// The signature takes the projection path rather than a parsed document so that
// a missing projection — the normal state before anything has run — is handled
// here rather than by every caller.
func Assess(plan DeliveryPlan, in Intent, projectionPath string, accepted []Acceptance) (Evidence, error) {
	answered := map[string]bool{}
	for _, a := range accepted {
		answered[a.CriterionID] = true
	}
	ev := Evidence{}
	for _, wp := range plan.Packages {
		ev.RequiredPackages = append(ev.RequiredPackages, wp.ID)
	}
	sort.Strings(ev.RequiredPackages)

	proj, found, err := readProjection(projectionPath)
	if err != nil {
		return ev, err
	}
	if !found {
		ev.OutstandingPackages = ev.RequiredPackages
		for _, c := range in.Acceptance {
			switch {
			case c.IsHuman() && answered[c.ID]:
				ev.AcceptanceMet = append(ev.AcceptanceMet, c.ID)
			case c.IsHuman():
				ev.AwaitingHuman = append(ev.AwaitingHuman, c.ID)
			default:
				ev.AcceptanceOutstanding = append(ev.AcceptanceOutstanding, c.ID)
			}
		}
		sort.Strings(ev.AcceptanceMet)
		sort.Strings(ev.AcceptanceOutstanding)
		sort.Strings(ev.AwaitingHuman)
		ev.Reasons = []string{"no delivery projection has been published yet"}
		return ev, nil
	}
	if proj.Project.ProjectID != "" && proj.Project.ProjectID != in.ProjectID {
		return ev, fmt.Errorf("%w: %s projects project %q, not %q",
			ErrProjectionUnreadable, projectionPath, proj.Project.ProjectID, in.ProjectID)
	}

	type observed struct{ status, gate string }
	byTask := map[string]observed{}
	for _, t := range proj.ActiveTasks {
		byTask[t.TaskID] = observed{status: t.Status, gate: t.CompletionGateStatus}
	}

	complete := map[string]bool{}
	for _, wp := range plan.Packages {
		o, seen := byTask[wp.ID]
		switch {
		case !seen:
			ev.OutstandingPackages = append(ev.OutstandingPackages, wp.ID)
		case o.status == "blocked":
			ev.BlockingTasks = append(ev.BlockingTasks, wp.ID)
			ev.OutstandingPackages = append(ev.OutstandingPackages, wp.ID)
		case !acceptedStatuses[o.status]:
			ev.OutstandingPackages = append(ev.OutstandingPackages, wp.ID)
		case o.gate != "met":
			// Reached the forge without earning it. Named separately because it
			// is the failure that looks most like success.
			ev.GateNotMet = append(ev.GateNotMet, wp.ID)
			ev.OutstandingPackages = append(ev.OutstandingPackages, wp.ID)
		default:
			complete[wp.ID] = true
			ev.CompletePackages = append(ev.CompletePackages, wp.ID)
		}
	}
	sort.Strings(ev.CompletePackages)
	sort.Strings(ev.OutstandingPackages)
	sort.Strings(ev.GateNotMet)
	sort.Strings(ev.BlockingTasks)

	// A criterion is met when every package that claimed it is complete. One
	// package out of three finishing does not make the criterion true.
	//
	// A criterion reserved to a person is answered by a person or not at all.
	// No package may claim one — the plan validator refuses that — so there is
	// nothing here to count, and counting the packages that merged around it
	// would be exactly the self-approval this separation exists to prevent.
	for _, c := range in.Acceptance {
		if c.IsHuman() {
			if answered[c.ID] {
				ev.AcceptanceMet = append(ev.AcceptanceMet, c.ID)
			} else {
				ev.AwaitingHuman = append(ev.AwaitingHuman, c.ID)
			}
			continue
		}
		claimed := 0
		done := 0
		for _, wp := range plan.Packages {
			for _, s := range wp.Satisfies {
				if s != c.ID {
					continue
				}
				claimed++
				if complete[wp.ID] {
					done++
				}
			}
		}
		if claimed > 0 && claimed == done {
			ev.AcceptanceMet = append(ev.AcceptanceMet, c.ID)
		} else {
			ev.AcceptanceOutstanding = append(ev.AcceptanceOutstanding, c.ID)
		}
	}
	sort.Strings(ev.AcceptanceMet)
	sort.Strings(ev.AcceptanceOutstanding)
	sort.Strings(ev.AwaitingHuman)

	ev.MergedMainSha = proj.Project.LatestAcceptedMainSha

	if n := len(ev.OutstandingPackages); n > 0 {
		ev.Reasons = append(ev.Reasons,
			fmt.Sprintf("%d of %d work packages are not complete: %s",
				n, len(ev.RequiredPackages), strings.Join(ev.OutstandingPackages, ", ")))
	}
	if n := len(ev.GateNotMet); n > 0 {
		ev.Reasons = append(ev.Reasons,
			fmt.Sprintf("%d package(s) reached the forge without a met completion gate: %s",
				n, strings.Join(ev.GateNotMet, ", ")))
	}
	if n := len(ev.AcceptanceOutstanding); n > 0 {
		ev.Reasons = append(ev.Reasons,
			"acceptance criteria not met: "+strings.Join(ev.AcceptanceOutstanding, ", "))
	}
	if n := len(ev.AwaitingHuman); n > 0 {
		ev.Reasons = append(ev.Reasons,
			"awaiting human acceptance: "+strings.Join(ev.AwaitingHuman, ", "))
	}
	if n := len(ev.BlockingTasks); n > 0 {
		ev.Reasons = append(ev.Reasons, "blocking work remains: "+strings.Join(ev.BlockingTasks, ", "))
	}
	// Only demanded when merge authority was granted. A delivery that was never
	// allowed to merge has a named human boundary instead, and failing it for
	// not doing what it was forbidden to do would be incoherent.
	if in.Policy.NeedMerge && strings.TrimSpace(ev.MergedMainSha) == "" {
		ev.Reasons = append(ev.Reasons, "no accepted commit is recorded on the authoritative branch")
	}

	ev.Met = len(ev.Reasons) == 0
	return ev, nil
}

// RunObservation is what the caller learned about the run from the authorities
// that own it: the worktree lock and the run's own published records.
//
// It is a parameter rather than something Status computes, because acquiring a
// lock to test liveness is a side effect and this function must have none.
type RunObservation struct {
	// Live is true when the delivery's worktree is currently held.
	Live bool
	// RunID names the run the observation came from.
	RunID string
	// Stage is what a live run says it is doing.
	Stage string
	// Boundaries are human actions the run reported.
	Boundaries []string
	// Finished is true when a completion event was published for this run.
	Finished bool
	// Outcome is the completion event's outcome, when Finished.
	Outcome string
}

// The completion outcomes this package interprets. They mirror the run layer's
// vocabulary; they are duplicated as strings rather than imported so that the
// state derivation does not depend on the run layer's package.
const (
	outcomeCompleted    = "completed"
	outcomeBlockedHuman = "blocked-human"
	outcomeAwaitingAuth = "awaiting-auth"
	outcomeFailed       = "failed"
)

// Derive computes the canonical delivery state.
//
// The ordering of the clauses is the design. A live run is reported as live
// even when a previous run's completion event is still on disk, because the
// question "what is happening now" must never be answered with a fact about a
// finished run. And a completed outcome is only ever reported as completed when
// the evidence agrees — a run that believes it finished, over a projection that
// shows unproven work, is precisely the case where the run must not be trusted.
func Derive(
	projectID string,
	admitted bool,
	plan DeliveryPlan,
	planFound bool,
	obs RunObservation,
	ev Evidence,
	now time.Time,
) Status {
	st := Status{
		ProjectID:  projectID,
		RunID:      obs.RunID,
		Live:       obs.Live,
		Boundaries: obs.Boundaries,
		Evidence:   ev,
		ObservedAt: now.UTC(),
	}
	if planFound {
		st.PackagesTotal = len(plan.Packages)
		st.PackagesComplete = len(ev.CompletePackages)
	}

	switch {
	case !admitted:
		st.State = StateNotStarted
		st.Detail = "No managed delivery has been started for this project."
		return st

	case ev.Met:
		// Evidence first. It is the only clause that may report completion, and
		// it does so regardless of whether a run is still tidying up.
		st.State = StateCompleted
		st.Detail = "Delivery is complete: every work package is merged with a met completion gate and every acceptance criterion is satisfied."
		return st

	case obs.Live && len(obs.Boundaries) > 0:
		st.State = StateBlocked
		st.Detail = "Delivery is running but waiting on a person: " + strings.Join(obs.Boundaries, "; ")
		return st

	case obs.Live:
		st.State = StateRunning
		st.Detail = describeRunning(obs, st)
		return st

	case obs.Finished:
		switch obs.Outcome {
		case outcomeFailed:
			st.State = StateFailed
			st.Detail = "The delivery run stopped on something it could not work around."
			return st
		case outcomeBlockedHuman, outcomeAwaitingAuth:
			st.State = StateBlocked
			st.Detail = "Delivery has done everything it can without a person: " + joinOr(obs.Boundaries, "see the run's evidence for what is needed")
			return st
		case outcomeCompleted:
			// The run finished cleanly and the evidence did not agree. This is
			// reported as blocked rather than completed, and the reasons say
			// exactly which clause failed.
			st.State = StateBlocked
			st.Detail = "The run finished, but delivery is not yet accepted: " + joinOr(ev.Reasons, "the evidence gate is not met")
			return st
		}
	}

	if !planFound {
		st.State = StatePlanning
		st.Detail = "Delivery has started and is planning the work."
		return st
	}
	st.State = StateQueued
	st.Detail = fmt.Sprintf("A plan of %d work package(s) is ready and no run is currently executing it.", len(plan.Packages))
	return st
}

func describeRunning(obs RunObservation, st Status) string {
	stage := obs.Stage
	if strings.TrimSpace(stage) == "" {
		stage = "working"
	}
	if st.PackagesTotal > 0 {
		return fmt.Sprintf("Delivery is running (%s); %d of %d work packages complete.",
			stage, st.PackagesComplete, st.PackagesTotal)
	}
	return fmt.Sprintf("Delivery is running (%s).", stage)
}

func joinOr(items []string, fallback string) string {
	if len(items) == 0 {
		return fallback
	}
	return strings.Join(items, "; ")
}
