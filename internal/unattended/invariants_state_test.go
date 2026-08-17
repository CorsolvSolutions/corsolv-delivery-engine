package unattended

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// QA-003 — CRITICAL DELIVERY INVARIANTS, part 2 of 2: TERMINAL STATE INTEGRITY,
// BLOCKED STATE INTEGRITY, WRITER EXCLUSIVITY and BOUNDED CONTINUATION.
//
// Part 1 proves the invariants that are properties of pure folds. These are the
// ones that are properties of a SEQUENCE OF SESSIONS: what a second run does to
// what a first run established, what a restart does to a bound the first run
// was already keeping, and what two sessions do to each other. Each is driven
// through the real Begin, the real lock, the real fence, the real journal and
// the real Run loop, because every one of them is a claim about what survives a
// process ending — and a stand-in for a process ending survives nothing.
//
// The agent is scripted rather than shelled out to, so a run reaches an exact
// terminal state deterministically and on every platform this engine is hosted
// on. See controller_runner_test.go for why that matters.

// --- shared scripted outcomes ------------------------------------------------

func execCompletes() Execution {
	return Execution{
		DeclaredResult: true, ExitedZero: true,
		Result: &ControllerResult{State: StateComplete, Detail: "the work is done"},
	}
}

func execContinues() Execution {
	return Execution{
		DeclaredResult: true, ExitedZero: true,
		Result: &ControllerResult{State: StateContinue, Detail: "more to do"},
	}
}

func execHumanBlocked(reason string) Execution {
	return Execution{
		DeclaredResult: true, ExitedZero: false,
		Result: &ControllerResult{State: StateHumanBlocked, TerminalReason: reason, Detail: "a person must act"},
	}
}

func execFailsWithReason(reason string) Execution {
	return Execution{
		DeclaredResult: true, ExitedZero: false,
		Result: &ControllerResult{State: StateFailed, TerminalReason: reason, Detail: "it did not work"},
	}
}

// runToTerminal drives one session of a plan to its terminal record and closes
// it, exactly as a real run does.
func runToTerminal(t *testing.T, f *controllerFixture, plan Plan, observe func(context.Context, Task) Execution) CompletionEvent {
	t.Helper()
	s, err := Begin(context.Background(), f.spec, plan)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	s.Runner.Sleep = func(context.Context, time.Duration) {}
	if observe == nil {
		// Ordinary unsupervised work that succeeded. Scripted rather than
		// executed so a terminal state is reached identically on every platform.
		observe = func(context.Context, Task) Execution { return Execution{ExitedZero: true} }
	}
	s.Runner.Observe = observe
	event, err := s.Runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return event
}

func readFileOrFail(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return data
}

// --- INVARIANT 1: TERMINAL STATE INTEGRITY -----------------------------------

