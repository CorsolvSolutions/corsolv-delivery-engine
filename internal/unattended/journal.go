package unattended

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// RecordKind is what a journal record records.
type RecordKind string

// The journal vocabulary. Every state change a resume needs to reconstruct has
// exactly one kind, so replay is a fold over records rather than an inference.
const (
	RecordRunStarted    RecordKind = "run-started"
	RecordPreflight     RecordKind = "preflight"
	RecordLockAcquired  RecordKind = "lock-acquired"
	RecordFenceTaken    RecordKind = "fence-taken"
	RecordFenceVerified RecordKind = "fence-verified"
	RecordFenceViolated RecordKind = "fence-violated"
	RecordFenceAdvanced RecordKind = "fence-advanced"
	RecordTaskStarted   RecordKind = "task-started"
	RecordTaskSucceeded RecordKind = "task-succeeded"
	RecordTaskFailed    RecordKind = "task-failed"
	RecordTaskHeld      RecordKind = "task-held"
	RecordTaskRetry     RecordKind = "task-retry-scheduled"
	RecordGateEvidence  RecordKind = "gate-evidence"
	RecordRunFinished   RecordKind = "run-finished"
)

// Record is one durable fact about a run.
//
// Records are append-only and never rewritten. A run that crashes mid-record
// loses that record and nothing else, which is the property that makes the
// journal safe to resume from: the last thing it says is the last thing that
// was durably true.
type Record struct {
	Seq   int        `json:"seq"`
	At    time.Time  `json:"at"`
	Kind  RecordKind `json:"kind"`
	RunID string     `json:"runId"`

	TaskID  string `json:"taskId,omitempty"`
	Attempt int    `json:"attempt,omitempty"`

	Class   FailureClass `json:"class,omitempty"`
	Outcome string       `json:"outcome,omitempty"`
	Detail  string       `json:"detail,omitempty"`

	DurationMS int64 `json:"durationMs,omitempty"`

	// Gate carries a QA gate's structured evidence. The journal is where it
	// lives because the journal is already the run's durable, append-only,
	// replayable record; giving gate evidence a second store would give the run
	// two accounts of itself that can disagree.
	Gate *GateEvidence `json:"gate,omitempty"`
}

// Journal is an append-only run log.
type Journal struct {
	path  string
	file  *os.File
	runID string
	seq   int
}

// JournalName is the file a run's journal lives in, inside the state directory.
const JournalName = "run-journal.jsonl"

// OpenJournal opens or creates a run journal for appending.
//
// An existing journal is continued rather than replaced: a resumed run must add
// to the record of what happened, not overwrite it. The sequence resumes from
// the highest number already durable.
func OpenJournal(path, runID string) (*Journal, error) {
	existing, _, err := ReadJournal(path)
	if err != nil {
		return nil, err
	}
	seq := 0
	for _, r := range existing {
		if r.Seq > seq {
			seq = r.Seq
		}
	}
	if err := osMkdirAll(dirOf(path)); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening run journal %q: %w", path, err)
	}
	return &Journal{path: path, file: f, runID: runID, seq: seq}, nil
}

// Append writes one record durably.
//
// It syncs before returning. Without the sync a run could report a task
// complete, crash, and resume believing the task had never started — which is
// how completion evidence gets lost and work gets repeated.
func (j *Journal) Append(r Record) (Record, error) {
	j.seq++
	r.Seq = j.seq
	if r.At.IsZero() {
		r.At = time.Now().UTC()
	}
	if r.RunID == "" {
		r.RunID = j.runID
	}
	line, err := json.Marshal(r)
	if err != nil {
		return r, fmt.Errorf("encoding journal record: %w", err)
	}
	if _, err := j.file.Write(append(line, '\n')); err != nil {
		return r, fmt.Errorf("writing journal record: %w", err)
	}
	if err := j.file.Sync(); err != nil {
		return r, fmt.Errorf("syncing journal record: %w", err)
	}
	return r, nil
}

// Close releases the journal file.
func (j *Journal) Close() error {
	if j == nil || j.file == nil {
		return nil
	}
	err := j.file.Close()
	j.file = nil
	if err != nil {
		return fmt.Errorf("closing run journal: %w", err)
	}
	return nil
}

// ErrJournalCorrupt is returned when a journal cannot be trusted as a record.
var ErrJournalCorrupt = errors.New("unattended: run journal is corrupt")

