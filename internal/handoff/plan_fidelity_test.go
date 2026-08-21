package handoff

import (
	"strings"
	"testing"
)

// Plan fidelity: a planner may reorganize the work, and may not change what the
// work has to achieve.
//
// THE DEFECT THESE EXIST FOR, from the CSV data-quality profiler pilot. The
// brief required a per-column inferred type drawn from
//
//	text / integer / decimal / boolean / date / mixed
//
// and the explicit identification of columns holding mixed types. The criterion
// that reached the engine was one sentence — "B2 - Profiler calculates the
// required overall and per-column data-quality measures." — and the planner
// wrote itself a contract whose vocabulary was
//
//	integer / number / boolean / date / string / empty
//
// `mixed` was gone. Every gate downstream was then derived from that plan: the
// worker's declared gates, the controller's re-run of them, required CI. All of
// them passed, because all of them were asking whether the plan had been
// carried out — and it had. The project reported 8 of 8 with a required
// behavior missing from the product.
//
// The coverage rule that was supposed to catch this only ever asked whether
// SOME package named the criterion's id. Naming it is not delivering it, and
// the criterion carried nothing a machine could compare a plan against.
//
// So a criterion may now carry `MustCover`: the authoritative acceptance detail,
// in the words of whoever wants the work, mechanically checkable. Nothing here
// judges prose — the author states what must be covered, and Go checks that the
// plan covering that criterion says it and can prove it.

// profilerIntent is the pilot's own criterion, with the detail the brief always
// had and the wire never carried.
func profilerIntent() Intent {
	in := validIntent()
	in.Acceptance = []Criterion{
		{
			ID:        "ac-3",
			Statement: "B2 - Profiler calculates the required overall and per-column data-quality measures.",
			MustCover: []string{
				"text|string",
				"integer",
				"decimal|number",
				"boolean",
				"date",
				"mixed",
				"columns containing mixed inferred types|mixed-type columns",
			},
		},
	}
	return in
}

// profilerPlan is a plan that keeps every required behavior.
func profilerPlan() DeliveryPlan {
	p := validPlan()
	p.Packages = []WorkPackage{{
		ID: "wp-profiler-core", Title: "profiler core", Phase: "Build",
		Objective: "Infer each column's simple type as one of text, integer, decimal, boolean, date or mixed, " +
			"and report the columns containing mixed inferred types in both the Markdown and the JSON report.",
		Artifact:        "src/profile.js",
		AuthorizedPaths: []string{"src/profile.js"},
		Gates:           []string{"npm test"},
		Satisfies:       []string{"ac-3"},
	}}
	return p
}

// 1. A plan that preserves every required behavior is accepted — and the
// author's own synonyms count, because `decimal|number` says the two words mean
// the same thing here and nothing else is entitled to decide that.
func TestAPlanThatKeepsEveryRequiredBehaviourIsAccepted(t *testing.T) {
	if err := profilerPlan().Validate(profilerIntent()); err != nil {
		t.Fatalf("a faithful plan must be accepted: %v", err)
	}

	// The same plan written in the alternative spellings the author sanctioned.
	p := profilerPlan()
	p.Packages[0].Objective = "Infer each column's simple type as one of string, integer, number, boolean, " +
		"date or mixed, and list the mixed-type columns in the Markdown and JSON reports."
	if err := p.Validate(profilerIntent()); err != nil {
		t.Fatalf("an equivalent plan in the author's own alternative words must be accepted: %v", err)
	}
}

// 2. The planner owns HOW the work is organized. Splitting one deliverable
// across several packages is a planning decision, not a narrowing, so the
// requirement is satisfied by what the covering packages say TOGETHER.
func TestOneDeliverableMaySpanSeveralWorkPackages(t *testing.T) {
	p := profilerPlan()
	p.Packages = []WorkPackage{
		{
			ID: "wp-types", Title: "type inference", Phase: "Build",
			Objective:       "Infer a column's simple type as text, integer, decimal, boolean or date.",
			Artifact:        "src/infer.js",
			AuthorizedPaths: []string{"src/infer.js"},
			Gates:           []string{"npm test"},
			Satisfies:       []string{"ac-3"},
		},
		{
			ID: "wp-mixed", Title: "mixed columns", Phase: "Build",
			Objective: "Report a column of incompatible populated types as mixed, and identify the " +
				"columns containing mixed inferred types in both reports.",
			Artifact:        "src/mixed.js",
			AuthorizedPaths: []string{"src/mixed.js"},
			Gates:           []string{"npm test"},
			DependsOn:       []string{"wp-types"},
			Satisfies:       []string{"ac-3"},
		},
	}
	if err := p.Validate(profilerIntent()); err != nil {
		t.Fatalf("a deliverable split across packages must be accepted: %v", err)
	}
}

