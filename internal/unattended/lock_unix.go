//go:build !windows

package unattended

import (
	"errors"
	"os"
	"syscall"
)

// tryLockFile takes a non-blocking flock. It reports whether the lock was
// taken; contention is a false return, not an error, because "someone else has
// it" is an ordinary answer rather than a fault.
func tryLockFile(f *os.File, exclusive bool) (bool, error) {
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.EWOULDBLOCK), errors.Is(err, syscall.EAGAIN):
		return false, nil
	default:
		return false, err
	}
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
