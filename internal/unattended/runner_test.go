package unattended

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// runFixture is a real git worktree plus a state directory outside it — the
// same shape a real run has, so the tests exercise the real ownership, fence
// and journal paths rather than stand-ins for them.
type runFixture struct {
	repo     string
	stateDir string
	spec     Spec
}

func newRunFixture(t *testing.T) *runFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the task fixtures assume a POSIX shell")
	}
	repo := newRepo(t, testOrigin)
	stateDir := filepath.Join(t.TempDir(), "run-state")

	return &runFixture{
		repo: repo, stateDir: stateDir,
		spec: Spec{
			ProjectID: "corsolv-delivery-engine",
			StateDir:  stateDir,
			Ownership: Ownership{
				ProjectID:      "corsolv-delivery-engine",
				Worktree:       repo,
				ExpectedOrigin: testOrigin,
				ExpectedBranch: "main",
				Role:           RoleWriter,
				Session:        "unattended-readiness-test",
			},
		},
	}
}

func (f *runFixture) begin(t *testing.T, plan Plan) *Session {
	t.Helper()
	s, err := Begin(context.Background(), f.spec, plan)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	t.Cleanup(func() { s.Close() }) //nolint:errcheck
	// Backoff policy is proved in failure_test; paying it here would only make
	// the suite slow.
	s.Runner.Sleep = func(context.Context, time.Duration) {}
	return s
}

func sh(script string) []string { return []string{"sh", "-c", script} }

func TestRunCompletesAPlanOfOrdinaryWork(t *testing.T) {
	f := newRunFixture(t)
	s := f.begin(t, Plan{RunID: "run-complete", Tasks: []Task{
		{ID: "primary", Title: "do the thing", Band: BandPrimary, Argv: sh("echo done")},
		{ID: "validate", Title: "prove it", Band: BandValidation, Argv: sh("true"), Needs: []string{"primary"}},
		{ID: "document", Title: "write it down", Band: BandDocumentation, Argv: sh("true")},
	}})

	event, err := s.Runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if event.Outcome != RunCompleted {
		t.Fatalf("outcome = %s (%s), want completed", event.Outcome, event.Reason)
	}
	if event.Tasks[TaskSucceeded] != 3 {
		t.Fatalf("succeeded = %d, want 3", event.Tasks[TaskSucceeded])
	}
	if event.Duration == "" || event.FinishedAt.IsZero() {
		t.Fatal("a completion event must carry real timing")
	}
}

func TestRunContinuesPastAnOrdinaryTestFailure(t *testing.T) {
	// Acceptance TEST 6. A failing test is what the run is for; it must be
	// retried and must not end the run, and other work must still get done.
	f := newRunFixture(t)
	marker := filepath.Join(f.stateDir, "attempts")
	s := f.begin(t, Plan{RunID: "run-flaky", Tasks: []Task{
		{
			ID: "flaky", Title: "a test that passes on the third attempt", Band: BandPrimary,
			// Succeeds once three attempt marks exist.
			Argv: sh(`mkdir -p "$(dirname ` + marker + `)"; echo x >> ` + marker +
				`; n=$(wc -l < ` + marker + `); if [ "$n" -ge 3 ]; then exit 0; fi; echo "--- FAIL: TestSomething"; exit 1`),
		},
		{ID: "docs", Title: "documentation", Band: BandDocumentation, Argv: sh("true")},
	}})

	event, err := s.Runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if event.Outcome != RunCompleted {
		t.Fatalf("outcome = %s (%s), want completed — a retried test failure is not a stop", event.Outcome, event.Reason)
	}
	flaky, _ := s.Queue.Task("flaky")
	if flaky.AttemptCount() != 3 {
		t.Fatalf("attempts = %d, want 3", flaky.AttemptCount())
	}
	if flaky.Attempts[0].Class != FailureCodeDefect {
		t.Fatalf("a test failure classified as %s, want code-defect", flaky.Attempts[0].Class)
	}
}

