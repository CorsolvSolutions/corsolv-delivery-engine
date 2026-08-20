package unattended

import (
	"context"
	"strings"
	"testing"
)

// A boundary the spec declares is knowledge the run already has. Publishing it
// as a check is what puts it in front of the person reading the report and in
// the map the queue holds tasks against.
func TestPreflightPublishesADeclaredBoundary(t *testing.T) {
	repo := newRepo(t, testOrigin)
	spec := specFor(repo, t.TempDir())
	spec.Boundaries = []KnownBoundary{{
		ID:     "acceptance.ac-2",
		Title:  "acceptance criterion ac-2 is a person's to answer",
		Detail: "A person accepts the release.",
		Action: "a person records their acceptance of ac-2",
	}}
	plan := minimalPlan()

	r := Preflight(context.Background(), spec, &plan)

	c, found := r.Check("acceptance.ac-2")
	if !found {
		t.Fatalf("the declared boundary was not published as a check\n%s", r)
	}
	if c.Outcome != OutcomeHumanBoundary {
		t.Fatalf("outcome = %q, want %q", c.Outcome, OutcomeHumanBoundary)
	}
	if c.Boundary != "a person records their acceptance of ac-2" {
		t.Fatalf("boundary action = %q, want the declared one", c.Boundary)
	}
	if got := r.Boundaries()["acceptance.ac-2"]; got == "" {
		t.Fatal("a published boundary must reach the map the queue is built with")
	}
	if r.Readiness != ReadyWithKnownHumanBoundary {
		t.Fatalf("readiness = %s, want %s — a named boundary is knowledge, not a refusal",
			r.Readiness, ReadyWithKnownHumanBoundary)
	}
	if !r.PermitsUnattendedRun() {
		t.Fatal("a known boundary must still permit the run")
	}
}

// A run that declares none is unchanged: no extra checks, and READY.
func TestPreflightPublishesNothingWhenNoBoundaryIsDeclared(t *testing.T) {
	repo := newRepo(t, testOrigin)
	plan := minimalPlan()

	r := Preflight(context.Background(), specFor(repo, t.TempDir()), &plan)

	if len(r.Checks.Boundaries()) != 0 {
		t.Fatalf("boundaries = %v, want none", r.Checks.Boundaries())
	}
	if r.Readiness != Ready {
		t.Fatalf("readiness = %s, want READY\n%s", r.Readiness, r)
	}
}

// A boundary the queue could hold a task against is worthless without the
// action a person must take, and a duplicate id would silently overwrite one
// boundary with another in that map.
func TestSpecRefusesABoundaryNothingCanBeDoneAbout(t *testing.T) {
	repo := newRepo(t, testOrigin)
	spec := specFor(repo, t.TempDir())
	spec.Boundaries = []KnownBoundary{
		{ID: "acceptance.ac-2", Title: "a person accepts ac-2"},
		{ID: "acceptance.ac-2", Title: "again", Action: "a person accepts ac-2"},
		{Action: "somebody does something"},
	}

	err := spec.Validate()
	if err == nil {
		t.Fatal("a boundary with no action, a duplicate id and a missing id must all be refused")
	}
	for _, want := range []string{"action", "duplicated", "id"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal must name %q, got: %v", want, err)
		}
	}
}