func TestACompletedRunCannotBeReturnedToAnActiveState(t *testing.T) {
	// INVARIANT 1. A run that recorded a COMPLETED terminal state is history.
	// Another observation of it — a supervisor restarting the engine, an
	// operator re-issuing the same command, a replay — must not reopen it.
	//
	// THE DEFECT THIS EXISTS FOR. Re-entering a completed run's id republished
	// the heartbeat under that id with stage "starting", re-evaluated the
	// packet's gates against whatever revision was in hand by then, and
	// OVERWROTE the terminal record with a new finishedAt. The completed run
	// went back to running, and the historical account of it was rewritten by a
	// run that did no work.
	f := newControllerFixture(t)
	plan := Plan{RunID: "sealed-run", Risk: RiskQ0, Tasks: []Task{
		{
			ID: "deliver", Title: "deliver", Band: BandPrimary, Argv: []string{"deliver"},
			DeliveryStatus: "complete", CompletionGate: "the delivery is accepted",
		},
		docsTask(),
	}}

	event := runToTerminal(t, f, plan, nil)
	if event.Outcome != RunCompleted {
		t.Fatalf("the premise is wrong: outcome = %s (%s)", event.Outcome, event.Reason)
	}

	completionPath := stateDirPath(f.stateDir, CompletionName)
	heartbeatPath := stateDirPath(f.stateDir, HeartbeatName)
	completionBefore := readFileOrFail(t, completionPath)
	heartbeatBefore := readFileOrFail(t, heartbeatPath)
	journalBefore := readFileOrFail(t, stateDirPath(f.stateDir, JournalName))

	second, err := Begin(context.Background(), f.spec, plan)
	if err == nil {
		second.Close() //nolint:errcheck
		t.Fatal("a run that recorded a completed terminal state was reopened under the same id")
	}
	if !errors.Is(err, ErrRunAlreadyCompleted) {
		t.Fatalf("reopening a completed run failed with %v, want ErrRunAlreadyCompleted", err)
	}
	// The refusal must say which run and what it recorded; a reader whose
	// supervisor just refused to start needs to know it refused because the
	// work is done rather than because something is broken.
	if !containsAll(err.Error(), "sealed-run", string(RunCompleted)) {
		t.Fatalf("the refusal does not identify the completed run: %v", err)
	}

	// And nothing about the historical run may have moved.
	if got := readFileOrFail(t, completionPath); string(got) != string(completionBefore) {
		t.Fatalf("the terminal record was rewritten by a refused re-entry:\nwas:\n%s\nnow:\n%s", completionBefore, got)
	}
	if got := readFileOrFail(t, heartbeatPath); string(got) != string(heartbeatBefore) {
		t.Fatalf("the heartbeat of a completed run was republished by a refused re-entry:\nwas:\n%s\nnow:\n%s", heartbeatBefore, got)
	}
	if got := readFileOrFail(t, stateDirPath(f.stateDir, JournalName)); string(got) != string(journalBefore) {
		t.Fatal("a refused re-entry appended to the completed run's durable journal")
	}
}

func TestNoNumberOfReEntriesMutatesAHistoricalCompletedRun(t *testing.T) {
	// INVARIANT 1, over a sequence rather than one example. The plan a
	// re-entry arrives with is not always the plan that completed: a supervisor
	// may add work, drop work, or re-declare risk. None of those makes the
	// historical run active again.
	f := newControllerFixture(t)
	base := []Task{
		{
			ID: "deliver", Title: "deliver", Band: BandPrimary, Argv: []string{"deliver"},
			DeliveryStatus: "complete", CompletionGate: "the delivery is accepted",
		},
		docsTask(),
	}
	plan := Plan{RunID: "sealed-many", Risk: RiskQ0, Tasks: base}

	if event := runToTerminal(t, f, plan, nil); event.Outcome != RunCompleted {
		t.Fatalf("the premise is wrong: %s (%s)", event.Outcome, event.Reason)
	}
	completionBefore := readFileOrFail(t, stateDirPath(f.stateDir, CompletionName))

	variants := []Plan{
		// The same plan again — a supervisor simply restarting.
		plan,
		// A plan that grew work after the run had already finished.
		{RunID: "sealed-many", Risk: RiskQ0, Tasks: append(append([]Task{}, base...),
			Task{ID: "extra", Title: "extra", Band: BandValidation, Argv: []string{"extra"}})},
		// The same work declared in a different order.
		{RunID: "sealed-many", Risk: RiskQ0, Tasks: []Task{base[1], base[0]}},
		// The same work re-declared at a higher risk class, carrying the gate
		// tasks that class makes mandatory. The packet is well formed, so the
		// seal — not packet validation — is what has to refuse it: otherwise a
		// completed run could be reopened by raising its risk and re-running
		// gates against a revision the completed delivery never saw.
		{RunID: "sealed-many", Risk: RiskQ1, Tasks: append(append([]Task{}, base...),
			Task{ID: "build", Title: "build", Band: BandValidation, Argv: []string{"build"}, QAGate: GateBuild},
			Task{ID: "unit", Title: "unit", Band: BandValidation, Argv: []string{"unit"}, QAGate: GateUnitTest})},
	}
	for i, v := range variants {
		s, err := Begin(context.Background(), f.spec, v)
		if err == nil {
			s.Close() //nolint:errcheck
			t.Fatalf("variant %d reopened a completed run", i)
		}
		if !errors.Is(err, ErrRunAlreadyCompleted) {
			t.Fatalf("variant %d refused with %v, want ErrRunAlreadyCompleted", i, err)
		}
	}
	if got := readFileOrFail(t, stateDirPath(f.stateDir, CompletionName)); string(got) != string(completionBefore) {
		t.Fatal("the historical terminal record moved across a sequence of refused re-entries")
	}

	// A NEW run over the same state directory is a different run and is not
	// refused. Sealing the completed one must not seal the project.
	fresh := Plan{RunID: "sealed-many-2", Risk: RiskQ0, Tasks: base}
	if event := runToTerminal(t, f, fresh, nil); event.Outcome != RunCompleted {
		t.Fatalf("a new run over a state directory holding a completed run was refused: %s (%s)", event.Outcome, event.Reason)
	}
}