// ReadJournal reads every durable record.
//
// A malformed final line is dropped and reported, not an error: it is the
// signature of a crash between write and sync, and it means precisely "this
// record never became durable". A malformed line anywhere else is corruption —
// something rewrote history — and is refused outright, because a resume built on
// it would silently skip or repeat real work.
func ReadJournal(path string) (records []Record, truncatedTail bool, err error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("opening run journal %q: %w", path, err)
	}
	defer f.Close() //nolint:errcheck

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	type parsed struct {
		rec Record
		ok  bool
		raw string
	}
	var lines []parsed
	for scanner.Scan() {
		raw := scanner.Text()
		if strings.TrimSpace(raw) == "" {
			continue
		}
		var r Record
		if jerr := json.Unmarshal([]byte(raw), &r); jerr != nil {
			lines = append(lines, parsed{raw: raw})
			continue
		}
		lines = append(lines, parsed{rec: r, ok: true, raw: raw})
	}
	if serr := scanner.Err(); serr != nil {
		return nil, false, fmt.Errorf("reading run journal %q: %w", path, serr)
	}

	for i, l := range lines {
		if l.ok {
			records = append(records, l.rec)
			continue
		}
		if i == len(lines)-1 {
			truncatedTail = true
			continue
		}
		return nil, false, fmt.Errorf("%w: %q line %d is not a record", ErrJournalCorrupt, path, i+1)
	}
	return records, truncatedTail, nil
}

func dirOf(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i > 0 {
		return path[:i]
	}
	return "."
}

// ResumeState is what a journal says about work already done.
type ResumeState struct {
	// Succeeded names tasks whose success is durably recorded. They are never
	// repeated.
	Succeeded map[string]bool
	// Attempts counts durable attempts per task, so a resumed run continues the
	// retry budget rather than restarting it — the alternative is a crash loop
	// that retries forever, one crash at a time.
	Attempts map[string]int
	// Interrupted names tasks that started and never reached a terminal record.
	// They are re-offered, and the interruption is recorded rather than erased.
	Interrupted map[string]bool
	// Gates is the QA evidence ledger this run has accumulated, folded by
	// MergeEvidence. A resumed run inherits it, so work whose gate already ran
	// is not re-gated — and, more importantly, a gate that already failed
	// against the code in hand is not forgotten by restarting.
	Gates map[string]GateEvidence
	// Finished reports whether the previous run recorded its own end.
	Finished bool
	// LastSeq is the highest durable sequence.
	LastSeq int
}

// Replay folds one run's records into its resume state.
//
// It is a pure fold with no side effects, so replaying twice produces the same
// answer as replaying once — which is what makes resuming a resumed run safe.
//
// Records belonging to other runs are skipped, and that filter is not
// bookkeeping. A state directory belongs to a project, not to a run, so one
// journal accumulates every run that project has had. The endurance run found
// this the hard way: it started against the same state directory as the run
// before it, and three of its tasks shared IDs with tasks that run had already
// completed, so it began by skipping work it had never done. Resuming means
// continuing *this* run; another run's history is history.
//
// LastSeq is deliberately taken across every record, because the sequence is a
// property of the journal file rather than of any one run.
func Replay(records []Record, runID string) ResumeState {
	st := ResumeState{
		Succeeded:   map[string]bool{},
		Attempts:    map[string]int{},
		Interrupted: map[string]bool{},
		Gates:       map[string]GateEvidence{},
	}
	for _, r := range records {
		if r.Seq > st.LastSeq {
			st.LastSeq = r.Seq
		}
		if r.RunID != runID {
			continue
		}
		switch r.Kind {
		case RecordTaskStarted:
			st.Interrupted[r.TaskID] = true
			st.Attempts[r.TaskID]++
		case RecordTaskSucceeded:
			st.Succeeded[r.TaskID] = true
			delete(st.Interrupted, r.TaskID)
		case RecordTaskFailed, RecordTaskHeld:
			delete(st.Interrupted, r.TaskID)
		case RecordGateEvidence:
			if r.Gate == nil || r.Gate.GateID == "" {
				continue
			}
			st.Gates[r.Gate.GateID] = MergeEvidence(st.Gates[r.Gate.GateID], *r.Gate)
		case RecordRunFinished:
			st.Finished = true
		}
	}
	return st
}
