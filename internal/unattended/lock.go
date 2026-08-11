package unattended

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Role is what a session intends to do with a worktree.
//
// The role is declared, not inferred. A session that means to mutate says so,
// and is then held to it: the lock it gets is the lock its declaration earns.
type Role string

// The three roles a session may hold over a worktree.
const (
	// RoleWriter mutates the worktree. Exclusive.
	RoleWriter Role = "writer"
	// RoleController mutates the repository on the run's behalf — it is the
	// only role permitted to publish. Exclusive, for the same reason as a
	// writer: it commits.
	RoleController Role = "controller"
	// RoleReadOnly reads the worktree and never mutates it. Shared, so
	// assurance sessions can observe a run without contending with it.
	RoleReadOnly Role = "read-only"
)

// Exclusive reports whether the role requires sole ownership of the worktree.
func (r Role) Exclusive() bool { return r == RoleWriter || r == RoleController }

// Valid reports whether the role is one of the declared three.
func (r Role) Valid() bool {
	return r == RoleWriter || r == RoleController || r == RoleReadOnly
}

const (
	lockFile            = "gc-unattended-writer.lock"
	ownerFile           = "gc-unattended-writer.owner.json"
	supersededOwnerFile = "gc-unattended-writer.superseded.json"
)

// Errors a caller is expected to distinguish.
var (
	// ErrWriterHeld — another live session holds the worktree. The correct
	// response is to not start, never to break the lock.
	ErrWriterHeld = errors.New("unattended: the worktree already has a live owner")

	// ErrForeignWriter — the worktree records an owner from another host or
	// another operating system. File locks do not compose across that boundary
	// (a Windows byte-range lock and a WSL flock on the same DrvFs path do not
	// see each other), so the OS lock cannot arbitrate and the text cannot
	// either. Refusing is the only honest answer; ForceClearOwner is the
	// governed way through.
	ErrForeignWriter = errors.New("unattended: the worktree records an owner on another host or OS whose liveness this process cannot verify")

	// ErrInvalidRole — the caller did not declare a valid role.
	ErrInvalidRole = errors.New("unattended: owner must declare a valid role")
)

// Owner is the evidence a lock holder leaves behind.
//
// It exists to answer "who has this, and since when" for a human reading the
// directory after something went wrong. It is *not* the authority for whether
// the lock is held — see Acquire.
type Owner struct {
	RunID      string    `json:"runId"`
	ProjectID  string    `json:"projectId"`
	Session    string    `json:"session"`
	Worktree   string    `json:"worktree"`
	Role       Role      `json:"role"`
	PID        int       `json:"pid"`
	Host       string    `json:"host"`
	OSFamily   string    `json:"osFamily"`
	AcquiredAt time.Time `json:"acquiredAt"`

	// Displaced records an owner whose evidence was still present when this
	// owner acquired the lock — proof that the previous holder was not holding
	// it. Recovery is bookkeeping, so the record it leaves is the whole of it.
	Displaced *Owner `json:"displaced,omitempty"`
}

// Lock is a held claim over a worktree. It is released by Release, and by
// process exit — the operating system drops the lock either way, which is what
// makes a crashed run recoverable without anyone deciding it crashed.
type Lock struct {
	dir   string
	file  *os.File
	owner Owner
}

// Owner returns the evidence recorded for this held lock.
func (l *Lock) Owner() Owner { return l.owner }

// Dir returns the directory the lock is held in.
func (l *Lock) Dir() string { return l.dir }

func hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "unknown-host"
	}
	return h
}

