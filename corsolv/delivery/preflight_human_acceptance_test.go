package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/handoff"
	"github.com/gastownhall/gascity/internal/unattended"
)

// mixedIntent is the shape the portal now creates: machine-owned work in the
// first phase, and a release criterion in the second that the project reserved
// for a person.
//
// It is the intent this file exists for. Preflight compiles a synthetic
// placeholder plan so that the ownership, tool and forge checks can run before
// a planning agent has said anything, and that placeholder used to claim EVERY
// criterion — including this one. The compiler refused it, correctly, so a
// perfectly valid intent could never reach the planner that would have written
// a lawful plan for it.
func mixedIntent() handoff.Intent {
	return handoff.Intent{
		SchemaVersion: handoff.SchemaVersion,
		ProjectID:     "human-acceptance-probe",
		Repository: handoff.Repository{
			Slug:          "CorsolvSolutions/human-acceptance-probe",
			Origin:        "https://github.com/CorsolvSolutions/human-acceptance-probe.git",
			DefaultBranch: "main",
		},
		Checkout:  `D:\Development\human-acceptance-probe`,
		Objective: "Prove a project with a human acceptance boundary can start managed delivery.",
		Lifecycle: []string{"Build", "Release"},
		Acceptance: []handoff.Criterion{
			{ID: "ac-1", Statement: "D1: a deterministic source change is merged into the default branch."},
			{ID: "ac-2", Statement: "D2: a person accepts the release.", AcceptedBy: handoff.AcceptedByHuman},
		},
		Policy: handoff.Policy{
			NeedPush: true, NeedPR: true, NeedChecks: true, NeedMerge: true,
		},
		RequestedBy: "portal",
		RequestedAt: time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC),
	}
}

// deliveryOnlyIntent is the same project with nothing reserved to a person —
// every delivery that worked before this fix.
func deliveryOnlyIntent() handoff.Intent {
	in := mixedIntent()
	in.Acceptance[1].AcceptedBy = ""
	return in
}

func preflightTestHost(t *testing.T) handoff.HostProfile {
	t.Helper()
	return handoff.HostProfile{
		DeliveryRoot: t.TempDir(),
		Driver:       "/opt/corsolv/delivery-driver",
		Provider:     "claude",
	}
}

// realPlan is what the planning agent produces after Start: work packages for
// the machine-owned criterion only.
func realPlan(in handoff.Intent) handoff.DeliveryPlan {
	return handoff.DeliveryPlan{
		SchemaVersion: handoff.PlanSchemaVersion,
		ProjectID:     in.ProjectID,
		PlannedBy:     "agent:planner-1",
		PlannedAt:     time.Date(2026, 8, 20, 9, 5, 0, 0, time.UTC),
		Packages: []handoff.WorkPackage{{
			ID:              "wp-d1",
			Title:           "D1: the deterministic source change",
			Phase:           "Build",
			Objective:       "Create src/d1.ts exporting the deterministic result, plus its test.",
			Artifact:        "src/d1.ts",
			AuthorizedPaths: []string{"src/d1.ts", "src/d1.test.ts"},
			Satisfies:       []string{"ac-1"},
		}},
	}
}

func satisfiedBy(plan handoff.DeliveryPlan, criterionID string) []string {
	var claimants []string
	for _, wp := range plan.Packages {
		for _, s := range wp.Satisfies {
			if s == criterionID {
				claimants = append(claimants, wp.ID)
			}
		}
	}
	return claimants
}

// A. The delivery-only intent preflights exactly as it did before.
func TestThePreflightPlanStillCoversEveryDeliveredCriterion(t *testing.T) {
	in := deliveryOnlyIntent()
	plan := preflightPlan(in)

	if err := plan.Validate(in); err != nil {
		t.Fatalf("the preflight placeholder must validate for a delivery-only intent, got: %v", err)
	}
	for _, c := range in.Acceptance {
		if len(satisfiedBy(plan, c.ID)) == 0 {
			t.Fatalf("%s is delivery's to satisfy and no placeholder package claims it", c.ID)
		}
	}
}

// B. THE DEFECT. The placeholder must not claim what only a person may accept.
func TestThePreflightPlanNeverClaimsAHumanAcceptedCriterion(t *testing.T) {
	in := mixedIntent()
	plan := preflightPlan(in)

	if err := plan.Validate(in); err != nil {
		t.Fatalf("the preflight placeholder must validate for an intent with a human boundary, got: %v", err)
	}
	if claimants := satisfiedBy(plan, "ac-2"); len(claimants) > 0 {
		t.Fatalf("the placeholder claims ac-2 in %v — a work package may never claim a human acceptance", claimants)
	}
	if len(satisfiedBy(plan, "ac-1")) == 0 {
		t.Fatal("ac-1 is delivery's to satisfy and the placeholder stopped claiming it")
	}
}

