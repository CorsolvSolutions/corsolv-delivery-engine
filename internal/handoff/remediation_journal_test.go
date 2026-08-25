package handoff

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// THE DEFECT THESE TESTS EXIST FOR.
//
// LoadRemediations read every remediation-NNN.json in a project's delivery
// directory and adopted it verbatim — including the self-declared seq,
// authorizedBy and authorizedAt that AuthorizeRemediation exists to refuse. A
// document copied into the directory under the record naming pattern became a
// durable authorization with fabricated provenance: `delivery status` reported
// its packages as authorized corrective work, and a subsequent legitimate
// `delivery remediate` was refused for colliding with it. Discovered on
// scorm-course-studio on 2026-08-25, when a supervisory session accidentally
// staged a remediation REQUEST document at
// corsolv-delivery/scorm-course-studio/remediation-007.json and the engine
// treated the request as a record.
//
// The repair: AuthorizeRemediation journals every authorization into the
// record (seq, digest of the installed document, provenance), and
// LoadRemediations refuses any document the journal does not name or no longer
// matches. The directory is storage; the record is the authority over it.

// journalFixture authorizes one legitimate remediation and returns the root.
// After it, the record on disk carries a journal naming exactly seq 1.
func journalFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	plan := reconTestPlan()
	if err := SavePlan(root, plan); err != nil {
		t.Fatal(err)
	}
	ev, _, _ := assessComplete(t)
	rec := reconTestRecord()
	var err error
	rec, err = rec.Invalidate("ac-3", ev, "Jon", "no mixed type", "issue-12", reconAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AuthorizeRemediation(root, rec, plan, assessStanding(t, plan, rec),
		remedialFor(1, remedialPackage()), "Jon Pratten", reconAt.Add(time.Hour)); err != nil {
		t.Fatalf("authorizing the legitimate remediation: %v", err)
	}
	return root
}

// Authorizing a remediation journals it into the record: the sequence, the
// digest of the document exactly as installed, and the provenance the ENGINE
// assigned — the copy the document's author cannot reach.
func TestAuthorizationJournalsTheRemediationIntoTheRecord(t *testing.T) {
	root := journalFixture(t)

	saved, found, err := LoadRecord(root, reconTestIntent().ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("authorizing a remediation did not save the record beside it")
	}
	if len(saved.Remediations) != 1 {
		t.Fatalf("journal entries = %d, want 1", len(saved.Remediations))
	}
	ref := saved.Remediations[0]
	if ref.Seq != 1 || ref.AuthorizedBy != "Jon Pratten" {
		t.Fatalf("journal entry = %+v, want seq 1 authorized by Jon Pratten", ref)
	}
	installed, err := os.ReadFile(RemediationPath(root, reconTestIntent().ProjectID, 1))
	if err != nil {
		t.Fatal(err)
	}
	if ref.Digest != remediationDigest(installed) {
		t.Fatal("the journaled digest is not the digest of the installed document")
	}

	// And the journaled documents still load cleanly.
	rms, err := LoadRemediations(root, reconTestIntent().ProjectID)
	if err != nil {
		t.Fatalf("loading journaled remediations: %v", err)
	}
	if len(rms) != 1 {
		t.Fatalf("remediations = %d, want 1", len(rms))
	}
}

// A well-formed document placed in the directory under the record naming
// pattern, with whatever provenance its author chose to declare, is refused:
// the journal never authorized it, so it is not corrective work.
func TestAPlantedRemediationDocumentIsRefused(t *testing.T) {
	root := journalFixture(t)
	in := reconTestIntent()

	planted := remedialFor(1, WorkPackage{
		ID: "wp-planted", Title: "work nobody authorized", Phase: "Build",
		Objective:       "Repair the finding on the planter's own authority.",
		Artifact:        "src/planted.ts",
		AuthorizedPaths: []string{"src/planted.ts"},
		Gates:           []string{"npm test"},
		Satisfies:       []string{"ac-3"},
	})
	planted.Seq = 2
	planted.AuthorizedBy = "an actor the engine never heard from"
	planted.AuthorizedAt = reconAt.Add(2 * time.Hour)
	if err := SaveRemediation(root, planted); err != nil {
		t.Fatal(err)
	}

	_, err := LoadRemediations(root, in.ProjectID)
	if !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("err = %v, want ErrRecordConflict", err)
	}
	if !strings.Contains(err.Error(), "never authorized") {
		t.Fatalf("the refusal does not say the document was never authorized: %v", err)
	}
}