// Acquire takes the worktree lock for the declared role without blocking.
//
// The held OS lock is the authority. Acquire never reads a PID out of the
// evidence file and decides from it whether the previous owner is alive: a PID
// is a number in a file, it can name a process that was recycled, and acting on
// it means one session deciding another deserves to lose its work. Instead the
// operating system arbitrates — if the previous holder still has the lock, this
// call is denied; if it does not, this call wins and the stale evidence is
// archived rather than obeyed.
//
// The one case the OS cannot arbitrate is an owner recorded on another host or
// another OS family, where the two lock implementations do not see each other.
// There Acquire refuses (ErrForeignWriter) rather than guessing.
func Acquire(dir string, o Owner) (*Lock, error) {
	if !o.Role.Valid() {
		return nil, fmt.Errorf("%w: got %q", ErrInvalidRole, string(o.Role))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating lock directory %q: %w", dir, err)
	}

	f, err := os.OpenFile(filepath.Join(dir, lockFile), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening worktree lock in %q: %w", dir, err)
	}
	ok, err := tryLockFile(f, o.Role.Exclusive())
	if err != nil {
		f.Close() //nolint:errcheck
		return nil, fmt.Errorf("locking worktree in %q: %w", dir, err)
	}
	if !ok {
		f.Close() //nolint:errcheck
		if prev, found, rerr := ReadOwner(dir); rerr == nil && found {
			return nil, fmt.Errorf("%w: held by run %q (session %q, pid %d on %s) since %s",
				ErrWriterHeld, prev.RunID, prev.Session, prev.PID, prev.Host,
				prev.AcquiredAt.UTC().Format(time.RFC3339))
		}
		return nil, ErrWriterHeld
	}

	prev, found, err := ReadOwner(dir)
	if err != nil {
		unlockAndClose(f)
		return nil, fmt.Errorf("reading owner evidence in %q: %w", dir, err)
	}
	if found && o.Role.Exclusive() && isForeign(prev) {
		unlockAndClose(f)
		return nil, fmt.Errorf("%w: recorded owner run %q, pid %d on %s/%s (this process is on %s/%s)",
			ErrForeignWriter, prev.RunID, prev.PID, prev.Host, prev.OSFamily, hostname(), runtime.GOOS)
	}

	o.PID = os.Getpid()
	o.Host = hostname()
	o.OSFamily = runtime.GOOS
	o.AcquiredAt = time.Now().UTC()
	if found {
		// The previous owner's evidence outlived its lock. Archive it, then
		// carry it forward in this owner's record so the takeover is visible
		// from either file.
		if err := archiveOwner(dir, prev); err != nil {
			unlockAndClose(f)
			return nil, fmt.Errorf("archiving displaced owner in %q: %w", dir, err)
		}
		displaced := prev
		displaced.Displaced = nil
		o.Displaced = &displaced
	}
	if err := writeOwner(dir, o); err != nil {
		unlockAndClose(f)
		return nil, fmt.Errorf("recording owner evidence in %q: %w", dir, err)
	}
	return &Lock{dir: dir, file: f, owner: o}, nil
}

// isForeign reports whether recorded evidence came from somewhere this process
// cannot arbitrate against. Evidence with no host recorded at all is treated as
// local: it predates this field rather than proving a foreign owner, and the OS
// lock has already spoken.
func isForeign(prev Owner) bool {
	if prev.Host == "" && prev.OSFamily == "" {
		return false
	}
	if prev.OSFamily != "" && prev.OSFamily != runtime.GOOS {
		return true
	}
	return prev.Host != "" && prev.Host != hostname()
}

// Release drops the lock and removes this owner's evidence.
//
// Evidence is removed only when it still names this owner. A lock released
// after its record was superseded must not delete the successor's claim.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	if cur, found, err := ReadOwner(l.dir); err == nil && found && cur.RunID == l.owner.RunID && cur.PID == l.owner.PID {
		if err := os.Remove(filepath.Join(l.dir, ownerFile)); err != nil && !os.IsNotExist(err) {
			unlockAndClose(l.file)
			l.file = nil
			return fmt.Errorf("removing owner evidence in %q: %w", l.dir, err)
		}
	}
	err := unlockFile(l.file)
	closeErr := l.file.Close()
	l.file = nil
	if err != nil {
		return fmt.Errorf("unlocking worktree in %q: %w", l.dir, err)
	}
	if closeErr != nil {
		return fmt.Errorf("closing worktree lock in %q: %w", l.dir, closeErr)
	}
	return nil
}

