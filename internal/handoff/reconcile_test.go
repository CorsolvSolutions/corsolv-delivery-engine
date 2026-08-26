package handoff

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests pin the reconciliation model: a delivery-owned criterion reported
// met, later disproved, and repaired by additive work — with nothing deleted at
// any point.
//
// The letters in the test comments are the acceptance letters of the packet
// this was built for, so a reader can find the requirement each one answers.

var reconAt = time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)

// reconTestIntent is planIntent with a third criterion that declares the
// behaviors it requires, so fidelity has something to be checked against.
func reconTestIntent() Intent {
	in := planIntent()
	in.Acceptance = append(in.Acceptance, Criterion{
		ID:        "ac-3",
		Statement: "Every column carries an inferred type.",
		MustCover: []string{"text", "integer", "decimal|number", "boolean", "date", "mixed"},
	})
	return in
}

// reconTestPlan is validPlan with a package for ac-3 that honestly names every
// behavior the criterion requires. It passed fidelity; what it BUILT is what a
// later finding disproves.
func reconTestPlan() DeliveryPlan {
	p := validPlan()
	p.Packages = append(p.Packages, WorkPackage{
		ID: "wp-types", Title: "inferred column types", Phase: "Build",
		Objective: "Create src/types.ts inferring each column as text, integer, decimal, " +
			"boolean or date, and reporting mixed where a column holds more than one.",
		Artifact:        "src/types.ts",
		AuthorizedPaths: []string{"src/types.ts", "src/types.test.ts"},
		Gates:           []string{"npm test"},
		Satisfies:       []string{"ac-3"},
	})
	return p
}

// completeRows is the projection of a delivery that finished: every package
// merged with a met completion gate.
func completeRows() [][3]string {
	return [][3]string{
		{"wp-add", "merged", "met"},
		{"wp-multiply", "merged", "met"},
		{"wp-types", "merged", "met"},
	}
}

// reconTestRecord is the durable record of that finished delivery.
func reconTestRecord() Record {
	in := reconTestIntent()
	return Record{
		SchemaVersion: RecordSchemaVersion,
		ProjectID:     in.ProjectID,
		Intent:        in,
		IntentDigest:  Digest(in),
		CreatedAt:     reconAt.Add(-72 * time.Hour),
		UpdatedAt:     reconAt.Add(-time.Hour),
		Runs:          []RunRef{{RunID: "run-1", StartedAt: reconAt.Add(-72 * time.Hour), Reason: ReasonInitial}},
	}
}

