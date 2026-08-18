package projector

import (
	"strings"
	"testing"
)

// What the projection was missing, and why it mattered.
//
// The document carried work packages and nothing else. A portal reading it
// could say four packages merged and could not say which of seven deliverables
// were finished — they are different taxonomies and one cannot stand for the
// other. The pilot that found this watched a project report "0 of 7" with its
// first package merged and its deliverable genuinely evidenced.
//
// The rule these pin is the one handoff.Assess applies to the same rows: a
// deliverable is met when EVERY package that claimed it is terminal AND carries
// a met completion gate. Both halves matter. One package of two finishing does
// not make a deliverable true, and a package that reached the forge without
// earning its gate has not delivered anything — that is the failure that looks
// most like success.

func deliverableState() *State {
	s := NewState("pilot")
	s.Tasks = map[string]*Task{
		"wp-a": {TaskID: "wp-a", Status: StatusMerged, CompletionGateStatus: GateMet},
		"wp-b": {TaskID: "wp-b", Status: StatusMerged, CompletionGateStatus: GateNotMet},
		"wp-c": {TaskID: "wp-c", Status: StatusActive, CompletionGateStatus: GateMet},
	}
	return s
}

func TestADeliverableIsMetOnlyWhenEveryClaimingPackageIsDoneAndGated(t *testing.T) {
	for _, tc := range []struct {
		name        string
		satisfiedBy []string
		want        bool
		because     string
	}{
		{
			name: "one package, merged with its gate met", satisfiedBy: []string{"wp-a"}, want: true,
			because: "the only package that claimed it delivered it",
		},
		{
			name: "two packages, both merged and gated", satisfiedBy: []string{"wp-a", "wp-a"}, want: true,
			because: "every claiming package is done",
		},
		{
			name: "one of two packages still active", satisfiedBy: []string{"wp-a", "wp-c"}, want: false,
			because: "part of the work a deliverable needs has not finished",
		},
		{
			name: "merged without earning its completion gate", satisfiedBy: []string{"wp-b"}, want: false,
			because: "reaching the forge is not the same as being accepted, and this is the failure that looks most like success",
		},
		{
			name: "claimed by a package this projection does not carry", satisfiedBy: []string{"wp-missing"}, want: false,
			because: "a package no row describes cannot evidence anything",
		},
		{
			name: "claimed by nothing at all", satisfiedBy: nil, want: false,
			because: "a deliverable nothing was raised for is outstanding, never trivially satisfied",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := deliverableState()
			s.Deliverables = []Deliverable{{ID: "ac-1", Statement: "the thing", SatisfiedBy: tc.satisfiedBy}}
			s.resolveDeliverables()
			if got := s.Deliverables[0].Met; got != tc.want {
				t.Errorf("met = %v, want %v: %s", got, tc.want, tc.because)
			}
		})
	}
}

// The consumer joins on the id. Everything else in the section is for a person
// reading the file directly — so the id must be there, spelled the way the
// project's own records spell it.
func TestTheRenderedDeliverablesSectionCarriesTheJoinKeyAndTheVerdict(t *testing.T) {
	s := deliverableState()
	s.Deliverables = []Deliverable{
		{ID: "ac-1", Statement: "P1 — the contract is persisted.", SatisfiedBy: []string{"wp-a"}},
		{ID: "ac-2", Statement: "B1 — the CLI finds duplicates.", SatisfiedBy: []string{"wp-c"}},
	}

	out, err := Render(s)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	text := string(out)

	for _, want := range []string{
		"\ndeliverables:\n",
		"  - deliverableId: \"ac-1\"",
		"    met: true",
		"  - deliverableId: \"ac-2\"",
		"    met: false",
		"satisfiedBy:",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the rendered document is missing %q:\n%s", want, text)
		}
	}
}

// A project whose delivery was never given acceptance criteria still renders a
// valid document. An absent section and an empty one are different facts, and
// the consumer must not have to tell them apart by the key being missing.
func TestADeliveryWithNoDeliverablesRendersAnExplicitEmptyList(t *testing.T) {
	out, err := Render(deliverableState())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(out), "deliverables: []") {
		t.Errorf("expected an explicit empty list, got:\n%s", out)
	}
}
