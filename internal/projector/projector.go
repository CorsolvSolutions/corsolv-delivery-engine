// Package projector turns Gas City's authoritative execution facts into a
// durable delivery projection.
//
// ONE FACT, ONE AUTHORITY. This package OWNS nothing. Execution facts come from
// Gas City (beads and the event bus); PR/CI/merge facts come from GitHub
// reconciliation; the file this writes is a generated PROJECTION of both. It is
// deliberately not a second execution-state database, and nothing may hand-edit
// its output: a value that cannot be derived from an authority is absent rather
// than invented.
//
// DEPENDENCY READINESS IS PROJECTED, NOT EVENTED. Gas City computes readiness as
// a query-time projection over dependency relationships and terminal state.
// There is no durable "dependency unblocked" event and this package does not
// invent one — it derives blocked/ready from the dependency edges and the
// terminal state of what they point at, which is the same derivation the engine
// itself makes.
//
// THE DURABILITY ORDER IS THE WHOLE DESIGN. State is written and fsynced first;
// only then is the cursor advanced. A crash between the two replays events that
// were already applied, which is safe because Apply is idempotent. The opposite
// order would skip them, which is not recoverable — a skipped event is a fact
// the projection never learns. Replay is cheap; a hole is permanent.
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

// Status is the closed delivery-status vocabulary the dashboard consumes.
//
// Spelling is load-bearing and near-misses are the known failure mode, so the
// tokens are constants rather than free strings: "pr-open" (never
// "pull-request-open" or "pr-open-scoring") and "deployed-uat" (never
// "deployed-to-uat").
type Status string

// The closed vocabulary. Every projected task carries exactly one of these.
const (
	StatusNotStarted  Status = "not-started"
	StatusInProgress  Status = "in-progress"
	StatusBlocked     Status = "blocked"
	StatusPROpen      Status = "pr-open"
	StatusInCI        Status = "in-ci"
	StatusMerged      Status = "merged"
	StatusDeployedUAT Status = "deployed-uat"
	StatusDone        Status = "done"
	StatusFailed      Status = "failed"
)

var knownStatuses = map[Status]bool{
	StatusNotStarted: true, StatusInProgress: true, StatusBlocked: true,
	StatusPROpen: true, StatusInCI: true, StatusMerged: true,
	StatusDeployedUAT: true, StatusDone: true, StatusFailed: true,
}

// ErrUnknownStatus is returned when a projected task carries a status outside
// the closed vocabulary.
//
// It is an ERROR rather than a default, because the failure it guards is
// silent: an unrecognized token that fell through to a zero value would render
// as 0% progress and read as "this work has not started" instead of "the
// producer and consumer disagree about the vocabulary". A loud refusal is the
// only honest handling.
var ErrUnknownStatus = errors.New("projector: status outside the closed vocabulary")

// ValidateStatus rejects any token outside the closed vocabulary.
func ValidateStatus(s Status) error {
	if !knownStatuses[s] {
		return fmt.Errorf("%w: %q", ErrUnknownStatus, string(s))
	}
	return nil
}

// Evidence is a structured pointer to where a fact came from. Owner and
// worktree travel here rather than as bare strings so the projection records
// WHICH session and WHICH directory produced a result, not merely that one did.
type Evidence struct {
	AgentSession string // gc.session_name / gc.session_id
	WorktreePath string // gc.work_dir, the canonical mirrored stamp
	SourceCommit string // the commit the controller published
	Ref          string // free-form pointer to a durable artifact (run id, path)
}

// GitHubFacts are reconciliation facts. They are NOT execution facts and are
// never derived from bead state: a PR number the engine "expects" is not a PR.
type GitHubFacts struct {
	PRNumber    int
	PRState     string
	PRHeadSHA   string
	CIState     string
	CITestedSHA string
	MergeState  string
	MergeSHA    string
}

// Task is one projected unit of delivery work.
type Task struct {
	ID           string
	Title        string
	Workstream   string
	Milestone    string
	Phase        string
	Status       Status
	DependsOn    []string
	Blockers     []string
	ActualStart  time.Time
	ActualFinish time.Time
	Attempts     int
	Evidence     Evidence
	GitHub       GitHubFacts

	// lastStartSeq is the event sequence that last incremented Attempts. It is
	// what makes the attempt count idempotent under replay: a start event whose
	// sequence has already been counted cannot count again.
	lastStartSeq uint64
}

// DurationSeconds is the actual elapsed time, or 0 when the task has not both
// started and finished. Derived, never stored: a duration that disagrees with
// its own start and finish is a second source of truth.
func (t Task) DurationSeconds() int64 {
	if t.ActualStart.IsZero() || t.ActualFinish.IsZero() {
		return 0
	}
	d := t.ActualFinish.Sub(t.ActualStart)
	if d < 0 {
		return 0
	}
	return int64(d.Seconds())
}

