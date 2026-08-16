package unattended

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// maxCapturedOutput bounds what a failing task contributes to the journal. A
// task that prints a hundred megabytes before failing must not turn the run's
// durable record into something nobody can read or replay.
const maxCapturedOutput = 8 << 10

// defaultTaskTimeout bounds a task that declares no timeout of its own.
const defaultTaskTimeout = 30 * time.Minute

// Runner executes a work plan under ownership, a fence, and a journal.
//
// It is the piece that turns the rest of this package into an unattended run:
// it decides nothing about what the work is, and everything about whether it is
// safe to do the next piece of it, what to do instead when it is not, and what
// to write down either way.
type Runner struct {
	Spec   Spec
	Plan   Plan
	Report *Report

	Lock    *Lock
	Fence   *Fence
	Journal *Journal
	Queue   *Queue

	// Evidence is the run's QA gate ledger, keyed by gate ID. It is seeded from
	// the journal on resume and folded by MergeEvidence as gates reach a
	// verdict, so it never forgets a failure by being restarted.
	Evidence map[string]GateEvidence

	// Sleep waits out a retry backoff. It is injectable so tests prove the
	// backoff *policy* without paying it in wall-clock.
	Sleep func(ctx context.Context, d time.Duration)
	// Now is the clock, injectable for the same reason.
	Now func() time.Time

	// OnProgress is called after every state change, before the heartbeat is
	// written, so a caller can mirror progress somewhere else.
	OnProgress func(Progress)

	startedAt     time.Time
	lastMilestone string
	stopReason    string
	stopClass     FailureClass
	attempts      int
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now().UTC()
}

