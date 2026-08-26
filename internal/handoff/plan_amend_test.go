package handoff

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE GAP THESE TESTS PIN. Once any package merges, whole-plan supersession
// correctly refuses — but a later, unmerged package whose authorized paths
// were drawn too tightly then had no governed correction at all, and finished
// gated work could never publish. AmendUnmergedPackages frees exactly the
// unmerged packages while the merged record stays byte-identical.

func amendStatus(merged ...string) Status {
	return Status{
		ProjectID: reconTestIntent().ProjectID,
		State:     StateFailed,
		Live:      false,
		Evidence:  Evidence{CompletePackages: merged},
	}
}

// An unmerged package may gain a path; the merged one is untouched; the prior
// plan is archived.
func TestAnUnmergedPackageMayBeAmended(t *testing.T) {
	root := t.TempDir()
	standing := reconTestPlan()
	if err := SavePlan(root, standing); err != nil {
		t.Fatal(err)
	}
	firstBytes, err := os.ReadFile(PlanPath(root, standing.ProjectID))
	if err != nil {
		t.Fatal(err)
	}
	if len(standing.Packages) < 2 {
		t.Skipf("fixture plan has %d packages; this test needs two", len(standing.Packages))
	}
	mergedID := standing.Packages[0].ID
	amendedID := standing.Packages[1].ID

	next := standing
	next.Packages = append([]WorkPackage(nil), standing.Packages...)
	next.Packages[1].AuthorizedPaths = append(
		append([]string(nil), next.Packages[1].AuthorizedPaths...), "src/newly/needed.ts")

	if err := AmendUnmergedPackages(root, standing, next, amendStatus(mergedID)); err != nil {
		t.Fatalf("amending an unmerged package: %v", err)
	}

	archived, err := os.ReadFile(filepath.Join(root, standing.ProjectID, SupersededPlanName(1)))
	if err != nil {
		t.Fatalf("the amended-away plan was not archived: %v", err)
	}
	if string(archived) != string(firstBytes) {
		t.Fatal("the archive is not byte-identical to the plan that was amended away")
	}

	loaded, _, err := LoadPlan(root, reconTestIntent())
	if err != nil {
		t.Fatal(err)
	}
	var got WorkPackage
	for _, wp := range loaded.Packages {
		if wp.ID == amendedID {
			got = wp
		}
	}
	found := false
	for _, p := range got.AuthorizedPaths {
		if p == "src/newly/needed.ts" {
			found = true
		}
	}
	if !found {
		t.Fatal("the amendment's new authorized path did not install")
	}
}

// A merged package must stay byte-identical: changing it refuses, and the
// standing plan is untouched.
func TestAMergedPackageCannotBeAmended(t *testing.T) {
	root := t.TempDir()
	standing := reconTestPlan()
	if err := SavePlan(root, standing); err != nil {
		t.Fatal(err)
	}
	mergedID := standing.Packages[0].ID

	next := standing
	next.Packages = append([]WorkPackage(nil), standing.Packages...)
	next.Packages[0].Title = "a different record of what merged"

	err := AmendUnmergedPackages(root, standing, next, amendStatus(mergedID))
	if !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("err = %v, want ErrRecordConflict", err)
	}
	if !strings.Contains(err.Error(), "does not move") {
		t.Fatalf("the refusal does not say the merged record does not move: %v", err)
	}
	loaded, _, lerr := LoadPlan(root, reconTestIntent())
	if lerr != nil {
		t.Fatal(lerr)
	}
	if loaded.Packages[0].Title != standing.Packages[0].Title {
		t.Fatal("a refused amendment changed the standing plan")
	}
}

// Dropping a package refuses: its beads may already exist, and work no plan
// describes is work nobody governs.
func TestAnAmendmentMayNotDropAPackage(t *testing.T) {
	root := t.TempDir()
	standing := reconTestPlan()
	if err := SavePlan(root, standing); err != nil {
		t.Fatal(err)
	}
	next := standing
	next.Packages = append([]WorkPackage(nil), standing.Packages[:len(standing.Packages)-1]...)

	err := AmendUnmergedPackages(root, standing, next, amendStatus())
	if !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("err = %v, want ErrRecordConflict", err)
	}
	if !strings.Contains(err.Error(), "drops package") {
		t.Fatalf("the refusal does not name the dropped package: %v", err)
	}
}

// A live run refuses: the amendment lands between runs.
func TestAnAmendmentRefusesUnderALiveRun(t *testing.T) {
	root := t.TempDir()
	standing := reconTestPlan()
	if err := SavePlan(root, standing); err != nil {
		t.Fatal(err)
	}
	st := amendStatus()
	st.Live = true
	if err := AmendUnmergedPackages(root, standing, standing, st); !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("err = %v, want ErrRecordConflict", err)
	}
}

// A package merged WITHOUT its gate is still merged history: it cannot change.
func TestAGateNotMetPackageIsStillMergedHistory(t *testing.T) {
	root := t.TempDir()
	standing := reconTestPlan()
	if err := SavePlan(root, standing); err != nil {
		t.Fatal(err)
	}
	st := amendStatus()
	st.Evidence.GateNotMet = []string{standing.Packages[0].ID}

	next := standing
	next.Packages = append([]WorkPackage(nil), standing.Packages...)
	next.Packages[0].Title = "changed"

	if err := AmendUnmergedPackages(root, standing, next, st); !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("err = %v, want ErrRecordConflict", err)
	}
}