func unlockAndClose(f *os.File) {
	_ = unlockFile(f)
	_ = f.Close()
}

// ReadOwner returns the recorded owner of a worktree lock directory, if any.
//
// It reports what the directory says, not what is true. Callers deciding
// whether they may write must call Acquire.
func ReadOwner(dir string) (Owner, bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, ownerFile))
	if os.IsNotExist(err) {
		return Owner{}, false, nil
	}
	if err != nil {
		return Owner{}, false, fmt.Errorf("reading owner evidence: %w", err)
	}
	var o Owner
	if err := json.Unmarshal(data, &o); err != nil {
		return Owner{}, false, fmt.Errorf("parsing owner evidence: %w", err)
	}
	return o, true, nil
}

// ProbeOwner reports what a lock directory records, and whether a live process
// is actually holding the lock.
//
// It exists because "a record is present" and "somebody is here" are different
// facts, and confusing them makes a crashed run unrestartable: the dead run's
// record outlives it, and a check that treated the record as authority would
// refuse every subsequent run until a person deleted a file. The lock is the
// authority here exactly as it is in Acquire — this only asks without claiming.
//
// The lock is taken and released immediately, so a live holder is detected
// without being disturbed. The tiny window this opens does not matter: nothing
// is decided on the strength of it, and the run's real claim moments later is
// the exclusive acquire that actually arbitrates.
func ProbeOwner(dir string) (owner Owner, recorded bool, live bool, err error) {
	owner, recorded, err = ReadOwner(dir)
	if err != nil {
		return owner, recorded, false, err
	}
	f, ferr := os.OpenFile(filepath.Join(dir, lockFile), os.O_CREATE|os.O_RDWR, 0o644)
	if ferr != nil {
		if os.IsNotExist(ferr) {
			return owner, recorded, false, nil
		}
		return owner, recorded, false, fmt.Errorf("opening worktree lock in %q: %w", dir, ferr)
	}
	defer f.Close() //nolint:errcheck

	got, lerr := tryLockFile(f, true)
	if lerr != nil {
		return owner, recorded, false, fmt.Errorf("probing the worktree lock in %q: %w", dir, lerr)
	}
	if !got {
		return owner, recorded, true, nil
	}
	if uerr := unlockFile(f); uerr != nil {
		return owner, recorded, false, fmt.Errorf("releasing the probe lock in %q: %w", dir, uerr)
	}
	return owner, recorded, false, nil
}

func writeOwner(dir string, o Owner) error {
	data, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding owner evidence: %w", err)
	}
	return writeFileAtomic(filepath.Join(dir, ownerFile), append(data, '\n'))
}

func archiveOwner(dir string, o Owner) error {
	data, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding displaced owner: %w", err)
	}
	return writeFileAtomic(filepath.Join(dir, supersededOwnerFile), append(data, '\n'))
}

// ForceClearOwner archives a lock directory's owner evidence under a stated
// reason, so a refused foreign claim has a governed way through.
//
// This is a human action with a human's justification attached. It clears a
// *record*; it cannot and does not take a lock away from a process that still
// holds one, so using it on a genuinely live owner achieves nothing beyond
// leaving a note.
func ForceClearOwner(dir, reason, actor string) error {
	if reason == "" || actor == "" {
		return errors.New("unattended: clearing owner evidence requires an actor and a reason")
	}
	prev, found, err := ReadOwner(dir)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	record := struct {
		Owner
		ClearedBy     string    `json:"clearedBy"`
		ClearedReason string    `json:"clearedReason"`
		ClearedAt     time.Time `json:"clearedAt"`
	}{Owner: prev, ClearedBy: actor, ClearedReason: reason, ClearedAt: time.Now().UTC()}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding cleared owner: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(dir, supersededOwnerFile), append(data, '\n')); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, ownerFile)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing owner evidence: %w", err)
	}
	return nil
}