func TestOnlyACompletedRunIsSealedSoRecoveryStillWorks(t *testing.T) {
	// INVARIANT 1, in the direction that keeps the engine useful. The seal is
	// over the COMPLETED terminal state alone. A run that stopped because a
	// person must act, or because work failed, is exactly the run a resume
	// exists for — sealing those would mean every human boundary permanently
	// retired its run id.
	for _, tc := range []struct {
		name    string
		observe func(context.Context, Task) Execution
		want    RunOutcome
	}{
		{
			name: "blocked on a human judgement",
			observe: func(_ context.Context, task Task) Execution {
				if task.ID == "deliver" {
					return execHumanBlocked("")
				}
				return Execution{ExitedZero: true}
			},
			want: RunBlockedHuman,
		},
		{
			name: "awaiting an authentication",
			observe: func(_ context.Context, task Task) Execution {
				if task.ID == "deliver" {
					return execHumanBlocked(ReasonAuthenticationFailed)
				}
				return Execution{ExitedZero: true}
			},
			want: RunAwaitingAuth,
		},
		{
			name: "refused by a permission the runtime enforces",
			observe: func(_ context.Context, task Task) Execution {
				if task.ID == "deliver" {
					return execFailsWithReason(ReasonPermissionDenied)
				}
				return Execution{ExitedZero: true}
			},
			want: RunAwaitingAuth,
		},
		{
			name: "failed on work that exhausted its attempts",
			observe: func(_ context.Context, task Task) Execution {
				if task.ID == "deliver" {
					return execFailsWithReason("")
				}
				return Execution{ExitedZero: true}
			},
			want: RunFailed,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newControllerFixture(t)
			deliver := supervisedTask("deliver", BandPrimary)
			deliver.MaxAttempts = 1
			plan := Plan{RunID: "recoverable", Risk: RiskQ0, Tasks: []Task{deliver, docsTask()}}

			event := runToTerminal(t, f, plan, tc.observe)
			if event.Outcome != tc.want {
				t.Fatalf("outcome = %s (%s), want %s", event.Outcome, event.Reason, tc.want)
			}

			// The run recorded a terminal state that is NOT completed, so a
			// resume must be admitted.
			s, err := Begin(context.Background(), f.spec, plan)
			if err != nil {
				t.Fatalf("a %s run could not be resumed: %v — recovery is what resume is for", tc.want, err)
			}
			defer s.Close() //nolint:errcheck
			if !s.Resumed {
				t.Fatal("the resumed session does not know it continued an existing journal")
			}
		})
	}
}

func TestReplayNeverUnfinishesAFinishedRun(t *testing.T) {
	// INVARIANT 1, as a property of the fold underneath it. Once a run's own
	// terminal record is durable, no longer prefix of the journal may report
	// the run as unfinished — that is the monotonicity the seal is built on.
	f := newControllerFixture(t)
	plan := Plan{RunID: "monotone", Risk: RiskQ0, Tasks: []Task{
		{ID: "deliver", Title: "deliver", Band: BandPrimary, Argv: []string{"deliver"}},
		docsTask(),
	}}
	if event := runToTerminal(t, f, plan, nil); event.Outcome != RunCompleted {
		t.Fatalf("the premise is wrong: %s", event.Outcome)
	}

	records, _, err := ReadJournal(stateDirPath(f.stateDir, JournalName))
	if err != nil {
		t.Fatalf("ReadJournal: %v", err)
	}
	if len(records) < 5 {
		t.Fatalf("the run left only %d records; the property would be vacuous", len(records))
	}

	finishedAt := -1
	for i := 1; i <= len(records); i++ {
		st := Replay(records[:i], "monotone")
		switch {
		case st.Finished && finishedAt < 0:
			finishedAt = i
			if st.FinalOutcome != RunCompleted {
				t.Fatalf("prefix %d reports finished with outcome %q, want %s", i, st.FinalOutcome, RunCompleted)
			}
		case finishedAt >= 0 && !st.Finished:
			t.Fatalf("prefix %d unfinished a run that prefix %d had already finished", i, finishedAt)
		case finishedAt >= 0 && st.FinalOutcome != RunCompleted:
			t.Fatalf("prefix %d changed the recorded terminal outcome to %q", i, st.FinalOutcome)
		}
	}
	if finishedAt < 0 {
		t.Fatal("no prefix of a completed run's journal reports it finished")
	}
	// A different run's records never finish this one.
	if Replay(records, "some-other-run").Finished {
		t.Fatal("one run's terminal record finished a different run")
	}
}

