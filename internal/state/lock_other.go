//go:build !unix

package state

import "os"

// Windows needs LockFileEx rather than flock. pit does not target it
// yet (see P10); these keep the package compiling so that porting is a
// matter of filling them in rather than untangling the call sites.
//
// Until then the state file is not protected against a second pit
// running at the same time on Windows.

func lockFile(*os.File) error { return nil }

func tryLockFile(*os.File) error { return nil }

func unlockFile(*os.File) error { return nil }