// State is the whole projection.
type State struct {
	Project   string
	Generated time.Time
	Tasks     map[string]*Task
}

// NewState returns an empty projection for a project.
func NewState(project string) *State {
	return &State{Project: project, Tasks: map[string]*Task{}}
}

// NextAuthorisedTask is the lowest-ID task that is ready to start: not
// terminal, not blocked, and with every dependency terminal.
//
// Derived at read time from dependency edges and terminal state — the same
// derivation the engine makes — rather than from a durable "unblocked" signal,
// because no such signal exists and inventing one would create a second
// authority for readiness.
func (s *State) NextAuthorisedTask() string {
	ids := make([]string, 0, len(s.Tasks))
	for id := range s.Tasks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		t := s.Tasks[id]
		if isTerminal(t.Status) || t.Status == StatusInProgress {
			continue
		}
		if len(s.blockersOf(t)) == 0 {
			return id
		}
	}
	return ""
}

func isTerminal(s Status) bool {
	return s == StatusMerged || s == StatusDone || s == StatusDeployedUAT || s == StatusFailed
}

// blockersOf returns the dependencies that are not yet terminal.
func (s *State) blockersOf(t *Task) []string {
	var out []string
	for _, dep := range t.DependsOn {
		d, ok := s.Tasks[dep]
		if !ok || !isTerminal(d.Status) {
			out = append(out, dep)
		}
	}
	sort.Strings(out)
	return out
}

// RecomputeBlockers refreshes every task's derived blocker list and demotes a
// not-started task with outstanding dependencies to blocked.
func (s *State) RecomputeBlockers() {
	for _, t := range s.Tasks {
		t.Blockers = s.blockersOf(t)
		if len(t.Blockers) > 0 && t.Status == StatusNotStarted {
			t.Status = StatusBlocked
		}
		if len(t.Blockers) == 0 && t.Status == StatusBlocked {
			t.Status = StatusNotStarted
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
// Idempotence here is structural rather than incidental. Start and finish
// timestamps are first-write-wins, so a replayed start cannot move a task's
// beginning; attempt counts key on the event sequence that produced them, so a
// replayed attempt cannot inflate the count.
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
		if t.Status == StatusNotStarted || t.Status == StatusBlocked {
			t.Status = StatusInProgress
		}
		// Attempts count distinct start events, keyed by the sequence that
		// produced them, so a replay of an already-counted start is a no-op.
		if ev.Seq > t.lastStartSeq {
			t.Attempts++
			t.lastStartSeq = ev.Seq
		}
	case "work.finished":
		if t.ActualFinish.IsZero() {
			t.ActualFinish = ev.Ts
		}
		if !isTerminal(t.Status) {
			t.Status = StatusDone
		}
	case "work.failed":
		if t.ActualFinish.IsZero() {
			t.ActualFinish = ev.Ts
		}
		t.Status = StatusFailed
	}
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

// SaveCursor persists the cursor atomically.
//
// Never a status file and never AGENTS.md: this is runtime state under the
// city's own .gc/ tree, written temp -> fsync -> rename like every other
// durable artifact in this repository.
func SaveCursor(path string, c Cursor) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("encoding cursor: %w", err)
	}
	return writeFileAtomic(path, append(raw, '\n'))
}

// writeFileAtomic writes via temp file, fsync, rename — so a reader sees either
// the previous content or the whole new content, never a partial write.
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
// The write order is the durability contract, and it is deliberately the
// "wasteful" one: state first, cursor second. A crash between them replays
// already-applied events, which Apply absorbs. The reverse order would advance
// past events whose effects were never persisted, and nothing downstream could
// ever detect the hole.
//
// beforeCursor is a test seam for exactly that crash: it runs after the state
// is durable and before the cursor moves.
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
	next := Cursor{Seq: applied, UpdatedAt: state.Generated}
	if err := SaveCursor(cursorPath, next); err != nil {
		return Cursor{}, err
	}
	return next, nil
}