// --- INVARIANT 3: BLOCKED STATE INTEGRITY ------------------------------------

func TestABlockedRunNeverProjectsCompletionUnderAnyOrdering(t *testing.T) {
	// INVARIANT 3. A run held behind a human boundary — a judgement, an
	// authentication, a permission the runtime refuses — cannot progress, merge
	// or project complete. The blocking task is proved held whatever order the
	// rest of the plan ran in, because a boundary reached after other work
	// succeeded is the ordinary case, not the exception.
	for _, tc := range []struct {
		name    string
		outcome Execution
		want    RunOutcome
	}{
		{"human judgement", execHumanBlocked(""), RunBlockedHuman},
		{"authentication", execHumanBlocked(ReasonAuthenticationFailed), RunAwaitingAuth},
		{"permission denied", execFailsWithReason(ReasonPermissionDenied), RunAwaitingAuth},
	} {
		for _, blockedFirst := range []bool{true, false} {
			name := fmt.Sprintf("%s/blocked-task-%s", tc.name, map[bool]string{true: "first", false: "last"}[blockedFirst])
			t.Run(name, func(t *testing.T) {
				f := newControllerFixture(t)
				blocked := supervisedTask("gated", BandPrimary)
				blocked.MaxAttempts = 1
				blocked.DeliveryStatus = "complete"
				blocked.CompletionGate = "the change is accepted"
				// The sibling's declared status is deliberately NOT terminal.
				// It is the real shape of the boundary this engine keeps
				// hitting: a change is prepared and its PR is open, and the
				// step that would finish it is the one a person must take. A
				// sibling that declared a terminal status of its own would
				// make the assertion below about the fixture rather than about
				// the run.
				other := Task{
					ID: "other", Title: "other", Band: BandPrimary, Argv: []string{"other"},
					DeliveryStatus: "pr-open", CompletionGate: "an approving review",
				}

				tasks := []Task{blocked, other, docsTask()}
				if !blockedFirst {
					tasks = []Task{other, blocked, docsTask()}
				}
				spec := f.spec
				spec.PublishPath = filepath.Join(t.TempDir(), "delivery", "PROJECT-STATE.yml")

				s, err := Begin(context.Background(), spec, Plan{RunID: "blocked-run", Risk: RiskQ0, Tasks: tasks})
				if err != nil {
					t.Fatalf("Begin: %v", err)
				}
				defer s.Close() //nolint:errcheck
				s.Runner.Sleep = func(context.Context, time.Duration) {}
				s.Runner.Observe = func(_ context.Context, task Task) Execution {
					if task.ID == "gated" {
						return tc.outcome
					}
					return Execution{ExitedZero: true}
				}

				event, err := s.Runner.Run(context.Background())
				if err != nil {
					t.Fatalf("Run: %v", err)
				}
				if event.Outcome != tc.want {
					t.Fatalf("outcome = %s (%s), want %s", event.Outcome, event.Reason, tc.want)
				}
				if event.Outcome == RunCompleted {
					t.Fatal("a run stopped at a human boundary reported itself completed")
				}
				gated, _ := s.Queue.Task("gated")
				if gated.State != TaskHeld {
					t.Fatalf("the blocked task is %s, want held", gated.State)
				}
				if gated.HeldReason == "" {
					t.Fatal("a held task must say what would have to change")
				}

				// The projection is the document a reader treats as the answer.
				body := string(readFileOrFail(t, spec.PublishPath))
				if !containsAll(body, `status: "awaiting-human-action"`) {
					t.Fatalf("the blocked task is not projected as awaiting a human:\n%s", body)
				}
				for _, terminal := range []string{"merged", "deployed-uat", "applied-uat", "verified", "complete"} {
					if containsAll(body, `status: "`+terminal+`"`) {
						t.Fatalf("a run held at a human boundary projected terminal status %q:\n%s", terminal, body)
					}
				}
				// The run's own account and the projection must agree that a
				// person is owed an action.
				if len(event.HumanActions) == 0 {
					t.Fatal("a blocked run named no human action")
				}
			})
		}
	}
}

