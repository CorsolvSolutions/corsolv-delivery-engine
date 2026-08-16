package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/handoff"
	"github.com/gastownhall/gascity/internal/unattended"
)

// A delivery whose planner died must not read as one still thinking.
//
// Planning happens before the run layer starts, so a failure there leaves
// nothing for the portal to read, and "planning" is both indistinguishable from
// progress and permanent. This is the case the live run actually hit: the agent
// runtime reported an exhausted spend limit, the run exited, and the project sat
// at Planning with nothing anywhere saying why.
func TestAPlanningFailureIsPublishedAsAHumanBoundary(t *testing.T) {
	host := handoff.HostProfile{
		DeliveryRoot: t.TempDir(),
		Driver:       "/opt/corsolv/delivery-driver",
		Provider:     "claude",
	}
	const projectID = "planning-boundary-test"
	started := time.Now().UTC().Add(-90 * time.Second)

	cause := errors.New(
		"handoff: planning failed after 2 attempt(s): the planning agent exited 1: " +
			"You've hit your monthly spend limit")

	recordPreRunBoundary(host, projectID, "run-1", started, cause)

	event, found, err := unattended.ReadCompletion(host.StateDir(projectID))
	if err != nil {
		t.Fatalf("reading the published boundary: %v", err)
	}
	if !found {
		t.Fatal("a planning failure must leave a durable record; nothing was written")
	}

	if event.Outcome != unattended.RunBlockedHuman {
		t.Fatalf("Outcome = %q, want %q — whatever stopped planning needs a person",
			event.Outcome, unattended.RunBlockedHuman)
	}
	if event.RunID != "run-1" || event.ProjectID != projectID {
		t.Fatalf("the record must identify its run and project, got %q / %q", event.RunID, event.ProjectID)
	}
	if len(event.HumanActions) == 0 {
		t.Fatal("a boundary with no stated action is not actionable")
	}
	// The planner's own words, verbatim. A boundary nobody can read is useless,
	// and this is the string that names what a person must actually go and do.
	if !strings.Contains(event.HumanActions[0], "spend limit") {
		t.Fatalf("the cause must survive into the record, got %q", event.HumanActions[0])
	}
	if event.Duration == "" {
		t.Error("the record should say how long delivery spent before giving up")
	}
}

// And the state derivation must turn that record into something a portal shows.
func TestAPublishedPlanningBoundaryDerivesAsBlocked(t *testing.T) {
	obs := handoff.RunObservation{
		Finished:   true,
		Outcome:    string(unattended.RunBlockedHuman),
		RunID:      "run-1",
		Boundaries: []string{"the planning agent exited 1: You've hit your monthly spend limit"},
	}
	// No plan exists — that is the whole point: planning is what failed.
	got := handoff.Derive(
		"planning-boundary-test", true,
		handoff.DeliveryPlan{}, false,
		obs,
		handoff.Evidence{Met: false, Reasons: []string{"no delivery projection has been published yet"}},
		time.Now(),
	)

	if got.State != handoff.StateBlocked {
		t.Fatalf("State = %q, want blocked — a delivery that cannot plan is not still planning", got.State)
	}
	if !strings.Contains(got.Detail, "spend limit") {
		t.Fatalf("the detail must name what a person has to do, got: %s", got.Detail)
	}
}
