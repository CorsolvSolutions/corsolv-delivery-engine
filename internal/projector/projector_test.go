package projector

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func ts(minute int) time.Time {
	return time.Date(2026, 8, 10, 12, minute, 0, 0, time.UTC)
}

// fixture builds a three-task graph: w3 depends on w1 and w2.
func fixture() *State {
	s := NewState("corsolv-autonomy-poc")
	s.Generated = ts(0)
	s.Tasks["w1"] = &Task{ID: "w1", Title: "add", Status: StatusNotStarted}
	s.Tasks["w2"] = &Task{ID: "w2", Title: "multiply", Status: StatusNotStarted}
	s.Tasks["w3"] = &Task{
		ID: "w3", Title: "calculator", Status: StatusNotStarted,
		DependsOn: []string{"w1", "w2"},
	}
	return s
}

func paths(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "delivery", "PROJECT-STATE.yml"), filepath.Join(dir, ".gc", "projector-cursor.json")
}

// A. first projection from an empty cursor.
func TestFirstProjectionFromEmptyCursor(t *testing.T) {
	statePath, cursorPath := paths(t)
	s := fixture()
	events := []Event{
		{Seq: 1, Type: "work.started", Ts: ts(1), Subject: "w1"},
		{Seq: 2, Type: "work.finished", Ts: ts(5), Subject: "w1"},
	}
	cur, err := Project(events, s, statePath, cursorPath, nil)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if cur.Seq != 2 {
		t.Errorf("cursor = %d, want 2", cur.Seq)
	}
	if s.Tasks["w1"].Status != StatusDone {
		t.Errorf("w1 status = %q, want done", s.Tasks["w1"].Status)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Errorf("projection was not written: %v", err)
	}
}

// B. resume from a persisted cursor: already-durable events are not re-applied,
// and the ones after it are.
func TestResumeFromPersistedCursor(t *testing.T) {
	statePath, cursorPath := paths(t)
	s := fixture()
	first := []Event{{Seq: 1, Type: "work.started", Ts: ts(1), Subject: "w1"}}
	if _, err := Project(first, s, statePath, cursorPath, nil); err != nil {
		t.Fatalf("first Project: %v", err)
	}
	startedAt := s.Tasks["w1"].ActualStart

	// A fresh projection resuming from the stored cursor sees the whole log but
	// must only apply what is past the cursor.
	resumed := fixture()
	all := []Event{
		{Seq: 1, Type: "work.started", Ts: ts(9), Subject: "w1"}, // replayed with a DIFFERENT ts
		{Seq: 2, Type: "work.finished", Ts: ts(6), Subject: "w1"},
	}
	cur, err := Project(all, resumed, statePath, cursorPath, nil)
	if err != nil {
		t.Fatalf("resumed Project: %v", err)
	}
	if cur.Seq != 2 {
		t.Errorf("cursor = %d, want 2", cur.Seq)
	}
	if !resumed.Tasks["w1"].ActualStart.IsZero() {
		t.Errorf("an event at or below the cursor was re-applied on resume (start=%v)",
			resumed.Tasks["w1"].ActualStart)
	}
	if resumed.Tasks["w1"].ActualFinish != ts(6) {
		t.Errorf("the event after the cursor was not applied on resume")
	}
	_ = startedAt
}

// C. replaying the same event does not duplicate or alter semantic state.
func TestReplayIsIdempotent(t *testing.T) {
	s := fixture()
	start := Event{Seq: 1, Type: "work.started", Ts: ts(1), Subject: "w1"}
	s.Apply(start)
	afterFirst := *s.Tasks["w1"]

	s.Apply(start) // exact replay
	s.Apply(Event{Seq: 1, Type: "work.started", Ts: ts(9), Subject: "w1"})

	got := s.Tasks["w1"]
	if got.Attempts != afterFirst.Attempts {
		t.Errorf("attempts = %d after replay, want %d; the count is not idempotent",
			got.Attempts, afterFirst.Attempts)
	}
	if !got.ActualStart.Equal(afterFirst.ActualStart) {
		t.Errorf("actual_start moved on replay: %v -> %v; start must be first-write-wins",
			afterFirst.ActualStart, got.ActualStart)
	}
}