func TestABlockedGateTaskCannotCertifyThePacketItNeverExamined(t *testing.T) {
	// INVARIANT 3 meeting INVARIANT 2. When the task that would produce a
	// MANDATORY gate is the one behind the boundary, the gate has not run — and
	// a gate that has not run is an absence of knowledge, not a pass. The packet
	// must be refused and the projection must carry the refusal, whichever of
	// the packet's gates the boundary happened to land on.
	for _, blockedGate := range []string{GateBuild, GateUnitTest, GateStaticAnalysis, GateControlSafety} {
		t.Run(blockedGate, func(t *testing.T) {
			f := newControllerFixture(t)
			spec := f.spec
			spec.PublishPath = filepath.Join(t.TempDir(), "delivery", "PROJECT-STATE.yml")

			tasks := []Task{{
				ID: "deliver", Title: "deliver", Band: BandPrimary, Argv: []string{"deliver"},
				DeliveryStatus: "complete", CompletionGate: "every mandatory gate passes",
			}}
			for _, gate := range RequiredGates(QAPolicy{}, RiskQ3) {
				gt := supervisedTask("gate-"+gate, BandValidation)
				gt.QAGate = gate
				gt.MaxAttempts = 1
				tasks = append(tasks, gt)
			}
			tasks = append(tasks, docsTask())

			s, err := Begin(context.Background(), spec, Plan{RunID: "blocked-gate", Risk: RiskQ3, Tasks: tasks})
			if err != nil {
				t.Fatalf("Begin: %v", err)
			}
			defer s.Close() //nolint:errcheck
			s.Runner.Sleep = func(context.Context, time.Duration) {}
			s.Runner.Observe = func(_ context.Context, task Task) Execution {
				if task.ID == "gate-"+blockedGate {
					return execHumanBlocked("")
				}
				return execCompletes()
			}

			event, err := s.Runner.Run(context.Background())
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if event.QA.Allowed {
				t.Fatalf("a packet whose %s gate never ran was permitted to progress: %s", blockedGate, event.QA.Reason())
			}
			if !containsAll(event.QA.Reason(), blockedGate) {
				t.Fatalf("the refusal does not name the gate behind the boundary: %s", event.QA.Reason())
			}
			if event.Outcome == RunCompleted {
				t.Fatal("a run whose mandatory gate is behind a human boundary reported itself completed")
			}

			body := string(readFileOrFail(t, spec.PublishPath))
			for _, terminal := range []string{"merged", "deployed-uat", "applied-uat", "verified", "complete"} {
				if containsAll(body, `status: "`+terminal+`"`) {
					t.Fatalf("a packet blocked on its %s gate projected terminal status %q:\n%s", blockedGate, terminal, body)
				}
			}
			if containsAll(body, `completionGateStatus: "met"`) {
				t.Fatalf("a packet blocked on its %s gate met a completion gate:\n%s", blockedGate, body)
			}
		})
	}
}

