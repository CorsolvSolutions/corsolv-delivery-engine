package unattended

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These are the run-level controller-safety regressions: the same outcome
// matrix as controller_test.go, driven through the real Run loop, the real
// queue, the real fence, the real journal and the real completion event.
//
// The agent is scripted rather than shelled out to, which is what makes them
// deterministic: a supervised outcome is an exact structured document plus an
// exact exit status, and reproducing "the harness stopped this agent at its
// turn cap" with a shell script would be reproducing an approximation of the
// thing under test.

// controllerFixture is a real git worktree plus a state directory outside it.
//
// Unlike runFixture it never shells out, so it runs on every platform this
// engine is hosted on — including the Windows host the pilot found these
// defects on.
type controllerFixture struct {
	repo     string
	stateDir string
	spec     Spec
}

func newControllerFixture(t *testing.T) *controllerFixture {
	t.Helper()
	repo := newRepo(t, testOrigin)
	stateDir := filepath.Join(t.TempDir(), "run-state")
	return &controllerFixture{
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
				Session:        "qa-002-controller-safety",
			},
		},
	}
}

func (f *controllerFixture) begin(t *testing.T, plan Plan, agent *scriptedAgent) *Session {
	t.Helper()
	s, err := Begin(context.Background(), f.spec, plan)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	t.Cleanup(func() { s.Close() }) //nolint:errcheck
	s.Runner.Sleep = func(context.Context, time.Duration) {}
	if agent != nil {
		s.Runner.Observe = agent.observe
	}
	return s
}

// scriptedAgent replays an exact sequence of attempt outcomes per task.
//
// It answers the question the shell cannot: what does the run do when the
// second drive of this task is cut off at its turn cap and the third completes.
// A sequence that runs out repeats its final step, so a task the run keeps
// driving cannot silently fall off the end of the script.
type scriptedAgent struct {
	steps map[string][]Execution
	calls map[string]int
}

func newScriptedAgent() *scriptedAgent {
	return &scriptedAgent{steps: map[string][]Execution{}, calls: map[string]int{}}
}

// on declares the sequence of outcomes a task produces, attempt by attempt.
func (a *scriptedAgent) on(taskID string, steps ...Execution) *scriptedAgent {
	a.steps[taskID] = steps
	return a
}

func (a *scriptedAgent) observe(_ context.Context, t Task) Execution {
	i := a.calls[t.ID]
	a.calls[t.ID]++
	steps, ok := a.steps[t.ID]
	if !ok || len(steps) == 0 {
		// A task with no script is ordinary unsupervised work that succeeded.
		return Execution{ExitedZero: true}
	}
	if i >= len(steps) {
		i = len(steps) - 1
	}
	return steps[i]
}

// count is how many times a task was driven.
func (a *scriptedAgent) count(taskID string) int { return a.calls[taskID] }

// supervisedTask is a task that declares it will state its own outcome.
func supervisedTask(id string, band Band) Task {
	return Task{
		ID: id, Title: id, Band: band,
		Argv:       []string{"the-agent-harness", "--task", id},
		ResultPath: id + "-result.json",
	}
}

// docsTask is ordinary unsupervised work below the primary band, so a run that
// stops has something it demonstrably did not get to.
func docsTask() Task {
	return Task{ID: "docs", Title: "docs", Band: BandDocumentation, Argv: []string{"true"}}
}

func journalKinds(t *testing.T, stateDir string) []Record {
	t.Helper()
	records, _, err := ReadJournal(stateDirPath(stateDir, JournalName))
	if err != nil {
		t.Fatalf("ReadJournal: %v", err)
	}
	return records
}

func countRecords(records []Record, kind RecordKind, taskID string) int {
	n := 0
	for _, r := range records {
		if r.Kind == kind && (taskID == "" || r.TaskID == taskID) {
			n++
		}
	}
	return n
}

// --- CONTINUE ---------------------------------------------------------------

