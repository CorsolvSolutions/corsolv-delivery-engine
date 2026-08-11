package tmuxtest

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/pidutil"
)

// The leak these tests exist for: the harness used to remove a tmux socket
// root directory while servers were still listening on sockets inside it.
// Deleting the directory does not stop the server. It keeps running with its
// TMUX_TMPDIR gone, so `tmux list-sessions` cannot see it and no later sweep
// can find it — a permanent orphan. A census of this development host found
// 135 such servers, 134 of them unreachable, alongside the directories that
// had been tidied away neatly on top of them.
//
// These tests spawn real tmux servers and assert on real process liveness,
// because the whole defect is that the filesystem looked clean while the
// process table did not.

// shortTempDir returns a test-owned directory with a short path.
//
// t.TempDir() embeds the test name, and a Unix socket path is capped near 108
// bytes, so a nested socket under a long test name fails to bind for reasons
// that have nothing to do with what is being tested. This is the same reason
// the harness puts its real socket parents under /tmp.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "gclk-")
	if err != nil {
		t.Fatalf("creating short temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// waitForExit polls until pid is gone, or reports failure. Signal delivery and
// process teardown are not synchronous, so a bare liveness check immediately
// after a kill would be flaky in the direction of a false pass.
func waitForExit(t *testing.T, pid int, within time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		if !pidutil.Alive(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestRemovingASocketRootAloneLeaksTheServer pins the defective behavior, so
// the fix cannot be quietly reverted to "just delete the directory" without a
// test going red. It asserts the leak, then reclaims the process it created.
func TestRemovingASocketRootAloneLeaksTheServer(t *testing.T) {
	RequireTmux(t)
	root := shortTempDir(t)
	_, pid := StartDetachedServer(t, root, "leak-probe", "victim")

	if !pidutil.Alive(pid) {
		t.Fatalf("server %d did not start", pid)
	}

	// Exactly what the harness used to do.
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("removing root: %v", err)
	}

	if waitForExit(t, pid, 2*time.Second) {
		t.Fatal("removing the socket root stopped the server; this test no longer describes the defect")
	}
	// Reclaim only the process this test started, by PID.
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}

// TestCleanupSocketRootLeavesNoServerBehind is the regression proper: the
// census of processes this path created must return to its baseline.
func TestCleanupSocketRootLeavesNoServerBehind(t *testing.T) {
	RequireTmux(t)
	root := shortTempDir(t)

	if got := len(SocketPathsUnder(root)); got != 0 {
		t.Fatalf("baseline sockets under a fresh root = %d, want 0", got)
	}

	socketPath, pid := StartDetachedServer(t, root, "cleanup-probe", "victim")
	if !pidutil.Alive(pid) {
		t.Fatalf("server %d did not start", pid)
	}
	if got := len(SocketPathsUnder(root)); got != 1 {
		t.Fatalf("sockets after start = %d, want 1", got)
	}

	CleanupSocketRoot(root, io.Discard)

	if !waitForExit(t, pid, 5*time.Second) {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
		t.Fatalf("tmux server %d survived CleanupSocketRoot: the harness leaked it", pid)
	}
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Errorf("socket %s survived cleanup", socketPath)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("root %s survived cleanup", root)
	}
	if got := len(SocketPathsUnder(root)); got != 0 {
		t.Fatalf("sockets after cleanup = %d, want 0 — census did not return to baseline", got)
	}
}

// TestCleanupIsRepeatableWithNoCumulativeGrowth covers the two-consecutive-runs
// requirement: a second cycle must end where the first began.
func TestCleanupIsRepeatableWithNoCumulativeGrowth(t *testing.T) {
	RequireTmux(t)
	var pids []int
	for run := range 2 {
		root := shortTempDir(t)
		_, pid := StartDetachedServer(t, root, "repeat-probe", "victim")
		pids = append(pids, pid)
		CleanupSocketRoot(root, io.Discard)
		if !waitForExit(t, pid, 5*time.Second) {
			if p, err := os.FindProcess(pid); err == nil {
				_ = p.Kill()
			}
			t.Fatalf("run %d: server %d survived cleanup", run+1, pid)
		}
	}
	for i, pid := range pids {
		if pidutil.Alive(pid) {
			t.Errorf("run %d's server %d still alive after both runs", i+1, pid)
		}
	}
}

// TestCleanupNeverTouchesAServerOutsideItsRoot is the safety half. The fix
// addresses sockets by path inside a root the caller owns; it must not reach a
// server living anywhere else, which is what an operator's own tmux is.
func TestCleanupNeverTouchesAServerOutsideItsRoot(t *testing.T) {
	RequireTmux(t)
	ours := shortTempDir(t)
	theirs := shortTempDir(t)

	_, ourPID := StartDetachedServer(t, ours, "ours", "victim")
	theirSocket, theirPID := StartDetachedServer(t, theirs, "theirs", "bystander")
	t.Cleanup(func() {
		if p, err := os.FindProcess(theirPID); err == nil {
			_ = p.Kill()
		}
	})

	CleanupSocketRoot(ours, io.Discard)

	if !waitForExit(t, ourPID, 5*time.Second) {
		t.Errorf("our own server %d survived cleanup", ourPID)
	}
	if !pidutil.Alive(theirPID) {
		t.Fatalf("cleanup killed a server outside its root (pid %d) — this would take an operator's tmux with it", theirPID)
	}
	if _, err := os.Stat(theirSocket); err != nil {
		t.Errorf("bystander socket %s disturbed: %v", theirSocket, err)
	}
}

// TestCleanupFindsSocketsInTheRealHarnessLayout is the shape the harness
// actually uses, and the one an earlier glob-based implementation missed.
//
// cmd/gc and test/integration create a socket *parent* and put the socket root
// at <parent>/tmux, so the socket itself lands at
// <parent>/tmux/tmux-<uid>/<name>. Cleanup is handed the parent. A pattern
// anchored at the parent finds nothing there — which is precisely how one
// server per run kept leaking while the shallower fixtures went green.
func TestCleanupFindsSocketsInTheRealHarnessLayout(t *testing.T) {
	RequireTmux(t)
	parent := shortTempDir(t)
	socketRoot := filepath.Join(parent, "tmux")
	if err := os.MkdirAll(socketRoot, 0o700); err != nil {
		t.Fatal(err)
	}

	_, pid := StartDetachedServer(t, socketRoot, "nested-probe", "victim")

	// Discovery must see it from the parent, not just from the socket root.
	if got := len(SocketPathsUnder(parent)); got != 1 {
		t.Fatalf("sockets discovered from the parent = %d, want 1: cleanup would miss the server", got)
	}

	CleanupSocketRoot(parent, io.Discard)

	if !waitForExit(t, pid, 5*time.Second) {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
		t.Fatalf("server %d survived cleanup of the socket parent: the real harness layout still leaks", pid)
	}
}

// TestSweepStopsServersBeforeRemovingAnOrphanedDir covers the other deletion
// path. The orphan sweep removes a dead sibling's socket parent; if it deletes
// without stopping first, it converts a recoverable orphan into a permanent
// one.
func TestSweepStopsServersBeforeRemovingAnOrphanedDir(t *testing.T) {
	RequireTmux(t)
	root := shortTempDir(t)
	const prefix = "gct-sweepleak-"

	// A dir shaped like a dead sibling's: PID-prefixed, no held sentinel.
	orphan := filepath.Join(root, prefix+"999999-orphan")
	if err := os.MkdirAll(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	_, pid := StartDetachedServer(t, orphan, "orphan-probe", "victim")
	backdatePastSweepAge(t, orphan)

	SweepOrphanPIDPrefixedDirs(root, prefix, io.Discard)

	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("sweep did not remove the orphaned dir %s", orphan)
	}
	if !waitForExit(t, pid, 5*time.Second) {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
		t.Fatalf("sweep removed the directory but left server %d running: a permanent orphan", pid)
	}
}
