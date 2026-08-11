package unattended

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func testOwner(role Role, run string) Owner {
	return Owner{
		RunID:     run,
		ProjectID: "corsolv-delivery-engine",
		Session:   "test-session",
		Worktree:  "/tmp/worktree",
		Role:      role,
	}
}

func TestAcquireIsExclusive(t *testing.T) {
	dir := t.TempDir()

	first, err := Acquire(dir, testOwner(RoleWriter, "run-1"))
	if err != nil {
		t.Fatalf("first writer denied: %v", err)
	}
	defer first.Release() //nolint:errcheck

	second, err := Acquire(dir, testOwner(RoleWriter, "run-2"))
	if !errors.Is(err, ErrWriterHeld) {
		t.Fatalf("second writer error = %v, want ErrWriterHeld", err)
	}
	if second != nil {
		t.Fatal("a denied acquire must not return a lock")
	}
}

func TestReleaseAllowsReacquire(t *testing.T) {
	dir := t.TempDir()

	first, err := Acquire(dir, testOwner(RoleWriter, "run-1"))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}

	second, err := Acquire(dir, testOwner(RoleWriter, "run-2"))
	if err != nil {
		t.Fatalf("reacquire after release denied: %v", err)
	}
	defer second.Release() //nolint:errcheck
}

func TestOwnerEvidenceIsRecorded(t *testing.T) {
	dir := t.TempDir()
	want := Owner{
		RunID:     "run-evidence",
		ProjectID: "corsolv-delivery-engine",
		Session:   "gascity-unattended-readiness",
		Worktree:  "/mnt/d/Development/corsolv-delivery-engine",
		Role:      RoleWriter,
	}

	lk, err := Acquire(dir, want)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer lk.Release() //nolint:errcheck

	got, ok, err := ReadOwner(dir)
	if err != nil || !ok {
		t.Fatalf("ReadOwner: ok=%v err=%v", ok, err)
	}
	if got.RunID != want.RunID || got.ProjectID != want.ProjectID ||
		got.Session != want.Session || got.Worktree != want.Worktree || got.Role != want.Role {
		t.Fatalf("owner evidence lost fields: %+v", got)
	}
	if got.PID != os.Getpid() {
		t.Fatalf("owner PID = %d, want %d", got.PID, os.Getpid())
	}
	if got.Host == "" || got.OSFamily != runtime.GOOS {
		t.Fatalf("owner host evidence incomplete: host=%q os=%q", got.Host, got.OSFamily)
	}
	if got.AcquiredAt.IsZero() {
		t.Fatal("owner evidence must carry an acquisition time")
	}
}

func TestHeldLockIsAuthorityNotPidText(t *testing.T) {
	dir := t.TempDir()

	// Evidence naming a process that is unquestionably alive — this one — but
	// with no OS lock behind it. Text must not block a new writer, because text
	// is not the authority.
	stale := testOwner(RoleWriter, "run-stale")
	stale.PID = os.Getpid()
	stale.Host = hostname()
	stale.OSFamily = runtime.GOOS
	stale.AcquiredAt = time.Now().UTC()
	if err := writeOwner(dir, stale); err != nil {
		t.Fatalf("seed evidence: %v", err)
	}

	lk, err := Acquire(dir, testOwner(RoleWriter, "run-new"))
	if err != nil {
		t.Fatalf("a live PID in text must not block acquisition: %v", err)
	}
	defer lk.Release() //nolint:errcheck

	if lk.Owner().Displaced == nil || lk.Owner().Displaced.RunID != "run-stale" {
		t.Fatalf("takeover must record the displaced owner, got %+v", lk.Owner().Displaced)
	}
}

func TestStaleEvidenceWithoutLockNeverKillsAnything(t *testing.T) {
	// Recovery from a stale lock must be a pure bookkeeping operation. The
	// package must not own any code that signals another process, because
	// deciding that a peer deserves to die is exactly the judgement this layer
	// is forbidden to make.
	dir := t.TempDir()
	stale := testOwner(RoleWriter, "run-stale")
	stale.PID = 999999
	stale.Host = hostname()
	stale.OSFamily = runtime.GOOS
	if err := writeOwner(dir, stale); err != nil {
		t.Fatalf("seed evidence: %v", err)
	}
	lk, err := Acquire(dir, testOwner(RoleWriter, "run-new"))
	if err != nil {
		t.Fatalf("acquire over stale evidence: %v", err)
	}
	defer lk.Release() //nolint:errcheck

	superseded := filepath.Join(dir, supersededOwnerFile)
	if _, err := os.Stat(superseded); err != nil {
		t.Fatalf("displaced owner must be archived for the record: %v", err)
	}
}