func TestExitZeroWithCONTINUEDrivesTheTaskAgain(t *testing.T) {
	f := newControllerFixture(t)
	agent := newScriptedAgent().on("work",
		structured(true, ControllerResult{State: StateContinue, NumTurns: 4}, "turn 1-4"),
		structured(true, ControllerResult{State: StateContinue, NumTurns: 8}, "turn 5-8"),
		structured(true, ControllerResult{State: StateComplete, NumTurns: 11}, "done"),
	)
	s := f.begin(t, Plan{RunID: "run-continue", Risk: RiskQ0, Tasks: []Task{
		supervisedTask("work", BandPrimary), docsTask(),
	}}, agent)

	event, err := s.Runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if event.Outcome != RunCompleted {
		t.Fatalf("outcome = %s (%s), want completed", event.Outcome, event.Reason)
	}
	if got := agent.count("work"); got != 3 {
		t.Fatalf("the task was driven %d time(s), want 3 — CONTINUE must re-offer it", got)
	}
	work, _ := s.Queue.Task("work")
	if work.State != TaskSucceeded {
		t.Fatalf("task state = %s, want succeeded", work.State)
	}
	// A CONTINUE is not a failure, so it must not have spent the retry budget.
	if work.AttemptCount() != 1 {
		t.Fatalf("attempts = %d, want 1 — a CONTINUE must not spend a retry", work.AttemptCount())
	}
	if work.Resumes != 2 {
		t.Fatalf("resumes = %d, want 2", work.Resumes)
	}
	records := journalKinds(t, f.stateDir)
	if n := countRecords(records, RecordTaskResumed, "work"); n != 2 {
		t.Fatalf("journal recorded %d resume(s), want 2", n)
	}
	if n := countRecords(records, RecordTaskFailed, "work"); n != 0 {
		t.Fatalf("journal recorded %d failure(s) for a task that never failed", n)
	}
	// The run's own tally must not report a healthy supervised agent as
	// something that went wrong repeatedly.
	if event.Attempts != 2 {
		t.Fatalf("the run reports %d attempt(s); it made one per task", event.Attempts)
	}
	if event.Resumes != 2 {
		t.Fatalf("the run reports %d resume(s), want 2", event.Resumes)
	}
	// And the resumes belong to the task that earned them. An unsupervised task
	// has no statement to make, so it can never be driven twice.
	if n := agent.count("docs"); n != 1 {
		t.Fatalf("the unsupervised task was driven %d time(s), want 1", n)
	}
	if n := countRecords(records, RecordTaskResumed, "docs"); n != 0 {
		t.Fatalf("an unsupervised task recorded %d resume(s)", n)
	}
}

// --- max_turns / error_max_turns --------------------------------------------

func TestATurnCapResumesRatherThanFailing(t *testing.T) {
	for name, cut := range map[string]ControllerResult{
		"terminal_reason=max_turns":  {State: StateFailed, TerminalReason: ReasonMaxTurns, IsError: true},
		"subtype=error_max_turns":    {State: StateFailed, Subtype: ReasonErrorMaxTurns, IsError: true},
		"max_turns beside CONTINUE":  {State: StateContinue, TerminalReason: ReasonMaxTurns},
		"error_max_turns on its own": {State: StateFailed, Subtype: ReasonErrorMaxTurns},
	} {
		t.Run(name, func(t *testing.T) {
			f := newControllerFixture(t)
			agent := newScriptedAgent().on("work",
				structured(false, cut, "the agent was stopped mid-sentence"),
				structured(true, ControllerResult{State: StateComplete}, "finished"),
			)
			s := f.begin(t, Plan{RunID: "run-turncap", Risk: RiskQ0, Tasks: []Task{
				supervisedTask("work", BandPrimary), docsTask(),
			}}, agent)

			event, err := s.Runner.Run(context.Background())
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if event.Outcome != RunCompleted {
				t.Fatalf("outcome = %s (%s), want completed — a turn cap is not a failure",
					event.Outcome, event.Reason)
			}
			work, _ := s.Queue.Task("work")
			if work.State != TaskSucceeded {
				t.Fatalf("task state = %s, want succeeded", work.State)
			}
			if work.AttemptCount() != 1 {
				t.Fatalf("attempts = %d, want 1 — a turn cap must not spend a retry", work.AttemptCount())
			}
			if work.Resumes != 1 {
				t.Fatalf("resumes = %d, want 1", work.Resumes)
			}
			records := journalKinds(t, f.stateDir)
			if n := countRecords(records, RecordTaskResumed, "work"); n != 1 {
				t.Fatalf("journal recorded %d resume(s), want 1", n)
			}
			if n := countRecords(records, RecordTaskFailed, "work"); n != 0 {
				t.Fatalf("a turn cap was journaled as %d failure(s)", n)
			}
		})
	}
}