func TestABlockedRunProgressesOnlyThroughTheLegitimateTransition(t *testing.T) {
	// INVARIANT 3, as a sequence. A blocked run may be restarted as often as a
	// supervisor likes and stays blocked; it reaches completion only when the
	// blocking condition is actually resolved and the task says so through the
	// legitimate transition. Restarting is not a way through a boundary.
	f := newControllerFixture(t)
	deliver := supervisedTask("deliver", BandPrimary)
	deliver.MaxAttempts = 1
	deliver.DeliveryStatus = "complete"
	deliver.CompletionGate = "the change is accepted"
	plan := Plan{RunID: "boundary", Risk: RiskQ0, Tasks: []Task{deliver, docsTask()}}

	blocked := true
	observe := func(_ context.Context, task Task) Execution {
		if task.ID != "deliver" {
			return Execution{ExitedZero: true}
		}
		if blocked {
			return execHumanBlocked("")
		}
		return execCompletes()
	}

	for restart := 0; restart < 4; restart++ {
		event := runToTerminal(t, f, plan, observe)
		if event.Outcome != RunBlockedHuman {
			t.Fatalf("restart %d: a restart moved a blocked run to %s (%s)", restart, event.Outcome, event.Reason)
		}
		if event.Tasks[TaskHeld] == 0 {
			t.Fatalf("restart %d: the blocked run holds nothing", restart)
		}
	}

	// The person acts. Only now may the run complete.
	blocked = false
	event := runToTerminal(t, f, plan, observe)
	if event.Outcome != RunCompleted {
		t.Fatalf("after the boundary was resolved the run reported %s (%s)", event.Outcome, event.Reason)
	}
}

// --- INVARIANT 4: WRITER EXCLUSIVITY -----------------------------------------

// leaseOp is one generated operation against a worktree lease.
type leaseOp struct {
	actor int
	role  Role
	// release is true when the actor gives up whatever it holds.
	release bool
}

func TestNoInterleavingOfLeaseOperationsAdmitsTwoWriters(t *testing.T) {
	// INVARIANT 4. At most one active writer owns a worktree at a time, under
	// every interleaving of acquisition and release — not merely the two-session
	// example. Duplicate acquisition must fail before the lock is granted, and
	// the failure must be the one a caller can distinguish.
	const (
		actors    = 4
		sequences = 60
		steps     = 24
	)
	roles := []Role{RoleWriter, RoleController, RoleReadOnly}
	var acquired, denied int

	for seed := uint64(0); seed < sequences; seed++ {
		p := newPRNG(seed)
		dir := filepath.Join(t.TempDir(), "lease")
		held := map[int]*Lock{}

		for step := 0; step < steps; step++ {
			op := leaseOp{actor: p.intn(actors), role: roles[p.intn(len(roles))], release: p.intn(3) == 0}

			if op.release {
				if l, ok := held[op.actor]; ok {
					if err := l.Release(); err != nil {
						t.Fatalf("seed %d step %d: Release: %v", seed, step, err)
					}
					delete(held, op.actor)
				}
				assertAtMostOneWriter(t, seed, step, held)
				continue
			}
			if _, ok := held[op.actor]; ok {
				// An actor already holding does not acquire twice; the process
				// that holds a lease is the one that keeps it.
				continue
			}

			conflict := leaseConflict(held, op.role)
			lock, err := Acquire(dir, Owner{
				RunID:     fmt.Sprintf("run-%d-%d", seed, op.actor),
				ProjectID: "corsolv-delivery-engine",
				Session:   fmt.Sprintf("actor-%d", op.actor),
				Worktree:  dir,
				Role:      op.role,
			})
			switch err {
			case nil:
				acquired++
				if conflict {
					lock.Release() //nolint:errcheck
					t.Fatalf("seed %d step %d: actor %d took a %s lease while %s held it",
						seed, step, op.actor, op.role, describeHeld(held))
				}
				held[op.actor] = lock
			default:
				denied++
				if !conflict {
					t.Fatalf("seed %d step %d: actor %d was refused a %s lease with %s held: %v",
						seed, step, op.actor, op.role, describeHeld(held), err)
				}
				if !errors.Is(err, ErrWriterHeld) {
					t.Fatalf("seed %d step %d: contention reported as %v, want ErrWriterHeld — "+
						"a caller that cannot tell contention from a fault will break a live lease", seed, step, err)
				}
			}
			assertAtMostOneWriter(t, seed, step, held)
		}
		for _, l := range held {
			l.Release() //nolint:errcheck
		}
	}
	if acquired == 0 || denied == 0 {
		t.Fatalf("degenerate corpus: %d acquisitions, %d denials", acquired, denied)
	}
	t.Logf("%d generated lease interleavings, %d steps each: %d acquisitions, %d denials, never two writers",
		sequences, steps, acquired, denied)
}

