//go:build integration

package unattended

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// These cases need a real operating-system process and a real socket, so they
// carry the integration tag. The resource census ratchets untagged subprocess,
// fixed-sleep and listener call sites and will not let them grow, and it is
// right that it does not — what is proved here cannot be proved without the real
// thing, and the tag is what says so.
//
// The child is spawned through runProbe rather than through a fresh
// exec.Command, because the all-source census counts call sites too and a new
// one would grow a baseline this work has no business moving. runProbe already
// owns a spawn site, bounds it, and reports a non-zero exit as an answer rather
// than an error — which is exactly the shape this test wants.

const (
	lockHelperEnv    = "GC_UNATTENDED_LOCK_HELPER"
	lockHelperDirEnv = "GC_UNATTENDED_LOCK_DIR"
	lockHelperDenied = 3
)

// TestLockHelperProcess is not a test. It is the body of the child process the
// cross-process case re-executes, and it is skipped in every ordinary run.
//
// The in-process cases prove the lock excludes concurrent goroutines. On Linux
// that is already the same kernel path — flock is per open-file-description, so
// two goroutines opening the file contend exactly as two processes do — but the
// property that matters is stated about processes, so it is proved about
// processes.
func TestLockHelperProcess(t *testing.T) {
	if os.Getenv(lockHelperEnv) == "" {
		t.Skip("helper process body; not a standalone test")
	}
	lk, err := Acquire(os.Getenv(lockHelperDirEnv), testOwner(RoleWriter, "child-"+fmt.Sprint(os.Getpid())))
	if err != nil {
		fmt.Println("DENIED")
		os.Exit(lockHelperDenied)
	}
	fmt.Println("ACQUIRED")
	_ = lk.Release()
	os.Exit(0)
}

func TestAcquireAcrossProcessesIsDenied(t *testing.T) {
	dir := t.TempDir()
	held, err := Acquire(dir, testOwner(RoleWriter, "parent"))
	if err != nil {
		t.Fatalf("parent acquire: %v", err)
	}
	defer held.Release() //nolint:errcheck

	// runProbe passes this process's environment to the child, so setting it
	// here is what arms the helper.
	t.Setenv(lockHelperEnv, "1")
	t.Setenv(lockHelperDirEnv, dir)

	out, ok, err := runProbe(context.Background(), 60*time.Second, "",
		[]string{os.Args[0], "-test.run=^TestLockHelperProcess$", "-test.timeout=60s"})
	if err != nil {
		t.Fatalf("spawning the child: %v", err)
	}
	if ok {
		t.Fatalf("a second OS process acquired a worktree this one holds:\n%s", out)
	}
	if !strings.Contains(out, "DENIED") {
		t.Fatalf("the child did not report denial:\n%s", out)
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
