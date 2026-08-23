package handoff

import (
	"strings"
	"testing"
	"time"
)

// A CRITERION WHOSE EVIDENCE IS ALREADY MERGED IS REPAIRED BY CHECKING IT.
//
// Remediation had exactly one shape: a worker changes files, the controller
// commits, opens a pull request and merges. That is right whenever the repair is
// work, and impossible whenever it is not. On scorm-course-studio two authorized
// remedial packages named evidence that was already on main, so there was no
// diff for a worker to produce and publication refused exactly as it should —
// nothing was produced. The criterion could not be repaired at all, and the only
// way to force it through would have been to invent a change nobody needed.
//
// These pin the shape of the answer at the schema layer: what a verification
// package must say about itself, and what it is forbidden to be.

const mergedSha = "73e4eebbd715b355d932c0791d5ccf6c14296b49"

// verificationRemediation is corrective work that changes nothing and checks
// what is already there.
func verificationRemediation() Remediation {
	return Remediation{
		SchemaVersion: RemediationSchemaVersion,
		ProjectID:     planIntent().ProjectID,
		Seq:           1,
		Repairs:       []Repair{{CriterionID: "ac-1", Invalidation: 1}},
		AuthorizedBy:  "Jon Pratten",
		AuthorizedAt:  time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC),
		Packages: []WorkPackage{{
			ID: "wp-verify-add", Title: "the add evidence is checked", Phase: "Build",
			Objective: "Verify evidence/add-report.json against the contract at the commit that carries it.",
			Artifact:  "evidence/add-report.json",
			Gates:     []string{"npm test"},
			Satisfies: []string{"ac-1"},
			Verifies:  &Verification{MergedSha: mergedSha},
		}},
	}
}

// planWith returns the base plan carrying one remediation, which is how the
// engine composes what a run actually executes.
func planWith(rm Remediation) DeliveryPlan {
	p := validPlan()
	p.Remediations = []Remediation{rm}
	return p
}

func validateWith(t *testing.T, rm Remediation) error {
	t.Helper()
	return planWith(rm).Validate(planIntent())
}

func TestAVerificationPackageIsAccepted(t *testing.T) {
	if err := validateWith(t, verificationRemediation()); err != nil {
		t.Fatalf("a verification package must validate, got: %v", err)
	}
	wp := verificationRemediation().Packages[0]
	if !wp.IsVerification() {
		t.Error("a package naming a merged commit must report itself as a verification")
	}
	if validPlan().Packages[0].IsVerification() {
		t.Error("an ordinary mutation package must not report itself as a verification")
	}
}

// THE CONTAINMENT BOUNDARY IS INVERTED, NOT RELAXED. A mutation package must be
// able to change something or it cannot produce its result; a verification
// package must be able to change NOTHING, or the evidence it reports on could be
// evidence it wrote itself.
func TestAVerificationPackageMayAuthorizeNoPaths(t *testing.T) {
	rm := verificationRemediation()
	rm.Packages[0].AuthorizedPaths = []string{"evidence/add-report.json"}

	err := validateWith(t, rm)
	if err == nil {
		t.Fatal("a verification package that may write must be refused")
	}
	if !strings.Contains(err.Error(), "cannot be the check on what was written") {
		t.Errorf("the refusal must say why writing disqualifies it, got: %v", err)
	}
}

func TestAVerificationPackageMustNameAFullCommitAndSomeGates(t *testing.T) {
	for name, mutate := range map[string]func(*WorkPackage){
		"an abbreviated sha": func(wp *WorkPackage) { wp.Verifies.MergedSha = mergedSha[:9] },
		"no sha at all":      func(wp *WorkPackage) { wp.Verifies.MergedSha = "" },
		"a branch name":      func(wp *WorkPackage) { wp.Verifies.MergedSha = "main" },
		"no gates":           func(wp *WorkPackage) { wp.Gates = nil },
	} {
		t.Run(name, func(t *testing.T) {
			rm := verificationRemediation()
			mutate(&rm.Packages[0])
			if err := validateWith(t, rm); err == nil {
				t.Fatalf("a verification package with %s must be refused", name)
			}
		})
	}
}

// A verification merges nothing, so a dependency in either direction names a
// merge that can never happen.
func TestNothingMayWaitOnAVerificationAndAVerificationWaitsOnNothing(t *testing.T) {
	rm := verificationRemediation()
	rm.Packages[0].DependsOn = []string{"wp-add"}
	if err := validateWith(t, rm); err == nil {
		t.Fatal("a verification package that declares dependencies must be refused")
	}

	rm = verificationRemediation()
	rm.Packages = append(rm.Packages, WorkPackage{
		ID: "wp-after", Title: "waits on the check", Phase: "Build",
		Objective:       "Create src/after.ts once the evidence is verified.",
		Artifact:        "src/after.ts",
		AuthorizedPaths: []string{"src/after.ts"},
		DependsOn:       []string{"wp-verify-add"},
		Satisfies:       []string{"ac-1"},
	})
	err := validateWith(t, rm)
	if err == nil {
		t.Fatal("a package waiting on a verification's merge must be refused")
	}
	if !strings.Contains(err.Error(), "merges nothing") {
		t.Errorf("the refusal must name what cannot happen, got: %v", err)
	}
}

