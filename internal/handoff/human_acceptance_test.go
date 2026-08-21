package handoff

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// humanIntent is planIntent with a third criterion only a person may accept.
func humanIntent() Intent {
	in := planIntent()
	in.Acceptance = append(in.Acceptance, Criterion{
		ID:         "ac-3",
		Statement:  "Human acceptance of the final sample output.",
		AcceptedBy: AcceptedByHuman,
	})
	return in
}

func TestIntentRefusesAnAcceptedByItDoesNotUnderstand(t *testing.T) {
	in := humanIntent()
	in.Acceptance[2].AcceptedBy = "reviewer"

	err := in.Validate()
	if !errors.Is(err, ErrIntentInvalid) {
		t.Fatalf("an unrecognized acceptedBy must be refused, got: %v", err)
	}
	if !strings.Contains(err.Error(), "acceptedBy") {
		t.Fatalf("the refusal must name the field, got: %v", err)
	}
}

func TestIntentRefusesDeliveryWithNothingItCanSatisfy(t *testing.T) {
	in := validIntent()
	in.Acceptance = []Criterion{
		{ID: "ac-1", Statement: "A person accepts the result.", AcceptedBy: AcceptedByHuman},
	}

	err := in.Validate()
	if !errors.Is(err, ErrIntentInvalid) {
		t.Fatalf("an intent delivery cannot advance must be refused, got: %v", err)
	}
}

func TestIntentAcceptsAHumanCriterionAlongsideDeliveredOnes(t *testing.T) {
	if err := humanIntent().Validate(); err != nil {
		t.Fatalf("a human-accepted criterion beside delivered ones must validate, got: %v", err)
	}
}

// THE DEFECT. A work package claiming a human acceptance is how a machine
// approves its own release: the package merges, every rule downstream reads the
// merge as evidence, and the criterion reserved for a person is scored met
// without one ever looking at it.
func TestPlanMayNotClaimAHumanAcceptedCriterion(t *testing.T) {
	in := humanIntent()
	plan := validPlan()
	plan.Packages[1].Satisfies = []string{"ac-2", "ac-3"}

	err := plan.Validate(in)
	if !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("a package claiming a human acceptance must be refused, got: %v", err)
	}
	if !strings.Contains(err.Error(), "ac-3") {
		t.Fatalf("the refusal must name the criterion, got: %v", err)
	}
}

// The other half of the same rule: coverage may not demand what delivery is
// forbidden to claim, or the only valid plan is the one that lies.
func TestPlanNeedNotCoverAHumanAcceptedCriterion(t *testing.T) {
	if err := validPlan().Validate(humanIntent()); err != nil {
		t.Fatalf("a plan that leaves a human acceptance to a person must validate, got: %v", err)
	}
}

// A criterion nothing claims and nobody marked human is still the planner
// forgetting it.
func TestPlanStillMustCoverEveryDeliveredCriterion(t *testing.T) {
	in := humanIntent()
	in.Acceptance[2].AcceptedBy = AcceptedByDelivery

	err := validPlan().Validate(in)
	if !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("an uncovered delivered criterion must still be refused, got: %v", err)
	}
}

func TestAssessNeverMeetsAHumanCriterionFromMergedWork(t *testing.T) {
	in := humanIntent()
	path := writeProjection(t, in.ProjectID, "abc123",
		[3]string{"wp-add", "merged", "met"}, [3]string{"wp-multiply", "merged", "met"})

	ev, err := Assess(validPlan(), in, path, nil, nil)
	if err != nil {
		t.Fatalf("Assess: %v", err)
	}
	if ev.Met {
		t.Fatal("every package merged is not human acceptance; delivery must not report itself complete")
	}
	if len(ev.AwaitingHuman) != 1 || ev.AwaitingHuman[0] != "ac-3" {
		t.Fatalf("AwaitingHuman = %v, want [ac-3]", ev.AwaitingHuman)
	}
	if !strings.Contains(strings.Join(ev.Reasons, " | "), "awaiting human acceptance") {
		t.Fatalf("the reason must say a person is being waited on, got: %v", ev.Reasons)
	}
	// It is a boundary, not an outstanding delivery obligation.
	for _, id := range ev.AcceptanceOutstanding {
		if id == "ac-3" {
			t.Fatal("a human acceptance is a boundary, not work delivery failed to do")
		}
	}
}

func TestAssessMeetsAHumanCriterionOnlyOnceAPersonRecordedIt(t *testing.T) {
	in := humanIntent()
	path := writeProjection(t, in.ProjectID, "abc123",
		[3]string{"wp-add", "merged", "met"}, [3]string{"wp-multiply", "merged", "met"})

	accepted := []Acceptance{{
		CriterionID: "ac-3",
		By:          "jon.pratten@corsolv.com",
		At:          time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC),
	}}

	ev, err := Assess(validPlan(), in, path, accepted, nil)
	if err != nil {
		t.Fatalf("Assess: %v", err)
	}
	if !ev.Met {
		t.Fatalf("a recorded human acceptance must complete the delivery, reasons: %v", ev.Reasons)
	}
	if len(ev.AwaitingHuman) != 0 {
		t.Fatalf("AwaitingHuman = %v, want none", ev.AwaitingHuman)
	}
}

// Derive must call this a boundary rather than a failure: nothing was proved
// wrong, and the run is resumable the moment a person answers.
func TestDeriveReportsAnUnansweredHumanCriterionAsBlocked(t *testing.T) {
	in := humanIntent()
	path := writeProjection(t, in.ProjectID, "abc123",
		[3]string{"wp-add", "merged", "met"}, [3]string{"wp-multiply", "merged", "met"})
	ev, err := Assess(validPlan(), in, path, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	st := Derive(in.ProjectID, true, validPlan(), true,
		RunObservation{Finished: true, Outcome: outcomeCompleted}, ev, time.Now())

	if st.State != StateBlocked {
		t.Fatalf("State = %q, want %q", st.State, StateBlocked)
	}
	if !strings.Contains(st.Detail, "ac-3") {
		t.Fatalf("the detail must name what a person owes, got: %q", st.Detail)
	}
}

func TestRecordAcceptRefusesACriterionDeliveryOwns(t *testing.T) {
	r := Record{ProjectID: "p", Intent: humanIntent()}

	if _, err := r.Accept("ac-1", "someone", "", time.Now()); !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("accepting a delivered criterion by hand must be refused, got: %v", err)
	}
	if _, err := r.Accept("ac-9", "someone", "", time.Now()); !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("accepting a criterion the intent never declared must be refused, got: %v", err)
	}
}

func TestRecordAcceptIsIdempotentAndKeepsTheFirstAnswer(t *testing.T) {
	r := Record{ProjectID: "p", Intent: humanIntent()}
	first := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)

	r, err := r.Accept("ac-3", "reviewer-a", "looks right", first)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	r, err = r.Accept("ac-3", "reviewer-b", "again", first.Add(time.Hour))
	if err != nil {
		t.Fatalf("re-accepting must not error: %v", err)
	}

	if len(r.Acceptances) != 1 {
		t.Fatalf("Acceptances = %d, want 1", len(r.Acceptances))
	}
	if got := r.Acceptances[0]; got.By != "reviewer-a" || !got.At.Equal(first) {
		t.Fatalf("the first answer must stand, got %+v", got)
	}
}

func TestRecordAcceptRequiresAPerson(t *testing.T) {
	r := Record{ProjectID: "p", Intent: humanIntent()}
	if _, err := r.Accept("ac-3", "  ", "", time.Now()); !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("an unattributed acceptance must be refused, got: %v", err)
	}
}
