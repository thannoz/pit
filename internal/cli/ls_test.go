package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/runtime"
	"github.com/thannoz/pit/internal/runtime/runtimetest"
	"github.com/thannoz/pit/internal/sandbox"
	"github.com/thannoz/pit/internal/state"
)

// withManager replaces the manager the commands use for the duration of
// a test, and returns the fake runtime so the test can steer it.
func withManager(t *testing.T, boxes ...state.Sandbox) (*sandbox.Manager, *runtimetest.Fake) {
	t.Helper()

	dir := t.TempDir()
	store, err := state.Open(dir)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	if len(boxes) > 0 {
		err := store.Update(func(f *state.File) error {
			for _, b := range boxes {
				f.Put(b)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
	}

	fake := runtimetest.New("web")
	m := &sandbox.Manager{Store: store, Runtime: fake, StateDir: dir}

	previous := manager
	manager = func() (*sandbox.Manager, error) { return m, nil }
	t.Cleanup(func() { manager = previous })

	return m, fake
}

func recorded(pr int, repo, ref, branch string, age time.Duration) state.Sandbox {
	return state.Sandbox{
		PR:           pr,
		Repo:         repo,
		RepoRef:      ref,
		Project:      "pit-" + ref + "-" + strconv.Itoa(pr),
		ComposeFiles: []string{"docker-compose.yml"},
		Worktree:     "/state/" + ref + "/pr-" + strconv.Itoa(pr),
		Port:         40000 + pr,
		URL:          "http://localhost:" + strconv.Itoa(40000+pr),
		Branch:       branch,
		Title:        "Some change",
		CreatedAt:    time.Now().Add(-age),
	}
}

func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()

	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)

	err := cmd.Execute()
	return out.String(), err
}

// TestLsShowsThreeSandboxes is the acceptance criterion for T-308.
func TestLsShowsThreeSandboxes(t *testing.T) {
	_, fake := withManager(t,
		recorded(482, "github.com/acme/shop", "acme-shop-c56680", "feat/checkout", 4*time.Minute),
		recorded(479, "github.com/acme/shop", "acme-shop-c56680", "fix/tax-rounding", 90*time.Minute),
		recorded(12, "github.com/acme/admin", "acme-admin-9f2b1a", "chore/deps", 50*time.Hour),
	)
	for _, ref := range []string{"acme-shop-c56680", "acme-admin-9f2b1a"} {
		_ = ref
	}
	// Bring them all up so the runtime reports them as running.
	for _, project := range []string{
		"pit-acme-shop-c56680-482", "pit-acme-shop-c56680-479", "pit-acme-admin-9f2b1a-12",
	} {
		if err := fake.Up(t.Context(), runtime.Sandbox{Project: project}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatalf("Up: %v", err)
		}
	}

	out, err := runCLI(t, "ls")
	if err != nil {
		t.Fatalf("ls: %v\n%s", err, out)
	}

	for _, want := range []string{
		"#482", "feat/checkout", "http://localhost:40482", "4m",
		"#479", "fix/tax-rounding", "1h",
		"#12", "chore/deps", "2d",
		"running",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the listing is missing %q:\n%s", want, out)
		}
	}

	// Two repositories are involved, so the listing has to say which.
	if !strings.Contains(out, "REPO") {
		t.Errorf("the listing has no repository column although two are involved:\n%s", out)
	}
}

func TestLsOmitsTheRepositoryColumnForASingleProject(t *testing.T) {
	// With one project it would be the same word on every line.
	withManager(t, recorded(482, "github.com/acme/shop", "acme-shop-c56680", "feat/checkout", time.Minute))

	out, err := runCLI(t, "ls")
	if err != nil {
		t.Fatalf("ls: %v", err)
	}
	if strings.Contains(out, "REPO") {
		t.Errorf("the listing has a repository column for a single project:\n%s", out)
	}
}

func TestLsWithNothingToShow(t *testing.T) {
	withManager(t)

	out, err := runCLI(t, "ls")
	if err != nil {
		t.Fatalf("ls: %v", err)
	}
	if !strings.Contains(out, "No sandboxes") {
		t.Errorf("output = %q, want it to say there is nothing", out)
	}
	if !strings.Contains(out, "pit <pull request number>") {
		t.Errorf("output = %q, want it to say what to do next", out)
	}
}

func TestLsJSON(t *testing.T) {
	_, fake := withManager(t, recorded(482, "github.com/acme/shop", "acme-shop-c56680", "feat/checkout", time.Minute))
	if err := fake.Up(t.Context(), runtime.Sandbox{Project: "pit-acme-shop-c56680-482"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Up: %v", err)
	}

	out, err := runCLI(t, "ls", "--json")
	if err != nil {
		t.Fatalf("ls --json: %v", err)
	}

	var rows []lsRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].PR != 482 || rows[0].Status != "running" {
		t.Errorf("row = %+v, want #482 running", rows[0])
	}
	if _, err := time.Parse(time.RFC3339, rows[0].CreatedAt); err != nil {
		t.Errorf("CreatedAt = %q, want RFC3339 so a script can parse it", rows[0].CreatedAt)
	}
}

func TestLsReportsAStoppedSandbox(t *testing.T) {
	// A sandbox whose containers have exited looks identical to a
	// running one in the record; only the runtime knows.
	withManager(t, recorded(482, "github.com/acme/shop", "acme-shop-c56680", "feat/checkout", time.Minute))

	out, err := runCLI(t, "ls")
	if err != nil {
		t.Fatalf("ls: %v", err)
	}
	if !strings.Contains(out, "stopped") && !strings.Contains(out, "gone") {
		t.Errorf("a sandbox that was never started reports as:\n%s", out)
	}
}

func TestLsSurvivesAnUnreachableRuntime(t *testing.T) {
	// `pit ls` has to work when Docker is not running: knowing what
	// was recorded is useful even then.
	_, fake := withManager(t, recorded(482, "github.com/acme/shop", "acme-shop-c56680", "feat/checkout", time.Minute))
	fake.Fail["Status"] = errors.New("cannot connect to the Docker daemon")

	out, err := runCLI(t, "ls")
	if err != nil {
		t.Fatalf("ls failed although only the runtime is unreachable: %v", err)
	}
	if !strings.Contains(out, "#482") {
		t.Errorf("the recorded sandbox is missing:\n%s", out)
	}
	if !strings.Contains(out, "unknown") {
		t.Errorf("the status is not reported as unknown:\n%s", out)
	}
	if !strings.Contains(out, "Docker daemon") {
		t.Errorf("the reason is not shown:\n%s", out)
	}
}

func TestShortDuration(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{4 * time.Minute, "4m"},
		{90 * time.Minute, "1h"},
		{50 * time.Hour, "2d"},
		{-time.Second, "0s"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := shortDuration(tt.in); got != tt.want {
				t.Errorf("shortDuration(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