func TestResumingIsBoundedAndAnUnconvergedTaskIsHeldNotFailed(t *testing.T) {
	// "Drive it again" with no limit is a run that never converges and never
	// says so. The bound is declared, and exhausting it holds the task: nothing
	// about the work was proved wrong, it simply did not finish.
	f := newControllerFixture(t)
	work := supervisedTask("work", BandPrimary)
	work.MaxResumes = 3
	agent := newScriptedAgent().on("work",
		structured(true, ControllerResult{State: StateContinue}, "still going"),
	)
	s := f.begin(t, Plan{RunID: "run-unbounded", Risk: RiskQ0, Tasks: []Task{work, docsTask()}}, agent)

	event, err := s.Runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := agent.count("work"); got != 4 {
		t.Fatalf("the task was driven %d time(s), want 4 — three resumes and the drive that earned them", got)
	}
	qt, _ := s.Queue.Task("work")
	if qt.State != TaskHeld {
		t.Fatalf("task state = %s, want held — it did not fail, it did not finish", qt.State)
	}
	if !strings.Contains(qt.HeldReason, "did not converge") {
		t.Fatalf("held reason = %q, must say the task did not converge", qt.HeldReason)
	}
	if event.Outcome != RunBlockedHuman {
		t.Fatalf("outcome = %s (%s), want blocked-human", event.Outcome, event.Reason)
	}
	if len(event.HumanActions) == 0 {
		t.Fatal("the completion event must say what a person has to decide")
	}
	// Work that did not depend on it must still have been done.
	docs, _ := s.Queue.Task("docs")
	if docs.State != TaskSucceeded {
		t.Fatalf("docs = %s, want succeeded — an unconverged task must not stop other work", docs.State)
	}
}

func TestAResumedRunDoesNotGetAFreshResumeBudget(t *testing.T) {
	// A run that forgot its resumes on restart would get a new budget every
	// crash and could drive one task forever, one crash at a time.
	f := newControllerFixture(t)
	work := supervisedTask("work", BandPrimary)
	work.MaxResumes = 2
	plan := Plan{RunID: "run-resume-budget", Risk: RiskQ0, Tasks: []Task{work, docsTask()}}

	first := f.begin(t, plan, newScriptedAgent().on("work",
		structured(true, ControllerResult{State: StateContinue}, "going")))
	if _, err := first.Runner.Run(context.Background()); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	records := journalKinds(t, f.stateDir)
	st := Replay(records, plan.RunID)
	// Two resumes were acted on and the third is the one that exceeded the
	// budget and held the task, so the journal records three.
	if st.Resumes["work"] != 3 {
		t.Fatalf("replayed resumes = %d, want 3", st.Resumes["work"])
	}
	// The attempts the journal shows must be the attempts the queue actually
	// spent. A resume's task-started record is not one.
	if st.Attempts["work"] != 0 {
		t.Fatalf("replayed attempts = %d, want 0 — a resume does not spend an attempt", st.Attempts["work"])
	}

	second := f.begin(t, plan, newScriptedAgent().on("work",
		structured(true, ControllerResult{State: StateContinue}, "going")))
	qt, _ := second.Queue.Task("work")
	if qt.Resumes != 3 {
		t.Fatalf("the resumed run restored %d resume(s), want 3", qt.Resumes)
	}
	if _, _, mayResume := second.Queue.RecordResume(qt); mayResume {
		t.Fatal("the resumed run was granted a fresh resume budget")
	}
}