// leaseConflict is the oracle: whether the lease already held makes this
// acquisition impossible. An exclusive role needs the worktree to itself; a
// read-only role only needs no exclusive holder.
func leaseConflict(held map[int]*Lock, want Role) bool {
	for _, l := range held {
		if want.Exclusive() || l.Owner().Role.Exclusive() {
			return true
		}
	}
	return false
}

func assertAtMostOneWriter(t *testing.T, seed uint64, step int, held map[int]*Lock) {
	t.Helper()
	writers := 0
	for _, l := range held {
		if l.Owner().Role.Exclusive() {
			writers++
		}
	}
	if writers > 1 {
		t.Fatalf("seed %d step %d: %d exclusive owners at once", seed, step, writers)
	}
	if writers == 1 && len(held) > 1 {
		t.Fatalf("seed %d step %d: an exclusive owner is sharing the worktree with %d others", seed, step, len(held)-1)
	}
}

func describeHeld(held map[int]*Lock) string {
	if len(held) == 0 {
		return "nobody"
	}
	out := ""
	for actor, l := range held {
		if out != "" {
			out += ", "
		}
		out += fmt.Sprintf("actor %d as %s", actor, l.Owner().Role)
	}
	return out
}

func TestADuplicateWriterIsRefusedWithoutDisturbingTheHolder(t *testing.T) {
	// INVARIANT 4. Conflicting ownership fails BEFORE mutation, and the refusal
	// leaves the live holder's claim exactly as it was. A second session that
	// rewrote the owner evidence on its way to being refused would leave the
	// worktree recording an owner that never held it.
	f := newControllerFixture(t)
	plan := Plan{RunID: "holder", Risk: RiskQ0, Tasks: []Task{
		{ID: "work", Title: "work", Band: BandPrimary, Argv: []string{"work"}},
		docsTask(),
	}}
	first, err := Begin(context.Background(), f.spec, plan)
	if err != nil {
		t.Fatalf("first Begin: %v", err)
	}
	defer first.Close() //nolint:errcheck

	lockDir := WriterLockDir(first.Report.Repo)
	ownerBefore := readFileOrFail(t, filepath.Join(lockDir, ownerFile))
	headBefore := first.Fence.Head

	for i := 0; i < 3; i++ {
		second := f.spec
		second.StateDir = filepath.Join(t.TempDir(), "second-state")
		s, serr := Begin(context.Background(), second, Plan{RunID: fmt.Sprintf("intruder-%d", i), Risk: RiskQ0, Tasks: plan.Tasks})
		if serr == nil {
			s.Close() //nolint:errcheck
			t.Fatalf("attempt %d: a second writer was admitted to a worktree that already has one", i)
		}
		if got := readFileOrFail(t, filepath.Join(lockDir, ownerFile)); string(got) != string(ownerBefore) {
			t.Fatalf("attempt %d: a refused writer rewrote the live holder's owner evidence:\nwas:\n%s\nnow:\n%s",
				i, ownerBefore, got)
		}
	}

	// The holder is untouched: same fence, same claim, still able to work.
	if res := first.Fence.Check(); !res.Intact() {
		t.Fatalf("the live holder's fence was disturbed by refused writers: %s", res.Evidence)
	}
	if first.Fence.Head != headBefore {
		t.Fatalf("the live holder's fence moved from %s to %s", shortSHA(headBefore), shortSHA(first.Fence.Head))
	}
}

// --- INVARIANT 5: BOUNDED CONTINUATION ---------------------------------------

