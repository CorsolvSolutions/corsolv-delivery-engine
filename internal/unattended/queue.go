package unattended

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// TaskState is where a task has got to.
type TaskState string

// The task states.
const (
	// TaskPending — not yet attempted, or awaiting a retry.
	TaskPending TaskState = "pending"
	// TaskSucceeded — the command exited zero.
	TaskSucceeded TaskState = "succeeded"
	// TaskFailed — the command exhausted its attempts.
	TaskFailed TaskState = "failed"
	// TaskHeld — the task cannot be attempted: a dependency did not succeed, or
	// a capability it requires is behind a human boundary. Held is not failed:
	// nothing was proved wrong, the run simply cannot get there from here.
	TaskHeld TaskState = "held"
)

// Terminal reports whether the queue is finished with a task.
func (s TaskState) Terminal() bool {
	return s == TaskSucceeded || s == TaskFailed || s == TaskHeld
}

// TaskAttempt is one recorded execution of a task.
type TaskAttempt struct {
	Number    int          `json:"number"`
	StartedAt time.Time    `json:"startedAt"`
	Duration  string       `json:"duration"`
	Succeeded bool         `json:"succeeded"`
	Class     FailureClass `json:"class,omitempty"`
	Reason    string       `json:"reason,omitempty"`
	Output    string       `json:"output,omitempty"`
}

// QueuedTask is a declared task plus everything that has happened to it.
type QueuedTask struct {
	Task     Task          `json:"task"`
	State    TaskState     `json:"state"`
	Attempts []TaskAttempt `json:"attempts,omitempty"`
	// HeldReason names what would have to change for this task to run.
	HeldReason string `json:"heldReason,omitempty"`
	// Interrupted records that a previous run started this task and did not
	// finish it. The attempt is preserved rather than erased.
	Interrupted bool `json:"interrupted,omitempty"`
	// Resumes counts the resumable outcomes — the task's own CONTINUE, or a
	// harness turn cap — this task has PRODUCED. The last one may be the one
	// that exceeded the budget and was therefore not acted on, so a task held
	// for not converging carries one more resume than its budget allowed.
	//
	// It is counted apart from Attempts because nothing failed. Folding a turn
	// cap into the retry budget is what made a long agent job look like a
	// flaky command: three interruptions and the task was "exhausted" without
	// anything having gone wrong.
	Resumes int `json:"resumes,omitempty"`
}

// AttemptCount is how many attempts this task has consumed.
func (t *QueuedTask) AttemptCount() int { return len(t.Attempts) }

// DefaultMaxResumes bounds resumable outcomes for a task that declares no bound
// of its own.
//
// It is generous because a turn cap is ordinary operation for a supervised
// agent rather than a symptom, and it is finite because a run that drives one
// task forever is indistinguishable from a hung one.
const DefaultMaxResumes = 20

// resumeBudget is how many resumable outcomes this task may consume.
func (t *QueuedTask) resumeBudget() int {
	if t.Task.MaxResumes > 0 {
		return t.Task.MaxResumes
	}
	return DefaultMaxResumes
}

// budget is the attempt allowance for a task, from its own override or from the
// policy for the class of failure it last hit.
func (t *QueuedTask) budget(class FailureClass) int {
	if t.Task.MaxAttempts > 0 {
		return t.Task.MaxAttempts
	}
	return PolicyFor(class).MaxAttempts
}

// Queue is the run's work, ordered by band and gated by dependencies.
//
// It exists to answer one question well: when the thing the run most wants to
// do cannot be done, what should it do instead? A run without that answer stops
// at its first dependency; a run that answers it badly invents busywork. The
// queue only ever selects work the plan declared, so it can do neither.
type Queue struct {
	tasks []*QueuedTask
	index map[string]*QueuedTask
	// boundaries maps a preflight check ID to the human action that clears it.
	boundaries map[string]string
}

// NewQueue builds a queue from a plan and the boundaries preflight found.
//
// Tasks requiring a capability that is behind a human boundary are held before
// the run starts rather than attempted and failed. That is the whole payoff of
// discovering boundaries early: the queue routes around them instead of walking
// into them one at a time.
func NewQueue(plan Plan, boundaries map[string]string) *Queue {
	q := &Queue{index: map[string]*QueuedTask{}, boundaries: boundaries}
	for _, t := range plan.Tasks {
		qt := &QueuedTask{Task: t, State: TaskPending}
		if held, why := q.boundaryHold(t); held {
			qt.State = TaskHeld
			qt.HeldReason = why
		}
		q.tasks = append(q.tasks, qt)
		q.index[t.ID] = qt
	}
	q.propagateHolds()
	return q
}

func (q *Queue) boundaryHold(t Task) (bool, string) {
	var blocked []string
	for _, id := range t.RequiresChecks {
		if action, ok := q.boundaries[id]; ok {
			blocked = append(blocked, fmt.Sprintf("%s (%s)", id, action))
		}
	}
	if len(blocked) == 0 {
		return false, ""
	}
	return true, "requires a capability behind a human boundary: " + strings.Join(blocked, "; ")
}

// Restore folds durable journal state back into the queue, so a resumed run
// neither repeats completed work nor restarts a retry budget it already spent.
func (q *Queue) Restore(st ResumeState) {
	for _, qt := range q.tasks {
		if st.Succeeded[qt.Task.ID] {
			qt.State = TaskSucceeded
			continue
		}
		if n := st.Attempts[qt.Task.ID]; n > 0 && len(qt.Attempts) == 0 {
			// The attempts themselves are gone with the crashed process; their
			// count is not, and the count is what bounds the retry.
			for i := 1; i <= n; i++ {
				qt.Attempts = append(qt.Attempts, TaskAttempt{
					Number: i, Succeeded: false,
					Reason: "attempt recorded by an earlier run of this journal",
				})
			}
		}
		// The resume budget is durable for the same reason the retry budget is:
		// a run that forgot it on restart would get a fresh one every crash and
		// could drive one task forever, one crash at a time.
		if n := st.Resumes[qt.Task.ID]; n > qt.Resumes {
			qt.Resumes = n
		}
		if st.Interrupted[qt.Task.ID] {
			qt.Interrupted = true
		}
	}
	q.propagateHolds()
}