// assessComplete is the assessment of that finished delivery: three of three.
func assessComplete(t *testing.T) (Evidence, Intent, DeliveryPlan) {
	t.Helper()
	in, plan := reconTestIntent(), reconTestPlan()
	path := writeProjection(t, in.ProjectID, "abc123", completeRows()...)
	ev, err := Assess(plan, in, path, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !ev.Met {
		t.Fatalf("the fixture must start met, got: %v", ev.Reasons)
	}
	return ev, in, plan
}

// remedialPackage is corrective work for ac-3 that carries every behavior the
// criterion requires, and repairs the file the disproved package produced.
func remedialPackage() WorkPackage {
	return WorkPackage{
		ID: "wp-types-fix", Title: "repair the inferred column types", Phase: "Build",
		Objective: "Rewrite src/types.ts so every column is inferred as text, integer, decimal, " +
			"boolean or date, and a column holding more than one is reported as mixed.",
		Artifact:        "src/types.ts",
		AuthorizedPaths: []string{"src/types.ts", "src/types.test.ts"},
		Gates:           []string{"npm test"},
		Satisfies:       []string{"ac-3"},
	}
}

func remedialFor(seq int, pkgs ...WorkPackage) Remediation {
	return Remediation{
		SchemaVersion: RemediationSchemaVersion,
		ProjectID:     reconTestIntent().ProjectID,
		Repairs:       []Repair{{CriterionID: "ac-3", Invalidation: seq}},
		Packages:      pkgs,
	}
}

// A. A delivery-owned criterion that is met can be invalidated through the
// governed operation, and the record says everything a later reader needs.
func TestAMetDeliveryOwnedCriterionCanBeInvalidated(t *testing.T) {
	ev, _, _ := assessComplete(t)
	rec := reconTestRecord()

	out, err := rec.Invalidate("ac-3", ev, "Jon Pratten",
		"the report has no mixed type; a column of ints and text is reported integer",
		"https://github.com/CorsolvSolutions/reconciliation-probe/issues/12", reconAt)
	if err != nil {
		t.Fatalf("invalidating a met delivery-owned criterion: %v", err)
	}

	if len(out.Invalidations) != 1 {
		t.Fatalf("invalidations = %d, want 1", len(out.Invalidations))
	}
	inv := out.Invalidations[0]
	switch {
	case inv.Seq != 1:
		t.Fatalf("Seq = %d, want 1", inv.Seq)
	case inv.CriterionID != "ac-3":
		t.Fatalf("CriterionID = %q", inv.CriterionID)
	case inv.By != "Jon Pratten":
		t.Fatalf("By = %q", inv.By)
	case !strings.Contains(inv.Reason, "no mixed type"):
		t.Fatalf("Reason = %q", inv.Reason)
	case !strings.Contains(inv.Evidence, "issues/12"):
		t.Fatalf("Evidence = %q", inv.Evidence)
	case inv.PreviousState != CriterionMet:
		t.Fatalf("PreviousState = %q, want %q", inv.PreviousState, CriterionMet)
	case !inv.At.Equal(reconAt):
		t.Fatalf("At = %v, want %v", inv.At, reconAt)
	}
}

// B. Actor, reason and evidence are each required, and each refusal says which.
func TestInvalidationRequiresAnActorAReasonAndEvidence(t *testing.T) {
	ev, _, _ := assessComplete(t)
	rec := reconTestRecord()

	for _, tc := range []struct {
		name                 string
		by, reason, evidence string
		wantIn               string
	}{
		{"no actor", "", "r", "e", "requires the name of the actor"},
		{"blank actor", "   ", "r", "e", "requires the name of the actor"},
		{"no reason", "Jon", "", "e", "requires a reason"},
		{"no evidence", "Jon", "r", "", "requires evidence"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := rec.Invalidate("ac-3", ev, tc.by, tc.reason, tc.evidence, reconAt)
			if !errors.Is(err, ErrRecordConflict) {
				t.Fatalf("err = %v, want ErrRecordConflict", err)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Fatalf("refusal does not say why: %v", err)
			}
			if len(out.Invalidations) != 0 {
				t.Fatal("a refused invalidation reached the record")
			}
		})
	}
}

// C, D, P. Invalidation is append-only: nothing already in the record is
// removed or rewritten, and the finding lands beside the history rather than
// over it.
func TestInvalidationIsAppendOnly(t *testing.T) {
	ev, _, _ := assessComplete(t)
	rec := reconTestRecord()
	rec.Acceptances = []Acceptance{{CriterionID: "ac-human", By: "Jon", At: reconAt.Add(-2 * time.Hour)}}
	before, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}

	out, err := rec.Invalidate("ac-3", ev, "Jon", "disproved", "issue-12", reconAt)
	if err != nil {
		t.Fatal(err)
	}

	// D. The record the invalidation was taken from is untouched — Invalidate is
	// a value method and returns a new record, so the caller's copy cannot have
	// been edited underneath it.
	after, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("Invalidate mutated the record it was given:\n%s\n%s", before, after)
	}

	// C. Everything that was there is still there.
	if len(out.Runs) != len(rec.Runs) || out.Runs[0].RunID != "run-1" {
		t.Fatalf("runs = %+v, want the original run", out.Runs)
	}
	if len(out.Acceptances) != 1 || out.Acceptances[0].By != "Jon" {
		t.Fatalf("acceptances = %+v — an invalidation may not remove one", out.Acceptances)
	}
	if out.IntentDigest != rec.IntentDigest {
		t.Fatal("an invalidation changed what was asked for")
	}

	// And a second finding, after the first is repaired, appends rather than
	// replaces: both are in the record, in the order they happened.
	repaired := ev
	second, err := out.Invalidate("ac-3", repaired, "Ada", "disproved again", "issue-19", reconAt.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Invalidations) != 2 {
		t.Fatalf("invalidations = %d, want 2", len(second.Invalidations))
	}
	if second.Invalidations[0].By != "Jon" || second.Invalidations[1].By != "Ada" {
		t.Fatalf("the earlier finding did not survive the later one: %+v", second.Invalidations)
	}
	if second.Invalidations[0].Seq != 1 || second.Invalidations[1].Seq != 2 {
		t.Fatalf("sequences = %d, %d — want 1, 2", second.Invalidations[0].Seq, second.Invalidations[1].Seq)
	}
}

