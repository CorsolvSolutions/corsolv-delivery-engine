//go:build windows

package unattended

import (
	"os/exec"

	"golang.org/x/sys/windows"
)

// superviseProcess makes a command killable as a whole tree.
//
// Windows has no process groups in the POSIX sense, and killing a process here
// does not kill its descendants. Creating the child in a new console process
// group at least makes it the root of one, so it does not inherit — and cannot
// be disturbed by — this process's group.
//
// The descendant case is covered by the WaitDelay the caller sets: after the
// timeout fires, Wait stops waiting on pipes an orphan is still holding open,
// so the run always regains control even when a grandchild survives. That is
// the property that matters — an unattended run must never be held open by
// something nobody is watching.
func superviseProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &windows.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
}
