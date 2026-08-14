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

func at(minute int) time.Time {
	return time.Date(2026, 8, 14, 9, minute, 0, 0, time.UTC)
}

func TestAdmitCreatesADeliveryOnce(t *testing.T) {
	root := t.TempDir()
	in := validIntent()

	first, err := Admit(root, in, at(0))
	if err != nil {
		t.Fatalf("first Admit: %v", err)
	}
	if !first.Created || first.AlreadyStarted {
		t.Fatalf("the first Start must create: %+v", first)
	}
	if err := SaveRecord(root, first.Record); err != nil {
		t.Fatalf("SaveRecord: %v", err)
	}

	second, err := Admit(root, in, at(1))
	if err != nil {
		t.Fatalf("second Admit must be accepted as a no-op, got: %v", err)
	}
	if second.Created {
		t.Fatal("a repeated Start must not create a second delivery")
	}
	if !second.AlreadyStarted {
		t.Fatal("a repeated Start must report the delivery as already started")
	}
	if second.Record.CreatedAt != first.Record.CreatedAt {
		t.Fatalf("the repeated Start rewrote the creation time: %v -> %v",
			first.Record.CreatedAt, second.Record.CreatedAt)
	}
}

// Provenance changes are not term changes: pressing Start again tomorrow, or
// from a different user, is the same request.
func TestRepeatedStartIgnoresProvenance(t *testing.T) {
	root := t.TempDir()
	in := validIntent()
	first, err := Admit(root, in, at(0))
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveRecord(root, first.Record); err != nil {
		t.Fatal(err)
	}

	again := in
	again.RequestedBy = "someone-else"
	again.RequestedAt = at(30)

	got, err := Admit(root, again, at(30))
	if err != nil {
		t.Fatalf("a Start differing only in provenance must be a no-op, got: %v", err)
	}
	if !got.AlreadyStarted {
		t.Fatal("expected the existing delivery to be reported")
	}
}

func TestAdmitRefusesContradictoryTerms(t *testing.T) {
	root := t.TempDir()
	in := validIntent()
	first, err := Admit(root, in, at(0))
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveRecord(root, first.Record); err != nil {
		t.Fatal(err)
	}

	changes := []struct {
		name   string
		mutate func(*Intent)
	}{
		{"a different objective", func(i *Intent) { i.Objective = "Something else entirely." }},
		{"a different repository", func(i *Intent) {
			i.Repository.Slug = "CorsolvSolutions/other"
			i.Repository.Origin = "https://github.com/CorsolvSolutions/other.git"
		}},
		{"a different checkout", func(i *Intent) { i.Checkout = `D:\Development\elsewhere` }},
		{"different acceptance", func(i *Intent) {
			i.Acceptance = []Criterion{{ID: "ac-9", Statement: "Something nobody agreed."}}
		}},
		{"a narrowed policy", func(i *Intent) {
			i.Policy.NeedMerge = false
			i.Policy.MergeHumanAction = "the delivery owner merges by hand"
		}},
	}

	for _, tc := range changes {
		t.Run(tc.name, func(t *testing.T) {
			changed := validIntent()
			tc.mutate(&changed)
			_, err := Admit(root, changed, at(5))
			if !errors.Is(err, ErrRecordConflict) {
				t.Fatalf("expected ErrRecordConflict for %s, got: %v", tc.name, err)
			}
			if !strings.Contains(err.Error(), "different terms") {
				t.Fatalf("the refusal must say the terms differ, got: %v", err)
			}
		})
	}
}

// Acceptance order is presentation, not terms.
func TestDigestIsStableUnderAcceptanceOrder(t *testing.T) {
	a := validIntent()
	a.Acceptance = []Criterion{
		{ID: "ac-1", Statement: "one"},
		{ID: "ac-2", Statement: "two"},
	}
	b := validIntent()
	b.Acceptance = []Criterion{
		{ID: "ac-2", Statement: "two"},
		{ID: "ac-1", Statement: "one"},
	}
	if Digest(a) != Digest(b) {
		t.Fatal("acceptance ordering must not change the delivery terms")
	}
}

// The cross-project failure in its most dangerous form: one project's record
// found under another project's directory.
func TestRecordUnderTheWrongProjectIsRefused(t *testing.T) {
	root := t.TempDir()
	in := validIntent()
	adm, err := Admit(root, in, at(0))
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveRecord(root, adm.Record); err != nil {
		t.Fatal(err)
	}

	// Same bytes, filed under a different project's directory.
	data, err := os.ReadFile(RecordPath(root, in.ProjectID))
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "some-other-project")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, RecordName), data, 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err = LoadRecord(root, "some-other-project")
	if !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("a record naming another project must be refused, got: %v", err)
	}
}

