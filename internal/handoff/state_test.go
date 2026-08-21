package handoff

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeProjection writes a PROJECT-STATE.yml with the given task rows.
func writeProjection(t *testing.T, projectID, mainSha string, rows ...[3]string) string {
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

func TestAssessRequiresBothMergeAndGate(t *testing.T) {
	in := planIntent()
	plan := validPlan()

	cases := []struct {
		name     string
		rows     [][3]string
		mainSha  string
		wantMet  bool
		wantSaid string
	}{
		{
			name:    "both merged with met gates",
			rows:    [][3]string{{"wp-add", "merged", "met"}, {"wp-multiply", "merged", "met"}},
			mainSha: "abc123",
			wantMet: true,
		},
		{
			name:     "merged but the gate was never met",
			rows:     [][3]string{{"wp-add", "merged", "met"}, {"wp-multiply", "merged", "not-met"}},
			mainSha:  "abc123",
			wantMet:  false,
			wantSaid: "without a met completion gate",
		},
		{
			name:     "gate partially met is not met",
			rows:     [][3]string{{"wp-add", "merged", "met"}, {"wp-multiply", "merged", "partially-met"}},
			mainSha:  "abc123",
			wantMet:  false,
			wantSaid: "without a met completion gate",
		},
		{
			name:     "one package never reached the projection",
			rows:     [][3]string{{"wp-add", "merged", "met"}},
			mainSha:  "abc123",
			wantMet:  false,
			wantSaid: "wp-multiply",
		},
		{
			name:     "a blocked task blocks completion",
			rows:     [][3]string{{"wp-add", "merged", "met"}, {"wp-multiply", "blocked", "not-met"}},
			mainSha:  "abc123",
			wantMet:  false,
			wantSaid: "blocking work remains",
		},
		{
			name:     "everything merged but nothing accepted on main",
			rows:     [][3]string{{"wp-add", "merged", "met"}, {"wp-multiply", "merged", "met"}},
			mainSha:  "",
			wantMet:  false,
			wantSaid: "authoritative branch",
		},
		{
			name:     "pr-open is not complete",
			rows:     [][3]string{{"wp-add", "pr-open", "not-met"}, {"wp-multiply", "merged", "met"}},
			mainSha:  "abc123",
			wantMet:  false,
			wantSaid: "wp-add",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeProjection(t, in.ProjectID, tc.mainSha, tc.rows...)
			ev, err := Assess(plan, in, path, nil, nil)
			if err != nil {
				t.Fatalf("Assess: %v", err)
			}
			if ev.Met != tc.wantMet {
				t.Fatalf("Met = %v, want %v (reasons: %v)", ev.Met, tc.wantMet, ev.Reasons)
			}
			if tc.wantMet && len(ev.Reasons) != 0 {
				t.Fatalf("a met assessment must give no reasons, got: %v", ev.Reasons)
			}
			if tc.wantSaid != "" && !strings.Contains(strings.Join(ev.Reasons, " | "), tc.wantSaid) {
				t.Fatalf("expected a reason mentioning %q, got: %v", tc.wantSaid, ev.Reasons)
			}
		})
	}
}

