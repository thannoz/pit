package proc

import (
	"strings"
	"testing"
)

// echoEnv asks the shell what a variable is set to inside the child.
func echoEnv(t *testing.T, name string, extra ...string) string {
	t.Helper()

	out, err := Exec{}.Output(t.Context(), Command{
		Name: "sh",
		Args: []string{"-c", "echo \"$" + name + "\""},
		Env:  extra,
	})
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// TestStableLocaleOverridesTheParent is the acceptance criterion for
// T-107: whatever language the machine is set to, the child speaks C.
func TestStableLocaleOverridesTheParent(t *testing.T) {
	t.Setenv("LC_ALL", "de_DE.UTF-8")
	t.Setenv("LANG", "de_DE.UTF-8")
	t.Setenv("LANGUAGE", "de")

	tests := []struct {
		name string
		want string
	}{
		{"LC_ALL", "C"},
		{"LANG", "C"},
		// gettext gives LANGUAGE precedence over LC_ALL, so leaving it
		// set would keep the messages translated regardless.
		{"LANGUAGE", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := echoEnv(t, tt.name); got != tt.want {
				t.Errorf("%s = %q inside the child, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestCallerCanStillOverrideTheLocale(t *testing.T) {
	// The stable locale is a default, not a cage: it is appended before
	// the caller's own entries, and the last one wins.
	if got := echoEnv(t, "LC_ALL", "LC_ALL=de_DE.UTF-8"); got != "de_DE.UTF-8" {
		t.Errorf("LC_ALL = %q, want the caller's value", got)
	}
}

func TestParentEnvironmentIsStillInherited(t *testing.T) {
	t.Setenv("PIT_INHERITED", "yes")

	if got := echoEnv(t, "PIT_INHERITED"); got != "yes" {
		t.Errorf("PIT_INHERITED = %q, want it inherited from the parent", got)
	}
}

func TestEnvironmentOrdering(t *testing.T) {
	env := environment([]string{"CUSTOM=1"})

	last := func(prefix string) int {
		idx := -1
		for i, e := range env {
			if strings.HasPrefix(e, prefix) {
				idx = i
			}
		}
		return idx
	}

	locale, custom := last("LC_ALL="), last("CUSTOM=")
	if locale < 0 || custom < 0 {
		t.Fatalf("environment() = %v, missing an expected entry", env)
	}
	if custom < locale {
		t.Errorf("the caller's entry is at %d, before the locale at %d; it would be ignored", custom, locale)
	}
}