func TestTamperedRecordIsRefused(t *testing.T) {
	root := t.TempDir()
	in := validIntent()
	adm, err := Admit(root, in, at(0))
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveRecord(root, adm.Record); err != nil {
		t.Fatal(err)
	}

	path := RecordPath(root, in.ProjectID)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	intent := raw["intent"].(map[string]any)
	intent["objective"] = "quietly replaced after the fact"
	tampered, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, tampered, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := LoadRecord(root, in.ProjectID); !errors.Is(err, ErrRecordCorrupt) {
		t.Fatalf("a record whose intent no longer matches its digest must be refused, got: %v", err)
	}
}

func TestUnknownRecordSchemaIsRefused(t *testing.T) {
	root := t.TempDir()
	in := validIntent()
	adm, _ := Admit(root, in, at(0))
	r := adm.Record
	r.SchemaVersion = RecordSchemaVersion + 1
	// Written directly: SaveRecord does not police the version it is given, the
	// reader does, which is the side that matters after an engine downgrade.
	path := RecordPath(root, in.ProjectID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(r)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := LoadRecord(root, in.ProjectID); !errors.Is(err, ErrRecordCorrupt) {
		t.Fatalf("a future record schema must be refused, got: %v", err)
	}
}

func TestMissingRecordIsAbsenceNotFailure(t *testing.T) {
	root := t.TempDir()
	r, found, err := LoadRecord(root, "never-started")
	if err != nil {
		t.Fatalf("a project with no delivery must not be an error, got: %v", err)
	}
	if found {
		t.Fatalf("expected absence, got %+v", r)
	}
}

func TestAdmitRefusesAnInvalidIntentBeforeTouchingDisk(t *testing.T) {
	root := t.TempDir()
	in := validIntent()
	in.Objective = ""

	if _, err := Admit(root, in, at(0)); !errors.Is(err, ErrIntentInvalid) {
		t.Fatalf("expected ErrIntentInvalid, got: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("a refused Start must leave nothing behind, found: %v", entries)
	}
}

func TestRunsAreAppendOnly(t *testing.T) {
	root := t.TempDir()
	adm, err := Admit(root, validIntent(), at(0))
	if err != nil {
		t.Fatal(err)
	}
	r := adm.Record.AppendRun("run-1", ReasonInitial, at(1))
	r = r.AppendRun("run-2", ReasonResumed, at(2))

	if len(r.Runs) != 2 {
		t.Fatalf("expected both runs retained, got %d", len(r.Runs))
	}
	if r.Runs[0].RunID != "run-1" || r.Runs[0].Reason != ReasonInitial {
		t.Fatalf("the first run was rewritten: %+v", r.Runs[0])
	}
	latest, ok := r.LatestRun()
	if !ok || latest.RunID != "run-2" || latest.Reason != ReasonResumed {
		t.Fatalf("LatestRun = %+v, %v", latest, ok)
	}

	if err := SaveRecord(root, r); err != nil {
		t.Fatal(err)
	}
	reloaded, found, err := LoadRecord(root, r.ProjectID)
	if err != nil || !found {
		t.Fatalf("reload: %v, %v", err, found)
	}
	if len(reloaded.Runs) != 2 {
		t.Fatalf("run history did not survive the round trip: %+v", reloaded.Runs)
	}
}

func TestPlanRoundTripsAndIsRevalidatedOnRead(t *testing.T) {
	root := t.TempDir()
	in := planIntent()
	if err := SavePlan(root, validPlan()); err != nil {
		t.Fatal(err)
	}

	got, found, err := LoadPlan(root, in)
	if err != nil || !found {
		t.Fatalf("LoadPlan: %v, %v", err, found)
	}
	if len(got.Packages) != 2 {
		t.Fatalf("expected both packages, got %d", len(got.Packages))
	}

	// The intent moving out from under a stored plan is exactly the case
	// re-validation exists for.
	drifted := in
	drifted.Acceptance = append(drifted.Acceptance, Criterion{ID: "ac-3", Statement: "added later"})
	if _, _, err := LoadPlan(root, drifted); !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("a plan that no longer covers the intent must be refused on read, got: %v", err)
	}
}

func TestSanitizeProjectIDStopsTraversal(t *testing.T) {
	for _, bad := range []string{"..", ".", "a/b", `a\b`, "../../etc", "", "UPPER"} {
		if err := SanitizeProjectID(bad); err == nil {
			t.Errorf("project id %q must be refused", bad)
		}
	}
	if err := SanitizeProjectID("corsolv-managed-delivery-test-1"); err != nil {
		t.Errorf("a normal project id must be accepted, got: %v", err)
	}
}
