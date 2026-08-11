//go:build windows

package unattended

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// tryLockFile takes a non-blocking byte-range lock over the first byte of the
// lock file. Windows byte-range locks are mandatory and per-handle, so this
// excludes other processes and other handles in this process alike.
//
// Only byte 0 is locked, and the lock file is never read: owner evidence lives
// in a separate file precisely so a reader is never refused by the lock it is
// trying to describe.
func tryLockFile(f *os.File, exclusive bool) (bool, error) {
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if exclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	var overlapped windows.Overlapped
	err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, &overlapped)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION), errors.Is(err, windows.ERROR_IO_PENDING):
		return false, nil
	default:
		return false, err
	}
}

func unlockFile(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlapped)
}
