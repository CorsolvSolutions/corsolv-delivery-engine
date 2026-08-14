//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// processAlive reports whether a pid names a live process.
//
// This is a REPORTING probe, never an arbitration one. Deciding that another
// run has died and its work may be taken over is the worktree lock's job, and
// the lock deliberately refuses to read a pid out of a file to make that call.
// Here the question is only the one a person asks when looking in on a run —
// "is it still going?" — and answering it from the process table is what the
// project's own rule asks for: query live state, never trust a status file.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 performs the permission and existence checks without delivering
	// anything. EPERM means the process exists and belongs to someone else.
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}

// spawnDetached starts the command in its own process group and returns
// immediately.
//
// The new process group is the point. Without it the child dies with whatever
// shell, portal request or terminal launched it, which would make a delivery
// run last exactly as long as the browser tab that asked for it.
func spawnDetached(self string, args []string, logFile *os.File) error {
	cmd := exec.Command(self, args...) //nolint:gosec // this executable, with arguments this process composed
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting the detached delivery run: %w", err)
	}
	// Release rather than Wait: this process is about to exit and the run must
	// not be reparented to a waiter that is going away.
	return cmd.Process.Release()
}
