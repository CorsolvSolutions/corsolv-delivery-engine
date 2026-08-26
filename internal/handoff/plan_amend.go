package handoff

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// THE GAP THIS FILE CLOSES, WHICH plan_supersede.go COULD NOT.
//
// SupersedeUnexecutedPlan replaces a plan while NOTHING was executed against
// it. But a delivery is a sequence: by the time a later package reveals that
// its authorized paths were drawn too tightly — a journey that must open a
// door in an existing screen, an existing test that pins what the package
// legitimately changes — its siblings have merged, the whole-plan replacement
// correctly refuses, and the two governed repairs still only exist for
// criteria REPORTED met. An unmerged package could not have its scope
// corrected by any governed means at all, and the delivery was wedged with
// finished, gated work it could never publish.
//
// Hit twice on scorm-studio-redesign-2 in one day: the shell package needed
// the existing navigation smoke test, and the runtime package needed the
// legacy preview route through which its storyboard journeys run.
//
// The repair keeps the original rule's whole rationale intact: THE MERGED
// WORK'S RECORD DOES NOT MOVE. An amendment must carry every package that has
// merge evidence byte-identically; only packages with none may change. The
// superseded plan is archived append-only, exactly as a full supersession
// archives it.

// AmendUnmergedPackages replaces a delivery's plan with `next`, refusing
// unless every package with merge evidence — complete, or merged without its
// gate — is byte-identical between the standing plan and the amendment. A
// package no evidence names may change its objective, its authorized paths,
// its gates; it may not disappear, because its beads may already exist and
// work no plan describes is work nobody governs.
//
// A live run still refuses: the amendment lands between runs, when nothing is
// mid-dispatch against the standing plan.
func AmendUnmergedPackages(deliveryRoot string, standing, next DeliveryPlan, st Status) error {
	if st.ProjectID != next.ProjectID {
		return fmt.Errorf("%w: the status observed %q but the amendment is for %q",
			ErrRecordConflict, st.ProjectID, next.ProjectID)
	}
	if st.Live {
		return fmt.Errorf("%w: a run currently holds %q — an amendment lands between runs, not under one",
			ErrRecordConflict, next.ProjectID)
	}

	merged := map[string]bool{}
	for _, id := range st.Evidence.CompletePackages {
		merged[id] = true
	}
	for _, id := range st.Evidence.GateNotMet {
		merged[id] = true
	}

	standingByID := map[string]WorkPackage{}
	for _, wp := range standing.Packages {
		standingByID[wp.ID] = wp
	}
	nextByID := map[string]WorkPackage{}
	for _, wp := range next.Packages {
		nextByID[wp.ID] = wp
	}

	for _, wp := range standing.Packages {
		replacement, present := nextByID[wp.ID]
		if !present {
			return fmt.Errorf("%w: the amendment drops package %q — its beads may already exist, and work no "+
				"plan describes is work nobody governs", ErrRecordConflict, wp.ID)
		}
		if merged[wp.ID] && !packagesIdentical(wp, replacement) {
			return fmt.Errorf("%w: package %q has merged against the standing plan and the amendment changes it — "+
				"the merged work's record does not move; only packages with no merge evidence may change",
				ErrRecordConflict, wp.ID)
		}
	}

	path := PlanPath(deliveryRoot, next.ProjectID)
	existing, err := os.ReadFile(path) //nolint:gosec // a path this process composed
	if err != nil {
		return fmt.Errorf("reading the standing plan %q: %w", path, err)
	}

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
		return fmt.Errorf("archiving the amended-away plan to %q: %w", archive, err)
	}

	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the amended plan: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil { //nolint:gosec // read by the portal
		return fmt.Errorf("writing the amended plan: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("installing the amended plan %q: %w", path, err)
	}
	return nil
}

// packagesIdentical compares two work packages by canonical encoding, so the
// comparison means "the record is unchanged" rather than "the structs happen
// to share pointers".
func packagesIdentical(a, b WorkPackage) bool {
	aj, aerr := json.Marshal(a)
	bj, berr := json.Marshal(b)
	if aerr != nil || berr != nil {
		return false
	}
	return string(aj) == string(bj)
}