// 3. Omission is refused. This is the pilot's own plan: everything else present,
// `mixed` simply absent.
func TestAPlanThatOmitsARequiredBehaviourIsRefused(t *testing.T) {
	p := profilerPlan()
	p.Packages[0].Objective = "Infer each column's simple type as one of text, integer, decimal, boolean or date, " +
		"and report the columns containing mixed inferred types in both reports."

	err := p.Validate(profilerIntent())
	if err == nil {
		t.Fatal("a plan that drops a required behavior must be refused — this is the plan that shipped a " +
			"product missing it while reporting 8 of 8")
	}
	for _, want := range []string{"ac-3", "mixed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name %q so the planner can correct it, got: %v", want, err)
		}
	}
}

// 4. Narrowing an enumerated set is refused. The pilot's exact substitution:
// six required type names in, a different five out.
func TestAPlanThatNarrowsAnEnumeratedBehaviourIsRefused(t *testing.T) {
	p := profilerPlan()
	p.Packages[0].Objective = "Infer each column's type as one of integer, number, boolean, date, string or empty, " +
		"and report the columns containing mixed inferred types in both reports."

	err := p.Validate(profilerIntent())
	if err == nil {
		t.Fatal("a plan that narrows the required vocabulary must be refused")
	}
	if !strings.Contains(err.Error(), "mixed") {
		t.Errorf("the refusal must name the behavior that was narrowed away, got: %v", err)
	}
}

// 5. Substituting a different requirement is refused. Doing something adjacent
// and calling the criterion covered is the failure this whole check exists for.
func TestAPlanThatSubstitutesADifferentRequirementIsRefused(t *testing.T) {
	p := profilerPlan()
	p.Packages[0].Objective = "Infer each column's simple type as one of text, integer, decimal, boolean or date, " +
		"and report the columns containing ambiguous inferred types in both reports."

	err := p.Validate(profilerIntent())
	if err == nil {
		t.Fatal("a plan that answers a different requirement must be refused")
	}
	if !strings.Contains(err.Error(), "mixed") {
		t.Errorf("the refusal must name what is still uncovered, got: %v", err)
	}
}

// 6. Covered is not the same as covered AND provable. A package that says the
// right words and declares no gate has nothing that can demonstrate it, and the
// criterion it claims cannot be satisfied by evidence — which is what
// completion is decided on.
func TestARequiredBehaviourMustBeVerifiableAndNotMerelyMentioned(t *testing.T) {
	p := profilerPlan()
	p.Packages[0].Gates = nil

	err := p.Validate(profilerIntent())
	if err == nil {
		t.Fatal("a criterion carrying required behavior must be claimed only by work that declares how it " +
			"is verified")
	}
	if !strings.Contains(err.Error(), "ac-3") {
		t.Errorf("the refusal must name the criterion, got: %v", err)
	}
}

// 7. A criterion that carries no authoritative detail behaves exactly as it did
// before this existed. Every plan written against every project already
// running stays valid.
func TestACriterionWithNoAuthoritativeDetailIsUnchanged(t *testing.T) {
	if err := validPlan().Validate(planIntent()); err != nil {
		t.Fatalf("an existing plan against an existing intent must stay valid: %v", err)
	}

	// Even an objective that says nothing about the criterion's words: without
	// declared detail there is nothing to check, and inventing something to
	// check would be Go judging prose.
	p := validPlan()
	p.Packages[0].Objective = "Create src/add.ts and its test."
	p.Packages[1].Objective = "Create src/multiply.ts and its test."
	if err := p.Validate(planIntent()); err != nil {
		t.Fatalf("a criterion with no MustCover must not acquire one: %v", err)
	}
}

