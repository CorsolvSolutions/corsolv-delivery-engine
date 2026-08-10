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

// fixture builds a three-task graph: W3 depends on W1 and W2.
func fixture() *State {
	s := NewState("corsolv-autonomy-poc")
	s.Project.CurrentPhase = "first-runner"
	s.Project.CurrentMilestone = "S-B promoted run"
	s.Tasks["W1"] = &Task{TaskID: "W1", Title: "add", Status: StatusPlanned, TaskType: "code", OwnerType: "agent"}
	s.Tasks["W2"] = &Task{TaskID: "W2", Title: "multiply", Status: StatusPlanned, TaskType: "code", OwnerType: "agent"}
	s.Tasks["W3"] = &Task{
		TaskID: "W3", Title: "calculator", Status: StatusPlanned, TaskType: "code", OwnerType: "agent",
		Dependencies: []string{"W1", "W2"},
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
		{Seq: 1, Type: "work.started", Ts: ts(1), Subject: "W1"},
		{Seq: 2, Type: "work.finished", Ts: ts(5), Subject: "W1"},
	}
	cur, err := Project(events, s, statePath, cursorPath, nil)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if cur.Seq != 2 {
		t.Errorf("cursor = %d, want 2", cur.Seq)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Errorf("projection was not written: %v", err)
	}
}

// B. resume from a persisted cursor.
func TestResumeFromPersistedCursor(t *testing.T) {
	statePath, cursorPath := paths(t)
	s := fixture()
	if _, err := Project([]Event{{Seq: 1, Type: "work.started", Ts: ts(1), Subject: "W1"}},
		s, statePath, cursorPath, nil); err != nil {
		t.Fatalf("first Project: %v", err)
	}

	resumed := fixture()
	all := []Event{
		{Seq: 1, Type: "work.started", Ts: ts(9), Subject: "W1"}, // replayed with a DIFFERENT ts
		{Seq: 2, Type: "work.finished", Ts: ts(6), Subject: "W1"},
	}
	cur, err := Project(all, resumed, statePath, cursorPath, nil)
	if err != nil {
		t.Fatalf("resumed Project: %v", err)
	}
	if cur.Seq != 2 {
		t.Errorf("cursor = %d, want 2", cur.Seq)
	}
	if !resumed.Tasks["W1"].ActualStart.IsZero() {
		t.Errorf("an event at or below the cursor was re-applied on resume")
	}
	if !resumed.Tasks["W1"].ActualFinish.Equal(ts(6)) {
		t.Errorf("the event after the cursor was not applied on resume")
	}
}

// C. replay does not duplicate attempts or move the start.
func TestReplayIsIdempotent(t *testing.T) {
	s := fixture()
	start := Event{Seq: 1, Type: "work.started", Ts: ts(1), Subject: "W1"}
	s.Apply(start)
	attemptsAfterFirst := len(s.Tasks["W1"].Attempts)
	startAfterFirst := s.Tasks["W1"].ActualStart

	s.Apply(start)
	s.Apply(Event{Seq: 1, Type: "work.started", Ts: ts(9), Subject: "W1"})

	got := s.Tasks["W1"]
	if len(got.Attempts) != attemptsAfterFirst {
		t.Errorf("attempts = %d after replay, want %d; the history is not idempotent",
			len(got.Attempts), attemptsAfterFirst)
	}
	if !got.ActualStart.Equal(startAfterFirst) {
		t.Errorf("actualStart moved on replay; start must be first-write-wins")
	}
}

