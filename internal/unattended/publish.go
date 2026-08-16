package unattended

import (
	"fmt"
	"time"

	"github.com/gastownhall/gascity/internal/projector"
)

// PublishDelivery renders the run's declared delivery milestones into the
// dashboard's projection and writes it to the spec's publish path.
//
// It produces the *existing* consumer schema through the *existing* projector
// rather than a second document with its own rules. The dashboard is the
// accepted control plane; teaching it a second way to be right would create two
// authorities that can disagree, and the one that disagrees is always the newer
// one.
//
// Only tasks that declare a delivery status appear. Most of a run is internal
// machinery — a lint pass, an evidence sweep — and projecting every green
// command as delivery progress would overstate what actually shipped.
//
// The progression decision is a parameter rather than something derived here,
// and it is not optional. A task's commands exiting zero proves its commands
// exited zero; it does not prove the packet may progress, and the projection is
// the document a reader treats as though it did. See applyProgressionCeiling.
func PublishDelivery(spec Spec, q *Queue, fence *Fence, qa ProgressionDecision, now time.Time) ([]byte, error) {
	state := projector.NewState(spec.ProjectID)
	state.Project.LastUpdateTimestamp = now.UTC()
	state.Project.AuthoritativeRef = spec.Ownership.ExpectedOrigin
	if fence != nil {
		state.Project.LatestAcceptedMainSha = fence.Head
	}

	for _, qt := range q.Tasks() {
		if qt.Task.DeliveryStatus == "" {
			continue
		}
		status := projector.TaskStatus(qt.Task.DeliveryStatus)
		if err := projector.ValidateTaskStatus(status); err != nil {
			return nil, fmt.Errorf("task %q: %w", qt.Task.ID, err)
		}
		state.Tasks[qt.Task.ID] = projectTask(qt, status, fence)
	}

	for _, qt := range q.Tasks() {
		if qt.State != TaskHeld || qt.HeldReason == "" {
			continue
		}
		state.CurrentBlockers = append(state.CurrentBlockers, projector.Blocker{
			BlockerID: qt.Task.ID,
			Summary:   qt.HeldReason,
			// A held task is held because something outside the run must change.
			// That is what a human boundary is, and the dashboard scores it very
			// differently from a task that merely failed.
			HumanBoundary: true,
			Evidence:      stateDirPath(spec.StateDir, JournalName),
		})
	}

	state.RecomputeBlockers()
	// The QA decision has the last word, after dependency recomputation rather
	// than before it: RecomputeBlockers promotes and demotes statuses of its
	// own accord, and a ceiling applied first would be silently lifted again.
	applyProgressionCeiling(state, qa)

	data, err := projector.Render(state)
	if err != nil {
		return nil, fmt.Errorf("rendering delivery projection: %w", err)
	}
	if spec.PublishPath != "" {
		if err := writeFileAtomic(spec.PublishPath, data); err != nil {
			return nil, err
		}
	}
	return data, nil
}

// terminalDeliveryStatuses are the statuses the consumer reads as "this is
// done". They are the ones the progression ceiling refuses.
//
// It duplicates the consumer's own isTerminalStatus deliberately: that function
// is unexported and belongs to the projector's blocker arithmetic, and taking
// a dependency on it would couple a QA refusal to a rule that exists for a
// different purpose and may reasonably change.
var terminalDeliveryStatuses = map[projector.TaskStatus]bool{
	projector.StatusMerged:      true,
	projector.StatusDeployedUAT: true,
	projector.StatusAppliedUAT:  true,
	projector.StatusVerified:    true,
	projector.StatusComplete:    true,
}

