package handoff

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// THE GAP THIS FILE CLOSES.
//
// A plan is never rewritten, because the merged work of a delivery was
// measured against it — that rule is right, and SavePlan keeps enforcing it.
// But the rule's whole rationale is the merged work, and a delivery can hold a
// plan that no work was ever executed against: the planner wrote it, the run
// failed before dispatch, and nothing anywhere was measured by it. For that
// delivery the refusal protected evidence that does not exist, and the two
// governed repairs — invalidate and remediate — were both out of reach,
// because they repair criteria that were REPORTED met and none ever was.
// A mis-planned, never-executed delivery had no governed way forward at all.
//
// Discovered on scorm-course-studio-production-line on 2026-08-25: the
// planner's first plan named a test script the repository does not have and
// wired packages into files that do not exist, the run failed at city-up
// before any package started, and the corrected plan was refused with a
// message asserting merged work on a delivery that had none.

// SupersededPlanName is where superseded plan n is archived, beside plan.json.
func SupersededPlanName(n int) string {
	return fmt.Sprintf("plan-superseded-%03d.json", n)
}

// SupersedeUnexecutedPlan replaces a delivery's plan with `next`, but ONLY
// while nothing was ever executed against the one being replaced, and never
// destructively: the superseded plan is archived append-only beside the new
// one, because a plan the delivery briefly held is part of its history even
// when no work ran against it.
//
// The guard is evidence, not trust: a live run holds the worktree and may be
// mid-dispatch, so it refuses; any complete package means work was measured
// against the standing plan, so it refuses; any gate-not-met package means
// something MERGED against it — the dangerous kind — so it refuses; and a
// delivery whose state says the run finished at all (blocked or completed)
// executed its packages, so it refuses. What remains is a delivery that is
// failed or still planning with zero package evidence: the only situation in
// which the standing plan measured nothing.
//
// `next` must already have passed the same validation an agent's plan would;
// this function orders the swap, it does not judge the plan.
func SupersedeUnexecutedPlan(deliveryRoot string, next DeliveryPlan, st Status) error {
	if st.ProjectID != next.ProjectID {
		return fmt.Errorf("%w: the status observed %q but the plan is for %q",
			ErrRecordConflict, st.ProjectID, next.ProjectID)
	}
	if st.Live {
		return fmt.Errorf("%w: a run currently holds %q — a live run may be mid-dispatch against the standing "+
			"plan, and swapping it now would leave the run reconciling two", ErrRecordConflict, next.ProjectID)
	}
	if st.State != StateFailed && st.State != StatePlanning {
		return fmt.Errorf("%w: delivery for %q is %q — a delivery whose run finished executed its packages "+
			"against the standing plan, and that plan is never replaced. Authorize corrective work as a "+
			"remediation instead", ErrRecordConflict, next.ProjectID, st.State)
	}
	if n := len(st.Evidence.CompletePackages); n > 0 {
		return fmt.Errorf("%w: %d package(s) of %q completed against the standing plan — it measured real work "+
			"and is never replaced. Authorize corrective work as a remediation instead",
			ErrRecordConflict, n, next.ProjectID)
	}
	if n := len(st.Evidence.GateNotMet); n > 0 {
		return fmt.Errorf("%w: %d package(s) of %q merged without their completion gate — work was measured "+
			"against the standing plan and the plan is never replaced", ErrRecordConflict, n, next.ProjectID)
	}

	path := PlanPath(deliveryRoot, next.ProjectID)
	existing, err := os.ReadFile(path) //nolint:gosec // a path this process composed
	if os.IsNotExist(err) {
		// Nothing to supersede: install normally.
		return SavePlan(deliveryRoot, next)
	}
	if err != nil {
		return fmt.Errorf("reading the standing plan %q: %w", path, err)
	}

	// Archive the standing plan append-only, at the first free slot.
	dir := filepath.Dir(path)
	var archive string
	for n := 1; ; n++ {
		candidate := filepath.Join(dir, SupersededPlanName(n))
		if _, serr := os.Stat(candidate); os.IsNotExist(serr) {
			archive = candidate
			break
		} else if serr != nil {
			return fmt.Errorf("checking for an archived plan %q: %w", candidate, serr)
		}
	}
	if err := os.WriteFile(archive, existing, 0o644); err != nil { //nolint:gosec // read by the portal
		return fmt.Errorf("archiving the superseded plan to %q: %w", archive, err)
	}

	// Install the replacement atomically. Not through SavePlan, whose refusal
	// of a differing standing plan is exactly the rule this function is the
	// one governed exception to — the archive above is what keeps the history.
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the superseding plan: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil { //nolint:gosec // read by the portal
		return fmt.Errorf("writing the superseding plan: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("installing the superseding plan %q: %w", path, err)
	}
	return nil
}