// propagateHolds holds every task whose dependency can no longer succeed.
//
// Without this a task with a failed dependency stays pending forever and the
// queue reports work remaining that it will never select — which reads exactly
// like a hung run.
func (q *Queue) propagateHolds() {
	// Iterate to a fixed point: a hold on one task holds its dependents, whose
	// holds hold theirs.
	for changed := true; changed; {
		changed = false
		for _, qt := range q.tasks {
			if qt.State != TaskPending {
				continue
			}
			var blocked []string
			for _, need := range qt.Task.Needs {
				dep, ok := q.index[need]
				if !ok {
					continue
				}
				if dep.State == TaskFailed || dep.State == TaskHeld {
					blocked = append(blocked, fmt.Sprintf("%s is %s", need, dep.State))
				}
			}
			if len(blocked) > 0 {
				qt.State = TaskHeld
				qt.HeldReason = "depends on work that did not succeed: " + strings.Join(blocked, "; ")
				changed = true
			}
		}
	}
}

// Next returns the highest-preference task that can be attempted now.
//
// Preference is band order, then declaration order. A task whose dependencies
// have not yet succeeded is skipped rather than held — it may still become
// eligible when they do.
func (q *Queue) Next() (*QueuedTask, bool) {
	var best *QueuedTask
	for _, qt := range q.tasks {
		if qt.State != TaskPending || !q.dependenciesSatisfied(qt) {
			continue
		}
		if best == nil || qt.Task.Band.Rank() < best.Task.Band.Rank() {
			best = qt
		}
	}
	return best, best != nil
}

func (q *Queue) dependenciesSatisfied(qt *QueuedTask) bool {
	for _, need := range qt.Task.Needs {
		dep, ok := q.index[need]
		if !ok {
			continue
		}
		if dep.State != TaskSucceeded {
			return false
		}
	}
	return true
}

// RecordAttempt folds one execution result into the queue and returns the
// retry delay, if the task will be retried.
//
// The task stays pending while it has budget, so the same task is offered
// again; when the budget is gone it fails, and its dependents are held.
func (q *Queue) RecordAttempt(qt *QueuedTask, attempt TaskAttempt) (retryIn time.Duration, willRetry bool) {
	attempt.Number = len(qt.Attempts) + 1
	qt.Attempts = append(qt.Attempts, attempt)

	if attempt.Succeeded {
		qt.State = TaskSucceeded
		q.propagateHolds()
		return 0, false
	}
	if len(qt.Attempts) >= qt.budget(attempt.Class) {
		qt.State = TaskFailed
		q.propagateHolds()
		return 0, false
	}
	qt.State = TaskPending
	return PolicyFor(attempt.Class).Backoff(len(qt.Attempts)), true
}

// RecordResume folds a resumable outcome into the queue and reports whether the
// task may be driven again.
//
// The task stays pending either way; what changes is whether the run is still
// entitled to re-offer it. A task that has spent its resume budget has not
// failed at anything — it simply has not converged — so exhausting the budget
// is left to the caller to hold rather than being turned into a failure here.
func (q *Queue) RecordResume(qt *QueuedTask) (used, budget int, mayResume bool) {
	qt.Resumes++
	qt.State = TaskPending
	return qt.Resumes, qt.resumeBudget(), qt.Resumes <= qt.resumeBudget()
}

// Hold marks a task un-attemptable for a stated reason.
func (q *Queue) Hold(qt *QueuedTask, reason string) {
	qt.State = TaskHeld
	qt.HeldReason = reason
	q.propagateHolds()
}

// Tasks returns every queued task in declaration order.
func (q *Queue) Tasks() []*QueuedTask { return q.tasks }

// Task returns a queued task by ID.
func (q *Queue) Task(id string) (*QueuedTask, bool) {
	t, ok := q.index[id]
	return t, ok
}

// Counts summarizes the queue by state.
func (q *Queue) Counts() map[TaskState]int {
	out := map[TaskState]int{}
	for _, qt := range q.tasks {
		out[qt.State]++
	}
	return out
}

// Exhausted reports whether no task can ever be attempted again. It is the one
// legitimate reason for a run to end because there is nothing left to do.
func (q *Queue) Exhausted() bool {
	_, ok := q.Next()
	return !ok
}

// PrimaryWorkOutstanding reports whether any primary-band task has yet to
// succeed.
//
// It is what separates "doing lower-band work because the primary path is
// blocked" from "doing lower-band work because the primary path is finished".
// From the band alone those look identical and mean opposite things, and the
// first unattended run published the wrong one of them: it reported validation
// work as fallback while every primary task had in fact already succeeded,
// which would tell a person reading the heartbeat that the run was in trouble
// when it was doing exactly what it should.
func (q *Queue) PrimaryWorkOutstanding() bool {
	for _, qt := range q.tasks {
		if qt.Task.Band == BandPrimary && qt.State != TaskSucceeded {
			return true
		}
	}
	return false
}

// Summary renders the queue for a progress report.
func (q *Queue) Summary() string {
	counts := q.Counts()
	states := []TaskState{TaskPending, TaskSucceeded, TaskFailed, TaskHeld}
	parts := make([]string, 0, len(states))
	for _, s := range states {
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", s, counts[s]))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}
