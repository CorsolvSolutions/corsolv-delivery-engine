//go:build integration

// A reused city that nobody is running, and a wait with nobody working.
//
// THE INCIDENT THESE EXIST FOR, on aws-infrastructure-intelligence. After one
// run ended an operator ran `gc stop <city>`, which also UNREGISTERS the city
// from the machine-wide supervisor. The next run reused the city: city-up took
// its "city already built" branch, asked the supervisor to reload — which it
// did, for every city it knew about, which was none — and logged that it had
// reconciled this one. The controller stayed stopped and the package sat in
// await for about an hour of its ninety-minute deadline with no worker session.
//
// Two defects, proved separately here:
//
//	A  city-up's reused-city branch never asked whether the city was
//	   registered, and read a supervisor's answer as the city running.
//	B  await waited on an open bead without ever asking whether anyone was
//	   working on it, so a worker that never started cost the whole deadline.
//
// Like the rest of the driver tests these spawn bash under the integration tag,
// and every gc call lands on the stub in driver_recovery_test.go.
package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/unattended"
)

// indexOfCall is the position of the first gc call containing needle, or -1.
func indexOfCall(calls []string, needle string) int {
	for i, c := range calls {
		if strings.Contains(c, needle) {
			return i
		}
	}
	return -1
}

// DEFECT A, the repair: a reused city that is not registered is registered
// BEFORE the supervisor is asked to reload, because a reload reconciles only the
// cities the supervisor knows about.
func TestCityUpRegistersAReusedCityTheSupervisorDoesNotKnow(t *testing.T) {
	e := newRecoveryEnv(t)
	e.seedRuntime(nil) // a city already exists, so city-up takes the reuse branch
	e.setRegistry()    // what `gc stop` leaves behind: cities = []

	code, out := e.runStage("city-up", []string{"DELIVERY_CONTROLLER_START_WAIT=0"})
	if code != 0 {
		t.Fatalf("city-up must succeed once the reused city is registered, got %d:\n%s", code, out)
	}

	calls := e.gcCalls()
	reg := indexOfCall(calls, "register "+e.city)
	if reg < 0 {
		t.Fatalf("a reused city the supervisor does not know must be registered\ncalls: %v\n%s", calls, out)
	}
	reload := indexOfCall(calls, "supervisor reload")
	if reload < 0 || reload < reg {
		t.Fatalf("registration must precede the reload that reconciles it (register at %d, reload at %d)\ncalls: %v",
			reg, reload, calls)
	}
	// `gc start` may restart a drifted machine-wide supervisor; that is its
	// owner's decision, so this run must never issue it.
	if found := callsContaining(calls, " start"); len(found) != 0 {
		t.Fatalf("city-up must register, never `gc start`, got: %v", found)
	}
	if !strings.Contains(out, "supervisor-managed") {
		t.Errorf("city-up must report the controller state it verified, got:\n%s", out)
	}
}

// And a city that IS registered is left alone: registration is reconciled, not
// replayed.
func TestCityUpDoesNotReRegisterARegisteredCity(t *testing.T) {
	e := newRecoveryEnv(t)
	e.seedRuntime(nil)

	code, out := e.runStage("city-up", []string{"DELIVERY_CONTROLLER_START_WAIT=0"})
	if code != 0 {
		t.Fatalf("city-up must succeed on a registered, running city, got %d:\n%s", code, out)
	}
	if found := callsContaining(e.gcCalls(), "register "); len(found) != 0 {
		t.Fatalf("a city already registered must not be registered again, got: %v", found)
	}
}

// DEFECT A, the verdict: a supervisor that answered is not a city that runs. A
// controller still stopped after registering and reconciling stops the run for a
// person — naming the city and what to run — instead of letting dispatch route
// work to agents that will never start.
func TestCityUpStopsWhenAReusedCityStillWillNotStart(t *testing.T) {
	e := newRecoveryEnv(t)
	e.seedRuntime(nil)
	e.setRegistry()

	result := filepath.Join(t.TempDir(), "result.json")
	code, out := e.runStageWithResult(t, result, []string{
		"DELIVERY_CONTROLLER_START_WAIT=0",
		"GC_STUB_CONTROLLER=stopped",
	}, "city-up")
	if code == 0 {
		t.Fatalf("city-up reported success over a city whose controller is stopped:\n%s", out)
	}
	if len(callsContaining(e.gcCalls(), "register "+e.city)) == 0 {
		t.Fatalf("registration must be attempted before giving up\ncalls: %v", e.gcCalls())
	}
	for _, want := range []string{e.city, "controller is stopped", "gc start " + e.city} {
		if !strings.Contains(out, want) {
			t.Errorf("the stop must say %q, got:\n%s", want, out)
		}
	}
	v := adjudicate(t, result, false)
	if v.Disposition != unattended.DispositionHumanBlocked {
		t.Fatalf("disposition = %s, want human-blocked — %s\n%s", v.Disposition, v.Reason, out)
	}
}