// --- bounded network timeout ------------------------------------------------

func TestABoundedNetworkTimeoutRetriesWithinItsPolicyAndStopsThere(t *testing.T) {
	f := newControllerFixture(t)
	timeout := ControllerResult{State: StateFailed, TerminalReason: ReasonNetworkTimeout}
	agent := newScriptedAgent().on("work", structured(false, timeout, "dial tcp: i/o timeout"))
	s := f.begin(t, Plan{RunID: "run-timeout", Risk: RiskQ0, Tasks: []Task{
		supervisedTask("work", BandPrimary), docsTask(),
	}}, agent)

	event, err := s.Runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	budget := PolicyFor(FailureExternalService).MaxAttempts
	if got := agent.count("work"); got != budget {
		t.Fatalf("the task was attempted %d time(s), want the external-service budget of %d", got, budget)
	}
	work, _ := s.Queue.Task("work")
	if work.State != TaskFailed {
		t.Fatalf("task state = %s, want failed once the bounded retry is spent", work.State)
	}
	if work.Attempts[0].Class != FailureExternalService {
		t.Fatalf("class = %s, want external-service", work.Attempts[0].Class)
	}
	if work.Resumes != 0 {
		t.Fatalf("a timeout consumed %d resume(s); it is a retry, not a resume", work.Resumes)
	}
	// Bounded means the run ends rather than retrying forever, and other work
	// still gets done.
	if event.Outcome != RunFailed {
		t.Fatalf("outcome = %s, want failed", event.Outcome)
	}
	docs, _ := s.Queue.Task("docs")
	if docs.State != TaskSucceeded {
		t.Fatalf("docs = %s, want succeeded", docs.State)
	}
}

// --- authentication and HUMAN_BLOCKED ---------------------------------------

func TestAnAuthenticationFailureStopsTheRunAwaitingAuth(t *testing.T) {
	f := newControllerFixture(t)
	agent := newScriptedAgent().on("work", structured(false, ControllerResult{
		State: StateFailed, TerminalReason: ReasonAuthenticationFailed,
		Detail: "the forge refused the credential",
	}, "HTTP 401"))
	s := f.begin(t, Plan{RunID: "run-auth", Risk: RiskQ0, Tasks: []Task{
		supervisedTask("work", BandPrimary), docsTask(),
	}}, agent)

	event, err := s.Runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if event.Outcome != RunAwaitingAuth {
		t.Fatalf("outcome = %s (%s), want awaiting-auth", event.Outcome, event.Reason)
	}
	if got := agent.count("work"); got != 1 {
		t.Fatalf("an authentication failure was attempted %d time(s); a credential does not become valid by being asked again", got)
	}
	work, _ := s.Queue.Task("work")
	if work.State != TaskHeld {
		t.Fatalf("task state = %s, want held", work.State)
	}
	if len(event.HumanActions) == 0 {
		t.Fatal("the completion event must say what a person has to do")
	}
}

func TestAnExplicitHumanBlockStopsSafelyAndDistinctly(t *testing.T) {
	f := newControllerFixture(t)
	agent := newScriptedAgent().on("work", structured(true, ControllerResult{
		State: StateHumanBlocked, Detail: "replacing a machine-wide supervisor is its owner's decision",
	}, ""))
	s := f.begin(t, Plan{RunID: "run-blocked", Risk: RiskQ0, Tasks: []Task{
		supervisedTask("work", BandPrimary), docsTask(),
	}}, agent)

	event, err := s.Runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if event.Outcome != RunBlockedHuman {
		t.Fatalf("outcome = %s (%s), want blocked-human — a person's decision is not a failed run",
			event.Outcome, event.Reason)
	}
	if event.Outcome == RunAwaitingAuth {
		t.Fatal("a judgement boundary must not be reported as an authentication boundary")
	}
	work, _ := s.Queue.Task("work")
	if work.State != TaskHeld {
		t.Fatalf("task state = %s, want held", work.State)
	}
	if !strings.Contains(strings.Join(event.HumanActions, " "), "owner's decision") {
		t.Fatalf("the human action must carry the block's own words: %v", event.HumanActions)
	}
	// A safe stop is a STOP: the run must not have gone on to other work after
	// crossing a boundary it may not cross.
	docs, _ := s.Queue.Task("docs")
	if docs.State == TaskSucceeded {
		t.Fatal("the run continued past a human boundary instead of stopping safely")
	}
}