// E, G. An invalidated criterion stops counting as met. Every other criterion,
// and every merged package, is untouched.
func TestAnInvalidatedCriterionStopsCountingAsMet(t *testing.T) {
	ev, in, plan := assessComplete(t)
	rec := reconTestRecord()
	out, err := rec.Invalidate("ac-3", ev, "Jon", "no mixed type is ever reported", "issue-12", reconAt)
	if err != nil {
		t.Fatal(err)
	}

	path := writeProjection(t, in.ProjectID, "abc123", completeRows()...)
	after, err := Assess(plan, in, path, nil, out.Invalidations)
	if err != nil {
		t.Fatal(err)
	}

	if after.Met {
		t.Fatal("the delivery is still assessed met over a disproved criterion")
	}
	// E.
	if containsPath(after.AcceptanceMet, "ac-3") {
		t.Fatalf("ac-3 still counts as met: %v", after.AcceptanceMet)
	}
	if !containsPath(after.AcceptanceOutstanding, "ac-3") {
		t.Fatalf("ac-3 is not outstanding either: %v", after.AcceptanceOutstanding)
	}
	// G. Unrelated criteria are exactly as they were.
	for _, id := range []string{"ac-1", "ac-2"} {
		if !containsPath(after.AcceptanceMet, id) {
			t.Fatalf("%s stopped being met: %v", id, after.AcceptanceMet)
		}
	}
	// H at this layer. No merged package was reopened.
	if len(after.CompletePackages) != 3 {
		t.Fatalf("CompletePackages = %v, want all three still complete", after.CompletePackages)
	}
	if len(after.OutstandingPackages) != 0 {
		t.Fatalf("OutstandingPackages = %v — an invalidation reopens no work", after.OutstandingPackages)
	}

	// J. And the assessment says WHY, in the finding's own words.
	if len(after.Invalidated) != 1 {
		t.Fatalf("Invalidated = %+v, want one entry", after.Invalidated)
	}
	got := after.Invalidated[0]
	if got.CriterionID != "ac-3" || got.By != "Jon" || got.Invalidation != 1 {
		t.Fatalf("Invalidated[0] = %+v", got)
	}
	if len(got.RemedialPackages) != 0 {
		t.Fatalf("RemedialPackages = %v, want none before remediation", got.RemedialPackages)
	}
	var said string
	for _, r := range after.Reasons {
		if strings.Contains(r, "ac-3 was met and is no longer") {
			said = r
		}
	}
	switch {
	case said == "":
		t.Fatalf("no reason explains why ac-3 is unmet: %v", after.Reasons)
	case !strings.Contains(said, "no mixed type is ever reported"):
		t.Fatalf("the reason omits the finding's reason: %q", said)
	case !strings.Contains(said, "issue-12"):
		t.Fatalf("the reason omits the evidence: %q", said)
	case !strings.Contains(said, "no remediation has been authorized"):
		t.Fatalf("the reason does not say nothing is repairing it: %q", said)
	}
}

// Q. Invalidating a criterion the project does not declare is refused.
func TestInvalidatingAnUnknownCriterionIsRefused(t *testing.T) {
	ev, _, _ := assessComplete(t)
	out, err := reconTestRecord().Invalidate("ac-99", ev, "Jon", "r", "e", reconAt)
	if !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("err = %v, want ErrRecordConflict", err)
	}
	if !strings.Contains(err.Error(), "declares no acceptance criterion") {
		t.Fatalf("refusal = %v", err)
	}
	if len(out.Invalidations) != 0 {
		t.Fatal("a finding was recorded against a criterion that does not exist")
	}
}

// R. Invalidating an already-invalidated criterion is deterministic, records
// nothing new, and names the finding that already stands.
func TestInvalidatingAnAlreadyInvalidatedCriterionIsDeterministic(t *testing.T) {
	ev, in, plan := assessComplete(t)
	rec := reconTestRecord()
	once, err := rec.Invalidate("ac-3", ev, "Jon", "no mixed type", "issue-12", reconAt)
	if err != nil {
		t.Fatal(err)
	}

	// The assessment as it stands AFTER the first finding — which is what a
	// second attempt would be made against.
	path := writeProjection(t, in.ProjectID, "abc123", completeRows()...)
	standing, err := Assess(plan, in, path, nil, once.Invalidations)
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		again, aerr := once.Invalidate("ac-3", standing, "Ada", "also broken", "issue-13", reconAt.Add(time.Hour))
		if !errors.Is(aerr, ErrRecordConflict) {
			t.Fatalf("attempt %d: err = %v, want ErrRecordConflict", i, aerr)
		}
		if !strings.Contains(aerr.Error(), "already invalidated") {
			t.Fatalf("attempt %d: refusal = %v", i, aerr)
		}
		// The standing finding is named, so the caller can act on it.
		if !strings.Contains(aerr.Error(), "invalidation 1") || !strings.Contains(aerr.Error(), "Jon") {
			t.Fatalf("attempt %d: the refusal does not name the standing finding: %v", i, aerr)
		}
		if len(again.Invalidations) != 1 {
			t.Fatalf("attempt %d: invalidations = %d — a refused attempt appended one", i, len(again.Invalidations))
		}
	}
}

