package config_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/config"
)

func parsed(t *testing.T, text string) *config.Config {
	t.Helper()

	c, err := config.Parse([]byte(text))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return c
}

const base = `web:
  service: web
  port: 3000
`

func TestDifferencesNamesTheSectionsThatChanged(t *testing.T) {
	mine := parsed(t, base)
	theirs := parsed(t, base+`hooks:
  after_up:
    - "compose exec -T web npm ci"
data:
  service: db
`)

	got := config.Differences(mine, theirs)
	if !slices.Equal(got, []string{"data", "hooks"}) {
		t.Errorf("Differences = %v, want data and hooks", got)
	}
}

func TestDifferencesOfTheSameFile(t *testing.T) {
	// The control: two readings of one file have to compare equal, or
	// every review would report a change.
	if got := config.Differences(parsed(t, base), parsed(t, base)); len(got) != 0 {
		t.Errorf("Differences = %v, want none", got)
	}
}

func TestDifferencesSeesAChangedValue(t *testing.T) {
	mine := parsed(t, base)
	theirs := parsed(t, "web:\n  service: web\n  port: 4000\n")

	if got := config.Differences(mine, theirs); !slices.Equal(got, []string{"web"}) {
		t.Errorf("Differences = %v, want web", got)
	}
}

func TestHostCommandsSeparatesTheSandboxFromTheMachine(t *testing.T) {
	// Everything pit does with a pull request already runs its code
	// inside containers it started from that code. A command without
	// the compose shorthand does not: it runs as the reviewer.
	c := parsed(t, base+`hooks:
  after_up:
    - "compose exec -T web npm ci"
    - "make seed"
data:
  migrate:
    - "compose exec -T web npm run migrate"
  scenarios:
    - name: standard
      apply:
        - "sh -c 'psql < fixtures.sql'"
  snapshot:
    save: "compose exec -T db pg_dump app"
    restore: "compose exec -T db psql app"
`)

	got := c.HostCommands()
	want := []string{"make seed", "sh -c 'psql < fixtures.sql'"}
	if !slices.Equal(got, want) {
		t.Errorf("HostCommands = %v, want %v", got, want)
	}
}

func TestHostCommandsOfAnOrdinaryConfiguration(t *testing.T) {
	// The control: a project that does everything through compose has
	// none, which is the case that must stay quiet.
	c := parsed(t, base+`hooks:
  after_up:
    - "compose exec -T web npm ci"
`)

	if got := c.HostCommands(); len(got) != 0 {
		t.Errorf("HostCommands = %v, want none", got)
	}
}

func TestDifferencesUsesTheFilesOwnWords(t *testing.T) {
	// The names have to be the ones the reader will look for in the
	// file, not Go field names.
	theirs := parsed(t, base+"data:\n  service: db\n")

	for _, name := range config.Differences(parsed(t, base), theirs) {
		if strings.ToLower(name) != name {
			t.Errorf("section %q is not spelled the way the file spells it", name)
		}
	}
}