func TestRunFallsBackWhenThePrimaryPathExhaustsItsAttempts(t *testing.T) {
	// Acceptance TEST 5. The primary path cannot finish; the run must keep doing
	// declared work of a lower band rather than ending.
	f := newRunFixture(t)
	s := f.begin(t, Plan{RunID: "run-fallback", Tasks: []Task{
		{ID: "primary", Title: "blocked work", Band: BandPrimary, Argv: sh(`echo "--- FAIL: TestBroken"; exit 1`), MaxAttempts: 1},
		{ID: "dependent", Title: "needs the primary", Band: BandPrimary, Argv: sh("true"), Needs: []string{"primary"}},
		{ID: "validate", Title: "tests", Band: BandValidation, Argv: sh("true")},
		{ID: "evidence", Title: "reconcile evidence", Band: BandEvidence, Argv: sh("true")},
		{ID: "docs", Title: "documentation", Band: BandDocumentation, Argv: sh("true")},
	}})

	event, err := s.Runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if event.Tasks[TaskSucceeded] != 3 {
		t.Fatalf("succeeded = %d, want the 3 fallback tasks", event.Tasks[TaskSucceeded])
	}
	if event.Outcome != RunFailed {
		t.Fatalf("outcome = %s, want failed — the primary work genuinely did not get done", event.Outcome)
	}
	dependent, _ := s.Queue.Task("dependent")
	if dependent.State != TaskHeld {
		t.Fatalf("dependent = %s, want held", dependent.State)
	}
	// The point of the scenario: useful work continued after the primary path
	// died, instead of the run ending at it.
	for _, id := range []string{"validate", "evidence", "docs"} {
		qt, _ := s.Queue.Task(id)
		if qt.State != TaskSucceeded {
			t.Fatalf("fallback task %s = %s, want succeeded", id, qt.State)
		}
	}
}

func TestRunStopsWhenTheBranchMovesUnderneathIt(t *testing.T) {
	// Acceptance TEST 3. Something checks out another branch mid-run; the next
	// mutating stage must detect it and fail closed rather than write into
	// somebody else's ref.
	f := newRunFixture(t)
	repo := f.repo
	s := f.begin(t, Plan{RunID: "run-fence", Tasks: []Task{
		{
			ID: "first", Title: "an early mutation", Band: BandPrimary, Mutates: true,
			Argv: sh(`echo one > one.txt && git add one.txt && git commit -qm "feat: one"`),
		},
		{
			ID: "hijack", Title: "an external agent moves the branch", Band: BandPrimary,
			Argv: sh(`git checkout -q -b someone-elses-branch`), Needs: []string{"first"},
		},
		{
			ID: "second", Title: "a later mutation", Band: BandPrimary, Mutates: true,
			Argv:  sh(`echo two > two.txt && git add two.txt && git commit -qm "feat: two"`),
			Needs: []string{"hijack"},
		},
		{ID: "evidence", Title: "reconcile evidence", Band: BandEvidence, Argv: sh("true"), Needs: []string{"second"}},
	}})

	event, err := s.Runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if event.Outcome != RunFailed {
		t.Fatalf("outcome = %s, want failed", event.Outcome)
	}
	if !strings.Contains(event.Reason, "branch-changed") {
		t.Fatalf("reason = %q, want it to name the branch change", event.Reason)
	}
	// The mutation must not have happened.
	if _, err := os.Stat(filepath.Join(repo, "two.txt")); err == nil {
		t.Fatal("the run mutated the worktree after the branch moved underneath it")
	}

	records, _, err := ReadJournal(stateDirPath(f.stateDir, JournalName))
	if err != nil {
		t.Fatal(err)
	}
	var violated *Record
	for i := range records {
		if records[i].Kind == RecordFenceViolated {
			violated = &records[i]
		}
	}
	if violated == nil {
		t.Fatal("a fence violation must be journaled as evidence")
	}
	if !strings.Contains(violated.Detail, "expected") || !strings.Contains(violated.Detail, "observed") {
		t.Fatalf("the violation record must carry expected and observed: %q", violated.Detail)
	}
}