// S. Human-owned acceptance semantics are unchanged: delivery does not withdraw
// a person's answer.
func TestAHumanOwnedCriterionIsNotDeliverysToInvalidate(t *testing.T) {
	in := reconTestIntent()
	in.Acceptance = append(in.Acceptance, Criterion{
		ID: "ac-sign", Statement: "A person accepts the release.", AcceptedBy: AcceptedByHuman,
	})
	rec := reconTestRecord()
	rec.Intent = in
	rec.IntentDigest = Digest(in)
	rec.Acceptances = []Acceptance{{CriterionID: "ac-sign", By: "Jon", At: reconAt.Add(-time.Hour)}}

	ev := Evidence{AcceptanceMet: []string{"ac-sign"}}
	out, err := rec.Invalidate("ac-sign", ev, "Ada", "they changed their mind", "email", reconAt)
	if !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("err = %v, want ErrRecordConflict", err)
	}
	if !strings.Contains(err.Error(), "a person's to accept") {
		t.Fatalf("refusal = %v", err)
	}
	if len(out.Invalidations) != 0 {
		t.Fatal("delivery recorded a finding against a person's answer")
	}
	// And the acceptance itself is untouched.
	if len(rec.Acceptances) != 1 {
		t.Fatal("the person's acceptance was disturbed")
	}
}

// A criterion nobody ever proved has no earlier conclusion to correct.
func TestInvalidatingACriterionThatIsNotMetIsRefused(t *testing.T) {
	in, plan := reconTestIntent(), reconTestPlan()
	// wp-types never merged, so ac-3 was never met.
	path := writeProjection(t, in.ProjectID, "abc123",
		[3]string{"wp-add", "merged", "met"},
		[3]string{"wp-multiply", "merged", "met"},
		[3]string{"wp-types", "planned", "not-met"})
	ev, err := Assess(plan, in, path, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	out, err := reconTestRecord().Invalidate("ac-3", ev, "Jon", "r", "e", reconAt)
	if !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("err = %v, want ErrRecordConflict", err)
	}
	if !strings.Contains(err.Error(), "not currently met") {
		t.Fatalf("refusal = %v", err)
	}
	if len(out.Invalidations) != 0 {
		t.Fatal("a finding was recorded against a criterion that was never met")
	}
}

// T. A project with no invalidation history assesses exactly as it did before
// this mechanism existed, and its record round-trips without gaining a field.
func TestARecordWithNoFindingsIsUnchanged(t *testing.T) {
	in, plan := reconTestIntent(), reconTestPlan()
	path := writeProjection(t, in.ProjectID, "abc123", completeRows()...)

	withNil, err := Assess(plan, in, path, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	withEmpty, err := Assess(plan, in, path, nil, []Invalidation{})
	if err != nil {
		t.Fatal(err)
	}
	if !withNil.Met || !withEmpty.Met {
		t.Fatalf("a delivery with no findings stopped being met: %v / %v", withNil.Reasons, withEmpty.Reasons)
	}
	if len(withNil.Invalidated) != 0 || len(withEmpty.Invalidated) != 0 {
		t.Fatal("a delivery with no findings reported one")
	}

	// The serialized record gains nothing: a portal or an operator reading
	// delivery.json for a project that has never had a finding sees the file it
	// has always seen.
	data, err := json.Marshal(reconTestRecord())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "invalidations") {
		t.Fatalf("a record with no findings serialized an invalidations key:\n%s", data)
	}
}

