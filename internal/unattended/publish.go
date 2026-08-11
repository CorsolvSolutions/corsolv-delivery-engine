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
func PublishDelivery(spec Spec, q *Queue, fence *Fence, now time.Time) ([]byte, error) {
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
