// Package projector turns Gas City's authoritative execution facts into a
// durable delivery projection the dashboard can read.
//
// ONE FACT, ONE AUTHORITY. This package OWNS nothing. Execution facts come from
// Gas City (the event bus for starts, the bead store for terminal records);
// the document it writes is a PROJECTION. It is deliberately not a second
// execution-state database, and a value no authority can supply is absent
// rather than invented.
//
// WHAT THIS DELIBERATELY DOES NOT CARRY. Live GitHub state — PR state, CI
// state, CI tested SHA, merge state, merge SHA — is read by the dashboard
// directly from GitHub. Snapshotting it here would create a second structured
// truth that goes stale the moment CI moves, so only the two stable identifiers
// the consumer's schema actually defines are emitted: pullRequest and
// implementationSha.
//
// DEPENDENCY READINESS IS PROJECTED, NOT EVENTED. Gas City computes readiness
// as a query-time projection over dependency edges and terminal state. There is
// no durable "dependency unblocked" event and none is invented here.
//
// THE DURABILITY ORDER IS THE WHOLE DESIGN. State is written and fsynced first;
// only then is the cursor advanced. A crash between the two replays events that
// were already applied, which Apply absorbs. The opposite order would skip them,
// which is not recoverable — a skipped event is a fact the projection never
// learns. Replay is cheap; a hole is permanent.
package projector

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ErrUnknownStatus is returned when a task carries a status outside the
// canonical vocabulary.
//
// An error rather than a default, because the consumer does not remap an
// unrecognized status — it holds the task at 0% and reports it. Emitting a
// near-miss is therefore worse than emitting nothing, so this refuses to render
// one at all.
var ErrUnknownStatus = errors.New("projector: status outside the canonical vocabulary")

// Task is one projected unit of delivery work, in the consumer's field
// vocabulary.
type Task struct {
	TaskID        string
	Title         string
	Phase         string
	TaskType      string // code | database | security | documentation
	Status        TaskStatus
	Priority      string
	OwnerType     string // human | agent
	Branch        string
	PullRequest   int
	Dependencies  []string
	ParallelGroup string
	CriticalPath  bool

	ActualStart  time.Time
	ActualFinish time.Time

	ImplementationSha string

	CompletionGate       string
	CompletionGateStatus CompletionGateStatus

	// Evidence is the consumer's flat string list. Structured values are
	// rendered as "key=value" entries because that is the shape it reads;
	// a nested object here would be dropped on the floor.
	Evidence []string

	Blocker            string
	NextPhysicalAction string
	Attempts           []Attempt
	LastReviewed       time.Time

	// lastStartSeq is the event sequence that last opened an attempt. It makes
	// the attempt history idempotent under replay: a start whose sequence has
	// already been recorded cannot record again.
	lastStartSeq uint64
}

// State is the whole projection.
type State struct {
	Project           ProjectMeta
	Tasks             map[string]*Task
	CurrentBlockers   []Blocker
	CompletedOutcomes []CompletedOutcome
}

// NewState returns an empty projection.
func NewState(projectID string) *State {
	return &State{
		Project: ProjectMeta{ProjectID: projectID},
		Tasks:   map[string]*Task{},
	}
}

func isTerminalStatus(s TaskStatus) bool {
	switch s {
	case StatusMerged, StatusDeployedUAT, StatusAppliedUAT, StatusVerified, StatusComplete:
		return true
	}
	return false
}

// blockersOf returns dependencies that are not yet terminal.
func (s *State) blockersOf(t *Task) []string {
	var out []string
	for _, dep := range t.Dependencies {
		d, ok := s.Tasks[dep]
		if !ok || !isTerminalStatus(d.Status) {
			out = append(out, dep)
		}
	}
	sort.Strings(out)
	return out
}

// RecomputeBlockers refreshes each task's derived blocker and demotes a planned
// task with outstanding dependencies to blocked.
//
// The consumer's per-task blocker is a single string or null, not a list, so
// outstanding dependencies are summarized into one sentence. The authoritative
// list still travels in currentBlockers when a real blocker record exists.
func (s *State) RecomputeBlockers() {
	for _, t := range s.Tasks {
		outstanding := s.blockersOf(t)
		if len(outstanding) > 0 {
			t.Blocker = "waiting on " + strings.Join(outstanding, ", ")
			if t.Status == StatusPlanned {
				t.Status = StatusBlocked
			}
			continue
		}
		// Only clear a blocker this projection derived. A blocker set from a
		// real authority is not this function's to remove.
		if strings.HasPrefix(t.Blocker, "waiting on ") {
			t.Blocker = ""
		}
		if t.Status == StatusBlocked {
			t.Status = StatusPlanned
		}
	}
}

