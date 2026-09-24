// Package glob matches slash-separated paths against patterns in which
// ** stands for any number of directories.
//
// The standard library's path.Match has no **, and every pattern a
// person writes for "all tests" or "everything under docs" needs one.
// Within a single segment the rules are path.Match's own, so *, ? and
// [...] mean what they mean everywhere else in Go.
package glob

import (
	"path"
	"strings"
)

// Match reports whether name matches pattern.
//
// ** as a whole segment matches zero or more segments: "docs/**" is
// everything under docs, "**/*.test.ts" is a test file at any depth,
// and "a/**/b" includes "a/b". A malformed pattern matches nothing;
// Valid is how to find that out before it matters.
func Match(pattern, name string) bool {
	return match(split(pattern), split(name))
}

// Valid reports whether a pattern can match anything at all. It exists
// so that a typo in a configured pattern is found when the file is
// read, rather than as a file that was never ignored.
func Valid(pattern string) error {
	for _, seg := range split(pattern) {
		if seg == "**" {
			continue
		}
		if _, err := path.Match(seg, ""); err != nil {
			return err
		}
	}
	return nil
}

func split(s string) []string {
	return strings.Split(strings.Trim(s, "/"), "/")
}

func match(pattern, name []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			// Consecutive ** are one **.
			for len(pattern) > 1 && pattern[1] == "**" {
				pattern = pattern[1:]
			}
			rest := pattern[1:]
			if len(rest) == 0 {
				return true
			}
			// Try every way of letting ** swallow segments, from none
			// to all of them.
			for i := 0; i <= len(name); i++ {
				if match(rest, name[i:]) {
					return true
				}
			}
			return false
		}

		if len(name) == 0 {
			return false
		}
		ok, err := path.Match(pattern[0], name[0])
		if err != nil || !ok {
			return false
		}
		pattern, name = pattern[1:], name[1:]
	}
	return len(name) == 0
}