func TestAHumanBoundaryStopIsRecordedBeforeTheRunEnds(t *testing.T) {
	// Failing safe is only worth anything if the evidence survives.
	f := newControllerFixture(t)
	agent := newScriptedAgent().on("work", structured(true, ControllerResult{
		State: StateHumanBlocked, Detail: "a person must approve the production change",
	}, "the runtime refused: approval required"))
	s := f.begin(t, Plan{RunID: "run-blocked-evidence", Risk: RiskQ0, Tasks: []Task{
		supervisedTask("work", BandPrimary), docsTask(),
	}}, agent)

	if _, err := s.Runner.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := countRecords(journalKinds(t, f.stateDir), RecordTaskHeld, "work"); n != 1 {
		t.Fatalf("journal recorded %d held record(s), want 1", n)
	}
	captured := filepath.Join(f.stateDir, FailuresDirName, "work-attempt-1.log")
	body, err := os.ReadFile(captured)
	if err != nil {
		t.Fatalf("the blocked attempt's output was not captured: %v", err)
	}
	if !strings.Contains(string(body), "approval required") {
		t.Fatalf("the captured output lost what the runtime actually said:\n%s", body)
	}
}

// --- malformed and missing results ------------------------------------------

func TestAMalformedResultFailsSafeAndPreservesTheEvidence(t *testing.T) {
	// The task exited zero. Believing that is precisely the defect.
	f := newControllerFixture(t)
	agent := newScriptedAgent().on("work", Execution{
		ExitedZero: true, DeclaredResult: true,
		Output:    "the harness crashed after printing half a document",
		ResultErr: errors.New("unattended: the task's structured controller result is unusable: it is not readable JSON"),
	})
	work := supervisedTask("work", BandPrimary)
	work.QAGate = GateUnitTest
	s := f.begin(t, Plan{RunID: "run-malformed", Risk: RiskQ0, Tasks: []Task{work, docsTask()}}, agent)

	event, err := s.Runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	qt, _ := s.Queue.Task("work")
	if qt.State == TaskSucceeded {
		t.Fatal("a supervised task that said nothing was recorded as a success on the strength of its exit status")
	}
	if qt.State != TaskFailed {
		t.Fatalf("task state = %s, want failed", qt.State)
	}
	if event.Outcome != RunFailed {
		t.Fatalf("outcome = %s, want failed", event.Outcome)
	}
	// The gate ledger must record an absence of knowledge, which blocks exactly
	// as a missing gate does — never a pass.
	ev, ok := s.Runner.Evidence[GateUnitTest]
	if !ok {
		t.Fatal("no gate evidence was recorded for a gate task that ran")
	}
	if ev.Result != GateError {
		t.Fatalf("gate result = %s, want error", ev.Result)
	}
	if ev.Certifies(s.Fence.Head) {
		t.Fatal("an unreadable result certified the revision")
	}
	// The evidence is preserved where a person can read it.
	if _, err := os.Stat(filepath.Join(f.stateDir, FailuresDirName, "work-attempt-1.log")); err != nil {
		t.Fatalf("the failing attempt's output was not preserved: %v", err)
	}
	if n := countRecords(journalKinds(t, f.stateDir), RecordGateEvidence, "work"); n == 0 {
		t.Fatal("the gate verdict was not journaled")
	}
}

