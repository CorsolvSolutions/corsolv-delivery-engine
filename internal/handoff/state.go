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

	// Criteria counts what the project agreed to deliver, rather than the work
	// delivery did to get there.
	//
	// WHY BOTH COUNTERS EXIST. They are different questions, and until a
	// criterion could be disproved they always agreed, so one of them looked
	// redundant. It is not. A criterion withdrawn on later evidence leaves every
	// package that claimed it exactly as it was — merged, gate met, genuinely
	// finished — because that is what happened and reopening finished work would
	// be rewriting it. So the package counter reads 3 of 3 while the project is
	// not delivered, and reporting that as progress would say a project with a
	// disproved deliverable is 100% done.
	//
	// The criterion counter is the one that answers "how much of what was asked
	// for is delivered", and it is the one that drops.
	CriteriaTotal int `json:"criteriaTotal"`
	CriteriaMet   int `json:"criteriaMet"`

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

	// Invalidated are the criteria whose earlier met result has been withdrawn
	// on later evidence, and which no authorized remediation has yet repaired.
	//
	// They are carried in full rather than as a list of ids because this is the
	// one reason a criterion can be outstanding that the packages cannot
	// explain. Every other entry in AcceptanceOutstanding is answered by looking
	// at the work; this one is answered only by the finding, so the finding
	// travels with the assessment to whoever reads it.
	Invalidated []InvalidatedCriterion `json:"invalidated,omitempty"`

	// BlockingTasks are tasks the projection reports as blocked.
	BlockingTasks []string `json:"blockingTasks,omitempty"`

	// MergedMainSha is the accepted commit on the authoritative branch. Empty
	// means nothing this delivery produced has been accepted there.
	MergedMainSha string `json:"mergedMainSha,omitempty"`

	// Reasons says, in order, what is still missing. Empty exactly when Met.
	Reasons []string `json:"reasons,omitempty"`
}