// K. Remediation is additive. The original plan file is never rewritten, and
// `Packages` still holds exactly what was planned first.
func TestRemediationIsAdditiveAndDoesNotReplaceThePlan(t *testing.T) {
	root := t.TempDir()
	in, plan := reconTestIntent(), reconTestPlan()
	if err := SavePlan(root, plan); err != nil {
		t.Fatal(err)
	}
	planBytes, err := os.ReadFile(PlanPath(root, in.ProjectID))
	if err != nil {
		t.Fatal(err)
	}

	ev, _, _ := assessComplete(t)
	rec := reconTestRecord()
	rec, err = rec.Invalidate("ac-3", ev, "Jon", "no mixed type", "issue-12", reconAt)
	if err != nil {
		t.Fatal(err)
	}

	authorized, err := AuthorizeRemediation(root, rec, plan, assessStanding(t, plan, rec),
		remedialFor(1, remedialPackage()), "Jon Pratten", reconAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("authorizing remediation: %v", err)
	}
	if authorized.Seq != 1 {
		t.Fatalf("Seq = %d, want 1", authorized.Seq)
	}

	// THE PROOF. The plan on disk is byte-identical to what was written before
	// any of this happened.
	afterBytes, err := os.ReadFile(PlanPath(root, in.ProjectID))
	if err != nil {
		t.Fatal(err)
	}
	if string(planBytes) != string(afterBytes) {
		t.Fatalf("the original plan was rewritten:\n--- before\n%s\n--- after\n%s", planBytes, afterBytes)
	}

	// The remediation is its own document beside it.
	if _, err := os.Stat(RemediationPath(root, in.ProjectID, 1)); err != nil {
		t.Fatalf("the remediation was not written as its own document: %v", err)
	}

	// And reading the plan back joins them without merging them.
	loaded, found, err := LoadPlan(root, in)
	if err != nil || !found {
		t.Fatalf("LoadPlan: %v (found=%v)", err, found)
	}
	if len(loaded.Packages) != 3 {
		t.Fatalf("Packages = %d, want the 3 originally planned", len(loaded.Packages))
	}
	if len(loaded.Remediations) != 1 || len(loaded.AllPackages()) != 4 {
		t.Fatalf("remediations = %d, all packages = %d, want 1 and 4",
			len(loaded.Remediations), len(loaded.AllPackages()))
	}
	for _, wp := range loaded.Packages {
		if wp.ID == "wp-types-fix" {
			t.Fatal("corrective work leaked into the original plan's packages")
		}
	}

	// SavePlan itself refuses to change it, so nothing else can either.
	changed := plan
	changed.PlannedBy = "someone-else"
	if err := SavePlan(root, changed); !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("SavePlan over an existing plan: %v, want ErrRecordConflict", err)
	}
}

// L. A remedial package may claim only the criteria its remediation repairs.
func TestARemedialPackageMayClaimOnlyTheCriteriaItRepairs(t *testing.T) {
	root := t.TempDir()
	in, plan := reconTestIntent(), reconTestPlan()
	if err := SavePlan(root, plan); err != nil {
		t.Fatal(err)
	}
	ev, _, _ := assessComplete(t)
	rec, err := reconTestRecord().Invalidate("ac-3", ev, "Jon", "no mixed type", "issue-12", reconAt)
	if err != nil {
		t.Fatal(err)
	}

	wide := remedialPackage()
	wide.Satisfies = []string{"ac-3", "ac-1"}

	_, err = AuthorizeRemediation(root, rec, plan, assessStanding(t, plan, rec), remedialFor(1, wide), "Jon", reconAt.Add(time.Hour))
	if !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("err = %v, want ErrPlanInvalid", err)
	}
	if !strings.Contains(err.Error(), "which this remediation does not repair") {
		t.Fatalf("refusal = %v", err)
	}
	if _, serr := os.Stat(RemediationPath(root, in.ProjectID, 1)); !os.IsNotExist(serr) {
		t.Fatal("a refused remediation was written to disk")
	}
}

