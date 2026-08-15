package handoff

import (
	"fmt"
	"testing"

	"github.com/gastownhall/gascity/internal/unattended"
)

// The deadlock these exist for. One `await` for the whole plan waited on every
// work bead; a dependent package's work bead only opens when its upstream's
// MERGE bead closes; and that merge bead is closed by the controller inside
// `publish`, which could not start until the await finished. The first pilot
// had one package and never saw it. The second sat for its full ninety-minute
// deadline with three workers that were never eligible to run.

// serialPlan is n packages in a strict chain: each depends on the one before.
func serialPlan(t *testing.T, n int) (Intent, DeliveryPlan) {
	t.Helper()
	in := planIntent()
	in.Acceptance = nil
	plan := DeliveryPlan{
		SchemaVersion: PlanSchemaVersion,
		ProjectID:     in.ProjectID,
		PlannedBy:     "agent:planner-1",
	}
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("wp-%d", i)
		ac := fmt.Sprintf("ac-%d", i)
		in.Acceptance = append(in.Acceptance, Criterion{ID: ac, Statement: "step " + id})
		wp := WorkPackage{
			ID: id, Title: "step " + id, Phase: "Build",
			Objective:       "Create src/" + id + ".ts.",
			Artifact:        "src/" + id + ".ts",
			AuthorizedPaths: []string{"src/" + id + ".ts"},
			Satisfies:       []string{ac},
			Gates:           []string{"npm run verify"},
		}
		if i > 1 {
			wp.DependsOn = []string{fmt.Sprintf("wp-%d", i-1)}
		}
		plan.Packages = append(plan.Packages, wp)
	}
	if err := plan.Validate(in); err != nil {
		t.Fatalf("the fixture plan must be valid: %v", err)
	}
	return in, plan
}

func needsOf(t *testing.T, work unattended.Plan, id string) []string {
	t.Helper()
	for _, task := range work.Tasks {
		if task.ID == id {
			return task.Needs
		}
	}
	t.Fatalf("no task %q in %v", id, taskIDs(work))
	return nil
}

func hasNeed(needs []string, want string) bool {
	for _, n := range needs {
		if n == want {
			return true
		}
	}
	return false
}

// drain runs the queue to exhaustion, succeeding every task, and returns the
// order the tasks were offered in.
func drain(t *testing.T, q *unattended.Queue, limit int) []string {
	t.Helper()
	var order []string
	for i := 0; i < limit; i++ {
		next, ok := q.Next()
		if !ok {
			break
		}
		order = append(order, next.Task.ID)
		q.RecordAttempt(next, unattended.TaskAttempt{Number: 1, Succeeded: true})
	}
	return order
}

// A one-package plan is the shape the first pilot ran, and it must keep working.
func TestOnePackagePlanStillCompiles(t *testing.T) {
	in, plan := serialPlan(t, 1)
	_, work, err := Compile(in, plan, testHost(t), "run-1")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		StageCityUp, StageDispatch,
		StageAwait + "-wp-1", StagePublish + "-wp-1",
		StageProject, StagePublishProjection,
	}
	if len(work.Tasks) != len(want) {
		t.Fatalf("compiled %d tasks, want %d: %v", len(work.Tasks), len(want), taskIDs(work))
	}
	for _, id := range want {
		needsOf(t, work, id)
	}
	if got := needsOf(t, work, StageAwait+"-wp-1"); len(got) != 1 || got[0] != StageDispatch {
		t.Fatalf("the only package must wait on dispatch alone, got %v", got)
	}
	if order := drain(t, unattended.NewQueue(work, nil), len(want)+1); len(order) != len(want) {
		t.Fatalf("a one-package plan must still drain completely, got %v", order)
	}
}

// The shape the second pilot deadlocked on: work → merge → work → merge.
func TestTwoSerialPackagesInterleaveWaitingAndPublication(t *testing.T) {
	in, plan := serialPlan(t, 2)
	_, work, err := Compile(in, plan, testHost(t), "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := needsOf(t, work, StagePublish+"-wp-1"); !hasNeed(got, StageAwait+"-wp-1") {
		t.Fatalf("publish-wp-1 must follow its own await, got %v", got)
	}
	// The edge that breaks the deadlock: the second package's WAIT comes after
	// the first package's PUBLICATION, which is what closes the merge bead its
	// work bead is blocked on.
	if got := needsOf(t, work, StageAwait+"-wp-2"); !hasNeed(got, StagePublish+"-wp-1") {
		t.Fatalf("await-wp-2 must wait for publish-wp-1, got %v", got)
	}
	if got := needsOf(t, work, StageAwait+"-wp-1"); hasNeed(got, StagePublish+"-wp-2") {
		t.Fatal("the first package must not wait for the second")
	}
}