func TestAuthorisedMutationsAdvanceTheFenceRatherThanTrippingIt(t *testing.T) {
	// The converse of the previous test: the run's own commits must not read as
	// external changes, or every mutating plan would stop at its second task.
	f := newRunFixture(t)
	s := f.begin(t, Plan{RunID: "run-advance", Tasks: []Task{
		{
			ID: "one", Title: "commit one", Band: BandPrimary, Mutates: true,
			Argv: sh(`echo one > one.txt && git add one.txt && git commit -qm "feat: one"`),
		},
		{
			ID: "two", Title: "commit two", Band: BandPrimary, Mutates: true, Needs: []string{"one"},
			Argv: sh(`echo two > two.txt && git add two.txt && git commit -qm "feat: two"`),
		},
		{
			ID: "three", Title: "commit three", Band: BandPrimary, Mutates: true, Needs: []string{"two"},
			Argv: sh(`echo three > three.txt && git add three.txt && git commit -qm "feat: three"`),
		},
		{ID: "evidence", Title: "reconcile evidence", Band: BandEvidence, Argv: sh("true"), Needs: []string{"three"}},
	}})

	event, err := s.Runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if event.Outcome != RunCompleted {
		t.Fatalf("outcome = %s (%s), want completed", event.Outcome, event.Reason)
	}
	if len(s.Fence.Advances) != 3 {
		t.Fatalf("recorded advances = %d, want 3", len(s.Fence.Advances))
	}
	for _, a := range s.Fence.Advances {
		if a.Reason == "" || a.From == a.To {
			t.Fatalf("an advance must record a real movement and its reason: %+v", a)
		}
	}
}

func TestRunHoldsWorkBehindAHumanBoundaryAndReportsIt(t *testing.T) {
	// Acceptance TEST 8. The boundary is classified, surfaced, and never
	// retried as though it were a transient fault.
	f := newRunFixture(t)
	f.spec.Credentials = []CredentialRequirement{{
		ID: "deployment-token", Title: "deployment token",
		Env:         "GC_UNATTENDED_TOKEN_THAT_IS_NOT_SET",
		HumanAction: "obtain a deployment token from the platform team",
	}}
	s := f.begin(t, Plan{RunID: "run-boundary", Tasks: []Task{
		{
			ID: "deploy", Title: "deploy", Band: BandPrimary, Argv: sh("true"),
			RequiresChecks: []string{"credential.deployment-token"},
		},
		{ID: "tests", Title: "tests", Band: BandValidation, Argv: sh("true")},
	}})

	if s.Report.Readiness != ReadyWithKnownHumanBoundary {
		t.Fatalf("readiness = %s, want READY-WITH-KNOWN-HUMAN-BOUNDARY", s.Report.Readiness)
	}
	event, err := s.Runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if event.Outcome != RunAwaitingAuth {
		t.Fatalf("outcome = %s (%s), want awaiting-auth", event.Outcome, event.Reason)
	}
	deploy, _ := s.Queue.Task("deploy")
	if deploy.State != TaskHeld {
		t.Fatalf("task behind a boundary = %s, want held", deploy.State)
	}
	if deploy.AttemptCount() != 0 {
		t.Fatalf("a human boundary was attempted %d time(s); it must never be retried", deploy.AttemptCount())
	}
	if len(event.HumanActions) == 0 {
		t.Fatal("the completion event must say what a person has to do")
	}
	tests, _ := s.Queue.Task("tests")
	if tests.State != TaskSucceeded {
		t.Fatal("work that did not need the boundary must still have been done")
	}
}

func TestSecondWriterIsRefusedAtSessionStart(t *testing.T) {
	// Acceptance TEST 1, at the level a real run meets it.
	f := newRunFixture(t)
	twoBandPlan := func(runID string) Plan {
		return Plan{RunID: runID, Tasks: []Task{
			{ID: "t", Title: "t", Band: BandPrimary, Argv: sh("true")},
			{ID: "docs", Title: "docs", Band: BandDocumentation, Argv: sh("true")},
		}}
	}
	f.begin(t, twoBandPlan("run-first"))

	second := f.spec
	second.Ownership.Session = "a-second-session"
	_, err := Begin(context.Background(), second, twoBandPlan("run-second"))
	if err == nil {
		t.Fatal("a second writer must not be admitted to a worktree a run already holds")
	}
	// Preflight sees the recorded owner first; the lock is what makes it true.
	if !errors.Is(err, ErrWriterHeld) && !errors.Is(err, ErrNotReady) {
		t.Fatalf("second writer error = %v, want ErrWriterHeld or ErrNotReady", err)
	}
}