// A delivery forbidden from merging must not be failed for not merging.
func TestAcceptedMainIsOnlyDemandedWhenMergeWasGranted(t *testing.T) {
	in := planIntent()
	in.Policy = Policy{
		NeedPush: true, NeedPR: true, NeedChecks: true, NeedMerge: false,
		MergeHumanAction: "the delivery owner merges",
	}
	path := writeProjection(t, in.ProjectID, "",
		[3]string{"wp-add", "merged", "met"}, [3]string{"wp-multiply", "merged", "met"})

	ev, err := Assess(validPlan(), in, path, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range ev.Reasons {
		if strings.Contains(r, "authoritative branch") {
			t.Fatalf("a delivery without merge authority must not be failed for not merging: %v", ev.Reasons)
		}
	}
}

// A criterion is met only when every package claiming it is complete.
func TestPartialAcceptanceCoverageIsNotMet(t *testing.T) {
	in := planIntent()
	plan := validPlan()
	// Both packages now claim ac-1; only one finishes.
	plan.Packages[1].Satisfies = []string{"ac-1", "ac-2"}

	path := writeProjection(t, in.ProjectID, "abc123",
		[3]string{"wp-add", "merged", "met"}, [3]string{"wp-multiply", "pr-open", "not-met"})

	ev, err := Assess(plan, in, path, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if contains(ev.AcceptanceMet, "ac-1") {
		t.Fatalf("ac-1 must not be met while a package claiming it is unfinished: %+v", ev)
	}
}

func TestMissingProjectionIsNotAnEmptyProject(t *testing.T) {
	in := planIntent()
	ev, err := Assess(validPlan(), in, filepath.Join(t.TempDir(), "absent.yml"), nil, nil)
	if err != nil {
		t.Fatalf("an absent projection is normal, got: %v", err)
	}
	if ev.Met {
		t.Fatal("no projection must never assess as complete")
	}
	if len(ev.OutstandingPackages) != 2 {
		t.Fatalf("every package must be outstanding, got %v", ev.OutstandingPackages)
	}
}

func TestUnreadableProjectionIsRefusedNotIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "PROJECT-STATE.yml")
	if err := os.WriteFile(path, []byte("this: [is not: valid yaml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Assess(validPlan(), planIntent(), path, nil, nil); !errors.Is(err, ErrProjectionUnreadable) {
		t.Fatalf("an unparseable projection must be refused, got: %v", err)
	}
}

func TestProjectionForAnotherProjectIsRefused(t *testing.T) {
	path := writeProjection(t, "someone-elses-project", "abc123",
		[3]string{"wp-add", "merged", "met"}, [3]string{"wp-multiply", "merged", "met"})
	if _, err := Assess(validPlan(), planIntent(), path, nil, nil); !errors.Is(err, ErrProjectionUnreadable) {
		t.Fatalf("a projection for another project must be refused, got: %v", err)
	}
}

// --- state derivation -------------------------------------------------------

func metEvidence() Evidence {
	return Evidence{Met: true, CompletePackages: []string{"wp-add", "wp-multiply"}}
}

func unmetEvidence(reason string) Evidence {
	return Evidence{Met: false, Reasons: []string{reason}}
}

func TestDeriveStates(t *testing.T) {
	now := at(0)
	plan := validPlan()

	cases := []struct {
		name      string
		admitted  bool
		planFound bool
		obs       RunObservation
		ev        Evidence
		want      DeliveryState
	}{
		{"no record", false, false, RunObservation{}, Evidence{}, StateNotStarted},
		{"admitted, no plan", true, false, RunObservation{}, unmetEvidence("not planned"), StatePlanning},
		{"plan ready, nothing running", true, true, RunObservation{}, unmetEvidence("nothing run"), StateQueued},
		{"a run holds the tree", true, true, RunObservation{Live: true, RunID: "r1", Stage: "dispatch"}, unmetEvidence("in flight"), StateRunning},
		{
			"a live run waiting on a person",
			true, true,
			RunObservation{Live: true, RunID: "r1", Boundaries: []string{"authenticate the forge"}},
			unmetEvidence("in flight"), StateBlocked,
		},
		{
			"a finished run that failed",
			true, true,
			RunObservation{Finished: true, Outcome: outcomeFailed, RunID: "r1"},
			unmetEvidence("stopped"), StateFailed,
		},
		{
			"a finished run at a human boundary",
			true, true,
			RunObservation{Finished: true, Outcome: outcomeBlockedHuman, Boundaries: []string{"merge the PR"}},
			unmetEvidence("awaiting merge"), StateBlocked,
		},
		{
			"evidence met",
			true, true,
			RunObservation{Finished: true, Outcome: outcomeCompleted},
			metEvidence(), StateCompleted,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Derive("p", tc.admitted, plan, tc.planFound, tc.obs, tc.ev, now)
			if got.State != tc.want {
				t.Fatalf("State = %q, want %q (detail: %s)", got.State, tc.want, got.Detail)
			}
			if strings.TrimSpace(got.Detail) == "" {
				t.Fatal("every state must carry a readable detail")
			}
		})
	}
}

// The claim this whole design exists to refuse: a run reporting success over a
// projection that does not support it.
func TestARunClaimingSuccessCannotCompleteWithoutEvidence(t *testing.T) {
	got := Derive("p", true, validPlan(), true,
		RunObservation{Finished: true, Outcome: outcomeCompleted, RunID: "r1"},
		unmetEvidence("wp-multiply reached the forge without a met completion gate"),
		at(0))

	if got.State == StateCompleted {
		t.Fatal("a run's own claim of success must never produce Completed")
	}
	if got.State != StateBlocked {
		t.Fatalf("State = %q, want blocked", got.State)
	}
	if !strings.Contains(got.Detail, "wp-multiply") {
		t.Fatalf("the detail must name what is missing, got: %s", got.Detail)
	}
}