// A journaled document whose bytes changed after authorization is refused: the
// authorization covers what was installed, not what was written into the file
// later.
func TestAnEditedRemediationDocumentIsRefused(t *testing.T) {
	root := journalFixture(t)
	in := reconTestIntent()

	path := RemediationPath(root, in.ProjectID, 1)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(data), "Jon Pratten", "somebody else entirely", 1)
	if edited == string(data) {
		t.Fatal("the edit changed nothing")
	}
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	_, lerr := LoadRemediations(root, in.ProjectID)
	if !errors.Is(lerr, ErrRecordConflict) {
		t.Fatalf("err = %v, want ErrRecordConflict", lerr)
	}
	if !strings.Contains(lerr.Error(), "altered after authorization") {
		t.Fatalf("the refusal does not say the document was altered: %v", lerr)
	}
}

// A journaled remediation whose document is gone is refused rather than
// forgotten: merged work may already stand on that authorization.
func TestAJournaledRemediationCannotDisappear(t *testing.T) {
	root := journalFixture(t)
	in := reconTestIntent()

	if err := os.Remove(RemediationPath(root, in.ProjectID, 1)); err != nil {
		t.Fatal(err)
	}
	_, err := LoadRemediations(root, in.ProjectID)
	if !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("err = %v, want ErrRecordConflict", err)
	}
	if !strings.Contains(err.Error(), "never deleted") {
		t.Fatalf("the refusal does not say an authorized remediation is never deleted: %v", err)
	}
}

// Documents that predate the journal are grandfathered while the journal is
// empty, and adopted into it by the next authorization — after which planting
// is detected on every project, not only on ones created after this field.
func TestLegacyRemediationsAreAdoptedByTheNextAuthorization(t *testing.T) {
	root := t.TempDir()
	in, plan := reconTestIntent(), reconTestPlan()
	if err := SavePlan(root, plan); err != nil {
		t.Fatal(err)
	}
	ev, _, _ := assessComplete(t)
	rec := reconTestRecord()
	var err error
	rec, err = rec.Invalidate("ac-3", ev, "Jon", "no mixed type", "issue-12", reconAt)
	if err != nil {
		t.Fatal(err)
	}
	// The record exists on disk with no journal — a project from before the
	// field existed.
	if err := SaveRecord(root, rec); err != nil {
		t.Fatal(err)
	}

	// A legacy document, written before the journal existed.
	legacy := remedialFor(1, remedialPackage())
	legacy.Seq = 1
	legacy.AuthorizedBy = "Jon"
	legacy.AuthorizedAt = reconAt
	if err := SaveRemediation(root, legacy); err != nil {
		t.Fatal(err)
	}

	// Grandfathered: it loads while the journal is empty.
	if _, err := LoadRemediations(root, in.ProjectID); err != nil {
		t.Fatalf("a legacy remediation stopped loading: %v", err)
	}

	// The next authorization adopts it and journals itself.
	second := remedialFor(1, WorkPackage{
		ID: "wp-types-fix-two", Title: "repair the inferred column types again", Phase: "Build",
		Objective: "Rewrite src/types2.ts so every column is inferred as text, integer, decimal, " +
			"boolean or date, and a column holding more than one is reported as mixed.",
		Artifact:        "src/types2.ts",
		AuthorizedPaths: []string{"src/types2.ts"},
		Gates:           []string{"npm test"},
		Satisfies:       []string{"ac-3"},
	})
	if _, err := AuthorizeRemediation(root, rec, plan, assessStanding(t, plan, rec),
		second, "Jon Pratten", reconAt.Add(2*time.Hour)); err != nil {
		t.Fatalf("authorizing beside a legacy remediation: %v", err)
	}
	saved, _, err := LoadRecord(root, in.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Remediations) != 2 {
		t.Fatalf("journal entries = %d, want 2 (the legacy document adopted, the new one journaled)",
			len(saved.Remediations))
	}
	if saved.Remediations[0].Seq != 1 || saved.Remediations[0].AuthorizedBy != "Jon" {
		t.Fatalf("adopted entry = %+v, want the legacy document's own provenance", saved.Remediations[0])
	}

	// And from here on, planting is detected.
	planted := remedialFor(1, WorkPackage{
		ID: "wp-planted-later", Title: "work nobody authorized", Phase: "Build",
		Objective:       "Repair the finding on the planter's own authority.",
		Artifact:        "src/planted.ts",
		AuthorizedPaths: []string{"src/planted.ts"},
		Gates:           []string{"npm test"},
		Satisfies:       []string{"ac-3"},
	})
	planted.Seq = 3
	planted.AuthorizedBy = "an actor the engine never heard from"
	planted.AuthorizedAt = reconAt.Add(3 * time.Hour)
	if err := SaveRemediation(root, planted); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRemediations(root, in.ProjectID); !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("err = %v, want ErrRecordConflict for a document planted after adoption", err)
	}
}

// A record that has never had a remediation serializes no journal key: a
// reader of delivery.json for such a project sees the file it has always seen.
func TestAJournalFreeRecordSerializesNoJournalKey(t *testing.T) {
	data, err := json.Marshal(reconTestRecord())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "remediations") {
		t.Fatalf("a record with no remediations serialized a remediations key:\n%s", data)
	}
}