func TestAMissingResultIsNotASuccessNoMatterWhatTheProcessSaid(t *testing.T) {
	f := newControllerFixture(t)
	agent := newScriptedAgent().on("work", Execution{
		ExitedZero: true, DeclaredResult: true,
		ResultErr: ErrControllerResultUnusable,
	})
	s := f.begin(t, Plan{RunID: "run-missing", Risk: RiskQ0, Tasks: []Task{
		supervisedTask("work", BandPrimary), docsTask(),
	}}, agent)

	event, err := s.Runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if event.Outcome == RunCompleted {
		t.Fatal("a run whose only task said nothing about itself reported completion")
	}
}

// --- COMPLETE and the QA ceiling --------------------------------------------

func TestCOMPLETEDoesNotLicenseProgressionWithoutGateEvidence(t *testing.T) {
	// A task reporting COMPLETE is a claim about the task. Whether the packet
	// may progress is decided from gate evidence bound to the revision in hand,
	// and no state in the controller vocabulary substitutes for it.
	f := newControllerFixture(t)
	build := supervisedTask("build", BandPrimary)
	build.QAGate = GateBuild
	tests := supervisedTask("tests", BandValidation)
	tests.QAGate = GateUnitTest

	agent := newScriptedAgent().
		on("build", structured(true, ControllerResult{State: StateComplete}, "built")).
		on("tests", structured(false, ControllerResult{State: StateFailed, TerminalReason: "assertion"}, "--- FAIL: TestThing"))

	s := f.begin(t, Plan{RunID: "run-complete-q1", Risk: RiskQ1, Tasks: []Task{build, tests}}, agent)
	event, err := s.Runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if event.QA.Allowed {
		t.Fatalf("progression was permitted with a failed mandatory gate: %s", event.QA.Reason())
	}
	if event.Outcome == RunCompleted {
		t.Fatal("a run whose mandatory unit-test gate failed reported completion")
	}
	buildTask, _ := s.Queue.Task("build")
	if buildTask.State != TaskSucceeded {
		t.Fatalf("the COMPLETE task = %s, want succeeded — its own claim is honored", buildTask.State)
	}
	if ev := s.Runner.Evidence[GateBuild]; !ev.Certifies(s.Fence.Head) {
		t.Fatalf("the build gate did not certify the revision it ran against: %+v", ev)
	}
}

func TestCOMPLETESucceedsWhenEveryMandatoryGateHasPassingEvidence(t *testing.T) {
	f := newControllerFixture(t)
	build := supervisedTask("build", BandPrimary)
	build.QAGate = GateBuild
	tests := supervisedTask("tests", BandValidation)
	tests.QAGate = GateUnitTest

	agent := newScriptedAgent().
		on("build", structured(true, ControllerResult{State: StateComplete}, "built")).
		on("tests", structured(true, ControllerResult{State: StateComplete}, "11 passed"))

	s := f.begin(t, Plan{RunID: "run-complete-ok", Risk: RiskQ1, Tasks: []Task{build, tests}}, agent)
	event, err := s.Runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !event.QA.Allowed {
		t.Fatalf("progression was refused with every mandatory gate passing: %s", event.QA.Reason())
	}
	if event.Outcome != RunCompleted {
		t.Fatalf("outcome = %s (%s), want completed", event.Outcome, event.Reason)
	}
}

// --- duplicate writer and dirty worktree ------------------------------------

