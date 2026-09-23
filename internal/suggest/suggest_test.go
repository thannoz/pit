package suggest

import "testing"

func TestClosest(t *testing.T) {
	scenarios := []string{"leer", "standard", "teilerstattung"}
	services := []string{"web", "db", "redis"}

	tests := []struct {
		name       string
		given      string
		candidates []string
		want       string
	}{
		{"two letters swapped", "tielerstattung", scenarios, "teilerstattung"},
		{"two letters swapped in a short name", "wbe", services, "web"},
		{"a letter transposed", "standrad", scenarios, "standard"},
		{"a letter missing", "standrd", scenarios, "standard"},
		{"a letter too many", "standardd", scenarios, "standard"},
		{"an abbreviation", "teil", scenarios, "teilerstattung"},
		{"different capitalisation", "Standard", scenarios, "standard"},
		{"nothing like any of them", "postgres", services, ""},
		{"a short name that is simply another one", "ab", services, ""},
		{"no candidates at all", "standard", nil, ""},
		{"no name", "", scenarios, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Closest(tc.given, tc.candidates); got != tc.want {
				t.Errorf("Closest(%q) = %q, want %q", tc.given, got, tc.want)
			}
		})
	}
}

func TestClosestPrefersTheFirstOfEquals(t *testing.T) {
	// Deterministic output matters more than which of two equally near
	// names wins, and the author's order is the only order there is.
	candidates := []string{"stage", "state"}

	if got := Closest("stateq", candidates); got != "state" {
		t.Errorf("Closest = %q, want the nearer one", got)
	}
	if got := Closest("staee", candidates); got != "stage" {
		t.Errorf("Closest = %q, want the first of two equally near names", got)
	}
}

func TestDistanceCountsATranspositionOnce(t *testing.T) {
	if d := distance("web", "wbe"); d != 1 {
		t.Errorf("distance = %d, want 1", d)
	}
	if d := distance("", "web"); d != 3 {
		t.Errorf("distance from nothing = %d, want 3", d)
	}
	if d := distance("web", "web"); d != 0 {
		t.Errorf("distance to itself = %d, want 0", d)
	}
}