// InvalidatedCriterion is a standing finding as the assessment reports it: what
// was withdrawn, who withdrew it, why, against what, and what has been
// authorized to repair it.
type InvalidatedCriterion struct {
	CriterionID string `json:"criterionId"`
	// Invalidation is the finding's sequence in the record, which is what a
	// remediation names when it answers this.
	Invalidation int       `json:"invalidation"`
	By           string    `json:"by"`
	Reason       string    `json:"reason"`
	Evidence     string    `json:"evidence"`
	At           time.Time `json:"at"`
	// PreviousState is what the assessment said before this finding withdrew it.
	PreviousState string `json:"previousState"`
	// RemedialPackages are the packages an authorized remediation added to
	// repair this criterion. Empty means no remediation has been authorized
	// yet — the criterion is disproved and nothing is being done about it, which
	// is a materially different state from disproved and under repair.
	RemedialPackages []string `json:"remedialPackages,omitempty"`
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
//
// `invalidations` are the record's findings that a criterion reported met was
// not actually satisfied. They are a parameter for the same reason `accepted`
// is: both are durable record facts, and this function's whole discipline is
// that it derives a verdict from facts it is handed rather than reading a
// stored one.
func Assess(plan DeliveryPlan, in Intent, projectionPath string, accepted []Acceptance, invalidations []Invalidation) (Evidence, error) {
	answered := map[string]bool{}
	for _, a := range accepted {
		answered[a.CriterionID] = true
	}
	// The latest finding against each criterion. Earlier ones are history: a
	// criterion can be disproved, repaired and disproved again, and only the
	// last finding can still be standing.
	latest := map[string]Invalidation{}
	for _, inv := range invalidations {
		latest[inv.CriterionID] = inv
	}
	ev := Evidence{}
	all := plan.AllPackages()
	for _, wp := range all {
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
	for _, wp := range all {
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
		for _, wp := range all {
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
		delivered := claimed > 0 && claimed == done

		// AND THE FINDING, IF THERE IS ONE.
		//
		// The arithmetic above asks whether the plan was carried out. That is
		// exactly the question a disproved criterion has already answered yes
		// to and been wrong about — the pilot's packages all merged with met
		// gates over a product missing a required behavior. So a standing
		// finding outranks the count until work authorized specifically to
		// answer THAT finding has itself completed.
		//
		// Authorized specifically for it, not merely present: a remediation
		// answering the first disproof of a criterion must not silently clear a
		// second one raised after it, which is why the finding's sequence is
		// what a remediation names.
		if inv, found := latest[c.ID]; found {
			repaired := repairedBy(plan, c.ID, inv.Seq)
			if len(repaired) == 0 || !delivered {
				delivered = false
				ev.Invalidated = append(ev.Invalidated, InvalidatedCriterion{
					CriterionID:      c.ID,
					Invalidation:     inv.Seq,
					By:               inv.By,
					Reason:           inv.Reason,
					Evidence:         inv.Evidence,
					At:               inv.At,
					PreviousState:    inv.PreviousState,
					RemedialPackages: repaired,
				})
			}
		}

		if delivered {
			ev.AcceptanceMet = append(ev.AcceptanceMet, c.ID)
		} else {
			ev.AcceptanceOutstanding = append(ev.AcceptanceOutstanding, c.ID)
		}
	}
	sort.Strings(ev.AcceptanceMet)
	sort.Strings(ev.AcceptanceOutstanding)
	sort.Strings(ev.AwaitingHuman)
	sort.Slice(ev.Invalidated, func(i, j int) bool {
		return ev.Invalidated[i].CriterionID < ev.Invalidated[j].CriterionID
	})

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
	// Named separately, and in the criterion's own terms. A disproved criterion
	// appears in the line above as an id among ids, which reads as work that has
	// not finished — and this one is the opposite: work that finished, was
	// believed, and was wrong. A reader who cannot tell those apart from the
	// status cannot act on either.
	for _, inv := range ev.Invalidated {
		reason := fmt.Sprintf("%s was %s and is no longer: %s (evidence: %s; invalidation %d recorded by %s at %s)",
			inv.CriterionID, inv.PreviousState, inv.Reason, inv.Evidence,
			inv.Invalidation, inv.By, inv.At.UTC().Format(time.RFC3339))
		switch {
		case len(inv.RemedialPackages) == 0:
			reason += " — no remediation has been authorized for it"
		default:
			reason += " — remediation authorized: " + strings.Join(inv.RemedialPackages, ", ")
		}
		ev.Reasons = append(ev.Reasons, reason)
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

// repairedBy names the packages an authorized remediation added to repair one
// specific finding against one criterion.
//
// The finding's sequence is matched, not merely the criterion. A remediation
// that answered an earlier disproof of the same criterion is history: letting
// it clear a later finding would mean a criterion could be disproved a second
// time and go on reporting met, on the strength of work that predates the
// evidence against it.
// SUPERSEDED WORK REPAIRS NOTHING, AND SAYING OTHERWISE SCORES A CRITERION MET
// ON WORK THAT PREDATES THE EVIDENCE AGAINST IT.
//
// This is not hypothetical. Counting a withdrawn package as a repair is enough
// on its own: the caller reads "something is repairing this finding", stops
// applying the finding, and falls back to the arithmetic over the packages that
// CLAIMED the criterion — which are the original plan's, which merged, with
// their gates met, before the audit that disproved them. scorm-course-studio
// produced exactly that the first time this was wrong: superseding the two
// unexecutable remedial packages turned ac-9, ac-10, ac-11 and ac-12 from unmet
// to met without one line of corrective work being done.
//
// So a superseded package is removed here as it is everywhere else. A finding
// whose only authorized repair has been withdrawn has NO repair, which is what
// keeps it standing until someone authorizes one that can actually run.
func repairedBy(plan DeliveryPlan, criterionID string, seq int) []string {
	gone := plan.Superseded()
	var out []string
	for _, rm := range plan.Remediations {
		if !rm.RepairsInvalidation(criterionID, seq) {
			continue
		}
		for _, id := range rm.PackagesFor(criterionID) {
			if gone[id] {
				continue
			}
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
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
		st.PackagesTotal = len(plan.AllPackages())
		st.PackagesComplete = len(ev.CompletePackages)
	}
	// Criteria are counted from the assessment rather than from the plan,
	// because the assessment is the only thing that knows about a criterion a
	// person answers — no package claims one, so a plan cannot count it.
	st.CriteriaMet = len(ev.AcceptanceMet)
	st.CriteriaTotal = len(ev.AcceptanceMet) + len(ev.AcceptanceOutstanding) + len(ev.AwaitingHuman)

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
