//go:build !unix && !windows

package state

import "os"

// A system that is neither Unix nor Windows has no lock pit knows how
// to take; these keep the package compiling. There, the state file is
// not protected against a second pit running at the same time.

func lockFile(*os.File) error { return nil }

func tryLockFile(*os.File) error { return nil }

func unlockFile(*os.File) error { return nil }