func TestWrongRepositoryIsRefusedBeforeAnyMutation(t *testing.T) {
	// Acceptance TEST 2. Every signal but the remote looks plausible, which is
	// exactly why this used to be discovered late.
	f := newRunFixture(t)
	other := newRepo(t, "https://github.com/gastownhall/gascity.git")
	f.spec.Ownership.Worktree = other

	_, err := Begin(context.Background(), f.spec, Plan{
		RunID: "run-wrong-repo",
		Tasks: []Task{
			{ID: "mutate", Title: "mutate", Band: BandPrimary, Mutates: true, Argv: sh("echo x > x.txt")},
			{ID: "docs", Title: "docs", Band: BandDocumentation, Argv: sh("true")},
		},
	})
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("Begin against the wrong repository = %v, want ErrNotReady", err)
	}
	if _, statErr := os.Stat(filepath.Join(other, "x.txt")); statErr == nil {
		t.Fatal("the run mutated a repository it did not own")
	}
	// The refusal must be durable, not merely returned.
	if _, rerr := os.Stat(stateDirPath(f.stateDir, PreflightReportName)); rerr != nil {
		t.Fatal("a refused run must still leave the reason it was refused")
	}
}

func TestInterruptedRunResumesWithoutRepeatingCompletedWork(t *testing.T) {
	// Acceptance TEST 9. The first session dies after some work; the second
	// must continue from the last durable boundary rather than starting over.
	f := newRunFixture(t)
	counter := filepath.Join(f.stateDir, "ran")
	countingTask := func(id string, band Band) Task {
		return Task{
			ID: id, Title: id, Band: band,
			Argv: sh(`mkdir -p ` + f.stateDir + ` && echo ` + id + ` >> ` + counter),
		}
	}
	plan := Plan{RunID: "run-resume", Tasks: []Task{
		countingTask("first", BandPrimary),
		countingTask("second", BandValidation),
		countingTask("third", BandDocumentation),
	}}

	// First session: run only the first task, then die without closing cleanly.
	first, err := Begin(context.Background(), f.spec, Plan{RunID: "run-resume", Tasks: plan.Tasks[:2]})
	if err != nil {
		t.Fatalf("first Begin: %v", err)
	}
	first.Runner.Sleep = func(context.Context, time.Duration) {}
	if _, err := first.Runner.Run(context.Background()); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Second session: the full plan, over the same journal.
	second, err := Begin(context.Background(), f.spec, plan)
	if err != nil {
		t.Fatalf("second Begin: %v", err)
	}
	defer second.Close() //nolint:errcheck
	second.Runner.Sleep = func(context.Context, time.Duration) {}

	if !second.Resumed {
		t.Fatal("the second session must know it resumed an existing journal")
	}
	if _, err := second.Runner.Run(context.Background()); err != nil {
		t.Fatalf("second Run: %v", err)
	}

	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	ran := strings.Fields(string(data))
	if len(ran) != 3 {
		t.Fatalf("tasks executed %v, want exactly three — completed work must not be repeated", ran)
	}
	firstTask, _ := second.Queue.Task("first")
	if firstTask.AttemptCount() != 0 {
		t.Fatalf("the already-completed task was attempted %d more time(s)", firstTask.AttemptCount())
	}
}