// 8. The check happens before anything executes. Every plan — an agent's and a
// hand-installed one alike — reaches the engine through DecodePlan, which
// validates before the plan is saved, before the run is compiled and long
// before a worker is routed to any of it.
func TestANarrowedPlanIsRefusedBeforeItCanBeExecuted(t *testing.T) {
	in := profilerIntent()
	narrowed := profilerPlan()
	narrowed.Packages[0].Objective = "Infer each column's type as one of integer, number, boolean, date, " +
		"string or empty, and report the columns containing mixed inferred types."

	raw, err := MarshalPlan(narrowed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePlan(raw, in); err == nil {
		t.Fatal("DecodePlan is the one door every plan enters through; a narrowed plan must not get past it")
	}

	// And the same document, faithful, does get through — so the door refuses
	// the narrowing rather than the shape.
	raw, err = MarshalPlan(profilerPlan())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePlan(raw, in); err != nil {
		t.Fatalf("a faithful plan must decode: %v", err)
	}
}

// A criterion only a person may accept is not the planner's to cover, and its
// required detail is not either: delivery may prepare what that person reads
// and may never claim their answer.
func TestAuthoritativeDetailOnAHumanCriterionDemandsNothingOfThePlan(t *testing.T) {
	in := profilerIntent()
	in.Acceptance = append(in.Acceptance, Criterion{
		ID:         "ac-9",
		Statement:  "A person approves the released sample output.",
		AcceptedBy: AcceptedByHuman,
		MustCover:  []string{"nothing a work package may claim"},
	})
	if err := profilerPlan().Validate(in); err != nil {
		t.Fatalf("a human-owned criterion must demand nothing of the plan: %v", err)
	}
}

// The plan that actually shipped, in its own words.
//
// This is the reproduction, pinned. The sentence below is verbatim from
// `wp-profiler-core`'s objective in the pilot's plan.json — the whole of what
// it said about type inference — and the package's objective never used the
// word `mixed` at all. Against the criterion as it reached the engine (a
// statement and nothing else) this plan was valid, and everything downstream
// agreed. Against the criterion with its authoritative detail it is refused,
// before a worker is routed to any of it.
func TestThePilotsOwnNarrowedPlanIsRefused(t *testing.T) {
	const shipped = "Create src/profile.js exporting `profile({header, rows, inputName, inputBytes})` " +
		"returning the plain report object whose shape and rounding rules are fixed by the contract, " +
		"including type inference (a column is integer, number, boolean or date only when every " +
		"non-empty value matches; date means ISO 8601 YYYY-MM-DD; empty when it has no non-empty " +
		"values; otherwise string), treatment of the empty string and the case-insensitive tokens " +
		"NULL, NA and N/A as empty, distinct and duplicate value counts over non-empty values."

	p := profilerPlan()
	p.Packages[0].Objective = shipped

	err := p.Validate(profilerIntent())
	if err == nil {
		t.Fatal("the plan that shipped a product with no mixed type in it must be refused")
	}
	for _, want := range []string{"ac-3", "mixed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name %q, got: %v", want, err)
		}
	}
	// The behaviors it DID keep must not be reported as missing, or the
	// refusal stops telling a planner what to correct.
	for _, kept := range []string{`"integer"`, `"boolean"`, `"date"`} {
		if strings.Contains(err.Error(), kept) {
			t.Errorf("the refusal names %s, which this plan does deliver: %v", kept, err)
		}
	}
}

// A requirement is a word, not a fragment of one. Without this, "date" is
// delivered by "candidate" and the check quietly stops meaning anything.
func TestARequiredBehaviourIsNotSatisfiedBySomeLongerWordContainingIt(t *testing.T) {
	in := validIntent()
	in.Acceptance = []Criterion{{
		ID:        "ac-1",
		Statement: "Dates are recognized.",
		MustCover: []string{"date"},
	}}

	p := validPlan()
	p.Packages = []WorkPackage{{
		ID: "wp-one", Title: "candidate ranking", Phase: "Build",
		Objective:       "Rank each candidate and validate the consolidated output.",
		Artifact:        "src/one.ts",
		AuthorizedPaths: []string{"src/one.ts"},
		Gates:           []string{"npm test"},
		Satisfies:       []string{"ac-1"},
	}}

	if err := p.Validate(in); err == nil {
		t.Fatal(`"date" must not be delivered by "candidate" or "validate"`)
	}

	p.Packages[0].Objective = "Rank each candidate, and infer a date column from ISO 8601 values."
	if err := p.Validate(in); err != nil {
		t.Fatalf("the same requirement, actually stated, must be accepted: %v", err)
	}
}
