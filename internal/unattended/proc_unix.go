//go:build !windows

package unattended

import (
	"errors"
	"os/exec"
	"syscall"
)

// superviseProcess makes a command killable as a whole tree.
//
// It exists because of a defect this package's own timeout test caught: a task
// declared `sh -c "sleep 60"` with a one-second timeout ran for the full sixty.
// Canceling the context kills the shell, but the `sleep` it spawned inherits
// the output pipes and keeps them open, so the run waits on a grandchild nobody
// is watching. An unattended run that can be held open by an orphaned
// descendant has no working timeout at all.
//
// Putting the command in its own process group and signaling the group closes
// that hole: the shell, the sleep, and anything either of them started go
// together.
func superviseProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Negative PID signals the whole process group. ESRCH means the group
		// is already gone, which is the outcome that was wanted.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return cmd.Process.Kill()
		}
		return nil
	}
}
