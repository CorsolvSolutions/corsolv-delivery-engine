//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// processAlive reports whether a pid names a live process.
//
// Windows has no signal-0 equivalent, so this opens the process and asks for
// its exit code. STILL_ACTIVE (259) means running. As on Unix this is a
// reporting probe only — arbitration belongs to the worktree lock.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	const processQueryLimitedInformation = 0x1000
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(handle) //nolint:errcheck // nothing actionable on close

	var code uint32
	if err := syscall.GetExitCodeProcess(handle, &code); err != nil {
		return false
	}
	const stillActive = 259
	return code == stillActive
}

// spawnDetached starts the command detached from this console and returns
// immediately.
func spawnDetached(self string, args []string, logFile *os.File) error {
	cmd := exec.Command(self, args...) //nolint:gosec // this executable, with arguments this process composed
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	// A new process group, detached from this console, so the run outlives
	// whatever asked for it.
	const (
		createNewProcessGroup = 0x00000200
		detachedProcess       = 0x00000008
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup | detachedProcess,
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting the detached delivery run: %w", err)
	}
	return cmd.Process.Release()
}
