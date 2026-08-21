package projector

import (
	"strings"
	"testing"
)

// THE DEFECT THESE PIN. A deliverable's task rows say its packages merged and
// their completion gates passed — which is exactly what happened, and exactly
// what a later finding disproves. Deriving `met` from those rows alone can
// therefore only ever conclude the thing the evidence proved false, so a
// disproved deliverable would go on reading complete in the document a portal
// renders while the engine's own state said otherwise: one project, two
// answers, and nothing to say which was right.
//
// That is the same shape as the human-acceptance defect this package already
// carries a fix for, and it is answered the same way: the record's finding
// travels as a FACT, and the verdict it supports stays derived here.

// disprovedDeliverable is a deliverable whose claiming package merged with a
// met gate, and against which a finding then stands.
func disprovedState() *State {
	s := NewState("reconciliation-probe")
	s.Tasks["wp-3"] = &Task{TaskID: "wp-3", Status: StatusMerged, CompletionGateStatus: GateMet}
	s.Deliverables = []Deliverable{{
		ID:          "ac-3",
		Statement:   "The report states an inferred type for every column.",
		SatisfiedBy: []string{"wp-3"},
		Invalidated: &DeliverableInvalidation{
			Seq:           1,
			By:            "Jon Pratten",
			Reason:        "no column is ever reported as mixed",
			Evidence:      "https://github.com/CorsolvSolutions/reconciliation-probe/issues/12",
			At:            "2026-08-21T14:00:00Z",
			PreviousState: "met",
		},
	}}
	return s
}

// A disproved deliverable is not met on the strength of the work that was
// disproved, and the document says whose evidence disproved it.
func TestADisprovedDeliverableIsNotMetByTheWorkThatWasDisproved(t *testing.T) {
	s := disprovedState()

	data, err := Render(s)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if s.Deliverables[0].Met {
		t.Fatal("a deliverable is still met over a finding that disproves it — its packages merging is what the finding contradicts")
	}

	out := string(data)
	// WHO, WHY and AGAINST WHAT, beside the verdict. A portal that can show only
	// "not met" cannot tell a reopened deliverable from one that never finished,
	// and those need different actions from a reader.
	for _, want := range []string{
		"met: false",
		"invalidated:",
		`by: "Jon Pratten"`,
		`reason: "no column is ever reported as mixed"`,
		"issues/12",
		`previousState: "met"`,
		"seq: 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the projection must carry %q so a reader can see why it reopened:\n%s", want, out)
		}
	}
}

// Authorized corrective work that has not finished does not make it met either.
// A repair in flight is not a repair.
func TestAuthorizedRepairInFlightDoesNotMeetADisprovedDeliverable(t *testing.T) {
	s := disprovedState()
	s.Tasks["wp-3-fix"] = &Task{TaskID: "wp-3-fix", Status: StatusPlanned, CompletionGateStatus: GateNotMet}
	s.Deliverables[0].RemediatedBy = []string{"wp-3-fix"}

	data, err := Render(s)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if s.Deliverables[0].Met {
		t.Fatal("a deliverable is met while the work repairing it has not run")
	}
	if !strings.Contains(string(data), "wp-3-fix") {
		t.Errorf("the projection does not name the work repairing it:\n%s", data)
	}
}

// A merged repair whose completion gate is NOT met is the failure that looks
// most like success, and it does not repair anything.
func TestAMergedRepairWithoutItsGateDoesNotMeetADisprovedDeliverable(t *testing.T) {
	s := disprovedState()
	s.Tasks["wp-3-fix"] = &Task{TaskID: "wp-3-fix", Status: StatusMerged, CompletionGateStatus: GateNotMet}
	s.Deliverables[0].RemediatedBy = []string{"wp-3-fix"}

	if _, err := Render(s); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if s.Deliverables[0].Met {
		t.Fatal("a repair that reached the forge without earning its gate repaired nothing")
	}
}

// And when the repair merges with its gate met, the deliverable is met again —
// with the finding still on the document, so the sequence stays visible.
func TestACompletedRepairMeetsADisprovedDeliverableAgain(t *testing.T) {
	s := disprovedState()
	s.Tasks["wp-3-fix"] = &Task{TaskID: "wp-3-fix", Status: StatusMerged, CompletionGateStatus: GateMet}
	s.Deliverables[0].RemediatedBy = []string{"wp-3-fix"}

	data, err := Render(s)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !s.Deliverables[0].Met {
		t.Fatal("the repair merged with its gate met and the deliverable is still not met")
	}
	out := string(data)
	if !strings.Contains(out, "met: true") {
		t.Errorf("the projection does not report the repaired deliverable met:\n%s", out)
	}
	// HISTORY SURVIVES THE REPAIR. A document that dropped the finding the moment
	// it was answered would leave a reader unable to tell a deliverable that was
	// always fine from one that was wrong and fixed.
	for _, want := range []string{"invalidated:", `by: "Jon Pratten"`, "remediatedBy:", "wp-3-fix"} {
		if !strings.Contains(out, want) {
			t.Errorf("the projection dropped %q once the finding was answered:\n%s", want, out)
		}
	}
}

// A deliverable nobody disputed renders exactly as it always did — no new keys,
// and the same verdict.
func TestADeliverableWithNoFindingIsUnchanged(t *testing.T) {
	s := NewState("p")
	s.Tasks["wp-1"] = &Task{TaskID: "wp-1", Status: StatusMerged, CompletionGateStatus: GateMet}
	s.Deliverables = []Deliverable{{ID: "ac-1", Statement: "Delivered.", SatisfiedBy: []string{"wp-1"}}}

	data, err := Render(s)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !s.Deliverables[0].Met {
		t.Fatal("a deliverable whose package merged with a met gate stopped being met")
	}
	for _, absent := range []string{"invalidated:", "remediatedBy:"} {
		if strings.Contains(string(data), absent) {
			t.Errorf("a deliverable with no finding emitted %q:\n%s", absent, data)
		}
	}
}