// There is nothing for a delivery's FIRST plan to verify, because none of its
// work has been done yet. Allowing one there would let a plan declare a
// criterion met by checking a commit that predates the project.
func TestTheORIGINALPlanMayNotCarryAVerificationPackage(t *testing.T) {
	p := validPlan()
	p.Packages = append(p.Packages, WorkPackage{
		ID: "wp-verify-add", Title: "the add evidence is checked", Phase: "Build",
		Objective: "Verify evidence/add-report.json.",
		Artifact:  "evidence/add-report.json",
		Gates:     []string{"npm test"},
		Satisfies: []string{"ac-1"},
		Verifies:  &Verification{MergedSha: mergedSha},
	})
	err := p.Validate(planIntent())
	if err == nil {
		t.Fatal("a verification package in the original plan must be refused")
	}
	if !strings.Contains(err.Error(), "none of its work has been done yet") {
		t.Errorf("the refusal must say why the first plan has nothing to verify, got: %v", err)
	}
}

// THE RUN SHAPE IS THE RECORD. A verification compiles to ONE task named for
// what it does, and the projection waits for that task rather than for a
// publication the run does not contain.
func TestAVerificationCompilesToOneVerifyTaskAndNoPublication(t *testing.T) {
	in := planIntent()
	plan := planWith(verificationRemediation())
	host := HostProfile{DeliveryRoot: t.TempDir(), Driver: "/engine/driver.sh", Provider: "claude"}

	_, work, err := Compile(in, plan, host, "run-1")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	byID := map[string]bool{}
	for _, task := range work.Tasks {
		byID[task.ID] = true
	}
	if !byID["verify-wp-verify-add"] {
		t.Fatalf("a verification package compiled to no verify task; tasks: %v", taskIDs(work))
	}
	for _, absent := range []string{"await-wp-verify-add", "publish-wp-verify-add"} {
		if byID[absent] {
			t.Errorf("a verification package compiled a %s task — it has no worker to wait for and nothing to publish", absent)
		}
	}
	// The ordinary packages are untouched.
	for _, want := range []string{"await-wp-add", "publish-wp-add", "await-wp-multiply", "publish-wp-multiply"} {
		if !byID[want] {
			t.Errorf("mutation package task %s went missing; tasks: %v", want, taskIDs(work))
		}
	}

	for _, task := range work.Tasks {
		switch task.ID {
		case "verify-wp-verify-add":
			if task.Mutates {
				t.Error("a verification task declared that it mutates — it changes nothing")
			}
			if task.DeliveryStatus != "verified" {
				t.Errorf("a verification task projects %q, want verified — a reader who cannot tell it from a merge has been told the wrong thing", task.DeliveryStatus)
			}
		case StageProject:
			var waitsForVerify bool
			for _, need := range task.Needs {
				if need == "publish-wp-verify-add" {
					t.Error("the projection waits for a publication the run does not contain")
				}
				if need == "verify-wp-verify-add" {
					waitsForVerify = true
				}
			}
			if !waitsForVerify {
				t.Errorf("the projection does not wait for the check that repairs the criterion; needs: %v", task.Needs)
			}
		}
	}
}

// AND ORDINARY CORRECTIVE WORK IS COMPLETELY UNCHANGED. A remediation whose
// repair really is work still authorizes paths, still names an artifact it will
// produce, and still runs the worker → branch → PR → merge lifecycle.
func TestOrdinaryMutationRemediationIsUnchanged(t *testing.T) {
	rm := verificationRemediation()
	rm.Packages = []WorkPackage{{
		ID: "wp-fix-add", Title: "repair add", Phase: "Build",
		Objective:       "Rewrite src/add.ts so the contract actually holds.",
		Artifact:        "src/add.ts",
		AuthorizedPaths: []string{"src/add.ts"},
		Gates:           []string{"npm test"},
		Satisfies:       []string{"ac-1"},
	}}
	if err := validateWith(t, rm); err != nil {
		t.Fatalf("ordinary corrective work must still validate, got: %v", err)
	}
	if rm.Packages[0].IsVerification() {
		t.Error("ordinary corrective work must not be treated as a verification")
	}
}
