package unattended

import (
	"context"
	"strings"
	"testing"
)

func specFor(dir, stateDir string) Spec {
	return Spec{
		ProjectID: "corsolv-delivery-engine",
		StateDir:  stateDir,
		Ownership: Ownership{
			ProjectID: "corsolv-delivery-engine", Worktree: dir,
			ExpectedOrigin: testOrigin, ExpectedBranch: "main",
			Role: RoleWriter, Session: "preflight-test",
		},
	}
}

func minimalPlan() Plan {
	return Plan{RunID: "run-preflight", Tasks: []Task{
		{ID: "primary", Title: "primary", Band: BandPrimary, Argv: []string{"true"}},
		{ID: "docs", Title: "docs", Band: BandDocumentation, Argv: []string{"true"}},
	}}
}

func TestPreflightOnAHealthyWorktreeIsReady(t *testing.T) {
	repo := newRepo(t, testOrigin)
	spec := specFor(repo, t.TempDir())
	plan := minimalPlan()

	r := Preflight(context.Background(), spec, &plan)
	if r.Readiness != Ready {
		t.Fatalf("readiness = %s, want READY\n%s", r.Readiness, r)
	}
	if !r.PermitsUnattendedRun() {
		t.Fatal("a READY verdict must permit a run")
	}
	if r.RunID != plan.RunID {
		t.Fatalf("report run id = %q, want %q", r.RunID, plan.RunID)
	}
}

func TestPreflightWithNoPlanReportsNotReachedNotReady(t *testing.T) {
	// A run whose plan could not be read has not been shown to have anywhere to
	// go, and NOT REACHED is the honest way to say so.
	repo := newRepo(t, testOrigin)
	r := Preflight(context.Background(), specFor(repo, t.TempDir()), nil)

	if r.Readiness != NotReady {
		t.Fatalf("readiness = %s, want NOT-READY", r.Readiness)
	}
	for _, id := range []string{"plan.work", "plan.fallback"} {
		c, ok := r.Check(id)
		if !ok || c.Outcome != OutcomeNotReached {
			t.Fatalf("%s = %v, want not-reached", id, c.Outcome)
		}
	}
}

func TestPreflightRefusesAPlanWithNoFallbackWork(t *testing.T) {
	// A queue with nothing below the primary band is the exact shape of "the run
	// stopped at its first dependency".
	repo := newRepo(t, testOrigin)
	plan := Plan{RunID: "r", Tasks: []Task{
		{ID: "only", Title: "only", Band: BandPrimary, Argv: []string{"true"}},
	}}
	r := Preflight(context.Background(), specFor(repo, t.TempDir()), &plan)

	c, _ := r.Check("plan.fallback")
	if c.Outcome != OutcomeFail {
		t.Fatalf("all-primary plan = %s, want fail", c.Outcome)
	}
}

func TestPreflightRefusesAStateDirInsideTheMutableWorktree(t *testing.T) {
	// A checkout, a cleanliness check or a branch switch would otherwise touch
	// the run's own record of what it was doing.
	repo := newRepo(t, testOrigin)
	spec := specFor(repo, repo+"/.run-state")
	plan := minimalPlan()

	r := Preflight(context.Background(), spec, &plan)
	c, _ := r.Check("state.dir")
	if c.Outcome != OutcomeFail {
		t.Fatalf("state dir inside the worktree = %s, want fail", c.Outcome)
	}
	if !strings.Contains(c.Remedy, "outside") {
		t.Fatalf("the remedy must say where to move it: %q", c.Remedy)
	}
}