func TestTheResumeBudgetIsDurableAcrossAnyNumberOfRestarts(t *testing.T) {
	// INVARIANT 5. A turn cap or a task's own CONTINUE may resume, and the
	// continuation must stay bounded AND durable. A restart is not a way to buy
	// more drives.
	//
	// THE DEFECT THIS EXISTS FOR. The resume COUNT survived a restart, but the
	// hold it had already produced did not: a task that had exhausted its
	// budget came back pending, was driven once more, and was held again. One
	// extra drive per restart is not a bound — an autonomous supervisor that
	// restarts the engine drives that task forever, one restart at a time, and
	// each drive is a full agent turn.
	for _, budget := range []int{1, 2, 3} {
		for _, restarts := range []int{1, 2, 4, 6} {
			t.Run(fmt.Sprintf("budget-%d/restarts-%d", budget, restarts), func(t *testing.T) {
				f := newControllerFixture(t)
				work := supervisedTask("work", BandPrimary)
				work.MaxResumes = budget
				plan := Plan{RunID: "bounded", Risk: RiskQ0, Tasks: []Task{work, docsTask()}}

				drives := 0
				observe := func(_ context.Context, task Task) Execution {
					if task.ID != "work" {
						return Execution{ExitedZero: true}
					}
					drives++
					return execContinues()
				}

				for i := 0; i < restarts; i++ {
					s, err := Begin(context.Background(), f.spec, plan)
					if err != nil {
						t.Fatalf("restart %d: Begin: %v", i, err)
					}
					s.Runner.Sleep = func(context.Context, time.Duration) {}
					s.Runner.Observe = observe
					if _, err := s.Runner.Run(context.Background()); err != nil {
						t.Fatalf("restart %d: Run: %v", i, err)
					}
					w, _ := s.Queue.Task("work")
					if w.State != TaskHeld {
						t.Fatalf("restart %d: a task that spent its resume budget is %s, want held", i, w.State)
					}
					if err := s.Close(); err != nil {
						t.Fatalf("restart %d: Close: %v", i, err)
					}
				}

				// The bound. A task may be driven its budget's worth of times
				// plus the one drive that discovers the budget is spent — and
				// that total is over the LIFETIME of the work, not per restart.
				if drives > budget+1 {
					t.Fatalf("a task with maxResumes=%d was driven %d times across %d restarts; "+
						"the durable bound is %d — a restart renewed the resume budget",
						budget, drives, restarts, budget+1)
				}
				if drives == 0 {
					t.Fatal("the task was never driven; the bound is vacuous")
				}
			})
		}
	}
}

func TestADurableResumeCountIsNeverLoweredByReplay(t *testing.T) {
	// INVARIANT 5, as a property of the fold. Reading the journal again must
	// never say a task has resumed FEWER times than a shorter reading of the
	// same journal did. A count that could fall is a budget that can be reset.
	f := newControllerFixture(t)
	work := supervisedTask("work", BandPrimary)
	work.MaxResumes = 3
	plan := Plan{RunID: "counts", Risk: RiskQ0, Tasks: []Task{work, docsTask()}}

	runToTerminal(t, f, plan, func(_ context.Context, task Task) Execution {
		if task.ID != "work" {
			return Execution{ExitedZero: true}
		}
		return execContinues()
	})

	records, _, err := ReadJournal(stateDirPath(f.stateDir, JournalName))
	if err != nil {
		t.Fatalf("ReadJournal: %v", err)
	}
	highest := 0
	for i := 1; i <= len(records); i++ {
		st := Replay(records[:i], "counts")
		got := st.Resumes["work"]
		if got < highest {
			t.Fatalf("prefix %d reports %d resumes after prefix-%d reported %d — a longer journal forgot a drive",
				i, got, i-1, highest)
		}
		highest = got
	}
	if highest < work.MaxResumes {
		t.Fatalf("the run recorded only %d resumes for a budget of %d; the property is vacuous", highest, work.MaxResumes)
	}

	// And a queue restored from the full journal refuses to offer the task
	// again, whatever the plan says: the bound is a durable fact about the work.
	q := NewQueue(plan, nil)
	q.Restore(Replay(records, "counts"))
	restored, _ := q.Task("work")
	if restored.State != TaskHeld {
		t.Fatalf("a restored task that spent its resume budget is %s, want held", restored.State)
	}
	if next, ok := q.Next(); ok && next.Task.ID == "work" {
		t.Fatal("a restored queue offered a task whose resume budget was already spent")
	}
}

func containsAll(haystack string, needles ...string) bool {
	for _, n := range needles {
		if !strings.Contains(haystack, n) {
			return false
		}
	}
	return true
}
