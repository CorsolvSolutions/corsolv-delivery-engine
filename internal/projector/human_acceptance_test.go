package projector

import (
	"strings"
	"testing"
)

// THE DEFECT THIS PINS. A criterion only a person may accept is claimed by no
// work package — the plan validator refuses one that claims it — so deriving
// `met` from claiming packages can never make it true, however many people
// accept it. The pilot recorded a real acceptance, the engine's own state
// turned `completed`, and the document the dashboard reads went on saying the
// deliverable was outstanding. Complete in one place, 6 of 7 in the other.
func TestAPersonsAcceptanceMeetsADeliverableNoPackageCouldClaim(t *testing.T) {
	s := NewState("p")
	s.Deliverables = []Deliverable{{
		ID:          "ac-7",
		Statement:   "Human acceptance of the final sample output.",
		SatisfiedBy: nil,
		AcceptedBy:  "Jon Pratten",
		AcceptedAt:  "2026-08-19T16:28:31Z",
	}}

	data, err := Render(s)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !s.Deliverables[0].Met {
		t.Fatal("a recorded human acceptance must meet the deliverable; nothing else ever can")
	}
	out := string(data)
	for _, want := range []string{"acceptedBy: \"Jon Pratten\"", "met: true"} {
		if !strings.Contains(out, want) {
			t.Errorf("the projection must carry %q so a reader can see WHO accepted it:\n%s", want, out)
		}
	}
}

// Unclaimed and unaccepted stays false. Emptiness is not satisfaction.
func TestAnUnclaimedDeliverableNobodyAcceptedIsStillNotMet(t *testing.T) {
	s := NewState("p")
	s.Deliverables = []Deliverable{{ID: "ac-7", Statement: "Human acceptance."}}

	if _, err := Render(s); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if s.Deliverables[0].Met {
		t.Fatal("nothing claimed it and nobody accepted it, so it is not met")
	}
}

// A person's acceptance answers for the person's criterion, and never for work
// delivery owes. A delivered criterion whose packages are outstanding stays
// outstanding even if an acceptance were somehow recorded against it.
func TestAnAcceptanceDoesNotExcuseAnUnfinishedPackage(t *testing.T) {
	s := NewState("p")
	s.Tasks["wp-one"] = &Task{TaskID: "wp-one", Status: TaskStatus("pr-open"), CompletionGateStatus: GateNotMet}
	s.Deliverables = []Deliverable{{
		ID: "ac-1", Statement: "Delivered work.", SatisfiedBy: []string{"wp-one"},
		AcceptedBy: "Jon Pratten",
	}}

	if _, err := Render(s); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if s.Deliverables[0].Met {
		t.Fatal("a criterion a package claimed is met by that package finishing, not by a signature")
	}
}