// D + E. crash between the durable state write and the cursor write.
func TestCrashBetweenStateAndCursorReplaysSafely(t *testing.T) {
	statePath, cursorPath := paths(t)
	s := fixture()
	events := []Event{
		{Seq: 1, Type: "work.started", Ts: ts(1), Subject: "W1"},
		{Seq: 2, Type: "work.finished", Ts: ts(5), Subject: "W1"},
	}

	crash := errors.New("crash before the cursor write")
	func() {
		defer func() {
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

	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("state was not durable before the crash: %v", err)
	}
	cur, err := LoadCursor(cursorPath)
	if err != nil {
		t.Fatalf("LoadCursor: %v", err)
	}
	if cur.Seq != 0 {
		t.Fatalf("cursor advanced to %d despite the crash; it must never lead durable state", cur.Seq)
	}

	replayed := fixture()
	if _, err := Project(events, replayed, statePath, cursorPath, nil); err != nil {
		t.Fatalf("replay Project: %v", err)
	}
	if len(replayed.Tasks["W1"].Attempts) != 1 {
		t.Errorf("attempts after replay = %d, want 1", len(replayed.Tasks["W1"].Attempts))
	}
}

// F. tasks keep independent start/finish.
func TestTasksKeepIndependentActualTimes(t *testing.T) {
	s := fixture()
	s.Apply(Event{Seq: 1, Type: "work.started", Ts: ts(1), Subject: "W1"})
	s.Apply(Event{Seq: 2, Type: "work.started", Ts: ts(2), Subject: "W2"})
	s.SetTerminalFinish("W2", ts(7))
	s.SetTerminalFinish("W1", ts(9))

	if got := s.Tasks["W1"]; !got.ActualStart.Equal(ts(1)) || !got.ActualFinish.Equal(ts(9)) {
		t.Errorf("W1 times = %v..%v, want %v..%v", got.ActualStart, got.ActualFinish, ts(1), ts(9))
	}
	if got := s.Tasks["W2"]; !got.ActualStart.Equal(ts(2)) || !got.ActualFinish.Equal(ts(7)) {
		t.Errorf("W2 times = %v..%v, want %v..%v", got.ActualStart, got.ActualFinish, ts(2), ts(7))
	}
}

// The terminal-record join is the ONLY authoritative finish source, because the
// city event log does not carry work-bead closures.
func TestTerminalBeadRecordProducesActualFinish(t *testing.T) {
	s := fixture()
	s.Apply(Event{Seq: 1, Type: "work.started", Ts: ts(1), Subject: "W1"})
	if !s.Tasks["W1"].ActualFinish.IsZero() {
		t.Fatalf("a started-but-unclosed task must have no finish")
	}

	closedAt := time.Date(2026, 8, 10, 17, 20, 10, 0, time.UTC)
	s.SetTerminalFinish("W1", closedAt)

	if !s.Tasks["W1"].ActualFinish.Equal(closedAt) {
		t.Errorf("actualFinish = %v, want the bead terminal closed_at %v", s.Tasks["W1"].ActualFinish, closedAt)
	}
	if got := s.Tasks["W1"].Attempts[0].Outcome; got != "succeeded" {
		t.Errorf("the attempt that reached a terminal record has outcome %q, want succeeded", got)
	}
	// A zero terminal record must never manufacture a finish.
	s.SetTerminalFinish("W2", time.Time{})
	if !s.Tasks["W2"].ActualFinish.IsZero() {
		t.Errorf("an absent terminal record produced a finish timestamp")
	}
}

// G. evidence and identifiers survive in the consumer's flat string shape.
func TestEvidenceSurvivesInConsumerShape(t *testing.T) {
	s := fixture()
	s.Tasks["W1"].Evidence = []string{
		"session=worker-w1-sc2-j1k",
		"worktree=/city/.gc/worktrees/rig/worker-w1",
		"run=sb-20260810T171804Z",
	}
	s.Tasks["W1"].ImplementationSha = "d75f28e57c8c8063d10667ceeb6571b09850d7af"
	s.Tasks["W1"].PullRequest = 13
	s.Tasks["W1"].Status = StatusMerged

	out, err := Render(s)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	text := string(out)
	for _, want := range []string{
		`- "session=worker-w1-sc2-j1k"`,
		`- "worktree=/city/.gc/worktrees/rig/worker-w1"`,
		`implementationSha: "d75f28e57c8c8063d10667ceeb6571b09850d7af"`,
		"pullRequest: 13",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered document is missing %q", want)
		}
	}
}