// applyProgressionCeiling refuses to project completion the packet's mandatory
// gates do not license.
//
// THE DEFECT IT EXISTS FOR. A task's declared delivery status was projected the
// moment its command exited zero, and its completion gate went to met with it —
// so a packet whose mandatory QA gate had failed, or had never run, or had run
// against different code, still rendered `status: complete` and
// `completionGateStatus: met` in the document the dashboard scores 100% from.
// The run's own completion event refused that packet; the projection beside it
// said it had shipped. Two accounts of the same run, and the reassuring one was
// the one a person reads.
//
// The ceiling is a ceiling and not a rewrite: it can only ever lower a claim.
// A permitted packet is left exactly as the run described it.
func applyProgressionCeiling(state *projector.State, qa ProgressionDecision) {
	if qa.Allowed {
		return
	}
	reason := "the packet may not progress: " + qa.Reason()
	for _, t := range state.Tasks {
		// No task claims a met completion gate while the packet is refused.
		// The gate the consumer reserves 100% for is a claim about the change
		// as a whole, and this run has not earned it.
		if t.CompletionGateStatus == projector.GateMet {
			t.CompletionGateStatus = projector.GateNotMet
		}
		if !terminalDeliveryStatuses[t.Status] {
			continue
		}
		t.Status = projector.StatusBlocked
		t.Blocker = reason
		if t.NextPhysicalAction == "" {
			t.NextPhysicalAction = reason
		}
	}
}

// projectTask maps one queued task onto the consumer's task shape.
//
// The declared status is what the task's success would establish. A task that
// has not succeeded does not get it: reaching for the declared status before the
// work is done is precisely the false-pass this whole layer exists to prevent.
func projectTask(qt *QueuedTask, declared projector.TaskStatus, fence *Fence) *projector.Task {
	t := &projector.Task{
		TaskID:         qt.Task.ID,
		Title:          qt.Task.Title,
		Phase:          qt.Task.Phase,
		TaskType:       "code",
		OwnerType:      "agent",
		Dependencies:   qt.Task.Needs,
		CompletionGate: qt.Task.CompletionGate,
	}
	if fence != nil {
		t.Branch = fence.Branch
		t.ImplementationSha = fence.Head
	}

	switch qt.State {
	case TaskSucceeded:
		t.Status = declared
		t.CompletionGateStatus = projector.GateMet
	case TaskHeld:
		t.Status = projector.StatusAwaitingHuman
		t.CompletionGateStatus = projector.GateNotMet
		t.Blocker = qt.HeldReason
		t.NextPhysicalAction = qt.HeldReason
	case TaskFailed:
		t.Status = projector.StatusBlocked
		t.CompletionGateStatus = projector.GateNotMet
		if n := len(qt.Attempts); n > 0 {
			t.Blocker = qt.Attempts[n-1].Reason
		}
	default:
		t.Status = projector.StatusPlanned
		t.CompletionGateStatus = projector.GateNotMet
	}
	// A task with no declared completion gate has nothing to have met, and
	// "met" against no gate would score 100% for an unexamined task.
	if qt.Task.CompletionGate == "" {
		t.CompletionGateStatus = projector.GateNotMet
	}

	for _, a := range qt.Attempts {
		outcome := "failed"
		if a.Succeeded {
			outcome = "succeeded"
		}
		summary := a.Reason
		if summary == "" {
			summary = fmt.Sprintf("attempt %d %s in %s", a.Number, outcome, a.Duration)
		}
		// Every attempt carries the timestamp of the execution that produced
		// it. An attempt with no date cannot be placed on the dashboard's
		// timeline, and inventing one would put a real failure on a fictional
		// day.
		t.Attempts = append(t.Attempts, projector.Attempt{
			Date: a.StartedAt.UTC(), Outcome: outcome, Summary: summary,
		})
		if t.ActualStart.IsZero() && !a.StartedAt.IsZero() {
			t.ActualStart = a.StartedAt.UTC()
		}
	}
	if qt.State == TaskSucceeded && len(qt.Attempts) > 0 {
		last := qt.Attempts[len(qt.Attempts)-1]
		if d, err := time.ParseDuration(last.Duration); err == nil && !last.StartedAt.IsZero() {
			t.ActualFinish = last.StartedAt.Add(d).UTC()
		}
	}
	return t
}
