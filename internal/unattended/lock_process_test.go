//go:build integration

package unattended

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// These cases need real operating-system processes and a real socket, so they
// carry the integration tag: the resource census ratchets untagged subprocess,
// fixed-sleep and listener call sites and will not let them grow, and it is
// right that it does not. What is proved here cannot be proved without the real
// thing, which is exactly what the tag is for.

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

func TestPortChecks(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}
	defer ln.Close() //nolint:errcheck
	live := ln.Addr().String()

	spare, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}
	dead := spare.Addr().String()
	spare.Close() //nolint:errcheck

	cs := PortChecks([]PortRequirement{
		{Address: live},
		{Address: dead, TimeoutSeconds: 1},
	})
	if cs[0].Outcome != OutcomePass {
		t.Fatalf("reachable port = %s, want pass", cs[0].Outcome)
	}
	if cs[1].Outcome != OutcomeFail {
		t.Fatalf("unreachable port = %s, want fail", cs[1].Outcome)
	}
}