func (r *Runner) sleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	if r.Sleep != nil {
		r.Sleep(ctx, d)
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// Run drains the work queue and returns how the run ended.
//
// The loop's shape is the whole answer to "why did the session stop after nine
// minutes". Every ordinary failure — a failing test, a build error, a merge
// conflict, a rate limit — returns to the top of this loop and takes the next
// piece of declared work. Only a failure a person must resolve leaves it.
func (r *Runner) Run(ctx context.Context) (CompletionEvent, error) {
	r.startedAt = r.now()

	if _, err := r.Journal.Append(Record{
		Kind: RecordRunStarted, RunID: r.Plan.RunID,
		Detail: fmt.Sprintf("session=%s worktree=%s branch=%s head=%s",
			r.Spec.Ownership.Session, r.Fence.Worktree, r.Fence.Branch, shortSHA(r.Fence.Head)),
	}); err != nil {
		return CompletionEvent{}, err
	}
	r.publish("starting", nil)

	for {
		if err := ctx.Err(); err != nil {
			r.stopReason = "the run was canceled: " + err.Error()
			r.stopClass = FailureEnvironment
			break
		}
		qt, ok := r.Queue.Next()
		if !ok {
			break
		}
		if stop := r.runOne(ctx, qt); stop {
			break
		}
	}

	event := r.finish()
	if _, err := r.Journal.Append(Record{
		Kind: RecordRunFinished, Outcome: string(event.Outcome), Detail: event.Reason,
		DurationMS: r.now().Sub(r.startedAt).Milliseconds(),
	}); err != nil {
		return event, err
	}
	if err := WriteCompletion(r.Spec.StateDir, event); err != nil {
		return event, err
	}
	r.publish("finished", nil)
	return event, nil
}

// runOne attempts a single task and reports whether the run must stop.
func (r *Runner) runOne(ctx context.Context, qt *QueuedTask) (stop bool) {
	r.publish("running", qt)

	// A mutating task re-verifies the fence first. This is the check that
	// catches a branch moved underneath a live run, and it is deliberately
	// immediately before the mutation rather than once at the start.
	if qt.Task.Mutates {
		res := r.Fence.Check()
		if !res.Intact() {
			r.Journal.Append(Record{ //nolint:errcheck
				Kind: RecordFenceViolated, TaskID: qt.Task.ID,
				Outcome: string(res.Status),
				Detail:  fmt.Sprintf("expected %q, observed %q — %s", res.Expected, res.Observed, res.Evidence),
			})
			r.Queue.Hold(qt, "the repository moved outside this run: "+string(res.Status))
			r.stopReason = res.Error().Error()
			r.stopClass = FailureConcurrentWriter
			return true
		}
		r.Journal.Append(Record{ //nolint:errcheck
			Kind: RecordFenceVerified, TaskID: qt.Task.ID, Detail: res.Expected,
		})
	}

	attemptNumber := qt.AttemptCount() + 1
	started := r.now()
	r.Journal.Append(Record{ //nolint:errcheck
		Kind: RecordTaskStarted, TaskID: qt.Task.ID, Attempt: attemptNumber,
		Detail: strings.Join(qt.Task.Argv, " "),
	})
	r.attempts++

	out, ok, err := r.execute(ctx, qt.Task)
	elapsed := r.now().Sub(started)

	if ok && err == nil {
		r.Journal.Append(Record{ //nolint:errcheck
			Kind: RecordTaskSucceeded, TaskID: qt.Task.ID, Attempt: attemptNumber,
			DurationMS: elapsed.Milliseconds(),
		})
		r.Queue.RecordAttempt(qt, TaskAttempt{
			StartedAt: started, Duration: elapsed.Round(time.Millisecond).String(), Succeeded: true,
		})
		r.lastMilestone = qt.Task.ID + " succeeded"
		r.recordGateEvidence(qt, GatePass, "")
		if qt.Task.Mutates {
			r.recordAdvance(qt)
		}
		r.publish("running", nil)
		return false
	}

	text := out
	if err != nil {
		text = err.Error() + "\n" + out
	}
	class := Classify(text, r.Spec.Classification)
	// The journal keeps one line per record so a truncated tail stays
	// recoverable, which means a failure's actual output has nowhere to go in
	// it. Without this the run says a task failed and cannot say why — and
	// "it failed at three in the morning and I need to know why" is the entire
	// point of running unattended.
	outputPath := r.captureFailure(qt.Task.ID, attemptNumber, text)
	delay, willRetry := r.Queue.RecordAttempt(qt, TaskAttempt{
		StartedAt: started, Duration: elapsed.Round(time.Millisecond).String(),
		Succeeded: false, Class: class.Class, Reason: class.Reason, Output: truncate(Redact(text)),
	})
	r.Journal.Append(Record{ //nolint:errcheck
		Kind: RecordTaskFailed, TaskID: qt.Task.ID, Attempt: attemptNumber,
		Class: class.Class, Outcome: string(qt.State), DurationMS: elapsed.Milliseconds(),
		Detail: class.Reason + " | " + firstLine(Redact(text)) + " | output: " + outputPath,
	})

	// A gate reaches a verdict when the queue is finished with it, not on every
	// attempt. An attempt that will be retried has not finished running the
	// gate, and recording an interim failure would make a retry pointless: the
	// evidence ledger keeps the worse verdict for a revision on purpose.
	if qt.State == TaskFailed {
		r.recordGateEvidence(qt, GateFail, class.Reason+" | "+firstLine(Redact(text))+" | output: "+outputPath)
	}

	policy := PolicyFor(class.Class)
	if policy.StopsRun {
		r.stopReason = fmt.Sprintf("%s failed with a %s failure, which ends the run: %s",
			qt.Task.ID, class.Class, policy.Why)
		r.stopClass = class.Class
		r.publish("stopping", nil)
		return true
	}
	if willRetry {
		r.Journal.Append(Record{ //nolint:errcheck
			Kind: RecordTaskRetry, TaskID: qt.Task.ID, Attempt: attemptNumber,
			Class: class.Class, Detail: "retrying in " + delay.String(),
		})
		r.publish("retrying", qt)
		r.sleep(ctx, delay)
		return false
	}
	r.publish("running", nil)
	return false
}

// recordGateEvidence folds a gate task's mechanical verdict into the run's
// evidence ledger and writes it durably.
//
// The revision it binds to is the fence's position at the moment the gate ran,
// which is what makes the evidence certify code rather than certify a task. A
// gate that ran before this run committed carries the pre-commit revision, and
// the progression decision then reports it stale rather than letting it license
// code it never saw.
func (r *Runner) recordGateEvidence(qt *QueuedTask, result GateResult, detail string) {
	if qt.Task.QAGate == "" {
		return
	}
	ev := GateEvidence{
		GateID:     qt.Task.QAGate,
		TaskID:     qt.Task.ID,
		Result:     result,
		ObservedAt: r.now().UTC(),
		Reproduce:  qt.Task.Argv,
		Detail:     detail,
	}
	if len(qt.Task.Argv) > 0 {
		ev.Tool = qt.Task.Argv[0]
		ev.ToolVersion = r.observedToolVersion(ev.Tool)
	}
	if r.Fence != nil {
		ev.TargetSHA = r.Fence.Head
	}

	if r.Evidence == nil {
		r.Evidence = map[string]GateEvidence{}
	}
	r.Evidence[ev.GateID] = MergeEvidence(r.Evidence[ev.GateID], ev)

	r.Journal.Append(Record{ //nolint:errcheck
		Kind: RecordGateEvidence, TaskID: qt.Task.ID, Outcome: string(result),
		Detail: fmt.Sprintf("gate %s against %s", ev.GateID, shortSHA(ev.TargetSHA)),
		Gate:   &ev,
	})
}

// observedToolVersion reuses what preflight already read about a tool, rather
// than probing it a second time at a different moment.
func (r *Runner) observedToolVersion(tool string) string {
	if r.Report == nil {
		return ""
	}
	c, ok := r.Report.Check("tool." + tool)
	if !ok || c.Outcome != OutcomePass {
		return ""
	}
	return c.Observed
}

// QADecision evaluates the packet's progression eligibility against the
// evidence recorded so far and the revision currently in hand.
func (r *Runner) QADecision() ProgressionDecision {
	head := ""
	if r.Fence != nil {
		head = r.Fence.Head
	}
	return EvaluateProgression(r.Spec.QA, r.Plan.Risk, head, r.Evidence)
}

// recordAdvance moves the fence to the commit this run just made.
//
// A refused advance is not fatal: the mutation already happened, and the fence
// reporting that the branch is not where it should be is information the next
// fence check will act on. It is journaled rather than swallowed.
func (r *Runner) recordAdvance(qt *QueuedTask) {
	res, err := r.Fence.RecordAuthorisedAdvance("task " + qt.Task.ID + " mutated the worktree")
	if err != nil {
		r.Journal.Append(Record{ //nolint:errcheck
			Kind: RecordFenceViolated, TaskID: qt.Task.ID, Outcome: string(res.Status),
			Detail: "an advance after a mutating task was refused: " + err.Error(),
		})
		return
	}
	r.Journal.Append(Record{ //nolint:errcheck
		Kind: RecordFenceAdvanced, TaskID: qt.Task.ID, Detail: shortSHA(r.Fence.Head),
	})
}

// execute runs a task's command, bounded and with output captured.
func (r *Runner) execute(ctx context.Context, t Task) (out string, ok bool, err error) {
	timeout := timeoutOr(t.TimeoutSeconds, defaultTaskTimeout)
	dir := t.Dir
	if dir == "" {
		dir = r.Spec.Ownership.Worktree
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, t.Argv[0], t.Argv[1:]...)
	cmd.Dir = dir
	// A task may not ask a person a question; there is nobody there.
	cmd.Stdin = nil
	superviseProcess(cmd)
	// Without a wait delay, an orphaned grandchild holding the output pipes
	// open keeps the run waiting long past the timeout it declared.
	cmd.WaitDelay = orphanWaitDelay
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GC_UNATTENDED_RUN_ID="+r.Plan.RunID,
		"GC_UNATTENDED_TASK_ID="+t.ID,
		"GC_UNATTENDED_STATE_DIR="+r.Spec.StateDir,
	)
	raw, runErr := cmd.CombinedOutput()
	out = strings.TrimSpace(string(raw))

	if ctx.Err() != nil {
		return out, false, fmt.Errorf("task %s timed out after %s", t.ID, timeout)
	}
	if runErr != nil {
		var ee *exec.ExitError
		if asExitError(runErr, &ee) {
			return out, false, nil
		}
		return out, false, runErr
	}
	return out, true, nil
}

