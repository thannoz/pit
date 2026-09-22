package config

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/errs"
)

// hasLineNumber reports whether a message points at a line in the file.
var hasLineNumber = regexp.MustCompile(`line \d+`)

// loadBroken writes a configuration into a repository-shaped directory
// and returns the error it produces.
func loadBroken(t *testing.T, yaml string, extra map[string]string) error {
	t.Helper()

	files := map[string]string{FileName: yaml}
	for k, v := range extra {
		files[k] = v
	}
	root := project(t, files)

	_, err := Load(filepath.Join(root, FileName))
	if err == nil {
		t.Fatal("the configuration was accepted, want an error")
	}
	return err
}

// TestFiveBrokenConfigurations is the acceptance criterion for T-203:
// five different mistakes, five different messages, each pointing at a
// line.
func TestFiveBrokenConfigurations(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string // a phrase the message must contain
	}{
		{
			name: "a field name is misspelled",
			yaml: "web:\n  service: web\n  prot: 3000\n",
			want: `unknown field "prot"`,
		},
		{
			name: "the port is out of range",
			yaml: "web:\n  service: web\n  port: 99999\n",
			want: "a port is between 1 and 65535",
		},
		{
			name: "a compose file does not exist",
			yaml: "web:\n  service: web\n  port: 3000\ncompose:\n  files:\n    - docker-compose.yml\n    - docker-compose.missing.yml\n",
			want: `names "docker-compose.missing.yml", which does not exist`,
		},
		{
			name: "the default names a scenario that is not there",
			yaml: "web:\n  service: web\n  port: 3000\ndata:\n  scenarios:\n    - name: leer\n  default: standard\n",
			want: `names "standard", which is not a scenario`,
		},
		{
			name: "polling slower than the timeout",
			yaml: "web:\n  service: web\n  port: 3000\nhealthcheck:\n  timeout: 5s\n  interval: 30s\n",
			want: "only one attempt would ever be made",
		},
	}

	seen := map[string]bool{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := loadBroken(t, tt.yaml, nil)
			msg := err.Error()

			if !strings.Contains(msg, tt.want) {
				t.Errorf("message does not explain the problem:\n%s\nwant it to contain %q", msg, tt.want)
			}
			if !hasLineNumber.MatchString(msg) {
				t.Errorf("message has no line number:\n%s", msg)
			}
			if seen[msg] {
				t.Errorf("this message was already produced by another mistake:\n%s", msg)
			}
			seen[msg] = true

			t.Logf("\n%s", msg)
		})
	}

	if len(seen) != len(tests) {
		t.Errorf("got %d distinct messages for %d mistakes", len(seen), len(tests))
	}
}

func TestUnknownFieldsAreRejected(t *testing.T) {
	// A silently ignored field is worse than a rejected one: the author
	// believes they configured something that has no effect.
	err := loadBroken(t, "web:\n  service: web\n  port: 3000\nnonsense: true\n", nil)

	if !strings.Contains(err.Error(), "unknown field") {
		t.Errorf("error = %q, want it to name the unknown field", err)
	}
	// yaml.v3 talks about Go types the author has never seen.
	if strings.Contains(err.Error(), "config.Config") {
		t.Errorf("error = %q, want it not to mention Go types", err)
	}
}

func TestAllProblemsAreReportedAtOnce(t *testing.T) {
	// Fixing one problem per run would make a file with three mistakes
	// take three runs to sort out.
	err := loadBroken(t, "web:\n  port: 0\nhealthcheck:\n  expect_status: 900\n  url: \"http://localhost/\"\n", nil)

	msg := err.Error()
	for _, want := range []string{"web.service", "web.port", "expect_status", "{port}"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message is missing %q:\n%s", want, msg)
		}
	}
	if !strings.Contains(msg, "problems") {
		t.Errorf("message does not say how many problems there are:\n%s", msg)
	}
}

func TestValidationProblems(t *testing.T) {
	tests := []struct {
		name  string
		yaml  string
		extra map[string]string
		want  string
	}{
		{
			name: "unknown schema version",
			yaml: "version: 99\nweb:\n  service: web\n  port: 3000\n",
			want: "this build of pit understands version 1",
		},
		{
			name: "no web service",
			yaml: "web:\n  port: 3000\n",
			want: "which service a reviewer opens",
		},
		{
			name: "duplicate scenario names",
			yaml: "web:\n  service: web\n  port: 3000\ndata:\n  scenarios:\n    - name: standard\n    - name: standard\n",
			want: "already used by an earlier scenario",
		},
		{
			name: "extends points nowhere",
			yaml: "web:\n  service: web\n  port: 3000\ndata:\n  scenarios:\n    - name: standard\n    - name: extra\n      extends: missing\n",
			want: `names "missing", which is not a scenario`,
		},
		{
			name: "half a snapshot configuration",
			yaml: "web:\n  service: web\n  port: 3000\ndata:\n  snapshot:\n    save: \"pg_dump\"\n",
			want: "snapshots need both",
		},
		{
			name: "env template does not exist",
			yaml: "web:\n  service: web\n  port: 3000\nenv:\n  from_file: .env.pit.example\n",
			want: `names ".env.pit.example", which does not exist`,
		},
		{
			name: "a duration written as a bare number",
			yaml: "web:\n  service: web\n  port: 3000\nhealthcheck:\n  timeout: 120\n",
			want: "is not a duration",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := loadBroken(t, tt.yaml, tt.extra)
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("message does not explain the problem:\n%s\nwant it to contain %q", err, tt.want)
			}
			if errs.Hint(err) == "" {
				t.Error("the error carries no hint")
			}
		})
	}
}

func TestLineNumbersPointAtTheRightLine(t *testing.T) {
	// The line number is the whole value of the message; if it is off
	// by one the user looks at the wrong place and trusts it less.
	err := loadBroken(t, "# a comment\nweb:\n  service: web\n  port: 0\n", nil)

	if !strings.Contains(err.Error(), "line 4") {
		t.Errorf("want the problem reported on line 4:\n%s", err)
	}
}

func TestScenarioHintDoesNotRepeatDuplicates(t *testing.T) {
	// The duplicate is already reported as its own problem; echoing it
	// in the hint reads as a bug in pit.
	err := loadBroken(t, "web:\n  service: web\n  port: 3000\ndata:\n  scenarios:\n    - name: standard\n    - name: standard\n  default: missing\n", nil)

	if strings.Contains(err.Error(), "standard, standard") {
		t.Errorf("the hint repeats a duplicate name:\n%s", err)
	}
}

func TestScenarioHintWhenNoneAreConfigured(t *testing.T) {
	err := loadBroken(t, "web:\n  service: web\n  port: 3000\ndata:\n  default: standard\n", nil)

	if !strings.Contains(err.Error(), "none are configured") {
		t.Errorf("want the hint to say there are no scenarios:\n%s", err)
	}
}