// Live GitHub state must NOT be snapshotted: the dashboard reads it from
// GitHub, and a second structured copy goes stale the moment CI moves.
func TestLiveGitHubStateIsNotSnapshotted(t *testing.T) {
	out, err := Render(fixture())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, forbidden := range []string{"ci_state", "ciState", "merge_sha", "mergeSha", "pr_head_sha", "prHeadSha", "merge_state"} {
		if strings.Contains(string(out), forbidden) {
			t.Errorf("document carries %q; live GitHub state is the dashboard's authority, not this file's", forbidden)
		}
	}
}

// One fact, one authority: the consumer derives duration from start and finish.
func TestNoDuplicateDurationAuthority(t *testing.T) {
	out, err := Render(fixture())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, forbidden := range []string{"actual_duration_seconds", "durationSeconds", "actualDuration"} {
		if strings.Contains(string(out), forbidden) {
			t.Errorf("document carries %q; duration is derived from actualStart/actualFinish", forbidden)
		}
	}
}

// I. dependency readiness is projected, not evented.
func TestDependencyReadinessIsProjectedNotEvented(t *testing.T) {
	s := fixture()
	s.RecomputeBlockers()
	if s.Tasks["W3"].Status != StatusBlocked {
		t.Errorf("W3 status = %q, want blocked while dependencies are outstanding", s.Tasks["W3"].Status)
	}
	if !strings.Contains(s.Tasks["W3"].Blocker, "W1") || !strings.Contains(s.Tasks["W3"].Blocker, "W2") {
		t.Errorf("W3 blocker = %q, want both outstanding dependencies named", s.Tasks["W3"].Blocker)
	}

	s.Tasks["W1"].Status = StatusMerged
	s.Tasks["W2"].Status = StatusMerged
	s.RecomputeBlockers()
	if s.Tasks["W3"].Blocker != "" {
		t.Errorf("W3 still blocked after both dependencies merged: %q", s.Tasks["W3"].Blocker)
	}
	if s.Tasks["W3"].Status != StatusPlanned {
		t.Errorf("W3 status = %q, want planned once unblocked", s.Tasks["W3"].Status)
	}
}

// J. deterministic rendering.
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

// K. the generated file announces that it is generated.
func TestGeneratedFileRefusesHandEditingByContract(t *testing.T) {
	out, err := Render(fixture())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(out), "DO NOT EDIT") {
		t.Errorf("the generated projection must say it is generated")
	}
}

// An unknown status must never render. The consumer holds it at 0% and reports
// it, so emitting a near-miss is worse than emitting nothing.
func TestUnknownStatusIsRefusedNotDefaulted(t *testing.T) {
	s := fixture()
	s.Tasks["W1"].Status = TaskStatus("pull-request-open")
	if _, err := Render(s); !errors.Is(err, ErrUnknownStatus) {
		t.Fatalf("Render error = %v, want ErrUnknownStatus", err)
	}
}

// The canonical vocabulary, including the spellings whose near-misses are the
// known failure mode.
func TestCanonicalStatusSpelling(t *testing.T) {
	if string(StatusPROpen) != "pr-open" {
		t.Errorf("StatusPROpen = %q, want pr-open", StatusPROpen)
	}
	if string(StatusDeployedUAT) != "deployed-uat" {
		t.Errorf("StatusDeployedUAT = %q, want deployed-uat", StatusDeployedUAT)
	}
	for _, wrong := range []TaskStatus{"pr-open-scoring", "pull-request-open", "deployed-to-uat", "done", "in-progress"} {
		if err := ValidateTaskStatus(wrong); err == nil {
			t.Errorf("%q was accepted; it is not in the canonical vocabulary", wrong)
		}
	}
	for _, right := range []TaskStatus{
		StatusPlanned, StatusActive, StatusBlocked, StatusChecksPassing,
		StatusMerged, StatusVerified, StatusComplete,
	} {
		if err := ValidateTaskStatus(right); err != nil {
			t.Errorf("canonical status %q was refused: %v", right, err)
		}
	}
}

