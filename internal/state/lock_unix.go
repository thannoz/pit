//go:build unix

package state

import (
	"os"
	"syscall"
)

// lockFile takes an exclusive lock on an open file, waiting until it is
// free. flock is per open file description, so two processes -- and two
// separately opened handles in one process -- exclude each other.
func lockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

// tryLockFile takes the lock if it is free and reports failure rather
// than waiting.
func tryLockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