// Event is the projector's view of a Gas City event envelope. Seq is the
// ordering and cursor authority.
type Event struct {
	Seq     uint64          `json:"seq"`
	Type    string          `json:"type"`
	Ts      time.Time       `json:"ts"`
	Subject string          `json:"subject,omitempty"`
	Actor   string          `json:"actor,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Apply folds one event into the state. It MUST be idempotent: replay after a
// crash is normal operation, not an error path.
//
// Idempotence is structural. actualStart is first-write-wins so a replayed
// start cannot move a task's beginning, and each attempt records the sequence
// that opened it so a replayed start cannot append a duplicate attempt.
func (s *State) Apply(ev Event) {
	if ev.Subject == "" {
		return
	}
	t, ok := s.Tasks[ev.Subject]
	if !ok {
		return
	}
	switch ev.Type {
	case "work.started":
		if t.ActualStart.IsZero() {
			t.ActualStart = ev.Ts
		}
		if t.Status == StatusPlanned || t.Status == StatusBlocked {
			t.Status = StatusActive
		}
		if ev.Seq > t.lastStartSeq {
			t.Attempts = append(t.Attempts, Attempt{
				Date:    ev.Ts,
				Outcome: "failed", // provisional until a terminal fact says otherwise
				Summary: "execution attempt observed on the event bus",
			})
			t.lastStartSeq = ev.Seq
		}
	case "work.finished":
		if t.ActualFinish.IsZero() {
			t.ActualFinish = ev.Ts
		}
		s.markLastAttemptSucceeded(t)
	case "work.failed":
		if t.ActualFinish.IsZero() {
			t.ActualFinish = ev.Ts
		}
	}
}

// markLastAttemptSucceeded records that the most recent attempt is the one that
// finished. Earlier attempts stay "failed", which is what makes a correction
// history plottable rather than a bare count.
func (s *State) markLastAttemptSucceeded(t *Task) {
	if len(t.Attempts) == 0 {
		return
	}
	t.Attempts[len(t.Attempts)-1].Outcome = "succeeded"
}

// SetTerminalFinish joins the bead store's terminal record onto a task.
//
// This is the authoritative finish source, and it exists because the city event
// log does not carry work-bead closures — those live in the rig's bead store,
// whose closed_at IS the terminal fact. Without this join every completed task
// projects actualFinish: null, which the dashboard renders as a Gantt bar with
// no end and work that looks unfinished.
//
// Deliberately NOT sourced from a report timestamp, a file mtime, a PR merge
// time or a CI finish. Those are adjacent facts about different things.
func (s *State) SetTerminalFinish(taskID string, closedAt time.Time) {
	t, ok := s.Tasks[taskID]
	if !ok || closedAt.IsZero() {
		return
	}
	t.ActualFinish = closedAt
	s.markLastAttemptSucceeded(t)
}

// Cursor is the durable projection position.
type Cursor struct {
	Seq       uint64    `json:"seq"`
	UpdatedAt time.Time `json:"updated_at"`
}

// LoadCursor reads a cursor. A missing cursor is seq 0 — a cold projection, not
// an error.
func LoadCursor(path string) (Cursor, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Cursor{}, nil
	}
	if err != nil {
		return Cursor{}, fmt.Errorf("reading cursor %s: %w", path, err)
	}
	var c Cursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return Cursor{}, fmt.Errorf("parsing cursor %s: %w", path, err)
	}
	return c, nil
}

// SaveCursor persists the cursor atomically. Runtime state under the city's own
// .gc/ tree — never a status file, never AGENTS.md.
func SaveCursor(path string, c Cursor) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("encoding cursor: %w", err)
	}
	return writeFileAtomic(path, append(raw, '\n'))
}

// writeFileAtomic writes temp -> fsync -> rename, so a reader sees either the
// previous content or the whole new content, never a partial write.
func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("creating temp for %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck // best-effort on the success path
	if _, err := tmp.Write(data); err != nil {
		tmp.Close() //nolint:errcheck
		return fmt.Errorf("writing %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close() //nolint:errcheck
		return fmt.Errorf("fsyncing %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("renaming %s -> %s: %w", tmpName, path, err)
	}
	return nil
}

// Project folds every event after the cursor into state, writes the projection,
// and only then advances the cursor.
//
// beforeCursor is a test seam for exactly the crash that ordering guards: it
// runs after the state is durable and before the cursor moves.
func Project(events []Event, state *State, statePath, cursorPath string, beforeCursor func()) (Cursor, error) {
	cur, err := LoadCursor(cursorPath)
	if err != nil {
		return Cursor{}, err
	}
	applied := cur.Seq
	for _, ev := range events {
		if ev.Seq <= cur.Seq {
			continue // already durable in a previous projection
		}
		state.Apply(ev)
		if ev.Seq > applied {
			applied = ev.Seq
		}
	}
	state.RecomputeBlockers()

	rendered, err := Render(state)
	if err != nil {
		return Cursor{}, err
	}
	if err := writeFileAtomic(statePath, rendered); err != nil {
		return Cursor{}, err
	}
	if beforeCursor != nil {
		beforeCursor()
	}
	next := Cursor{Seq: applied, UpdatedAt: state.Project.LastUpdateTimestamp}
	if err := SaveCursor(cursorPath, next); err != nil {
		return Cursor{}, err
	}
	return next, nil
}

// Render emits the projection in the consumer's document shape.
//
// Hand-emitted in fixed field order with sorted task keys, so the same
// authoritative state is byte-identical every run. Determinism is a
// requirement: the file is committed, and a projection that reordered its own
// keys would make real change indistinguishable from churn.
func Render(s *State) ([]byte, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "# GENERATED BY THE DELIVERY ENGINE PROJECTOR - DO NOT EDIT.\n")
	fmt.Fprintf(&b, "# Execution facts: Gas City event bus (starts) and bead store (terminal records).\n")
	fmt.Fprintf(&b, "# Live PR/CI/merge state is read by the dashboard from GitHub, not from this file.\n")
	fmt.Fprintf(&b, "# Hand edits are overwritten and are not a source of truth.\n")
	fmt.Fprintf(&b, "schemaVersion: %d\n", SchemaVersion)

	p := s.Project
	fmt.Fprintf(&b, "project:\n")
	fmt.Fprintf(&b, "  projectId: %s\n", yamlScalar(p.ProjectID))
	fmt.Fprintf(&b, "  strategy: %s\n", yamlScalar(p.Strategy))
	fmt.Fprintf(&b, "  authoritativeRef: %s\n", yamlOpt(p.AuthoritativeRef))
	fmt.Fprintf(&b, "  currentPhase: %s\n", yamlScalar(p.CurrentPhase))
	fmt.Fprintf(&b, "  currentMilestone: %s\n", yamlScalar(p.CurrentMilestone))
	fmt.Fprintf(&b, "  overallRag: %s\n", yamlScalar(p.OverallRag))
	fmt.Fprintf(&b, "  overallRagReason: %s\n", yamlScalar(p.OverallRagReason))
	fmt.Fprintf(&b, "  lastUpdateTimestamp: %s\n", yamlOptTime(p.LastUpdateTimestamp))
	fmt.Fprintf(&b, "  latestAcceptedMainSha: %s\n", yamlOpt(p.LatestAcceptedMainSha))

	ids := make([]string, 0, len(s.Tasks))
	for id := range s.Tasks {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	fmt.Fprintf(&b, "activeTasks:\n")
	for _, id := range ids {
		t := s.Tasks[id]
		if err := ValidateTaskStatus(t.Status); err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "  - taskId: %s\n", yamlScalar(t.TaskID))
		fmt.Fprintf(&b, "    title: %s\n", yamlScalar(t.Title))
		fmt.Fprintf(&b, "    phase: %s\n", yamlScalar(t.Phase))
		fmt.Fprintf(&b, "    taskType: %s\n", yamlScalar(t.TaskType))
		fmt.Fprintf(&b, "    status: %s\n", yamlScalar(string(t.Status)))
		fmt.Fprintf(&b, "    priority: %s\n", yamlScalar(t.Priority))
		fmt.Fprintf(&b, "    ownerType: %s\n", yamlScalar(t.OwnerType))
		fmt.Fprintf(&b, "    branch: %s\n", yamlOpt(t.Branch))
		fmt.Fprintf(&b, "    pullRequest: %s\n", yamlOptInt(t.PullRequest))
		writeList(&b, "    ", "dependencies", t.Dependencies)
		fmt.Fprintf(&b, "    parallelGroup: %s\n", yamlOpt(t.ParallelGroup))
		fmt.Fprintf(&b, "    criticalPath: %t\n", t.CriticalPath)
		fmt.Fprintf(&b, "    actualStart: %s\n", yamlOptTime(t.ActualStart))
		fmt.Fprintf(&b, "    actualFinish: %s\n", yamlOptTime(t.ActualFinish))
		fmt.Fprintf(&b, "    implementationSha: %s\n", yamlOpt(t.ImplementationSha))
		fmt.Fprintf(&b, "    completionGate: %s\n", yamlOpt(t.CompletionGate))
		fmt.Fprintf(&b, "    completionGateStatus: %s\n", yamlScalar(string(t.CompletionGateStatus)))
		writeList(&b, "    ", "evidence", t.Evidence)
		fmt.Fprintf(&b, "    blocker: %s\n", yamlOpt(t.Blocker))
		fmt.Fprintf(&b, "    nextPhysicalAction: %s\n", yamlOpt(t.NextPhysicalAction))
		fmt.Fprintf(&b, "    lastReviewed: %s\n", yamlOptTime(t.LastReviewed))
		if len(t.Attempts) == 0 {
			fmt.Fprintf(&b, "    attempts: []\n")
			continue
		}
		fmt.Fprintf(&b, "    attempts:\n")
		for _, a := range t.Attempts {
			fmt.Fprintf(&b, "      - date: %s\n", yamlOptTime(a.Date))
			fmt.Fprintf(&b, "        outcome: %s\n", yamlScalar(a.Outcome))
			fmt.Fprintf(&b, "        summary: %s\n", yamlScalar(a.Summary))
			fmt.Fprintf(&b, "        evidence: %s\n", yamlOpt(a.Evidence))
		}
	}

	if len(s.CurrentBlockers) == 0 {
		fmt.Fprintf(&b, "currentBlockers: []\n")
	} else {
		fmt.Fprintf(&b, "currentBlockers:\n")
		for _, bl := range s.CurrentBlockers {
			fmt.Fprintf(&b, "  - blockerId: %s\n", yamlScalar(bl.BlockerID))
			fmt.Fprintf(&b, "    summary: %s\n", yamlScalar(bl.Summary))
			fmt.Fprintf(&b, "    humanBoundary: %t\n", bl.HumanBoundary)
			fmt.Fprintf(&b, "    evidence: %s\n", yamlOpt(bl.Evidence))
		}
	}
	fmt.Fprintf(&b, "deferredWork: []\n")

	if len(s.CompletedOutcomes) == 0 {
		fmt.Fprintf(&b, "recentCompletedOutcomes: []\n")
	} else {
		fmt.Fprintf(&b, "recentCompletedOutcomes:\n")
		for _, o := range s.CompletedOutcomes {
			fmt.Fprintf(&b, "  - date: %s\n", yamlOptTime(o.Date))
			fmt.Fprintf(&b, "    summary: %s\n", yamlScalar(o.Summary))
			fmt.Fprintf(&b, "    evidence: %s\n", yamlOpt(o.Evidence))
		}
	}
	return []byte(b.String()), nil
}

// writeList emits a sorted YAML sequence, or an explicit empty list. Sorting is
// part of the determinism contract.
func writeList(b *strings.Builder, indent, key string, vals []string) {
	if len(vals) == 0 {
		fmt.Fprintf(b, "%s%s: []\n", indent, key)
		return
	}
	fmt.Fprintf(b, "%s%s:\n", indent, key)
	sorted := append([]string(nil), vals...)
	sort.Strings(sorted)
	for _, v := range sorted {
		fmt.Fprintf(b, "%s  - %s\n", indent, yamlScalar(v))
	}
}

func yamlScalar(v string) string {
	if v == "" {
		return `""`
	}
	return `"` + strings.ReplaceAll(v, `"`, `\"`) + `"`
}