// D + E. a crash between the durable state write and the cursor write must
// replay safely, and the cursor must never be ahead of durable state.
func TestCrashBetweenStateAndCursorReplaysSafely(t *testing.T) {
	statePath, cursorPath := paths(t)
	s := fixture()
	events := []Event{
		{Seq: 1, Type: "work.started", Ts: ts(1), Subject: "w1"},
		{Seq: 2, Type: "work.finished", Ts: ts(5), Subject: "w1"},
	}

	crash := errors.New("crash before the cursor write")
	func() {
		defer func() {
			// recover() yields `any`, so this compares the recovered value to
			// the sentinel directly. Re-panic on anything else: swallowing an
			// unexpected panic here would turn a real defect into a green test.
			r := recover()
			if r == nil {
				return
			}
			if err, ok := r.(error); !ok || !errors.Is(err, crash) {
				panic(r)
			}
		}()
		_, _ = Project(events, s, statePath, cursorPath, func() { panic(crash) })
	}()

	// The state is durable...
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("state was not durable before the crash: %v", err)
	}
	// ...and the cursor did NOT advance. A cursor ahead of durable state would
	// skip events, which is the one unrecoverable outcome.
	cur, err := LoadCursor(cursorPath)
	if err != nil {
		t.Fatalf("LoadCursor: %v", err)
	}
	if cur.Seq != 0 {
		t.Fatalf("cursor advanced to %d despite the crash; it must never lead durable state", cur.Seq)
	}

	// Replay from the un-advanced cursor reaches the same semantic state.
	replayed := fixture()
	if _, err := Project(events, replayed, statePath, cursorPath, nil); err != nil {
		t.Fatalf("replay Project: %v", err)
	}
	if replayed.Tasks["w1"].Status != StatusDone {
		t.Errorf("w1 status after replay = %q, want done", replayed.Tasks["w1"].Status)
	}
	if replayed.Tasks["w1"].Attempts != 1 {
		t.Errorf("attempts after replay = %d, want 1", replayed.Tasks["w1"].Attempts)
	}
}

// F. tasks keep independent start/finish timestamps.
func TestTasksKeepIndependentActualTimes(t *testing.T) {
	s := fixture()
	s.Apply(Event{Seq: 1, Type: "work.started", Ts: ts(1), Subject: "w1"})
	s.Apply(Event{Seq: 2, Type: "work.started", Ts: ts(2), Subject: "w2"})
	s.Apply(Event{Seq: 3, Type: "work.finished", Ts: ts(7), Subject: "w2"})
	s.Apply(Event{Seq: 4, Type: "work.finished", Ts: ts(9), Subject: "w1"})

	if got := s.Tasks["w1"]; !got.ActualStart.Equal(ts(1)) || !got.ActualFinish.Equal(ts(9)) {
		t.Errorf("w1 times = %v..%v, want %v..%v", got.ActualStart, got.ActualFinish, ts(1), ts(9))
	}
	if got := s.Tasks["w2"]; !got.ActualStart.Equal(ts(2)) || !got.ActualFinish.Equal(ts(7)) {
		t.Errorf("w2 times = %v..%v, want %v..%v", got.ActualStart, got.ActualFinish, ts(2), ts(7))
	}
	if got := s.Tasks["w1"].DurationSeconds(); got != 480 {
		t.Errorf("w1 duration = %ds, want 480", got)
	}
	if got := s.Tasks["w2"].DurationSeconds(); got != 300 {
		t.Errorf("w2 duration = %ds, want 300", got)
	}
}

// G + H. evidence, owner, worktree and GitHub facts survive into the rendered
// projection.
func TestEvidenceAndGitHubFactsSurviveProjection(t *testing.T) {
	s := fixture()
	s.Tasks["w1"].Evidence = Evidence{
		AgentSession: "worker-w1-sc2-abc",
		WorktreePath: "/city/.gc/worktrees/rig/worker-w1",
		SourceCommit: "d75f28e57c8c8063d10667ceeb6571b09850d7af",
		Ref:          "sb-20260810T171804Z",
	}
	s.Tasks["w1"].GitHub = GitHubFacts{
		PRNumber: 13, PRState: "MERGED",
		PRHeadSHA:   "d75f28e57c8c8063d10667ceeb6571b09850d7af",
		CIState:     "success",
		CITestedSHA: "d75f28e57c8c8063d10667ceeb6571b09850d7af",
		MergeState:  "MERGED",
		MergeSHA:    "3365da401bf5782bf923270735f9571d28451e83",
	}
	s.Tasks["w1"].Status = StatusMerged

	out, err := Render(s)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	text := string(out)
	for _, want := range []string{
		"worker-w1-sc2-abc",
		"/city/.gc/worktrees/rig/worker-w1",
		"pr_number: 13",
		"ci_tested_sha: \"d75f28e57c8c8063d10667ceeb6571b09850d7af\"",
		"merge_sha: \"3365da401bf5782bf923270735f9571d28451e83\"",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered projection is missing %q", want)
		}
	}
	// CI must be reported as its own tested SHA, so a CI result can be checked
	// against the PR head rather than assumed to match it.
	if !strings.Contains(text, "pr_head_sha:") || !strings.Contains(text, "ci_tested_sha:") {
		t.Errorf("PR head and CI tested SHA must both be projected as distinct facts")
	}
}

