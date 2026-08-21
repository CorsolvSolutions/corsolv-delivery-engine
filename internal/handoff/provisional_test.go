package handoff

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// THE DEFECT THIS EXISTS FOR.
//
// Preflight compiles a SYNTHETIC plan — one placeholder package per
// delivery-owned criterion, no gates, an objective that says it will never run
// — so that the questions which do not depend on what the work turns out to be
// (ownership, tools, forge, durable state) can be asked before anyone waits on
// a planner.
//
// `MustCover` then began measuring every plan against what its criteria
// require. A placeholder carries none of those behaviors, because it is not a
// proposal to do the work; it is a shape the compiler will accept. So the
// moment a project declared required behaviors, preflight refused it — at the
// front door, before the planner that would have written a lawful plan was
// ever asked, with an error listing behaviors nobody had been given a chance to
// deliver.
//
// This is the second time this trap has been sprung. `preflightPackages`
// already carries the note from the first: a placeholder that claimed a
// person's criterion "refused every intent with a human boundary at the front
// door". Same shape, different rule.
//
// Fidelity is a claim about work that will be done. A placeholder claims
// nothing, so it is not measured — and says so about itself.

// mustCoverIntent is a project whose criterion declares what it actually
// requires, which is the case that could not start.
func mustCoverIntent() Intent {
	in := planIntent()
	in.Acceptance[0].MustCover = []string{"integer", "decimal|number", "mixed"}
	return in
}

// placeholderPlan is the shape preflight compiles: one empty package per
// delivery-owned criterion.
func placeholderPlan(in Intent) DeliveryPlan {
	out := make([]WorkPackage, 0, len(in.Acceptance))
	for _, c := range in.Acceptance {
		if c.IsHuman() {
			continue
		}
		out = append(out, WorkPackage{
			ID:              "wp-" + c.ID,
			Title:           "placeholder for " + c.ID,
			Phase:           in.Lifecycle[0],
			Objective:       "Placeholder used only to preflight the ground; never executed.",
			Artifact:        "preflight/" + c.ID + ".placeholder",
			AuthorizedPaths: []string{"preflight/" + c.ID + ".placeholder"},
			Satisfies:       []string{c.ID},
		})
	}
	return DeliveryPlan{
		SchemaVersion: PlanSchemaVersion,
		ProjectID:     in.ProjectID,
		PlannedBy:     "preflight",
		Packages:      out,
		Provisional:   true,
	}
}

// A provisional plan is not measured against what the criteria require, so a
// project that declares required behaviors can reach its planner.
func TestAProvisionalPlanIsNotMeasuredAgainstRequiredBehaviour(t *testing.T) {
	in := mustCoverIntent()
	if err := placeholderPlan(in).Validate(in); err != nil {
		t.Fatalf("preflight's placeholder plan must validate for a project that declares required behaviors, got: %v", err)
	}
}

// And the rule still bites where it means something. The same packages, offered
// as a real plan, are refused — naming both the missing gate and every behavior
// dropped.
func TestTheSamePackagesOfferedAsARealPlanAreStillRefused(t *testing.T) {
	in := mustCoverIntent()
	proposed := placeholderPlan(in)
	proposed.Provisional = false

	err := proposed.Validate(in)
	if !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("err = %v, want ErrPlanInvalid", err)
	}
	for _, want := range []string{"declares a gate", `"integer"`, `"decimal|number"`, `"mixed"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must still name %s: %v", want, err)
		}
	}
}

// Provisional is this process's word about a plan it built itself. It is not a
// field a document may carry: a plan file that could declare itself provisional
// would be a plan file that could opt out of the fidelity rule.
func TestAPlanFileCannotDeclareItselfProvisional(t *testing.T) {
	in := mustCoverIntent()

	// It never serializes.
	data, err := json.Marshal(placeholderPlan(in))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(data)), "provisional") {
		t.Fatalf("a provisional plan serialized the flag:\n%s", data)
	}

	// And it is refused on the way in, like any unknown field.
	withFlag := `{"schemaVersion":1,"projectId":"` + in.ProjectID +
		`","plannedBy":"x","provisional":true,"packages":[]}`
	if _, err := DecodePlan([]byte(withFlag), in); !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("a plan declaring itself provisional must be refused, got: %v", err)
	}

	// A plan read from disk is never provisional, so every real plan faces the
	// rule regardless of what produced it.
	clean, err := json.Marshal(placeholderPlan(in))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePlan(clean, in); !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("the placeholder plan decoded from JSON must face the fidelity rule, got: %v", err)
	}
}

// Everything else a plan must satisfy still applies to a provisional one: it is
// exempt from ONE clause, not from validation.
func TestAProvisionalPlanStillFacesEveryOtherRule(t *testing.T) {
	in := mustCoverIntent()

	bad := placeholderPlan(in)
	bad.Packages[0].Phase = "not-a-declared-phase"
	if err := bad.Validate(in); !errors.Is(err, ErrPlanInvalid) ||
		!strings.Contains(err.Error(), "not one of the lifecycle phases") {
		t.Fatalf("a provisional plan must still be refused an undeclared phase, got: %v", err)
	}

	collide := placeholderPlan(in)
	collide.Packages[1].AuthorizedPaths = collide.Packages[0].AuthorizedPaths
	collide.Packages[1].Artifact = collide.Packages[0].Artifact
	if err := collide.Validate(in); !errors.Is(err, ErrPlanInvalid) ||
		!strings.Contains(err.Error(), "collision") {
		t.Fatalf("a provisional plan must still be refused a path collision, got: %v", err)
	}
}