// THE NEGATIVE GATE CASE. merged is a publication fact, not an acceptance one:
// the consumer scores it at 70% and reserves 100% for a met completion gate.
// Deriving "met" from status alone would manufacture acceptance.
func TestMergedStatusDoesNotImplyCompletionGateMet(t *testing.T) {
	s := fixture()
	s.Tasks["W1"].Status = StatusMerged
	out, err := Render(s)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(string(out), `completionGateStatus: "met"`) {
		t.Errorf("a merged task rendered completionGateStatus met with no gate evidence")
	}

	// And when a real authority does assert it, it renders.
	s.Tasks["W1"].CompletionGateStatus = GateMet
	s.Tasks["W1"].CompletionGate = "required CI on exact head + independent assurance + governed merge"
	out, err = Render(s)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(out), `completionGateStatus: "met"`) {
		t.Errorf("a proven gate did not render as met")
	}
}

// Absent optional values render as null, never as an epoch or a blank that
// could read as a real answer.
func TestAbsentValuesRenderAsNull(t *testing.T) {
	out, err := Render(fixture())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{"actualStart: null", "actualFinish: null", "pullRequest: null", "implementationSha: null"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("expected %q for an unknown value", want)
		}
	}
}

// The structural compatibility rules the consumer enforces: schemaVersion 1, an
// activeTasks array, and a project OBJECT. Failing any one makes the dashboard
// refuse the whole document.
func TestDocumentSatisfiesConsumerStructuralContract(t *testing.T) {
	out, err := Render(fixture())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	text := string(out)
	if !strings.Contains(text, "schemaVersion: 1") {
		t.Errorf("missing schemaVersion: 1 — the consumer refuses any other version")
	}
	if !strings.Contains(text, "\nproject:\n  projectId:") {
		t.Errorf("project must be an OBJECT with projectId; a bare string is refused")
	}
	if !strings.Contains(text, "\nactiveTasks:\n") {
		t.Errorf("missing activeTasks — the consumer reads activeTasks, not tasks")
	}
	if strings.Contains(text, "\ntasks:\n") {
		t.Errorf("document still emits a legacy tasks array")
	}
	for _, want := range []string{"taskId:", "dependencies:", "parallelGroup:", "currentBlockers:", "recentCompletedOutcomes:"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing consumer field %q", want)
		}
	}
	for _, gone := range []string{"\n  - id:", "depends_on:", "actual_start:", "actual_finish:", "next_authorized_task:"} {
		if strings.Contains(text, gone) {
			t.Errorf("document still carries the legacy producer spelling %q", gone)
		}
	}
}

// Attempts must be a structured, dated array — a count cannot be plotted.
func TestAttemptsAreStructuredAndDated(t *testing.T) {
	s := fixture()
	s.Apply(Event{Seq: 1, Type: "work.started", Ts: ts(1), Subject: "W3"})
	s.Apply(Event{Seq: 2, Type: "work.started", Ts: ts(4), Subject: "W3"})
	s.SetTerminalFinish("W3", ts(8))

	got := s.Tasks["W3"].Attempts
	if len(got) != 2 {
		t.Fatalf("attempts = %d, want 2 dated entries", len(got))
	}
	if got[0].Date.IsZero() || got[1].Date.IsZero() {
		t.Errorf("every attempt must carry an authoritative date")
	}
	if got[0].Outcome != "failed" || got[1].Outcome != "succeeded" {
		t.Errorf("attempt outcomes = %q,%q; want the earlier failed and the terminal one succeeded",
			got[0].Outcome, got[1].Outcome)
	}

	out, err := Render(s)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(out), "attempts:\n      - date:") {
		t.Errorf("attempts must render as a structured array")
	}
	if strings.Contains(string(out), "attempts: 2") {
		t.Errorf("attempts must not render as a bare count")
	}
}