// I. dependency readiness is projected from edges and terminal state, with no
// invented unblock event.
func TestDependencyReadinessIsProjectedNotEvented(t *testing.T) {
	s := fixture()
	s.RecomputeBlockers()
	if s.Tasks["w3"].Status != StatusBlocked {
		t.Errorf("w3 status = %q, want blocked while its dependencies are outstanding", s.Tasks["w3"].Status)
	}
	if got := s.NextAuthorisedTask(); got != "w1" {
		t.Errorf("next authorized = %q, want w1", got)
	}

	s.Tasks["w1"].Status = StatusMerged
	s.RecomputeBlockers()
	if len(s.Tasks["w3"].Blockers) != 1 || s.Tasks["w3"].Blockers[0] != "w2" {
		t.Errorf("w3 blockers = %v, want [w2]", s.Tasks["w3"].Blockers)
	}

	s.Tasks["w2"].Status = StatusMerged
	s.RecomputeBlockers()
	if len(s.Tasks["w3"].Blockers) != 0 {
		t.Errorf("w3 still blocked after both dependencies merged: %v", s.Tasks["w3"].Blockers)
	}
	if s.Tasks["w3"].Status != StatusNotStarted {
		t.Errorf("w3 status = %q, want not-started once unblocked", s.Tasks["w3"].Status)
	}
	if got := s.NextAuthorisedTask(); got != "w3" {
		t.Errorf("next authorized = %q, want w3", got)
	}
}

// J. the same authoritative state renders byte-identically.
func TestRenderIsDeterministic(t *testing.T) {
	a, err := Render(fixture())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for i := 0; i < 5; i++ {
		b, err := Render(fixture())
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if string(a) != string(b) {
			t.Fatalf("projection is not deterministic; run %d differs", i)
		}
	}
}

// K. the generated file announces that it is generated, so nobody edits it and
// expects the edit to survive.
func TestGeneratedFileRefusesHandEditingByContract(t *testing.T) {
	out, err := Render(fixture())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(out), "DO NOT EDIT") {
		t.Errorf("the generated projection must say it is generated")
	}
}

// An unknown status must never render — silently becoming 0% progress is the
// failure this refuses.
func TestUnknownStatusIsRefusedNotDefaulted(t *testing.T) {
	s := fixture()
	s.Tasks["w1"].Status = Status("pull-request-open") // a plausible near-miss
	if _, err := Render(s); !errors.Is(err, ErrUnknownStatus) {
		t.Fatalf("Render error = %v, want ErrUnknownStatus; an unrecognized token must not render", err)
	}
}

// The two tokens whose spelling is called out explicitly.
func TestCanonicalStatusSpelling(t *testing.T) {
	if string(StatusPROpen) != "pr-open" {
		t.Errorf("StatusPROpen = %q, want pr-open", StatusPROpen)
	}
	if string(StatusDeployedUAT) != "deployed-uat" {
		t.Errorf("StatusDeployedUAT = %q, want deployed-uat", StatusDeployedUAT)
	}
	for _, wrong := range []Status{"pr-open-scoring", "pull-request-open", "deployed-to-uat"} {
		if err := ValidateStatus(wrong); err == nil {
			t.Errorf("%q was accepted; near-miss spellings must be refused", wrong)
		}
	}
}

// Absent timestamps render as null, never as an epoch that would read as a real
// date the producer has no authority for.
func TestAbsentTimesRenderAsNull(t *testing.T) {
	out, err := Render(fixture())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(out), "actual_start: null") {
		t.Errorf("an unknown actual_start must render as null, not as an epoch")
	}
}
