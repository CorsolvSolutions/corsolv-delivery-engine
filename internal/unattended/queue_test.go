package unattended

import (
	"testing"
	"time"
)

func planOf(tasks ...Task) Plan { return Plan{RunID: "run-queue", Risk: RiskQ0, Tasks: tasks} }

func task(id string, band Band, needs ...string) Task {
	return Task{ID: id, Title: id, Band: band, Argv: []string{"true"}, Needs: needs}
}

func TestQueuePrefersThePrimaryBand(t *testing.T) {
	q := NewQueue(planOf(
		task("docs", BandDocumentation),
		task("tests", BandValidation),
		task("build", BandPrimary),
	), nil)

	next, ok := q.Next()
	if !ok || next.Task.ID != "build" {
		t.Fatalf("Next() = %v, want the primary task", next)
	}
}

func TestQueueSelectsFallbackWhenThePrimaryPathIsBlocked(t *testing.T) {
	// The core of the long-run requirement: a blocked primary path must move the
	// run on, not end it.
	q := NewQueue(planOf(
		task("primary", BandPrimary),
		task("validation", BandValidation),
		task("docs", BandDocumentation),
	), nil)

	primary, _ := q.Next()
	q.Hold(primary, "waiting on an upstream release")

	next, ok := q.Next()
	if !ok {
		t.Fatal("a blocked primary must not exhaust the queue while fallback work exists")
	}
	if next.Task.ID != "validation" {
		t.Fatalf("fallback = %s, want the next band down", next.Task.ID)
	}
}

func TestQueueNeverInventsWork(t *testing.T) {
	// Fallback means "declared work of a lower band", never "something to do".
	q := NewQueue(planOf(task("primary", BandPrimary)), nil)
	primary, _ := q.Next()
	q.Hold(primary, "blocked")

	if _, ok := q.Next(); ok {
		t.Fatal("the queue offered work that was never declared")
	}
	if !q.Exhausted() {
		t.Fatal("a queue with nothing declared left must report exhausted")
	}
}

func TestQueueHoldsTasksBehindAHumanBoundary(t *testing.T) {
	// This is the payoff of discovering boundaries at preflight: the task that
	// needs the boundary is held before the run starts, and the rest proceeds.
	plan := planOf(
		Task{
			ID: "merge", Title: "merge the PR", Band: BandPrimary, Argv: []string{"true"},
			RequiresChecks: []string{"github.merge"},
		},
		task("tests", BandValidation),
	)
	q := NewQueue(plan, map[string]string{
		"github.merge": "a human reviewer approves the pull request",
	})

	merge, _ := q.Task("merge")
	if merge.State != TaskHeld {
		t.Fatalf("task behind a boundary = %s, want held", merge.State)
	}
	if merge.HeldReason == "" {
		t.Fatal("a held task must say what would have to change")
	}
	next, ok := q.Next()
	if !ok || next.Task.ID != "tests" {
		t.Fatalf("Next() = %v, want the unblocked task", next)
	}
}

func TestQueueWithdrawsTasksWhoseDependencyCannotSucceed(t *testing.T) {
	// A task whose dependency failed would otherwise stay pending forever, and
	// a queue reporting work it will never select reads exactly like a hang.
	q := NewQueue(planOf(
		task("a", BandPrimary),
		task("b", BandPrimary, "a"),
		task("c", BandPrimary, "b"),
	), nil)

	a, _ := q.Task("a")
	q.RecordAttempt(a, TaskAttempt{Succeeded: false, Class: FailureAuth})

	for _, id := range []string{"b", "c"} {
		qt, _ := q.Task(id)
		if qt.State != TaskHeld {
			t.Fatalf("task %s = %s, want held transitively", id, qt.State)
		}
	}
	if !q.Exhausted() {
		t.Fatal("queue must be exhausted once nothing can be attempted")
	}
}

func TestQueueDoesNotOfferATaskBeforeItsDependency(t *testing.T) {
	q := NewQueue(planOf(
		task("second", BandPrimary, "first"),
		task("first", BandValidation),
	), nil)

	next, _ := q.Next()
	if next.Task.ID != "first" {
		t.Fatalf("Next() = %s, want the dependency even though it is a lower band", next.Task.ID)
	}
	q.RecordAttempt(next, TaskAttempt{Succeeded: true})

	next, ok := q.Next()
	if !ok || next.Task.ID != "second" {
		t.Fatalf("Next() = %v, want the dependent task once its need succeeded", next)
	}
}

func TestRetryKeepsTheTaskPendingUntilItsBudgetIsSpent(t *testing.T) {
	q := NewQueue(planOf(task("flaky", BandPrimary)), nil)
	qt, _ := q.Task("flaky")
	budget := PolicyFor(FailureRetryable).MaxAttempts

	for i := 1; i < budget; i++ {
		delay, willRetry := q.RecordAttempt(qt, TaskAttempt{Succeeded: false, Class: FailureRetryable})
		if !willRetry {
			t.Fatalf("attempt %d of %d ended the task early", i, budget)
		}
		if delay <= 0 {
			t.Fatalf("attempt %d scheduled no backoff", i)
		}
		if qt.State != TaskPending {
			t.Fatalf("a task with budget left = %s, want pending", qt.State)
		}
	}
	if _, willRetry := q.RecordAttempt(qt, TaskAttempt{Succeeded: false, Class: FailureRetryable}); willRetry {
		t.Fatal("a task must stop retrying once its budget is spent")
	}
	if qt.State != TaskFailed {
		t.Fatalf("exhausted task = %s, want failed", qt.State)
	}
}

