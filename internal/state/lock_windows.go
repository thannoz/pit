//go:build windows

package state

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockFile takes an exclusive lock on an open file, waiting until it is
// free. LockFileEx is per handle, so two processes -- and two separately
// opened handles in one process -- exclude each other, as flock's do.
func lockFile(f *os.File) error {
	return lockEx(f, windows.LOCKFILE_EXCLUSIVE_LOCK)
}

// tryLockFile takes the lock if it is free and reports failure rather
// than waiting.
func tryLockFile(f *os.File) error {
	return lockEx(f, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY)
}

func unlockFile(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, new(windows.Overlapped))
}

// lockEx locks the file's first byte, which is what stands for the
// whole file: every pit locks the same one.
func lockEx(f *os.File, flags uint32) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, new(windows.Overlapped))
}