// A stale completion event from a previous run must not answer a question about
// the run happening now.
func TestALiveRunOutranksAnOldCompletionEvent(t *testing.T) {
	got := Derive("p", true, validPlan(), true,
		RunObservation{Live: true, RunID: "r2", Stage: "workers", Finished: true, Outcome: outcomeCompleted},
		unmetEvidence("r2 still working"), at(0))

	if got.State != StateRunning {
		t.Fatalf("State = %q, want running", got.State)
	}
	if got.RunID != "r2" {
		t.Fatalf("RunID = %q, want the live run", got.RunID)
	}
}

// Evidence outranks everything, including a run still tidying up.
func TestMetEvidenceCompletesEvenWhileARunIsLive(t *testing.T) {
	got := Derive("p", true, validPlan(), true,
		RunObservation{Live: true, RunID: "r1", Stage: "evidence"}, metEvidence(), at(0))
	if got.State != StateCompleted {
		t.Fatalf("State = %q, want completed", got.State)
	}
}

func TestStatusCountsPackages(t *testing.T) {
	ev := Evidence{CompletePackages: []string{"wp-add"}}
	got := Derive("p", true, validPlan(), true,
		RunObservation{Live: true, RunID: "r1"}, ev, at(0))

	if got.PackagesTotal != 2 || got.PackagesComplete != 1 {
		t.Fatalf("counts = %d/%d, want 1/2", got.PackagesComplete, got.PackagesTotal)
	}
	if !strings.Contains(got.Detail, "1 of 2") {
		t.Fatalf("the running detail should carry the counts, got: %s", got.Detail)
	}
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// A delivery renders TWO documents, and they are not interchangeable.
//
// `ProjectionPath` is where the run publisher renders RUN progress: its rows are
// keyed by run task ids (`publish-wp-add`) and it carries no per-package
// completion gate, because the run layer does not adjudicate one.
//
// `DeliveryProjectionPath` is the delivery projection the driver's project stage
// renders from the forge and the control ledger, keyed by PACKAGE ids and
// carrying the gate. It is the document `stage_publish_projection` commits into
// the project's repository, so it is the one a consumer ever sees.
//
// Assess only ever understood the second — every case in this file is keyed by
// package id — and it was being handed the first. The result was a delivery that
// had merged all four packages with every gate met reporting "4 of 4 work
// packages are not complete", because no row could match by construction.
func TestTheDeliveryProjectionIsNotTheRunProgressProjection(t *testing.T) {
	host := HostProfile{DeliveryRoot: "/srv/delivery"}

	run := host.ProjectionPath("proj")
	delivery := host.DeliveryProjectionPath("proj")

	if run == delivery {
		t.Fatal("the run-progress and delivery projections must be distinct documents")
	}
	if filepath.Dir(delivery) != host.ProjectDir("proj") {
		t.Errorf("the delivery projection belongs beside the record, got %s", delivery)
	}
	if filepath.Dir(run) != host.StateDir("proj") {
		t.Errorf("the run-progress projection belongs in the run's state dir, got %s", run)
	}
}

// The failure this reproduces: run-task ids can never satisfy a package-keyed
// assessment, so reading the wrong document reports a complete delivery as
// entirely outstanding rather than reporting an error anyone would notice.
func TestAssessAgainstRunTaskIdsFindsNoPackage(t *testing.T) {
	in := planIntent()
	plan := validPlan()

	path := writeProjection(t, in.ProjectID, "abc123",
		[3]string{"publish-wp-add", "merged", "met"},
		[3]string{"publish-wp-multiply", "merged", "met"})

	ev, err := Assess(plan, in, path, nil, nil)
	if err != nil {
		t.Fatalf("Assess: %v", err)
	}
	if ev.Met {
		t.Fatal("run-task ids must not satisfy a package-keyed gate")
	}
	if len(ev.OutstandingPackages) != len(plan.Packages) {
		t.Fatalf("every package must read outstanding, got %v", ev.OutstandingPackages)
	}
}