// FailuresDirName is where a run keeps the output of every failed attempt.
const FailuresDirName = "failures"

// captureFailure writes a failed attempt's output where a person can read it,
// and returns the path it used.
//
// It is redacted before it is written. A failing command's output is arbitrary
// text from arbitrary tooling, it is being written to a file that outlives the
// run, and a token that reaches it stays there.
//
// A capture that cannot be written is reported into the journal and otherwise
// ignored: losing the diagnostic is bad, and failing the run because the
// diagnostic could not be filed would be worse.
func (r *Runner) captureFailure(taskID string, attempt int, text string) string {
	path := stateDirPath(r.Spec.StateDir, fmt.Sprintf("%s/%s-attempt-%d.log", FailuresDirName, taskID, attempt))
	if err := writeFileAtomic(path, []byte(Redact(text)+"\n")); err != nil {
		r.Journal.Append(Record{ //nolint:errcheck
			Kind: RecordTaskFailed, TaskID: taskID, Attempt: attempt,
			Outcome: "failure-output-not-captured", Detail: err.Error(),
		})
		return "not captured"
	}
	return path
}

func truncate(s string) string {
	if len(s) <= maxCapturedOutput {
		return s
	}
	return s[:maxCapturedOutput] + "\n… truncated"
}

// finish decides how the run ended and builds its terminal record.
//
// The distinction that matters is between "stopped" and "finished with work
// held". A run that drained everything it could and left two tasks behind a
// review requirement did its whole job; reporting that as a failure would train
// people to ignore the report.
func (r *Runner) finish() CompletionEvent {
	counts := r.Queue.Counts()
	var (
		failures     []string
		humanActions []string
		auth         bool
	)
	seen := map[string]bool{}
	for _, qt := range r.Queue.Tasks() {
		switch qt.State {
		case TaskFailed:
			failures = append(failures, qt.Task.ID)
		case TaskHeld:
			if qt.HeldReason != "" && !seen[qt.HeldReason] {
				seen[qt.HeldReason] = true
				humanActions = append(humanActions, qt.Task.ID+": "+qt.HeldReason)
			}
			for _, id := range qt.Task.RequiresChecks {
				if _, isBoundary := r.boundaries()[id]; isBoundary && isAuthBoundary(id) {
					auth = true
				}
			}
		}
	}
	sort.Strings(failures)

	// The QA decision is taken before the task tallies, and it can only ever
	// refuse. A run whose every task succeeded has not thereby proved its work
	// is fit to progress: it has proved its commands exited zero, which is a
	// claim the run makes about itself. Only gate evidence bound to the
	// revision in hand can license progression, and no assertion, task success
	// or authored summary substitutes for it.
	qa := r.QADecision()

	outcome := RunCompleted
	reason := "every declared task succeeded"
	switch {
	case r.stopReason != "":
		outcome = RunFailed
		reason = r.stopReason
	case len(failures) > 0:
		outcome = RunFailed
		reason = fmt.Sprintf("%d task(s) exhausted their attempts: %s", len(failures), strings.Join(failures, ", "))
	case counts[TaskHeld] > 0 && auth:
		outcome = RunAwaitingAuth
		reason = fmt.Sprintf("%d task(s) are waiting on an authentication a person must perform", counts[TaskHeld])
	case counts[TaskHeld] > 0:
		outcome = RunBlockedHuman
		reason = fmt.Sprintf("%d task(s) are held behind a human boundary; everything else the run could do is done", counts[TaskHeld])
	case !qa.Allowed:
		outcome = RunFailed
		reason = "the packet may not progress: " + qa.Reason()
	}

	finished := r.now()
	return CompletionEvent{
		RunID:        r.Plan.RunID,
		ProjectID:    r.Spec.ProjectID,
		Session:      r.Spec.Ownership.Session,
		SessionLabel: r.Spec.Ownership.Session,
		Outcome:      outcome,
		Reason:       reason,
		StartedAt:    r.startedAt,
		FinishedAt:   finished,
		Duration:     finished.Sub(r.startedAt).Round(time.Second).String(),
		Worktree:     r.Fence.Worktree,
		Branch:       r.Fence.Branch,
		Head:         r.Fence.Head,
		Tasks:        counts,
		Attempts:     r.attempts,
		HumanActions: humanActions,
		Failures:     failures,
		QA:           qa,
	}
}

