package unattended

import (
	"encoding/json"
	"fmt"
	"time"
)

// HeartbeatName is the live progress file, inside the run's state directory.
const HeartbeatName = "heartbeat.json"

// CompletionName is the terminal record a notification layer watches for.
const CompletionName = "completion.json"

// Progress is what a run publishes about itself while it is still running.
//
// The fields are the ones a person actually asks when they look in on an
// unattended run at midnight: what is it doing, how long has it been doing it,
// is it stuck, what will it do next, and who owns the tree. It is published
// without anyone asking for it, because a run that only answers when questioned
// is not unattended.
type Progress struct {
	RunID     string `json:"runId"`
	ProjectID string `json:"projectId"`
	Session   string `json:"session"`

	Stage       string `json:"stage"`
	CurrentTask string `json:"currentTask,omitempty"`
	CurrentBand Band   `json:"currentBand,omitempty"`

	StartedAt time.Time `json:"startedAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Elapsed   string    `json:"elapsed"`

	LastMilestone string `json:"lastMilestone,omitempty"`
	ActiveBlocker string `json:"activeBlocker,omitempty"`
	// UsingFallback says the run is doing lower-band work because the primary
	// path is blocked. Without it, steady progress on documentation looks
	// identical to steady progress on the thing the run was for.
	UsingFallback bool   `json:"usingFallback"`
	NextAction    string `json:"nextAction,omitempty"`

	WriterOwner string `json:"writerOwner"`
	WriterPID   int    `json:"writerPid"`
	Worktree    string `json:"worktree"`
	Branch      string `json:"branch"`
	Head        string `json:"head"`

	Tasks    map[TaskState]int `json:"tasks"`
	Attempts int               `json:"attempts"`
	// Boundaries are the human actions this run already knows it cannot take.
	Boundaries []string `json:"boundaries,omitempty"`
}

// RunOutcome is how a run ended.
//
// The four values are the vocabulary a notification layer needs: they are the
// difference between "look at this now", "this is waiting for you", and
// "nothing to do".
type RunOutcome string

// The run outcomes.
const (
	// RunCompleted — every declared task succeeded.
	RunCompleted RunOutcome = "completed"
	// RunBlockedHuman — work remains, and only a person can unblock it.
	RunBlockedHuman RunOutcome = "blocked-human"
	// RunAwaitingAuth — the specific human action needed is an authentication.
	// It is separated from blocked-human because it is the one boundary a person
	// can usually clear in seconds.
	RunAwaitingAuth RunOutcome = "awaiting-auth"
	// RunFailed — the run stopped on something it could not work around.
	RunFailed RunOutcome = "failed"
)

// NeedsAttention reports whether a person should be told now.
func (o RunOutcome) NeedsAttention() bool { return o != RunCompleted }

// CompletionEvent is the terminal record of a run.
//
// It is written to a file rather than delivered anywhere, on purpose: the
// notification layer — a sound, a Windows toast, a chat message — is a separate
// concern with its own failure modes, and a run's evidence must not depend on
// one of them working. Emitting the event correctly is the run's obligation;
// noticing it is somebody else's.
type CompletionEvent struct {
	RunID     string `json:"runId"`
	ProjectID string `json:"projectId"`
	Session   string `json:"session"`
	// SessionLabel is the logical window name a notification layer announces.
	SessionLabel string `json:"sessionLabel"`

	Outcome RunOutcome `json:"outcome"`
	Reason  string     `json:"reason"`

	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
	Duration   string    `json:"duration"`

	Worktree string `json:"worktree"`
	Branch   string `json:"branch"`
	Head     string `json:"head"`

	Tasks    map[TaskState]int `json:"tasks"`
	Attempts int               `json:"attempts"`
	// HumanActions are exactly what a person must do next, in order.
	HumanActions []string `json:"humanActions,omitempty"`
	// Failures name the tasks that exhausted their attempts.
	Failures []string `json:"failures,omitempty"`
	// QA is the packet's progression decision: which gates its risk class made
	// mandatory, which have passing evidence for the revision in hand, and
	// which do not. It is published whether or not it permitted progression,
	// because a reader's first question about a completed run is what was
	// actually proved about it.
	QA ProgressionDecision `json:"qa"`
}

// WriteProgress publishes live progress atomically.
func WriteProgress(stateDir string, p Progress) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding progress: %w", err)
	}
	return writeFileAtomic(stateDirPath(stateDir, HeartbeatName), append(data, '\n'))
}

// ReadProgress reads the published progress of a run.
func ReadProgress(stateDir string) (Progress, bool, error) {
	var p Progress
	data, err := readFileIfPresent(stateDirPath(stateDir, HeartbeatName))
	if err != nil || data == nil {
		return p, false, err
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, false, fmt.Errorf("parsing progress: %w", err)
	}
	return p, true, nil
}

// WriteCompletion publishes the terminal record atomically.
func WriteCompletion(stateDir string, e CompletionEvent) error {
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding completion event: %w", err)
	}
	return writeFileAtomic(stateDirPath(stateDir, CompletionName), append(data, '\n'))
}

// ReadCompletion reads a run's terminal record.
func ReadCompletion(stateDir string) (CompletionEvent, bool, error) {
	var e CompletionEvent
	data, err := readFileIfPresent(stateDirPath(stateDir, CompletionName))
	if err != nil || data == nil {
		return e, false, err
	}
	if err := json.Unmarshal(data, &e); err != nil {
		return e, false, fmt.Errorf("parsing completion event: %w", err)
	}
	return e, true, nil
}

// String renders a completion event as the one line a person needs.
func (e CompletionEvent) String() string {
	return fmt.Sprintf("%s — %s in %s (%s): %s",
		e.SessionLabel, e.Outcome, e.Duration, summarizeCounts(e.Tasks), e.Reason)
}

func summarizeCounts(counts map[TaskState]int) string {
	out := ""
	for _, s := range []TaskState{TaskSucceeded, TaskFailed, TaskHeld, TaskPending} {
		if counts[s] > 0 {
			if out != "" {
				out += " "
			}
			out += fmt.Sprintf("%s=%d", s, counts[s])
		}
	}
	if out == "" {
		return "no tasks"
	}
	return out
}