// The whole reproduction, at the layer that refused: compiling the run for a
// mixed intent must succeed.
func TestPreflightCompilesAnIntentThatReservesACriterionToAPerson(t *testing.T) {
	in := mixedIntent()
	host := preflightTestHost(t)

	if _, _, err := handoff.Compile(in, preflightPlan(in), host, "preflight"); err != nil {
		t.Fatalf("managed delivery must be able to preflight a human boundary, got: %v", err)
	}
}

// C. The invariant itself is untouched: a REAL package claiming ac-2 is refused.
func TestARealPackageClaimingTheHumanCriterionIsStillRefused(t *testing.T) {
	in := mixedIntent()
	plan := realPlan(in)
	plan.Packages[0].Satisfies = []string{"ac-1", "ac-2"}

	err := plan.Validate(in)
	if !errors.Is(err, handoff.ErrPlanInvalid) {
		t.Fatalf("a package claiming a human acceptance must be refused, got: %v", err)
	}
	if !strings.Contains(err.Error(), "ac-2") {
		t.Fatalf("the refusal must name the criterion, got: %v", err)
	}
	if _, _, cerr := handoff.Compile(in, plan, preflightTestHost(t), "preflight"); !errors.Is(cerr, handoff.ErrPlanInvalid) {
		t.Fatalf("the compiler must refuse the same plan, got: %v", cerr)
	}
}

// Leaving reserved criteria out of the placeholder cannot produce an empty
// plan, because an intent that reserves EVERY criterion to a person is refused
// one layer earlier — and the refusal says that, rather than complaining about
// a plan with no packages.
func TestAnIntentThatReservesEverythingToAPersonIsRefusedBeforeThePlan(t *testing.T) {
	in := mixedIntent()
	in.Acceptance[0].AcceptedBy = handoff.AcceptedByHuman

	_, _, err := handoff.Compile(in, preflightPlan(in), preflightTestHost(t), "preflight")
	if !errors.Is(err, handoff.ErrIntentInvalid) {
		t.Fatalf("an intent delivery may not advance at all must be refused as an intent, got: %v", err)
	}
}

// D. Preflight reads the intent; it never rewrites who accepts what.
func TestPreflightDoesNotChangeWhoAcceptsACriterion(t *testing.T) {
	in := mixedIntent()
	preflightPlan(in)

	if got := in.Acceptance[1].AcceptedBy; got != handoff.AcceptedByHuman {
		t.Fatalf("acceptance[1].acceptedBy = %q, want %q — preflight must not convert a human criterion to delivery's",
			got, handoff.AcceptedByHuman)
	}
}

// E. The boundary stays visible. Excluding ac-2 from the placeholder must not
// make it disappear: the compiled spec declares it, so preflight publishes it
// as a known human boundary rather than silently dropping it.
func TestPreflightDeclaresTheHumanCriterionAsAKnownBoundary(t *testing.T) {
	in := mixedIntent()
	spec, _, err := handoff.Compile(in, preflightPlan(in), preflightTestHost(t), "preflight")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	var found *unattended.KnownBoundary
	for i, b := range spec.Boundaries {
		if strings.Contains(b.ID, "ac-2") {
			found = &spec.Boundaries[i]
		}
		if strings.Contains(b.ID, "ac-1") {
			t.Fatalf("ac-1 is delivery's own to satisfy and must not be declared a human boundary: %+v", b)
		}
	}
	if found == nil {
		t.Fatalf("the compiled spec declares no boundary for ac-2: %+v", spec.Boundaries)
	}
	if strings.TrimSpace(found.Action) == "" {
		t.Fatal("a boundary without the action only a person can take is indistinguishable from a vague failure")
	}
}

// F. A preflight whose only outstanding item is that boundary reports a known
// human boundary — which permits the run — rather than a refusal.
func TestAKnownHumanBoundaryPermitsManagedDeliveryToStart(t *testing.T) {
	report := &unattended.Report{Checks: unattended.Checks{
		{ID: "ownership.worktree", Category: unattended.CategoryOwnership, Outcome: unattended.OutcomePass},
		{
			ID: "acceptance.ac-2", Category: unattended.CategoryProject,
			Outcome: unattended.OutcomeHumanBoundary, Boundary: "a person accepts ac-2",
		},
		// Planning has not happened yet, and never has at preflight.
		{ID: "plan.work", Category: unattended.CategoryProject, Outcome: unattended.OutcomeNotReached},
	}}

	if got := deliveryReadiness(report); got != unattended.ReadyWithKnownHumanBoundary {
		t.Fatalf("readiness = %s, want %s", got, unattended.ReadyWithKnownHumanBoundary)
	}
	if !deliveryReadiness(report).PermitsUnattendedRun() {
		t.Fatal("a known human boundary must not stop managed delivery from starting")
	}
}