func TestADuplicateWriterIsRejectedBeforeAnyMutation(t *testing.T) {
	// Two supervisors over one worktree is the failure this layer exists to
	// prevent, and the rejection has to land before the second one does
	// anything at all.
	f := newControllerFixture(t)
	plan := func(runID string) Plan {
		return Plan{RunID: runID, Risk: RiskQ0, Tasks: []Task{
			{ID: "mutate", Title: "mutate", Band: BandPrimary, Mutates: true, Argv: []string{"true"}},
			docsTask(),
		}}
	}
	held := f.begin(t, plan("run-first"), newScriptedAgent())
	if held.Lock == nil {
		t.Fatal("the first run did not take the writer lock")
	}

	second := f.spec
	second.Ownership.Session = "a-second-supervisor"
	dup, err := Begin(context.Background(), second, plan("run-second"))
	if err == nil {
		t.Fatal("a second writer was admitted to a worktree a run already holds")
	}
	if dup != nil {
		t.Fatal("a refused session must not be returned")
	}
	// Preflight sees the recorded owner first; the OS lock is what makes it
	// true. Either refusal is correct and both are before any mutation.
	if !errors.Is(err, ErrWriterHeld) && !errors.Is(err, ErrNotReady) {
		t.Fatalf("refusal = %v, want ErrWriterHeld or ErrNotReady", err)
	}
	owner, found, oerr := ReadOwner(WriterLockDir(held.Report.Repo))
	if oerr != nil || !found {
		t.Fatalf("owner evidence: found=%v err=%v", found, oerr)
	}
	if owner.RunID != "run-first" {
		t.Fatalf("the recorded owner is %q; the refused run took the claim", owner.RunID)
	}
}

func TestADirtyWorktreeIsRejectedBeforeAnyMutation(t *testing.T) {
	// A writer that starts on top of somebody's uncommitted work cannot tell
	// its own change from theirs afterwards, and the fence it takes fences a
	// position that was never clean.
	f := newControllerFixture(t)
	writeRepoFile(t, f.repo, "in-progress.txt", "a person was in the middle of this\n")

	_, err := Begin(context.Background(), f.spec, Plan{RunID: "run-dirty", Risk: RiskQ0, Tasks: []Task{
		{ID: "mutate", Title: "mutate", Band: BandPrimary, Mutates: true, Argv: []string{"true"}},
		docsTask(),
	}})
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("Begin on a dirty worktree = %v, want ErrNotReady", err)
	}
	if !strings.Contains(err.Error(), "clean") {
		t.Fatalf("the refusal must name the cleanliness check: %v", err)
	}
	// The refusal is durable, and the person's work is untouched.
	if _, rerr := os.Stat(stateDirPath(f.stateDir, PreflightReportName)); rerr != nil {
		t.Fatal("a refused run must still leave the reason it was refused")
	}
	body, rerr := os.ReadFile(filepath.Join(f.repo, "in-progress.txt"))
	if rerr != nil || !strings.Contains(string(body), "in the middle") {
		t.Fatal("the refused run disturbed the uncommitted work it refused to start on top of")
	}
	// And nothing claimed the worktree.
	state, perr := ProbeRepo(f.repo)
	if perr != nil {
		t.Fatalf("ProbeRepo: %v", perr)
	}
	if _, found, oerr := ReadOwner(WriterLockDir(state)); oerr == nil && found {
		t.Fatal("a run refused at preflight still claimed the worktree")
	}
}

func TestADeclaredDirtyWorktreeIsStillAdmitted(t *testing.T) {
	// The rejection is a default, not a prohibition: a run whose dirt is
	// expected declares it, and the dirt is still enumerated in the report.
	f := newControllerFixture(t)
	writeRepoFile(t, f.repo, "expected.txt", "this run expects to find this\n")
	f.spec.Ownership.AllowDirtyWorktree = true

	s := f.begin(t, Plan{RunID: "run-dirty-declared", Risk: RiskQ0, Tasks: []Task{
		supervisedTask("work", BandPrimary), docsTask(),
	}}, newScriptedAgent().on("work", structured(true, ControllerResult{State: StateComplete}, "")))
	check, ok := s.Report.Check("repository.clean")
	if !ok {
		t.Fatal("the report must still carry the cleanliness check")
	}
	if check.Outcome != OutcomePass {
		t.Fatalf("declared dirt reported %s, want pass", check.Outcome)
	}
	if !strings.Contains(check.Detail, "expected.txt") {
		t.Fatalf("the dirt must still be enumerated: %q", check.Detail)
	}
}