func TestResumeDoesNotDuplicateCompletionEvidence(t *testing.T) {
	f := newRunFixture(t)
	plan := Plan{RunID: "run-once", Tasks: []Task{
		{ID: "only", Title: "only", Band: BandPrimary, Argv: sh("true")},
		{ID: "docs", Title: "docs", Band: BandDocumentation, Argv: sh("true")},
	}}

	for i := 0; i < 2; i++ {
		s, err := Begin(context.Background(), f.spec, plan)
		if err != nil {
			t.Fatalf("Begin %d: %v", i, err)
		}
		s.Runner.Sleep = func(context.Context, time.Duration) {}
		if _, err := s.Runner.Run(context.Background()); err != nil {
			t.Fatalf("Run %d: %v", i, err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close %d: %v", i, err)
		}
	}

	records, _, err := ReadJournal(stateDirPath(f.stateDir, JournalName))
	if err != nil {
		t.Fatal(err)
	}
	succeeded := 0
	for _, r := range records {
		if r.Kind == RecordTaskSucceeded && r.TaskID == "only" {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("task success recorded %d times across two runs, want 1", succeeded)
	}
}

func TestRunPublishesProgressWhileItIsStillRunning(t *testing.T) {
	// An unattended run that only answers when questioned is not unattended.
	f := newRunFixture(t)
	s := f.begin(t, Plan{RunID: "run-progress", Tasks: []Task{
		{ID: "primary", Title: "primary", Band: BandPrimary, Argv: sh("true")},
		{ID: "docs", Title: "docs", Band: BandDocumentation, Argv: sh("true")},
	}})

	var seen []Progress
	s.Runner.OnProgress = func(p Progress) { seen = append(seen, p) }
	if _, err := s.Runner.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(seen) < 3 {
		t.Fatalf("progress published %d times, want one per state change", len(seen))
	}

	p, ok, err := ReadProgress(f.stateDir)
	if err != nil || !ok {
		t.Fatalf("ReadProgress: ok=%v err=%v", ok, err)
	}
	for name, got := range map[string]string{
		"runId": p.RunID, "projectId": p.ProjectID, "session": p.Session,
		"stage": p.Stage, "worktree": p.Worktree, "branch": p.Branch, "head": p.Head,
		"elapsed": p.Elapsed, "writerOwner": p.WriterOwner,
	} {
		if got == "" {
			t.Fatalf("published progress omits %s", name)
		}
	}
	if p.WriterPID == 0 {
		t.Fatal("published progress must name the owning process")
	}
}

func TestProgressSaysWhenTheRunIsOnFallbackWork(t *testing.T) {
	// Steady progress on documentation must not look identical to steady
	// progress on the thing the run was for.
	f := newRunFixture(t)
	s := f.begin(t, Plan{RunID: "run-fallback-flag", Tasks: []Task{
		{ID: "primary", Title: "primary", Band: BandPrimary, Argv: sh("exit 1"), MaxAttempts: 1},
		{ID: "docs", Title: "docs", Band: BandDocumentation, Argv: sh("true")},
	}})

	sawFallback := false
	s.Runner.OnProgress = func(p Progress) {
		if p.CurrentTask == "docs" && p.UsingFallback {
			sawFallback = true
		}
	}
	if _, err := s.Runner.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !sawFallback {
		t.Fatal("progress never reported that the run had dropped to fallback work")
	}
}

func TestProgressDoesNotCallOrdinaryWorkAFallback(t *testing.T) {
	// The converse. Once every primary task has succeeded, validation and
	// documentation work is the plan. Reporting it as fallback tells a person
	// reading the heartbeat that the run is in trouble when it is doing exactly
	// what it should — which the first real run did.
	f := newRunFixture(t)
	s := f.begin(t, Plan{RunID: "run-not-fallback", Tasks: []Task{
		{ID: "primary", Title: "primary", Band: BandPrimary, Argv: sh("true")},
		{ID: "docs", Title: "docs", Band: BandDocumentation, Argv: sh("true")},
	}})

	claimedFallback := false
	s.Runner.OnProgress = func(p Progress) {
		if p.CurrentTask == "docs" && p.UsingFallback {
			claimedFallback = true
		}
	}
	if _, err := s.Runner.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if claimedFallback {
		t.Fatal("lower-band work after the primary path succeeded was reported as a fallback")
	}
}

func TestCompletionEventIsWrittenForANotificationLayerToFind(t *testing.T) {
	f := newRunFixture(t)
	s := f.begin(t, Plan{RunID: "run-event", Tasks: []Task{
		{ID: "t", Title: "t", Band: BandPrimary, Argv: sh("true")},
		{ID: "docs", Title: "docs", Band: BandDocumentation, Argv: sh("true")},
	}})
	if _, err := s.Runner.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	e, ok, err := ReadCompletion(f.stateDir)
	if err != nil || !ok {
		t.Fatalf("ReadCompletion: ok=%v err=%v", ok, err)
	}
	if e.Outcome != RunCompleted {
		t.Fatalf("event outcome = %s", e.Outcome)
	}
	if e.SessionLabel == "" {
		t.Fatal("the event must carry the logical session name a notification announces")
	}
	if line := e.String(); line == "" || strings.Contains(line, "\n") {
		t.Fatalf("the event must render to a single line, got %q", line)
	}
	if RunCompleted.NeedsAttention() {
		t.Fatal("a clean completion must not demand attention")
	}
	for _, o := range []RunOutcome{RunFailed, RunBlockedHuman, RunAwaitingAuth} {
		if !o.NeedsAttention() {
			t.Fatalf("%s must demand attention", o)
		}
	}
}

func TestDeliveryProjectionExistsWhileTheRunIsStillRunning(t *testing.T) {
	// The GUK BPM pilot found this. Its evidence task exists to commit the
	// delivery projection into the target repository, and a projection written
	// only after the run has ended does not exist while the run is still
	// executing the task that needs it.
	f := newRunFixture(t)
	f.spec.PublishPath = filepath.Join(f.stateDir, "PROJECT-STATE.yml")
	s := f.begin(t, Plan{RunID: "run-projection", Tasks: []Task{
		{
			ID: "ship", Title: "ship", Band: BandPrimary, Argv: sh("true"),
			DeliveryStatus: "merged", CompletionGate: "a gate", Phase: "p",
		},
		{
			// Reads the projection the way the pilot's evidence task does.
			ID: "consume", Title: "consume the projection", Band: BandEvidence,
			Argv:  sh("test -s " + filepath.Join(f.stateDir, "PROJECT-STATE.yml")),
			Needs: []string{"ship"},
		},
	}})

	event, err := s.Runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	consume, _ := s.Queue.Task("consume")
	if consume.State != TaskSucceeded {
		t.Fatalf("a task consuming the projection mid-run = %s, want succeeded (%s)", consume.State, event.Reason)
	}
}

func TestFailureOutputIsCapturedWhereAPersonCanReadIt(t *testing.T) {
	// The journal keeps one line per record, so a failure's actual output has
	// nowhere to go in it. Without this the run says a task failed and cannot
	// say why — and "it failed at three in the morning and I need to know why"
	// is the entire point of running unattended.
	f := newRunFixture(t)
	s := f.begin(t, Plan{RunID: "run-capture", Tasks: []Task{
		{
			ID: "loud", Title: "a task that explains itself before failing", Band: BandPrimary,
			MaxAttempts: 1,
			Argv:        sh(`echo "line one of the explanation"; echo "line two names a token ghp_0123456789abcdefghijklmnopqrstuvwxyz"; exit 1`),
		},
		{ID: "docs", Title: "docs", Band: BandDocumentation, Argv: sh("true")},
	}})

	if _, err := s.Runner.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	path := filepath.Join(f.stateDir, FailuresDirName, "loud-attempt-1.log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failure output was not captured: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, "line one of the explanation") || !strings.Contains(body, "line two") {
		t.Fatalf("the capture lost the output that explains the failure:\n%s", body)
	}
	// It outlives the run, so it must not carry credential material out of it.
	if strings.Contains(body, "ghp_0123456789") {
		t.Fatalf("a credential reached durable failure output:\n%s", body)
	}

	records, _, err := ReadJournal(stateDirPath(f.stateDir, JournalName))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range records {
		if r.Kind == RecordTaskFailed && r.TaskID == "loud" && strings.Contains(r.Detail, path) {
			found = true
		}
	}
	if !found {
		t.Fatal("the journal must point at the captured output, or nobody will find it")
	}
}

func TestATaskThatOverrunsItsTimeoutIsAFailureNotAHang(t *testing.T) {
	f := newRunFixture(t)
	s := f.begin(t, Plan{RunID: "run-timeout", Tasks: []Task{
		{
			ID: "slow", Title: "a task that never finishes", Band: BandPrimary,
			Argv: sh("sleep 60"), TimeoutSeconds: 1, MaxAttempts: 1,
		},
		{ID: "docs", Title: "docs", Band: BandDocumentation, Argv: sh("true")},
	}})

	start := time.Now()
	event, err := s.Runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if time.Since(start) > 30*time.Second {
		t.Fatal("the run waited out a task that should have been bounded")
	}
	slow, _ := s.Queue.Task("slow")
	if slow.State != TaskFailed {
		t.Fatalf("timed-out task = %s, want failed", slow.State)
	}
	if event.Tasks[TaskSucceeded] != 1 {
		t.Fatal("the run must have carried on to the remaining work")
	}
}