func TestPreflightDetectsALiveCompetingWriter(t *testing.T) {
	repo := newRepo(t, testOrigin)
	state, err := ProbeRepo(repo)
	if err != nil {
		t.Fatal(err)
	}
	lk, err := Acquire(WriterLockDir(state), Owner{
		RunID: "somebody-else", ProjectID: "p", Session: "other", Worktree: repo, Role: RoleWriter,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Release() //nolint:errcheck

	plan := minimalPlan()
	r := Preflight(context.Background(), specFor(repo, t.TempDir()), &plan)

	c, _ := r.Check("concurrency.writer")
	if c.Outcome != OutcomeFail {
		t.Fatalf("live competing writer = %s, want fail", c.Outcome)
	}
	if !strings.Contains(c.Observed, "somebody-else") {
		t.Fatalf("the check must name the holder: %q", c.Observed)
	}
	if r.PermitsUnattendedRun() {
		t.Fatal("a run must not start into a worktree somebody else is holding")
	}
}

func TestPreflightTreatsAStaleRecordAsRecoverableNotBlocking(t *testing.T) {
	// The crash-recovery case. A dead run's record outlives it; a check that
	// treated the record as authority would make every crashed run
	// unrestartable until a person deleted a file.
	repo := newRepo(t, testOrigin)
	state, err := ProbeRepo(repo)
	if err != nil {
		t.Fatal(err)
	}
	lk, err := Acquire(WriterLockDir(state), Owner{
		RunID: "a-run-that-died", ProjectID: "p", Session: "dead", Worktree: repo, Role: RoleWriter,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Drop the lock the way a crash does — the OS releases it — while leaving
	// the record behind.
	if err := unlockFile(lk.file); err != nil {
		t.Fatal(err)
	}
	lk.file.Close() //nolint:errcheck
	lk.file = nil

	plan := minimalPlan()
	r := Preflight(context.Background(), specFor(repo, t.TempDir()), &plan)

	c, _ := r.Check("concurrency.writer")
	if c.Outcome != OutcomePass {
		t.Fatalf("stale record with no live lock = %s, want pass — a crashed run must be restartable", c.Outcome)
	}
	if !strings.Contains(c.Observed, "stale") || !strings.Contains(c.Observed, "a-run-that-died") {
		t.Fatalf("the check must say the record is stale and whose it was: %q", c.Observed)
	}
	if r.Readiness != Ready {
		t.Fatalf("readiness = %s, want READY\n%s", r.Readiness, r)
	}
}

func TestProbeOwnerDistinguishesARecordFromAHolder(t *testing.T) {
	dir := t.TempDir()

	_, recorded, live, err := ProbeOwner(dir)
	if err != nil || recorded || live {
		t.Fatalf("empty dir: recorded=%v live=%v err=%v", recorded, live, err)
	}

	lk, err := Acquire(dir, testOwner(RoleWriter, "holder"))
	if err != nil {
		t.Fatal(err)
	}
	owner, recorded, live, err := ProbeOwner(dir)
	if err != nil || !recorded || !live {
		t.Fatalf("held lock: recorded=%v live=%v err=%v", recorded, live, err)
	}
	if owner.RunID != "holder" {
		t.Fatalf("owner = %q, want holder", owner.RunID)
	}

	// Probing must not have disturbed the holder.
	if _, err := Acquire(dir, testOwner(RoleWriter, "intruder")); err == nil {
		t.Fatal("probing released a lock it was only supposed to look at")
	}
	lk.Release() //nolint:errcheck

	_, recorded, live, err = ProbeOwner(dir)
	if err != nil || recorded || live {
		t.Fatalf("after release: recorded=%v live=%v err=%v", recorded, live, err)
	}
}

func TestReportRendersEverythingAPersonNeedsAtThreeInTheMorning(t *testing.T) {
	repo := newRepo(t, testOrigin)
	spec := specFor(repo, t.TempDir())
	spec.Credentials = []CredentialRequirement{{
		ID: "a-token", Title: "a token", Env: "GC_UNATTENDED_DEFINITELY_UNSET",
		HumanAction: "obtain the token from the platform team",
	}}
	plan := minimalPlan()

	r := Preflight(context.Background(), spec, &plan)
	body := r.String()
	for _, want := range []string{
		"UNATTENDED PREFLIGHT", "VERDICT:", string(ReadyWithKnownHumanBoundary),
		"Known human boundaries", "obtain the token from the platform team",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("the report omits %q:\n%s", want, body)
		}
	}
	if data, err := r.JSON(); err != nil || len(data) == 0 {
		t.Fatalf("the report must render as JSON for durable evidence: %v", err)
	}
}