// awaitFixture is a dispatched package whose bead is open and whose worktree
// exists, so await goes straight to waiting.
func awaitFixture(t *testing.T) *recoveryEnv {
	t.Helper()
	e := newRecoveryEnv(t)
	e.seedRuntime(map[string]string{
		"dispatched":    "2026-10-11T08:00:00Z",
		"bead.wp-one":   beadOne,
		"bead.wp-two":   beadTwo,
		"wt.wp-one":     e.makeWorktree("wp-one"),
		"branch.wp-one": "delivery/20260814T164300Z/wp-one",
	})
	e.setBead(beadOne, "open")
	e.setBead(beadTwo, "open")
	return e
}

// DEFECT B: a worker that never starts stops the stage after the grace, with
// the reason, instead of silently consuming the package's deadline.
//
// The deadline here is long enough that an await which never asked about the
// worker reaches it and says "not finished" — the incident's shape — while the
// watch must stop well before it.
func TestAwaitStopsWhenNoWorkerSessionEverStarts(t *testing.T) {
	e := awaitFixture(t)
	// No session at all, and a city that is registered and running: the repair
	// is attempted once and changes nothing.
	const deadline = 25
	result := filepath.Join(t.TempDir(), "result.json")
	start := time.Now()
	code, out := e.runStageWithResult(t, result, []string{
		"DELIVERY_WORK_DEADLINE=25",
		"DELIVERY_WORKER_START_GRACE=1",
		"DELIVERY_AWAIT_POLL=1",
		"DELIVERY_CONTROLLER_START_WAIT=0",
	}, "await", "-package", "wp-one")
	elapsed := time.Since(start)

	if code == 0 {
		t.Fatalf("await must not succeed with no worker on open work:\n%s", out)
	}
	if !strings.Contains(out, "no worker session for wp-one started within 1s") {
		t.Fatalf("the stop must name the package and the grace, got:\n%s", out)
	}
	if !strings.Contains(out, "controller is supervisor-managed") {
		t.Errorf("the stop must report the controller's state, got:\n%s", out)
	}
	if elapsed >= deadline*time.Second {
		t.Fatalf("the watch must stop before the package deadline, took %s", elapsed)
	}
	if len(callsContaining(e.gcCalls(), "supervisor reload")) != 1 {
		t.Errorf("the registration-and-reconcile repair must be attempted exactly once\ncalls: %v", e.gcCalls())
	}
	v := adjudicate(t, result, false)
	if v.Disposition != unattended.DispositionHumanBlocked {
		t.Fatalf("disposition = %s, want human-blocked — %s\n%s", v.Disposition, v.Reason, out)
	}
}

// DEFECT B, the incident exactly: the city was unregistered by a `gc stop`. The
// watch re-registers it; when its controller still will not run, the stop names
// that, rather than the deadline.
func TestAwaitReRegistersAnUnregisteredCityWhenNoWorkerStarts(t *testing.T) {
	e := awaitFixture(t)
	e.setRegistry()

	code, out := e.runStage("await", []string{
		"DELIVERY_WORK_DEADLINE=25",
		"DELIVERY_WORKER_START_GRACE=1",
		"DELIVERY_AWAIT_POLL=1",
		"DELIVERY_CONTROLLER_START_WAIT=0",
		"GC_STUB_CONTROLLER=stopped",
	}, "-package", "wp-one")
	if code == 0 {
		t.Fatalf("await must not succeed over a stopped city:\n%s", out)
	}
	if len(callsContaining(e.gcCalls(), "register "+e.city)) != 1 {
		t.Fatalf("the watch must register the unregistered city once\ncalls: %v\n%s", e.gcCalls(), out)
	}
	if !strings.Contains(out, "controller is stopped") || !strings.Contains(out, "gc start "+e.city) {
		t.Fatalf("the stop must name the stopped controller and the command to run, got:\n%s", out)
	}
}

// The converse, which keeps the watch honest: a worker that IS running is
// left to work. The stage runs to its deadline exactly as before, with no
// repair attempted and no stop.
func TestAwaitLeavesALiveWorkerToItsDeadline(t *testing.T) {
	e := awaitFixture(t)
	e.setSessions(liveSession("worker-wp-one"))

	code, out := e.runStage("await", []string{
		"DELIVERY_WORK_DEADLINE=3",
		"DELIVERY_WORKER_START_GRACE=0",
		"DELIVERY_AWAIT_POLL=1",
	}, "-package", "wp-one")
	if code == 0 {
		t.Fatalf("await must not succeed with its work still open:\n%s", out)
	}
	if strings.Contains(out, "no worker session") {
		t.Fatalf("a live worker must not be reported as missing:\n%s", out)
	}
	if !strings.Contains(out, "still open") {
		t.Fatalf("the stage must end on its deadline as before, got:\n%s", out)
	}
	for _, directive := range []string{"register ", "supervisor reload"} {
		if found := callsContaining(e.gcCalls(), directive); len(found) != 0 {
			t.Errorf("no repair may be attempted while a worker is live, but %q was issued: %v", directive, found)
		}
	}
}