func TestForeignHostWriterIsRefusedRatherThanOverridden(t *testing.T) {
	dir := t.TempDir()
	foreign := testOwner(RoleWriter, "run-foreign")
	foreign.PID = 4242
	foreign.Host = "some-other-machine"
	foreign.OSFamily = "plan9"
	foreign.AcquiredAt = time.Now().UTC()
	if err := writeOwner(dir, foreign); err != nil {
		t.Fatalf("seed evidence: %v", err)
	}

	lk, err := Acquire(dir, testOwner(RoleWriter, "run-local"))
	if !errors.Is(err, ErrForeignWriter) {
		t.Fatalf("acquire over foreign evidence = %v, want ErrForeignWriter", err)
	}
	if lk != nil {
		t.Fatal("a refused acquire must not return a lock")
	}

	// And the refusal must not have left the OS lock held.
	if err := ForceClearOwner(dir, "acceptance: foreign owner is provably dead", "test"); err != nil {
		t.Fatalf("ForceClearOwner: %v", err)
	}
	lk, err = Acquire(dir, testOwner(RoleWriter, "run-local"))
	if err != nil {
		t.Fatalf("acquire after governed clear: %v", err)
	}
	defer lk.Release() //nolint:errcheck
}

func TestReadOnlyRolesCoexistButExcludeAWriter(t *testing.T) {
	dir := t.TempDir()

	r1, err := Acquire(dir, testOwner(RoleReadOnly, "reader-1"))
	if err != nil {
		t.Fatalf("first reader: %v", err)
	}
	defer r1.Release() //nolint:errcheck

	r2, err := Acquire(dir, testOwner(RoleReadOnly, "reader-2"))
	if err != nil {
		t.Fatalf("read-only assurance sessions must coexist: %v", err)
	}
	defer r2.Release() //nolint:errcheck

	if _, err := Acquire(dir, testOwner(RoleWriter, "writer")); !errors.Is(err, ErrWriterHeld) {
		t.Fatalf("writer against live readers = %v, want ErrWriterHeld", err)
	}
}

func TestWriterExcludesAReader(t *testing.T) {
	dir := t.TempDir()
	w, err := Acquire(dir, testOwner(RoleWriter, "writer"))
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	defer w.Release() //nolint:errcheck

	if _, err := Acquire(dir, testOwner(RoleReadOnly, "reader")); !errors.Is(err, ErrWriterHeld) {
		t.Fatalf("reader against a live writer = %v, want ErrWriterHeld", err)
	}
}

func TestControllerRoleIsExclusiveToo(t *testing.T) {
	dir := t.TempDir()
	c, err := Acquire(dir, testOwner(RoleController, "controller"))
	if err != nil {
		t.Fatalf("controller: %v", err)
	}
	defer c.Release() //nolint:errcheck
	if _, err := Acquire(dir, testOwner(RoleWriter, "writer")); !errors.Is(err, ErrWriterHeld) {
		t.Fatalf("writer against a live controller = %v, want ErrWriterHeld", err)
	}
}

func TestConcurrentGoroutinesElectExactlyOneWriter(t *testing.T) {
	dir := t.TempDir()
	const n = 16
	var (
		wg        sync.WaitGroup
		attempted sync.WaitGroup
		mu        sync.Mutex
		winners   int
	)
	// The winner holds until every contender has had its turn, and it does so
	// by waiting on a channel rather than by sleeping. A sleep would only make
	// the overlap probable; this makes it certain, and it costs no wall time.
	start := make(chan struct{})
	release := make(chan struct{})

	attempted.Add(n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			lk, err := Acquire(dir, testOwner(RoleWriter, fmt.Sprintf("goroutine-%d", i)))
			attempted.Done()
			if err != nil {
				return
			}
			mu.Lock()
			winners++
			mu.Unlock()
			<-release
			lk.Release() //nolint:errcheck
		}(i)
	}
	close(start)
	attempted.Wait() // every contender has tried while the winner still holds
	close(release)
	wg.Wait()

	if winners != 1 {
		t.Fatalf("winners = %d, want exactly 1", winners)
	}
}