// Render emits the projection as YAML.
//
// Hand-emitted in a fixed field order with sorted task keys, so the same
// authoritative state always produces byte-identical output. Determinism is a
// requirement rather than a nicety: the file is committed, and a projection
// that reordered its own keys would produce a diff on every run and make a real
// change indistinguishable from churn.
func Render(s *State) ([]byte, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "# GENERATED BY THE DELIVERY ENGINE PROJECTOR - DO NOT EDIT.\n")
	fmt.Fprintf(&b, "# Execution facts: Gas City beads and event bus.\n")
	fmt.Fprintf(&b, "# PR/CI/merge facts: GitHub reconciliation.\n")
	fmt.Fprintf(&b, "# Hand edits are overwritten and are not a source of truth.\n")
	fmt.Fprintf(&b, "project: %s\n", yamlScalar(s.Project))
	fmt.Fprintf(&b, "generated: %s\n", s.Generated.UTC().Format(time.RFC3339))

	ids := make([]string, 0, len(s.Tasks))
	for id := range s.Tasks {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	fmt.Fprintf(&b, "next_authorized_task: %s\n", yamlScalar(s.NextAuthorisedTask()))
	fmt.Fprintf(&b, "tasks:\n")
	for _, id := range ids {
		t := s.Tasks[id]
		if err := ValidateStatus(t.Status); err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "  - id: %s\n", yamlScalar(t.ID))
		fmt.Fprintf(&b, "    title: %s\n", yamlScalar(t.Title))
		fmt.Fprintf(&b, "    phase: %s\n", yamlScalar(t.Phase))
		fmt.Fprintf(&b, "    milestone: %s\n", yamlScalar(t.Milestone))
		fmt.Fprintf(&b, "    workstream: %s\n", yamlScalar(t.Workstream))
		fmt.Fprintf(&b, "    status: %s\n", yamlScalar(string(t.Status)))
		writeList(&b, "    ", "depends_on", t.DependsOn)
		writeList(&b, "    ", "blockers", t.Blockers)
		fmt.Fprintf(&b, "    actual_start: %s\n", yamlTime(t.ActualStart))
		fmt.Fprintf(&b, "    actual_finish: %s\n", yamlTime(t.ActualFinish))
		fmt.Fprintf(&b, "    actual_duration_seconds: %d\n", t.DurationSeconds())
		fmt.Fprintf(&b, "    attempts: %d\n", t.Attempts)
		fmt.Fprintf(&b, "    evidence:\n")
		// Evidence and GitHub values are AUTHORITATIVE FACTS: something was
		// supposed to supply each one. They render through yamlFact so an
		// absent one says "unknown" rather than going blank, because a blank
		// ci_state is indistinguishable from "nothing to check" and a consumer
		// may reasonably render it as success-shaped nothing.
		fmt.Fprintf(&b, "      agent_session: %s\n", yamlFact(t.Evidence.AgentSession))
		fmt.Fprintf(&b, "      worktree_path: %s\n", yamlFact(t.Evidence.WorktreePath))
		fmt.Fprintf(&b, "      source_commit: %s\n", yamlFact(t.Evidence.SourceCommit))
		fmt.Fprintf(&b, "      ref: %s\n", yamlFact(t.Evidence.Ref))
		fmt.Fprintf(&b, "    github:\n")
		fmt.Fprintf(&b, "      pr_number: %s\n", yamlFactInt(t.GitHub.PRNumber))
		fmt.Fprintf(&b, "      pr_state: %s\n", yamlFact(t.GitHub.PRState))
		fmt.Fprintf(&b, "      pr_head_sha: %s\n", yamlFact(t.GitHub.PRHeadSHA))
		fmt.Fprintf(&b, "      ci_state: %s\n", yamlFact(t.GitHub.CIState))
		fmt.Fprintf(&b, "      ci_tested_sha: %s\n", yamlFact(t.GitHub.CITestedSHA))
		fmt.Fprintf(&b, "      merge_state: %s\n", yamlFact(t.GitHub.MergeState))
		fmt.Fprintf(&b, "      merge_sha: %s\n", yamlFact(t.GitHub.MergeSHA))
	}
	return []byte(b.String()), nil
}

// writeList emits a sorted YAML sequence, or an explicit empty list. Sorting is
// part of the determinism contract: dependency order is a set, and letting map
// iteration decide it would churn the committed file.
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

// yamlTime renders an absent timestamp as an explicit null rather than as an
// epoch. A planned or actual date the producer has no authority for must read
// as "unknown", never as 1970.
func yamlTime(t time.Time) string {
	if t.IsZero() {
		return "null"
	}
	return t.UTC().Format(time.RFC3339)
}

func yamlScalar(v string) string {
	if v == "" {
		return `""`
	}
	return `"` + strings.ReplaceAll(v, `"`, `\"`) + `"`
}

// UnknownValue is what an authoritative fact renders as when no authority could
// supply it.
//
// An empty string is not good enough. A blank pr_state or ci_state is
// indistinguishable from "not applicable" and a consumer may reasonably render
// it as nothing-to-see — which, for a fact that was supposed to be checked, is
// the same silent zero the status vocabulary already refuses. "unknown" says
// the projector looked and could not answer.
const UnknownValue = "unknown"

// yamlFact renders an authoritative fact, making absence explicit rather than
// blank. Use it for values a source was supposed to supply; use yamlScalar for
// values that are legitimately empty (a title nobody set).
func yamlFact(v string) string {
	if strings.TrimSpace(v) == "" {
		return `"` + UnknownValue + `"`
	}
	return yamlScalar(v)
}

// yamlFactInt is the integer peer: 0 is a real PR number to nobody, so an
// absent one renders as unknown rather than as zero.
func yamlFactInt(v int) string {
	if v == 0 {
		return `"` + UnknownValue + `"`
	}
	return fmt.Sprintf("%d", v)
}