// M. Plan fidelity applies to remediation, over the REMEDIAL packages alone. A
// repair that drops behaviors the criterion requires is refused even though the
// work it repairs named every one of them.
func TestRemediationMustCarryTheCriterionsRequiredBehaviours(t *testing.T) {
	root := t.TempDir()
	in, plan := reconTestIntent(), reconTestPlan()
	if err := SavePlan(root, plan); err != nil {
		t.Fatal(err)
	}
	ev, _, _ := assessComplete(t)
	rec, err := reconTestRecord().Invalidate("ac-3", ev, "Jon", "no mixed type", "issue-12", reconAt)
	if err != nil {
		t.Fatal(err)
	}

	narrow := remedialPackage()
	narrow.Objective = "Rewrite src/types.ts to infer text, integer and date columns."

	_, err = AuthorizeRemediation(root, rec, plan, assessStanding(t, plan, rec), remedialFor(1, narrow), "Jon", reconAt.Add(time.Hour))
	if !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("err = %v, want ErrPlanInvalid", err)
	}
	for _, want := range []string{`"decimal|number"`, `"boolean"`, `"mixed"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not name the dropped behavior %s: %v", want, err)
		}
	}
	// And it says why the original plan's coverage does not count for the repair.
	if !strings.Contains(err.Error(), "was disproved") {
		t.Fatalf("the refusal does not explain why coverage is not inherited: %v", err)
	}

	// A repair that declares no gate is refused for the same reason: the work
	// being repaired passed every gate IT declared.
	ungated := remedialPackage()
	ungated.Gates = nil
	_, err = AuthorizeRemediation(root, rec, plan, assessStanding(t, plan, rec), remedialFor(1, ungated), "Jon", reconAt.Add(time.Hour))
	if !errors.Is(err, ErrPlanInvalid) || !strings.Contains(err.Error(), "declares a gate") {
		t.Fatalf("an ungated repair was not refused for want of a gate: %v", err)
	}

	if _, serr := os.Stat(RemediationPath(root, in.ProjectID, 1)); !os.IsNotExist(serr) {
		t.Fatal("a refused remediation was written to disk")
	}
}

// Remediation repairs a RECORDED finding. Corrective work against a criterion
// nobody disproved has no authority behind it.
func TestRemediationIsRefusedWithoutAStandingFinding(t *testing.T) {
	root := t.TempDir()
	plan := reconTestPlan()
	if err := SavePlan(root, plan); err != nil {
		t.Fatal(err)
	}
	ev, _, _ := assessComplete(t)
	rec := reconTestRecord()

	_, err := AuthorizeRemediation(root, rec, plan, ev, remedialFor(1, remedialPackage()), "Jon", reconAt)
	if !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("err = %v, want ErrPlanInvalid", err)
	}
	if !strings.Contains(err.Error(), "has no standing invalidation") {
		t.Fatalf("refusal = %v", err)
	}

	// And a remediation naming the wrong finding is refused rather than silently
	// clearing whichever one is open.
	rec, err = rec.Invalidate("ac-3", ev, "Jon", "no mixed type", "issue-12", reconAt)
	if err != nil {
		t.Fatal(err)
	}
	_, err = AuthorizeRemediation(root, rec, plan, assessStanding(t, plan, rec), remedialFor(7, remedialPackage()), "Jon", reconAt)
	if !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("err = %v, want ErrPlanInvalid", err)
	}
	if !strings.Contains(err.Error(), "the standing finding against it is invalidation 1") {
		t.Fatalf("refusal = %v", err)
	}
}

// The engine owns the provenance of an authorization. A document that tries to
// name its own position, timestamp or author is refused rather than trusted.
func TestARemediationDoesNotAuthorizeItself(t *testing.T) {
	root := t.TempDir()
	plan := reconTestPlan()
	if err := SavePlan(root, plan); err != nil {
		t.Fatal(err)
	}
	ev, _, _ := assessComplete(t)
	rec, err := reconTestRecord().Invalidate("ac-3", ev, "Jon", "no mixed type", "issue-12", reconAt)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		mutate func(*Remediation)
		wantIn string
	}{
		{"its own seq", func(r *Remediation) { r.Seq = 4 }, "does not declare its own seq"},
		{"its own timestamp", func(r *Remediation) { r.AuthorizedAt = reconAt }, "does not declare its own authorizedAt"},
		{"its own author", func(r *Remediation) { r.AuthorizedBy = "itself" }, "does not declare its own authorizedBy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rm := remedialFor(1, remedialPackage())
			tc.mutate(&rm)
			_, aerr := AuthorizeRemediation(root, rec, plan, assessStanding(t, plan, rec), rm, "Jon", reconAt)
			if !errors.Is(aerr, ErrPlanInvalid) || !strings.Contains(aerr.Error(), tc.wantIn) {
				t.Fatalf("err = %v, want a refusal saying %q", aerr, tc.wantIn)
			}
		})
	}

	// And authorizing with no actor at all is refused.
	_, err = AuthorizeRemediation(root, rec, plan, assessStanding(t, plan, rec), remedialFor(1, remedialPackage()), "  ", reconAt)
	if !errors.Is(err, ErrPlanInvalid) || !strings.Contains(err.Error(), "the name of the actor") {
		t.Fatalf("err = %v, want a refusal for an unattributed authorization", err)
	}
}

// WRITER ISOLATION, both halves. A remedial package may take over a path the
// work it repairs owned — that is what repairing a file means — and two
// packages in the SAME generation still may not share one.
func TestWriterIsolationHoldsWithinAGenerationAndYieldsAcrossThem(t *testing.T) {
	in, plan := reconTestIntent(), reconTestPlan()

	// Across generations: wp-types-fix claims src/types.ts, which wp-types owns.
	acrossOK := plan
	acrossOK.Remediations = []Remediation{remedialFor(1, remedialPackage())}
	acrossOK.Remediations[0].AuthorizedBy = "Jon"
	acrossOK.Remediations[0].Seq = 1
	if err := acrossOK.Validate(in); err != nil {
		t.Fatalf("a repair may claim the path it repairs, got: %v", err)
	}

	// Within one generation: two remedial packages over one path is still a
	// collision, exactly as it always was.
	second := remedialPackage()
	second.ID = "wp-types-fix-2"
	collide := plan
	rm := remedialFor(1, remedialPackage(), second)
	rm.AuthorizedBy = "Jon"
	rm.Seq = 1
	collide.Remediations = []Remediation{rm}
	err := collide.Validate(in)
	if !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("err = %v, want ErrPlanInvalid", err)
	}
	if !strings.Contains(err.Error(), "is a collision, not a plan") {
		t.Fatalf("refusal = %v", err)
	}

	// And a remedial package may not reuse a merged package's id.
	dup := remedialPackage()
	dup.ID = "wp-types"
	reused := plan
	rmDup := remedialFor(1, dup)
	rmDup.AuthorizedBy = "Jon"
	rmDup.Seq = 1
	reused.Remediations = []Remediation{rmDup}
	if err := reused.Validate(in); !errors.Is(err, ErrPlanInvalid) ||
		!strings.Contains(err.Error(), `"wp-types" is duplicated`) {
		t.Fatalf("err = %v, want a duplicate-id refusal", err)
	}
}

// N, O, P. After the remedial work completes the criterion is met again, the
// delivery is complete again, and the whole sequence is still on the record.
func TestRemedialWorkMakesTheCriterionMetAgain(t *testing.T) {
	root := t.TempDir()
	in, plan := reconTestIntent(), reconTestPlan()
	if err := SavePlan(root, plan); err != nil {
		t.Fatal(err)
	}
	ev, _, _ := assessComplete(t)
	rec, err := reconTestRecord().Invalidate("ac-3", ev, "Jon", "no mixed type", "issue-12", reconAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveRecord(root, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := AuthorizeRemediation(root, rec, plan, assessStanding(t, plan, rec),
		remedialFor(1, remedialPackage()), "Jon", reconAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	loaded, _, err := LoadPlan(root, in)
	if err != nil {
		t.Fatal(err)
	}

	// While the remedial package is outstanding the criterion stays unmet, and
	// the assessment now names the work that is repairing it.
	path := writeProjection(t, in.ProjectID, "abc123",
		append(completeRows(), [3]string{"wp-types-fix", "planned", "not-met"})...)
	during, err := Assess(loaded, in, path, nil, rec.Invalidations)
	if err != nil {
		t.Fatal(err)
	}
	if during.Met {
		t.Fatal("the delivery is complete with its repair unfinished")
	}
	if len(during.Invalidated) != 1 ||
		strings.Join(during.Invalidated[0].RemedialPackages, ",") != "wp-types-fix" {
		t.Fatalf("Invalidated = %+v, want the repair named", during.Invalidated)
	}

	// N, O. The repair merges with its gate met.
	path = writeProjection(t, in.ProjectID, "def456",
		append(completeRows(), [3]string{"wp-types-fix", "merged", "met"})...)
	after, err := Assess(loaded, in, path, nil, rec.Invalidations)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Met {
		t.Fatalf("the repaired delivery is not complete: %v", after.Reasons)
	}
	if !containsPath(after.AcceptanceMet, "ac-3") {
		t.Fatalf("ac-3 is not met again: %v", after.AcceptanceOutstanding)
	}
	if len(after.Invalidated) != 0 {
		t.Fatalf("Invalidated = %+v — an answered finding is not a standing one", after.Invalidated)
	}
	if len(after.CompletePackages) != 4 {
		t.Fatalf("CompletePackages = %v, want all four", after.CompletePackages)
	}

	// P. And the audit trail is intact on disk: the original plan, the finding,
	// and the corrective work authorized to answer it.
	onDisk, _, err := LoadRecord(root, in.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(onDisk.Invalidations) != 1 || onDisk.Invalidations[0].By != "Jon" {
		t.Fatalf("the finding is not on the record: %+v", onDisk.Invalidations)
	}
	if len(onDisk.Runs) != 1 {
		t.Fatalf("the original run history is gone: %+v", onDisk.Runs)
	}
	raw, err := os.ReadFile(RemediationPath(root, in.ProjectID, 1))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"criterionId": "ac-3"`, `"invalidation": 1`, `"authorizedBy": "Jon"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("the remediation record is missing %s:\n%s", want, raw)
		}
	}
}

// A SECOND FINDING IS NOT CLEARED BY THE FIRST REPAIR.
//
// This is why a remediation names the invalidation it answers rather than just
// the criterion. Without it, a criterion disproved again after a repair would
// go on reporting met on the strength of work that predates the evidence
// against it.
func TestARepairDoesNotClearAFindingRaisedAfterIt(t *testing.T) {
	root := t.TempDir()
	in, plan := reconTestIntent(), reconTestPlan()
	if err := SavePlan(root, plan); err != nil {
		t.Fatal(err)
	}
	ev, _, _ := assessComplete(t)
	rec, err := reconTestRecord().Invalidate("ac-3", ev, "Jon", "no mixed type", "issue-12", reconAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AuthorizeRemediation(root, rec, plan, assessStanding(t, plan, rec),
		remedialFor(1, remedialPackage()), "Jon", reconAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := LoadPlan(root, in)
	if err != nil {
		t.Fatal(err)
	}

	// The repair merged and ac-3 was met again.
	repairedPath := writeProjection(t, in.ProjectID, "def456",
		append(completeRows(), [3]string{"wp-types-fix", "merged", "met"})...)
	repaired, err := Assess(loaded, in, repairedPath, nil, rec.Invalidations)
	if err != nil {
		t.Fatal(err)
	}
	if !repaired.Met {
		t.Fatalf("the repair did not take: %v", repaired.Reasons)
	}

	// And is disproved a second time.
	rec, err = rec.Invalidate("ac-3", repaired, "Ada", "dates are still read as text", "issue-19", reconAt.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("a second finding after a repair must be allowed: %v", err)
	}
	after, err := Assess(loaded, in, repairedPath, nil, rec.Invalidations)
	if err != nil {
		t.Fatal(err)
	}
	if after.Met {
		t.Fatal("the first repair cleared a finding raised after it")
	}
	if len(after.Invalidated) != 1 || after.Invalidated[0].Invalidation != 2 {
		t.Fatalf("Invalidated = %+v, want the second finding standing", after.Invalidated)
	}
	if len(after.Invalidated[0].RemedialPackages) != 0 {
		t.Fatalf("the earlier repair was credited to the later finding: %v", after.Invalidated[0].RemedialPackages)
	}
}

// A remediation is the durable authorization for work that may already have
// merged, so it is never rewritten.
func TestARemediationIsNeverRewritten(t *testing.T) {
	root := t.TempDir()
	in := reconTestIntent()
	rm := remedialFor(1, remedialPackage())
	rm.Seq = 1
	rm.AuthorizedBy = "Jon"
	rm.AuthorizedAt = reconAt
	if err := SaveRemediation(root, rm); err != nil {
		t.Fatal(err)
	}
	rm.AuthorizedBy = "somebody else"
	err := SaveRemediation(root, rm)
	if !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("err = %v, want ErrRecordConflict", err)
	}
	raw, rerr := os.ReadFile(RemediationPath(root, in.ProjectID, 1))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !strings.Contains(string(raw), `"authorizedBy": "Jon"`) {
		t.Fatalf("the authorization was overwritten:\n%s", raw)
	}
}

// A remediation whose file name and sequence disagree is refused rather than
// read: the file name is how the sequence is found, and a document that
// disagreed with its own name could be loaded in the wrong order.
func TestARemediationFileNameIsItsSequence(t *testing.T) {
	root := t.TempDir()
	in := reconTestIntent()
	rm := remedialFor(1, remedialPackage())
	rm.Seq = 3
	rm.AuthorizedBy = "Jon"
	rm.AuthorizedAt = reconAt
	data, err := json.MarshalIndent(rm, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, in.ProjectID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "remediation-001.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadRemediations(root, in.ProjectID); !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("err = %v, want ErrPlanInvalid", err)
	}
}

// assessStanding is the assessment as it stands AFTER a finding — which is what
// an authorization is made against, and what StandingInvalidations reads to
// decide a finding is unanswered. Handing it the assessment from before the
// finding would be asking whether a criterion that still counts as met needs
// repairing, and the answer to that is correctly no.
func assessStanding(t *testing.T, plan DeliveryPlan, rec Record) Evidence {
	t.Helper()
	path := writeProjection(t, rec.ProjectID, "abc123", completeRows()...)
	ev, err := Assess(plan, rec.Intent, path, rec.Acceptances, rec.Invalidations)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}
