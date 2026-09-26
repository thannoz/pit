package sandbox_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/runtime"
	"github.com/thannoz/pit/internal/runtime/runtimetest"
)

// vault answers for the secrets it holds, by command, and notes what
// it was asked.
type vault struct {
	values map[string]string
	asked  []string
}

func (v *vault) Output(_ context.Context, c proc.Command) ([]byte, error) {
	v.asked = append(v.asked, c.String())
	if value, ok := v.values[c.String()]; ok {
		return []byte(value), nil
	}
	return nil, errors.New("vault exited with code 2\n    * permission denied")
}

const secretYAML = "web:\n  service: web\n  port: 80\nenv:\n  secrets:\n    STRIPE_KEY: \"vault://secret/shop#stripe\"\n"

func secretFixture(t *testing.T) (*vault, func(yaml string) *config.Config) {
	t.Helper()
	v := &vault{values: map[string]string{"vault kv get -field=stripe secret/shop": "sk_test_4242\n"}}
	return v, func(yaml string) *config.Config {
		c, err := config.Parse([]byte(yaml))
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
}

// The services get the secret; nothing pit keeps does.
func TestSecretsReachTheServicesAndNoFile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, fake := upFixture(t)
	v, parse := secretFixture(t)
	m.Secrets = v
	req.Config = parse(secretYAML)
	rep := &quietReporter{}
	box, err := m.Up(t.Context(), req, rep)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	var up []map[string]string
	for _, c := range fake.Calls() {
		if c.Method == "Up" {
			up = append(up, c.Secrets)
		}
	}
	if len(up) != 1 || up[0]["STRIPE_KEY"] != "sk_test_4242" || len(up[0]) != 1 {
		t.Errorf("up got %v", up)
	}
	if i := slices.Index(rep.begun, "secrets"); i < 0 || rep.steps[i] != "1 secret, from Vault" || i > slices.Index(rep.begun, "build") {
		t.Errorf("steps = %q / %q", rep.begun, rep.steps)
	}
	override, err := os.ReadFile(box.ComposeFiles[len(box.ComposeFiles)-1])
	if err != nil || !strings.Contains(string(override), `STRIPE_KEY: "${PIT_SECRET_STRIPE_KEY:-}"`) {
		t.Errorf("override = %s, %v", override, err)
	}
	_ = filepath.WalkDir(m.StateDir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if data, _ := os.ReadFile(path); strings.Contains(string(data), "sk_test_4242") {
				t.Errorf("%s holds the secret", path)
			}
		}
		return nil
	})
}

// A store that says no stops the sandbox before anything is built,
// with what it said.
func TestASecretThatCannotBeFetched(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, fake := upFixture(t)
	v, parse := secretFixture(t)
	v.values = nil
	m.Secrets = v
	req.Config = parse(secretYAML)
	_, err := m.Up(t.Context(), req, &quietReporter{})
	if err == nil || !strings.Contains(err.Error(), "cannot fetch STRIPE_KEY from Vault") || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err = %v", err)
	}
	if methods := fake.Methods(); slices.Contains(methods, "Build") || slices.Contains(methods, "Up") {
		t.Errorf("went on: %q", methods)
	}
}

// A pull request that names a secret the reviewer's checkout does not,
// or takes one from somewhere else, is asked about before anything is
// fetched: it could name any secret the reviewer can read.
func TestASecretOnlyThePullRequestNamesIsAskedAbout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	for name, mine := range map[string]string{
		"none of mine": "web:\n  service: web\n  port: 80\n",
		"another one":  strings.Replace(secretYAML, "secret/shop#stripe", "secret/shop#stripe_test", 1),
	} {
		m, req, fake := upFixture(t)
		v, parse := secretFixture(t)
		m.Secrets = v
		req.Config = parse(mine)
		pushToPullRequest(t, req.Repo.Root, 7, map[string]string{".pit.yaml": secretYAML})
		rep := &quietReporter{}
		asked := ""
		req.Confirm = func(q string) bool { asked = q; return false }
		_, err := m.Up(t.Context(), req, rep)
		if err == nil || !strings.Contains(err.Error(), "stopped before fetching #7's secrets") || asked != "Give it to #7?" {
			t.Errorf("%s: err = %v, asked %q", name, err, asked)
		}
		if !slices.Contains(rep.notes, "    STRIPE_KEY from vault://secret/shop#stripe") {
			t.Errorf("%s: notes = %q", name, rep.notes)
		}
		if len(v.asked) != 0 || slices.Contains(fake.Methods(), "Up") {
			t.Errorf("%s: fetched %q, ran %q", name, v.asked, fake.Methods())
		}

		// Without anyone to ask, the same.
		req.Confirm = nil
		if _, err := m.Up(t.Context(), req, &quietReporter{}); err == nil || !strings.Contains(err.Error(), "#7 wants 1 secret of yours") {
			t.Errorf("%s: err = %v", name, err)
		}
	}

	// Answered yes, it goes on and fetches it.
	m, req, fake := upFixture(t)
	v, parse := secretFixture(t)
	m.Secrets = v
	req.Config = parse("web:\n  service: web\n  port: 80\n")
	pushToPullRequest(t, req.Repo.Root, 7, map[string]string{".pit.yaml": secretYAML})
	req.Confirm = func(string) bool { return true }
	if _, err := m.Up(t.Context(), req, &quietReporter{}); err != nil || len(v.asked) != 1 || !slices.Contains(fake.Methods(), "Up") {
		t.Errorf("err = %v, fetched %q", err, v.asked)
	}
}

// The runtime for processes is given them too.
func TestSecretsReachProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req := processFixture(t, "web: node index.js\n", "processes: {file: Procfile}\nweb: {service: web}\nenv:\n  secrets:\n    STRIPE_KEY: \"vault://secret/shop#stripe\"\n")
	v, _ := secretFixture(t)
	m.Secrets = v
	req.Confirm = func(string) bool { return true }
	processes := runtimeFake()
	m.Runtime = runtime.Either{Compose: runtimeFake(), Processes: processes}
	if _, err := m.Up(t.Context(), req, &quietReporter{}); err != nil {
		t.Fatalf("Up: %v", err)
	}
	up := 0
	for _, c := range processes.Calls() {
		if c.Method == "Up" {
			up++
			if c.Secrets["STRIPE_KEY"] != "sk_test_4242" {
				t.Errorf("up got %v", c.Secrets)
			}
		}
	}
	if up != 1 {
		t.Errorf("up %d times", up)
	}
}

func runtimeFake() *runtimetest.Fake { return runtimetest.New("web") }