// G. The placeholder can never read as delivered. It exists to prove the
// ground, and nothing about its existence may score acceptance.
func TestThePreflightPlaceholderIsNeverEvidenceOfAcceptance(t *testing.T) {
	in := mixedIntent()
	ev, err := handoff.Assess(preflightPlan(in), in, filepath.Join(t.TempDir(), "PROJECT-STATE.yml"), nil, nil)
	if err != nil {
		t.Fatalf("Assess: %v", err)
	}
	if ev.Met {
		t.Fatal("a synthetic preflight plan must never satisfy the completion gate")
	}
	if len(ev.AwaitingHuman) != 1 || ev.AwaitingHuman[0] != "ac-2" {
		t.Fatalf("AwaitingHuman = %v, want [ac-2]", ev.AwaitingHuman)
	}
}

// H. The whole progression this packet is for: preflight, plan, machine work
// merged with its gate met — and a stop at the person, not a completion.
//
// The acceptance recorded at the end is a synthetic fixture confined to this
// test. It is here to prove the boundary CLEARS when a person answers, which is
// the other half of proving it holds while nobody has.
func TestMachineWorkCompletesAndDeliveryStopsForThePerson(t *testing.T) {
	in := mixedIntent()
	host := preflightTestHost(t)

	if _, _, err := handoff.Compile(in, preflightPlan(in), host, "preflight"); err != nil {
		t.Fatalf("preflight must compile: %v", err)
	}

	plan := realPlan(in)
	if _, _, err := handoff.Compile(in, plan, host, "run-1"); err != nil {
		t.Fatalf("the real plan must compile after preflight: %v", err)
	}

	projection := writeDeliveryProjection(t, in.ProjectID, "abc123",
		[3]string{"wp-d1", "merged", "met"})

	ev, err := handoff.Assess(plan, in, projection, nil, nil)
	if err != nil {
		t.Fatalf("Assess: %v", err)
	}
	st := handoff.Derive(in.ProjectID, true, plan, true,
		handoff.RunObservation{Finished: true, Outcome: string(unattended.RunCompleted)}, ev, time.Now())

	if st.State != handoff.StateBlocked {
		t.Fatalf("State = %q, want %q — machine work is done and a person has not answered",
			st.State, handoff.StateBlocked)
	}
	if !strings.Contains(strings.Join(ev.AwaitingHuman, ","), "ac-2") {
		t.Fatalf("AwaitingHuman = %v, want ac-2", ev.AwaitingHuman)
	}
	if !strings.Contains(st.Detail, "ac-2") {
		t.Fatalf("the detail must name what the person owes, got: %q", st.Detail)
	}

	rec := handoff.Record{ProjectID: in.ProjectID, Intent: in}
	rec, err = rec.Accept("ac-2", "jon.pratten@corsolv.com", "test fixture", time.Now())
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}

	ev, err = handoff.Assess(plan, in, projection, rec.Acceptances, nil)
	if err != nil {
		t.Fatalf("Assess after acceptance: %v", err)
	}
	st = handoff.Derive(in.ProjectID, true, plan, true,
		handoff.RunObservation{Finished: true, Outcome: string(unattended.RunCompleted)}, ev, time.Now())
	if st.State != handoff.StateCompleted {
		t.Fatalf("State = %q, want %q after a person accepted: %v", st.State, handoff.StateCompleted, ev.Reasons)
	}
}

func writeDeliveryProjection(t *testing.T, projectID, mainSha string, rows ...[3]string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("schemaVersion: 1\nproject:\n")
	b.WriteString("  projectId: \"" + projectID + "\"\n")
	b.WriteString("  latestAcceptedMainSha: \"" + mainSha + "\"\n")
	b.WriteString("activeTasks:\n")
	for _, r := range rows {
		b.WriteString("  - taskId: \"" + r[0] + "\"\n")
		b.WriteString("    status: \"" + r[1] + "\"\n")
		b.WriteString("    completionGateStatus: \"" + r[2] + "\"\n")
	}
	path := filepath.Join(t.TempDir(), "PROJECT-STATE.yml")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