// Four packages is the pilot's own plan. The queue must be able to walk the
// whole chain, one package at a time, with nothing unreachable.
func TestFourSerialPackagesProgressToCompletion(t *testing.T) {
	in, plan := serialPlan(t, 4)
	_, work, err := Compile(in, plan, testHost(t), "run-1")
	if err != nil {
		t.Fatal(err)
	}

	order := drain(t, unattended.NewQueue(work, nil), len(work.Tasks)+1)
	if len(order) != len(work.Tasks) {
		t.Fatalf("the queue drained %d of %d tasks — the rest are unreachable: got %v",
			len(order), len(work.Tasks), order)
	}

	position := map[string]int{}
	for i, id := range order {
		position[id] = i
	}
	for i := 1; i <= 4; i++ {
		id := fmt.Sprintf("wp-%d", i)
		if position[StageAwait+"-"+id] > position[StagePublish+"-"+id] {
			t.Errorf("%s published before it was awaited", id)
		}
		if i > 1 {
			prev := fmt.Sprintf("wp-%d", i-1)
			if position[StagePublish+"-"+prev] > position[StageAwait+"-"+id] {
				t.Errorf("%s was awaited before %s merged", id, prev)
			}
		}
	}
}

// Dependent work must not become eligible until its prerequisite's controller
// merge has actually happened — proved against the queue, not against the
// compiled edges alone.
func TestDependentWorkWaitsForTheControllerMerge(t *testing.T) {
	in, plan := serialPlan(t, 3)
	_, work, err := Compile(in, plan, testHost(t), "run-1")
	if err != nil {
		t.Fatal(err)
	}
	q := unattended.NewQueue(work, nil)

	for _, id := range []string{StageCityUp, StageDispatch, StageAwait + "-wp-1"} {
		next, ok := q.Next()
		if !ok || next.Task.ID != id {
			t.Fatalf("expected %q next, ok=%v", id, ok)
		}
		q.RecordAttempt(next, unattended.TaskAttempt{Number: 1, Succeeded: true})
	}
	next, ok := q.Next()
	if !ok {
		t.Fatal("the queue stalled before publishing the first package")
	}
	if next.Task.ID != StagePublish+"-wp-1" {
		t.Fatalf("the second package became eligible before the first merged: got %q", next.Task.ID)
	}
}

// A package that depends on nothing is not made to wait for a sibling.
func TestIndependentPackagesAreNotSerialized(t *testing.T) {
	in := planIntent()
	plan := validPlan()
	plan.Packages[1].DependsOn = nil
	if err := plan.Validate(in); err != nil {
		t.Fatal(err)
	}
	_, work, err := Compile(in, plan, testHost(t), "run-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, wp := range plan.Packages {
		got := needsOf(t, work, StageAwait+"-"+wp.ID)
		if len(got) != 1 || got[0] != StageDispatch {
			t.Errorf("independent package %s waits on %v; it should wait only on dispatch", wp.ID, got)
		}
	}
}

// Failed work must hold its dependents rather than releasing them.
func TestFailedPublicationHoldsDependentWork(t *testing.T) {
	in, plan := serialPlan(t, 3)
	_, work, err := Compile(in, plan, testHost(t), "run-1")
	if err != nil {
		t.Fatal(err)
	}
	q := unattended.NewQueue(work, nil)

	for _, id := range []string{StageCityUp, StageDispatch, StageAwait + "-wp-1"} {
		next, ok := q.Next()
		if !ok || next.Task.ID != id {
			t.Fatalf("expected %q", id)
		}
		q.RecordAttempt(next, unattended.TaskAttempt{Number: 1, Succeeded: true})
	}
	next, ok := q.Next()
	if !ok || next.Task.ID != StagePublish+"-wp-1" {
		t.Fatal("expected publish-wp-1")
	}
	q.RecordAttempt(next, unattended.TaskAttempt{Number: 1, Succeeded: false, Reason: "merge refused"})

	for {
		offered, ok := q.Next()
		if !ok {
			break
		}
		if offered.Task.ID == StageAwait+"-wp-2" || offered.Task.ID == StagePublish+"-wp-2" {
			t.Fatalf("%s was offered for work although wp-1 never merged", offered.Task.ID)
		}
		q.RecordAttempt(offered, unattended.TaskAttempt{Number: 1, Succeeded: true})
	}
	for _, id := range []string{StageAwait + "-wp-2", StagePublish + "-wp-2", StageAwait + "-wp-3"} {
		task, ok := q.Task(id)
		if !ok {
			t.Fatalf("no task %q", id)
		}
		if task.State == unattended.TaskSucceeded {
			t.Errorf("%s succeeded although the package it depends on did not merge", id)
		}
	}
}

// A resumed run does not replay a package that already merged.
func TestResumeDoesNotReplayCompletedPackages(t *testing.T) {
	in, plan := serialPlan(t, 3)
	_, work, err := Compile(in, plan, testHost(t), "run-1")
	if err != nil {
		t.Fatal(err)
	}
	q := unattended.NewQueue(work, nil)
	q.Restore(unattended.ResumeState{
		Succeeded: map[string]bool{
			StageCityUp: true, StageDispatch: true,
			StageAwait + "-wp-1": true, StagePublish + "-wp-1": true,
		},
		Attempts:    map[string]int{},
		Interrupted: map[string]bool{},
	})

	next, ok := q.Next()
	if !ok {
		t.Fatal("a resumed run must continue rather than stall")
	}
	if next.Task.ID != StageAwait+"-wp-2" {
		t.Fatalf("resume replayed %q; the run should continue at await-wp-2", next.Task.ID)
	}
}