// isAuthBoundary reports whether a boundary check is one a person clears by
// authenticating, which is usually seconds of work rather than a conversation.
func isAuthBoundary(checkID string) bool {
	return strings.HasPrefix(checkID, "credential.") || checkID == "github.auth"
}

func (r *Runner) boundaries() map[string]string {
	if r.Report == nil {
		return nil
	}
	return r.Report.Boundaries()
}

// publish writes the heartbeat. A failure to publish is recorded and does not
// stop the run: losing the ability to say what you are doing is not a reason to
// stop doing it.
func (r *Runner) publish(stage string, current *QueuedTask) {
	now := r.now()
	p := Progress{
		RunID:         r.Plan.RunID,
		ProjectID:     r.Spec.ProjectID,
		Session:       r.Spec.Ownership.Session,
		Stage:         stage,
		StartedAt:     r.startedAt,
		UpdatedAt:     now,
		Elapsed:       now.Sub(r.startedAt).Round(time.Second).String(),
		LastMilestone: r.lastMilestone,
		Worktree:      r.Fence.Worktree,
		Branch:        r.Fence.Branch,
		Head:          r.Fence.Head,
		Tasks:         r.Queue.Counts(),
		Attempts:      r.attempts,
	}
	if r.Lock != nil {
		p.WriterOwner = r.Lock.Owner().RunID
		p.WriterPID = r.Lock.Owner().PID
	}
	if current != nil {
		p.CurrentTask = current.Task.ID
		p.CurrentBand = current.Task.Band
		// Lower-band work is only a fallback while primary work is still
		// outstanding. Once every primary task has succeeded, validation and
		// documentation work is the plan, not a detour.
		p.UsingFallback = current.Task.Band != BandPrimary && r.Queue.PrimaryWorkOutstanding()
	}
	if next, ok := r.Queue.Next(); ok {
		p.NextAction = next.Task.ID + " (" + string(next.Task.Band) + ")"
	}
	for _, qt := range r.Queue.Tasks() {
		if qt.State == TaskHeld && qt.HeldReason != "" {
			if p.ActiveBlocker == "" {
				p.ActiveBlocker = qt.Task.ID + ": " + qt.HeldReason
			}
			p.Boundaries = append(p.Boundaries, qt.Task.ID+": "+qt.HeldReason)
		}
	}
	if r.OnProgress != nil {
		r.OnProgress(p)
	}
	if err := WriteProgress(r.Spec.StateDir, p); err != nil {
		r.Journal.Append(Record{ //nolint:errcheck
			Kind: RecordPreflight, Outcome: "progress-publication-failed", Detail: err.Error(),
		})
	}

	// The delivery projection is refreshed here rather than only after the run.
	//
	// The GUK BPM pilot found the reason: its evidence task exists to commit the
	// projection into the target repository, and a projection written after the
	// run has ended does not exist while the run is still executing the task
	// that needs it. Refreshing it alongside the heartbeat also means the
	// dashboard sees delivery state *during* a long run instead of only once it
	// is over, which is the more useful behavior anyway.
	if _, err := PublishDelivery(r.Spec, r.Queue, r.Fence, now); err != nil {
		r.Journal.Append(Record{ //nolint:errcheck
			Kind: RecordPreflight, Outcome: "delivery-projection-failed", Detail: err.Error(),
		})
	}
}

func readFileIfPresent(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %q: %w", path, err)
	}
	return data, nil
}
