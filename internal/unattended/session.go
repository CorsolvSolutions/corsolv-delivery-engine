package unattended

import (
	"context"
	"errors"
	"fmt"
)

// PreflightReportName is where a run's preflight verdict is kept.
const PreflightReportName = "preflight.json"

// Session is a run that has passed preflight and holds the worktree.
//
// Its constructor is the ordering that matters: preflight first, then the lock,
// then the fence, then the journal, then the queue. Taking the lock before
// preflight would mean a NOT-READY run had already claimed a worktree it was
// never going to use; taking the fence before the lock would mean fencing a
// position somebody else was entitled to move.
type Session struct {
	Spec   Spec
	Plan   Plan
	Report *Report

	Lock    *Lock
	Fence   *Fence
	Journal *Journal
	Queue   *Queue
	Runner  *Runner

	// Resumed reports whether this session continued an existing journal.
	Resumed bool
	// TruncatedTail reports that the previous run died mid-record.
	TruncatedTail bool
}

// ErrRunAlreadyCompleted is returned when a run id is re-entered after that run
// durably recorded a COMPLETED terminal state.
//
// A completed run is history. Re-entering it republishes its heartbeat,
// re-evaluates its gates against whatever revision is in hand by then, and
// rewrites its terminal record — so a delivery that finished at one revision
// can be reported as running, or refused, by a session that did no work. A new
// revision starts a new run id; it does not reopen a finished one.
var ErrRunAlreadyCompleted = errors.New("unattended: this run already recorded a completed terminal state")

// Begin runs preflight, claims the worktree, and prepares the queue.
//
// It refuses a NOT-READY verdict rather than starting and failing later, and it
// refuses a worktree somebody else holds rather than becoming the second writer.
// Both refusals happen before a single byte of work is done.
func Begin(ctx context.Context, spec Spec, plan Plan) (*Session, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	if err := ValidateClassificationRules(spec.Classification); err != nil {
		return nil, err
	}
	// The QA contract is checked here, before preflight, before the lock and
	// before any task runs. A packet that cannot say what would have to pass
	// before it may progress must not start a worker: the alternative is a run
	// that does hours of work and only then discovers it has no way to certify
	// any of it.
	if err := ValidateQAPacket(spec, plan); err != nil {
		return nil, err
	}

	s := &Session{Spec: spec, Plan: plan}

	// The run's own durable journal is read here — before preflight, before the
	// lock, before the fence — because one of the things it can say is that
	// this run is already over. A completed run that got as far as claiming a
	// worktree would have republished its heartbeat under an id whose delivery
	// finished, and a session that must not start must not claim anything on
	// its way to finding that out.
	journalPath := stateDirPath(spec.StateDir, JournalName)
	records, truncated, err := ReadJournal(journalPath)
	if err != nil {
		return nil, err
	}
	resume := Replay(records, plan.RunID)
	if resume.Finished && resume.FinalOutcome == RunCompleted {
		return nil, fmt.Errorf("%w: run %q recorded outcome %s in %s — a new revision starts a new run id rather than reopening a finished one",
			ErrRunAlreadyCompleted, plan.RunID, resume.FinalOutcome, journalPath)
	}
	s.TruncatedTail = truncated

	s.Report = Preflight(ctx, spec, &plan)

	// The verdict is written before it is acted on, so a refused run still
	// leaves behind the reason it was refused.
	if data, err := s.Report.JSON(); err == nil {
		_ = writeFileAtomic(stateDirPath(spec.StateDir, PreflightReportName), data)
	}
	if !s.Report.PermitsUnattendedRun() {
		return nil, fmt.Errorf("%w: %s", ErrNotReady, s.Report.Blocking().String())
	}

	lockDir := WriterLockDir(s.Report.Repo)
	lock, err := Acquire(lockDir, Owner{
		RunID:     plan.RunID,
		ProjectID: spec.ProjectID,
		Session:   spec.Ownership.Session,
		Worktree:  s.Report.Repo.Root,
		Role:      spec.Ownership.Role,
	})
	if err != nil {
		return nil, err
	}
	s.Lock = lock

	// From here on, any failure must release the claim: a session that dies in
	// its constructor holding a lock is the stale-lock problem, self-inflicted.
	defer func() {
		if s.Runner == nil {
			_ = lock.Release()
		}
	}()

	s.Fence = TakeFence(s.Report.Repo, lockDir, lock.Owner())

	journal, err := OpenJournal(journalPath, plan.RunID)
	if err != nil {
		return nil, err
	}
	s.Journal = journal

	// Resumed means this run has been here before — not that the journal has
	// other runs in it. A state directory belongs to a project and accumulates
	// every run it has had.
	s.Resumed = resume.LastSeq > 0 && (len(resume.Succeeded) > 0 || len(resume.Attempts) > 0)

	s.Queue = NewQueue(plan, s.Report.Boundaries())
	if s.Resumed {
		s.Queue.Restore(resume)
	}

	if _, err := journal.Append(Record{
		Kind: RecordLockAcquired,
		Detail: fmt.Sprintf("role=%s worktree=%s pid=%d",
			spec.Ownership.Role, s.Report.Repo.Root, lock.Owner().PID),
	}); err != nil {
		return nil, err
	}
	if _, err := journal.Append(Record{
		Kind:   RecordFenceTaken,
		Detail: fmt.Sprintf("%s@%s", s.Fence.Branch, shortSHA(s.Fence.Head)),
	}); err != nil {
		return nil, err
	}
	if _, err := journal.Append(Record{
		Kind: RecordPreflight, Outcome: string(s.Report.Readiness),
		Detail: fmt.Sprintf("%d checks, %d boundaries", len(s.Report.Checks), len(s.Report.Boundaries())),
	}); err != nil {
		return nil, err
	}
	if truncated {
		if _, err := journal.Append(Record{
			Kind: RecordPreflight, Outcome: "resumed-after-interruption",
			Detail: "the previous run died between writing and syncing its last record; that record was never durable and is not replayed",
		}); err != nil {
			return nil, err
		}
	}

	s.Runner = &Runner{
		Spec: spec, Plan: plan, Report: s.Report,
		Lock: lock, Fence: s.Fence, Journal: journal, Queue: s.Queue,
		// The evidence ledger is inherited from the journal unconditionally,
		// not only when the queue was restored. A resumed run must not be able
		// to clear a gate failure by starting again.
		Evidence: resume.Gates,
	}
	return s, nil
}

// Close releases the worktree claim and the journal.
//
// The lock is released last and unconditionally. A journal that fails to close
// is worth reporting; it is not worth leaving a worktree claimed over.
func (s *Session) Close() error {
	var firstErr error
	if s.Journal != nil {
		if err := s.Journal.Close(); err != nil {
			firstErr = err
		}
	}
	if s.Lock != nil {
		if err := s.Lock.Release(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
