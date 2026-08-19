package main

import (
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/unattended"
)

// A run publishes `finished` once, microseconds AFTER the completion event it
// follows. Comparing timestamps alone read that as progress superseding the
// completion, and a delivery that had finished every package reported as
// `queued` — "no run is currently executing it", exit 0 — over evidence whose
// only outstanding clause was a person's acceptance.
func TestARunsOwnFinalHeartbeatDoesNotSupersedeItsCompletion(t *testing.T) {
	dir := t.TempDir()
	finishedAt := time.Date(2026, 8, 19, 8, 9, 13, 803401639, time.UTC)

	if err := unattended.WriteCompletion(dir, unattended.CompletionEvent{
		RunID:      "run-1",
		ProjectID:  "p",
		Outcome:    "completed",
		Reason:     "every declared task succeeded",
		FinishedAt: finishedAt,
	}); err != nil {
		t.Fatalf("WriteCompletion: %v", err)
	}
	if err := unattended.WriteProgress(dir, unattended.Progress{
		RunID:     "run-1",
		ProjectID: "p",
		Stage:     unattended.StageFinished,
		UpdatedAt: finishedAt.Add(3 * time.Millisecond),
	}); err != nil {
		t.Fatalf("WriteProgress: %v", err)
	}

	obs, err := observeRun(dir)
	if err != nil {
		t.Fatalf("observeRun: %v", err)
	}
	if !obs.Finished {
		t.Fatal("the run finished and said so; its own last heartbeat is not a reason to doubt it")
	}
	if obs.Outcome != "completed" {
		t.Errorf("Outcome = %q, want %q", obs.Outcome, "completed")
	}
	if obs.Live {
		t.Error("a finished run is not live")
	}
}

// The rule it must not lose: progress that shows a run still WORKING after a
// completion event is a stale completion, and the run happening now wins.
func TestProgressThatShowsWorkAfterACompletionStillSupersedesIt(t *testing.T) {
	dir := t.TempDir()
	finishedAt := time.Date(2026, 8, 19, 8, 9, 13, 0, time.UTC)

	if err := unattended.WriteCompletion(dir, unattended.CompletionEvent{
		RunID: "run-1", ProjectID: "p", Outcome: "completed", FinishedAt: finishedAt,
	}); err != nil {
		t.Fatalf("WriteCompletion: %v", err)
	}
	if err := unattended.WriteProgress(dir, unattended.Progress{
		RunID: "run-1", ProjectID: "p", Stage: "running",
		UpdatedAt: finishedAt.Add(time.Minute),
	}); err != nil {
		t.Fatalf("WriteProgress: %v", err)
	}

	obs, err := observeRun(dir)
	if err != nil {
		t.Fatalf("observeRun: %v", err)
	}
	if obs.Finished {
		t.Fatal("a completion record with later work recorded against it is stale, not the answer")
	}
}

// And the other half: a completion left by a DIFFERENT run never answers a
// question about this one.
func TestACompletionFromAnotherRunDoesNotAnswerForThisOne(t *testing.T) {
	dir := t.TempDir()
	finishedAt := time.Date(2026, 8, 19, 8, 0, 0, 0, time.UTC)

	if err := unattended.WriteCompletion(dir, unattended.CompletionEvent{
		RunID: "run-1", ProjectID: "p", Outcome: "completed", FinishedAt: finishedAt,
	}); err != nil {
		t.Fatalf("WriteCompletion: %v", err)
	}
	if err := unattended.WriteProgress(dir, unattended.Progress{
		RunID: "run-2", ProjectID: "p", Stage: unattended.StageFinished,
		UpdatedAt: finishedAt.Add(time.Hour),
	}); err != nil {
		t.Fatalf("WriteProgress: %v", err)
	}

	obs, err := observeRun(dir)
	if err != nil {
		t.Fatalf("observeRun: %v", err)
	}
	if obs.Finished {
		t.Fatal("run-1's completion must not be reported as run-2's outcome")
	}
}
