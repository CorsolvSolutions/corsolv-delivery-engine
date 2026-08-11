package unattended

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners int
		start   = make(chan struct{})
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			lk, err := Acquire(dir, testOwner(RoleWriter, fmt.Sprintf("goroutine-%d", i)))
			if err != nil {
				return
			}
			mu.Lock()
			winners++
			mu.Unlock()
			time.Sleep(150 * time.Millisecond)
			lk.Release() //nolint:errcheck
		}(i)
	}
	close(start)
	wg.Wait()
	if winners != 1 {
		t.Fatalf("winners = %d, want exactly 1", winners)
	}
}

// --- real second OS process -------------------------------------------------

const (
	lockHelperEnv    = "GC_UNATTENDED_LOCK_HELPER"
	lockHelperDirEnv = "GC_UNATTENDED_LOCK_DIR"
	lockHelperExit   = 3
)

// TestLockHelperProcess is not a test. It is the body of the child process the
// cross-process cases re-exec, and it is skipped in every ordinary run.
//
// The in-process cases prove the lock excludes concurrent *goroutines*, which
// on some platforms is a weaker claim than excluding concurrent *processes*.
// The whole point of this lock is the second claim, so it is proved directly.
func TestLockHelperProcess(t *testing.T) {
	if os.Getenv(lockHelperEnv) == "" {
		t.Skip("helper process body; not a standalone test")
	}
	dir := os.Getenv(lockHelperDirEnv)
	lk, err := Acquire(dir, testOwner(RoleWriter, "child-"+fmt.Sprint(os.Getpid())))
	if err != nil {
		fmt.Println("DENIED")
		os.Exit(lockHelperExit)
	}
	fmt.Println("ACQUIRED")
	time.Sleep(2 * time.Second)
	_ = lk.Release()
	os.Exit(0)
}

func lockHelperCmd(t *testing.T, dir string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockHelperProcess$", "-test.timeout=60s")
	cmd.Env = append(os.Environ(), lockHelperEnv+"=1", lockHelperDirEnv+"="+dir)
	return cmd
}

func TestAcquireAcrossProcessesIsDenied(t *testing.T) {
	dir := t.TempDir()
	held, err := Acquire(dir, testOwner(RoleWriter, "parent"))
	if err != nil {
		t.Fatalf("parent acquire: %v", err)
	}
	defer held.Release() //nolint:errcheck

	out, err := lockHelperCmd(t, dir).CombinedOutput()
	if err == nil {
		t.Fatalf("child acquired a worktree the parent process holds:\n%s", out)
	}
	if !strings.Contains(string(out), "DENIED") {
		t.Fatalf("child did not report denial:\n%s", out)
	}
}

func TestConcurrentProcessesElectExactlyOneWriter(t *testing.T) {
	dir := t.TempDir()
	const n = 5

	cmds := make([]*exec.Cmd, n)
	outs := make([]strings.Builder, n)
	for i := range cmds {
		cmds[i] = lockHelperCmd(t, dir)
		cmds[i].Stdout = &outs[i]
		cmds[i].Stderr = &outs[i]
		if err := cmds[i].Start(); err != nil {
			t.Fatalf("start child %d: %v", i, err)
		}
	}
	winners := 0
	for i, c := range cmds {
		_ = c.Wait()
		if strings.Contains(outs[i].String(), "ACQUIRED") {
			winners++
		}
	}
	// Each winner holds for 2s, comfortably longer than the spawn spread, so a
	// second winner would mean genuine concurrent ownership rather than
	// sequential reuse.
	if winners != 1 {
		t.Fatalf("winners = %d, want exactly 1", winners)
	}
}