func TestAnAuthFailureIsNotRetried(t *testing.T) {
	// A credential does not become valid by being asked again.
	q := NewQueue(planOf(task("push", BandPrimary)), nil)
	qt, _ := q.Task("push")
	if _, willRetry := q.RecordAttempt(qt, TaskAttempt{Succeeded: false, Class: FailureAuth}); willRetry {
		t.Fatal("an auth failure must not be retried")
	}
}

func TestATaskMayOverrideItsAttemptBudget(t *testing.T) {
	q := NewQueue(planOf(Task{
		ID: "stubborn", Title: "stubborn", Band: BandPrimary, Argv: []string{"true"}, MaxAttempts: 2,
	}), nil)
	qt, _ := q.Task("stubborn")

	if _, willRetry := q.RecordAttempt(qt, TaskAttempt{Succeeded: false, Class: FailureExternalService}); !willRetry {
		t.Fatal("the first of two declared attempts must retry")
	}
	if _, willRetry := q.RecordAttempt(qt, TaskAttempt{Succeeded: false, Class: FailureExternalService}); willRetry {
		t.Fatal("the declared budget of 2 must override the class policy's larger one")
	}
}

func TestRestoreSkipsCompletedWorkAndKeepsTheRetryBudget(t *testing.T) {
	q := NewQueue(planOf(
		task("done", BandPrimary),
		task("partly", BandPrimary),
		task("fresh", BandValidation),
	), nil)

	q.Restore(ResumeState{
		Succeeded:   map[string]bool{"done": true},
		Attempts:    map[string]int{"partly": 2},
		Interrupted: map[string]bool{"partly": true},
	})

	done, _ := q.Task("done")
	if done.State != TaskSucceeded {
		t.Fatalf("durably completed task = %s, want succeeded — work must not be repeated", done.State)
	}
	partly, _ := q.Task("partly")
	if partly.AttemptCount() != 2 {
		t.Fatalf("restored attempts = %d, want 2 — a crash loop must not reset the budget", partly.AttemptCount())
	}
	if !partly.Interrupted {
		t.Fatal("an interrupted attempt must be recorded, not erased")
	}
	if partly.State != TaskPending {
		t.Fatalf("interrupted task = %s, want pending so it is re-offered", partly.State)
	}
}

func TestRestoreIsIdempotent(t *testing.T) {
	// Resuming a resumed run must not double-count anything.
	build := func() *Queue {
		return NewQueue(planOf(task("a", BandPrimary), task("b", BandValidation)), nil)
	}
	st := ResumeState{
		Succeeded: map[string]bool{"a": true},
		Attempts:  map[string]int{"b": 1},
	}
	once := build()
	once.Restore(st)
	twice := build()
	twice.Restore(st)
	twice.Restore(st)

	for _, id := range []string{"a", "b"} {
		o, _ := once.Task(id)
		tw, _ := twice.Task(id)
		if o.State != tw.State || o.AttemptCount() != tw.AttemptCount() {
			t.Fatalf("task %s differs after a second restore: %s/%d vs %s/%d",
				id, o.State, o.AttemptCount(), tw.State, tw.AttemptCount())
		}
	}
}

func TestPrimaryWorkOutstandingSeparatesBlockedFromFinished(t *testing.T) {
	// Lower-band work while the primary path is stuck, and lower-band work
	// after it finished, look identical from the band alone and mean opposite
	// things. The first real run published the wrong one.
	q := NewQueue(planOf(
		task("primary", BandPrimary),
		task("docs", BandDocumentation),
	), nil)

	if !q.PrimaryWorkOutstanding() {
		t.Fatal("an unattempted primary task is outstanding")
	}
	primary, _ := q.Task("primary")
	q.RecordAttempt(primary, TaskAttempt{Succeeded: true})
	if q.PrimaryWorkOutstanding() {
		t.Fatal("a succeeded primary task is not outstanding — the run is on plan, not on a detour")
	}
}

func TestPrimaryWorkStaysOutstandingWhenItFailedOrIsHeld(t *testing.T) {
	for _, tc := range []struct {
		name string
		hold bool
	}{{"failed", false}, {"held", true}} {
		t.Run(tc.name, func(t *testing.T) {
			q := NewQueue(planOf(task("primary", BandPrimary), task("docs", BandDocumentation)), nil)
			primary, _ := q.Task("primary")
			if tc.hold {
				q.Hold(primary, "behind a boundary")
			} else {
				q.RecordAttempt(primary, TaskAttempt{Succeeded: false, Class: FailureAuth})
			}
			if !q.PrimaryWorkOutstanding() {
				t.Fatalf("a %s primary task must still count as outstanding", tc.name)
			}
		})
	}
}

func TestQueueSummaryReportsEveryState(t *testing.T) {
	q := NewQueue(planOf(task("a", BandPrimary), task("b", BandValidation)), nil)
	a, _ := q.Task("a")
	q.RecordAttempt(a, TaskAttempt{Succeeded: true, StartedAt: time.Now()})
	if got := q.Summary(); got == "" {
		t.Fatal("the summary must describe the queue")
	}
	counts := q.Counts()
	if counts[TaskSucceeded] != 1 || counts[TaskPending] != 1 {
		t.Fatalf("counts = %v", counts)
	}
}
