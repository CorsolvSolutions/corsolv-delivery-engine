//go:build integration

// A worktree that survived is not a worker that was started.
//
// THE INCIDENT THESE EXIST FOR, on aws-infrastructure-intelligence
// (2026-10-11/12). A re-run found the worktrees of wp-rules-engine and
// wp-aws-access left over from an earlier run, cut from a base that was still
// current. ensure_package_worktree routed a bead only in the branch that CUTS a
// missing tree, so for these it prepared the tree and returned: the beads stayed
// open with no `gc.routed_to` and no worker session, and the run waited on
// await-wp-<pkg>, spending the deadline, until an operator ran `gc sling` by hand.
//
// These reuse the city-supervision await fixture: a dispatched package whose
// bead is open and whose worktree already exists, and the stub gc that records
// every `sling`.
package main

import (
	"strings"
	"testing"
)

// The defect itself: an open bead, a surviving tree, nobody on it — await
// routes it before it starts waiting.
func TestAwaitRoutesAnOpenBeadWhoseWorktreeSurvived(t *testing.T) {
	e := awaitFixture(t)

	code, out := e.runStage("await", []string{"DELIVERY_WORK_DEADLINE=0"}, "-package", "wp-one")
	if code == 0 {
		t.Fatalf("await must not succeed with its work still open:\n%s", out)
	}
	if got := slingsFor(e.gcCalls(), "wp-one"); got != 1 {
		t.Fatalf("an open bead whose worktree survived and that no worker holds must be routed once, got %d sling(s)\ncalls: %v\n%s",
			got, e.gcCalls(), out)
	}
	if got := slingsFor(e.gcCalls(), "wp-two"); got != 0 {
		t.Fatalf("a package this stage does not wait for must not be routed, got %d sling(s)", got)
	}
}

// A live worker already holds the bead: routing it again would be a duplicate.
func TestAwaitDoesNotRouteASurvivingWorktreeALiveWorkerHolds(t *testing.T) {
	e := awaitFixture(t)
	e.setSessions(liveSession("worker-wp-one"))

	code, out := e.runStage("await", []string{"DELIVERY_WORK_DEADLINE=0"}, "-package", "wp-one")
	if code == 0 {
		t.Fatalf("await must not succeed with its work still open:\n%s", out)
	}
	if got := slingsFor(e.gcCalls(), "wp-one"); got != 0 {
		t.Fatalf("a bead a live worker holds must not be routed again, got %d sling(s)\ncalls: %v\n%s",
			got, e.gcCalls(), out)
	}
}

// Finished work is never handed back to a worker, tree or no tree.
func TestAwaitDoesNotRouteAClosedBeadWhoseWorktreeSurvived(t *testing.T) {
	e := awaitFixture(t)
	e.setBead(beadOne, "closed")

	code, out := e.runStage("await", []string{"DELIVERY_WORK_DEADLINE=0"}, "-package", "wp-one")
	if code != 0 {
		t.Fatalf("await must succeed once the work it waits for is closed, got %d:\n%s", code, out)
	}
	if got := slingsFor(e.gcCalls(), "wp-one"); got != 0 {
		t.Fatalf("a closed bead must not be routed, got %d sling(s)\ncalls: %v\n%s", got, e.gcCalls(), out)
	}
}

// The worker-start watch's repair routes too. A healthy controller starts only
// the workers whose beads are routed to them, so a repair that re-registers the
// city but leaves an unrouted bead unrouted changes nothing. Here the worker
// never comes up, so the bead is routed by the worktree stage and again by the
// watch's one repair (gc skips an already-routed bead), and the stage still
// stops for a person when that does not help.
func TestWorkerStartWatchRoutesAnUnheldBeadWhenItRepairs(t *testing.T) {
	e := awaitFixture(t)

	code, out := e.runStage("await", []string{
		"DELIVERY_WORK_DEADLINE=25",
		"DELIVERY_WORKER_START_GRACE=1",
		"DELIVERY_AWAIT_POLL=1",
		"DELIVERY_CONTROLLER_START_WAIT=0",
	}, "-package", "wp-one")
	if code == 0 {
		t.Fatalf("await must not succeed with no worker on open work:\n%s", out)
	}
	calls := e.gcCalls()
	if got := slingsFor(calls, "wp-one"); got != 2 {
		t.Fatalf("expected the worktree stage and the watch's repair each to route wp-one once, got %d sling(s)\ncalls: %v\n%s",
			got, calls, out)
	}
	reload := indexOfCall(calls, "supervisor reload")
	lastSling := -1
	for i, c := range calls {
		if strings.Contains(c, "sling ") && strings.Contains(c, "worker-wp-one ") {
			lastSling = i
		}
	}
	if reload < 0 || lastSling < reload {
		t.Fatalf("the watch must route after it reconciles the city (reload at %d, last sling at %d)\ncalls: %v",
			reload, lastSling, calls)
	}
	if !strings.Contains(out, "no worker session for wp-one started within 1s") {
		t.Fatalf("the stage must still stop for a person when routing does not start a worker, got:\n%s", out)
	}
}
