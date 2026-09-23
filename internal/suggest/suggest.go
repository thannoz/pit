// Package suggest turns a name that does not exist into the one that
// was probably meant.
package suggest

import "strings"

// Closest returns the candidate nearest to name, or an empty string
// when none is near enough to be worth offering.
//
// A wrong suggestion is worse than none: it sends the reader looking
// for something that has nothing to do with what they meant, and the
// list of real names is already in the hint next to it.
func Closest(name string, candidates []string) string {
	if name == "" {
		return ""
	}

	want := strings.ToLower(name)
	budget := budget(len([]rune(want)))

	best, bestAt := "", budget+1
	for _, c := range candidates {
		if c == "" {
			continue
		}
		lower := strings.ToLower(c)

		// A prefix is not a typo but an abbreviation, and answering it
		// is just as useful.
		if lower != want && strings.HasPrefix(lower, want) {
			return c
		}

		// Ties go to the first candidate, which is the order the
		// author wrote them in.
		if d := distance(want, lower); d < bestAt {
			best, bestAt = c, d
		}
	}
	return best
}

// budget is how many edits still count as a typo.
//
// Short names are held to a stricter one: at two edits "db" and "web"
// would be neighbours of nearly everything, and a tool that guesses
// wildly is one people stop reading.
func budget(n int) int {
	switch {
	case n < 3:
		return 0
	case n < 5:
		return 1
	default:
		return 2
	}
}

// distance is the edit distance between two strings, counting a
// transposition as one edit rather than two.
//
// That matters more than it sounds: "wbe" for "web" is the commonest
// typo there is, and under plain Levenshtein it costs two edits, which
// a three-letter name cannot afford.
func distance(a, b string) int {
	ar, br := []rune(a), []rune(b)

	// Three rows: the one before last is what a transposition looks
	// back at.
	prev2 := make([]int, len(br)+1)
	prev := make([]int, len(br)+1)
	cur := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= len(ar); i++ {
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)

			if i > 1 && j > 1 && ar[i-1] == br[j-2] && ar[i-2] == br[j-1] {
				cur[j] = min(cur[j], prev2[j-2]+1)
			}
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[len(br)]
}
