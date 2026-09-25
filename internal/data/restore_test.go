package data_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/data"
	"github.com/thannoz/pit/internal/proc"
)

// feeding remembers each command and what it got on stdin.
type feeding struct {
	mu   sync.Mutex
	ran  []string
	fed  []string
	fail string // a command containing this fails
}

func (f *feeding) Stream(_ context.Context, c proc.Command, _, _ io.Writer) error {
	var in string
	if c.Stdin != nil {
		b, err := io.ReadAll(c.Stdin)
		if err != nil {
			return err
		}
		in = string(b)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	line := strings.Join(c.Args, " ")
	f.ran = append(f.ran, line)
	f.fed = append(f.fed, in)
	if f.fail != "" && strings.Contains(line, f.fail) {
		return io.ErrUnexpectedEOF
	}
	return nil
}

const promotedScenarios = `version: 1
web: {service: web, port: 80}
data:
  migrate: ["compose exec -T db migrate"]
  snapshot:
    save: "compose exec -T db dump"
    restore: "compose exec -T db load"
  scenarios:
    - name: voucher
      snapshot: fixtures/voucher.sql
    - name: voucher-expired
      extends: voucher
      apply: ["compose exec -T db expire"]
`

func promotedConfig(t *testing.T) *config.Config {
	t.Helper()
	root := t.TempDir()
	for name, content := range map[string]string{
		".git": "", "docker-compose.yml": "services:\n  web:\n    image: nginx\n",
		".pit.yaml": promotedScenarios, "fixtures/voucher.sql": "-- the voucher case\n",
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c, err := config.Load(filepath.Join(root, ".pit.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A promoted snapshot is loaded through the restore command, then the
// schema is brought to this commit, then what builds on it runs.
func TestAPromotedScenarioIsRestoredThenMigrated(t *testing.T) {
	c := promotedConfig(t)
	sc, err := data.Select(c, "voucher-expired")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	f := &feeding{}
	if err := (data.Commands{Runner: f}).Apply(t.Context(), sandbox(), sc, io.Discard, io.Discard); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(f.ran) != 3 {
		t.Fatalf("ran %q", f.ran)
	}
	for i, want := range []string{"exec -T db load", "exec -T db migrate", "exec -T db expire"} {
		if !strings.HasSuffix(f.ran[i], want) {
			t.Errorf("command %d = %q, want it to end in %q", i, f.ran[i], want)
		}
	}
	if f.fed[0] != "-- the voucher case\n" || f.fed[1] != "" {
		t.Errorf("fed %q", f.fed)
	}
}

func TestAPromotedScenarioWhoseFileIsGone(t *testing.T) {
	c := promotedConfig(t)
	sc, err := data.Select(c, "voucher")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sc.Steps[0].Restores[0].File); err != nil {
		t.Fatal(err)
	}
	f := &feeding{}
	err = (data.Commands{Runner: f}).Apply(t.Context(), sandbox(), sc, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), `scenario "voucher" loads voucher.sql, which cannot be read`) {
		t.Errorf("err = %v", err)
	}
	if len(f.ran) != 0 {
		t.Errorf("ran %q without the data", f.ran)
	}
}

func TestAPromotedScenarioThatFailsToLoadStops(t *testing.T) {
	c := promotedConfig(t)
	sc, err := data.Select(c, "voucher-expired")
	if err != nil {
		t.Fatal(err)
	}
	f := &feeding{fail: "load"}
	err = (data.Commands{Runner: f}).Apply(t.Context(), sandbox(), sc, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), `loading voucher.sql for scenario "voucher" failed`) {
		t.Errorf("err = %v", err)
	}
	if len(f.ran) != 1 {
		t.Errorf("went on after the load failed: %q", f.ran)
	}
}

// Shown as what it does, for the listings that show commands.
func TestAPromotedScenarioListsItsLoad(t *testing.T) {
	c := promotedConfig(t)
	sc, err := data.Select(c, "voucher")
	if err != nil {
		t.Fatal(err)
	}
	// Nothing to apply, and still something to load.
	if sc.Empty() {
		t.Error("a scenario that loads a snapshot is not empty")
	}
	got := sc.Commands()
	if len(got) != 1 || !strings.HasPrefix(got[0], "compose exec -T db load < ") || !strings.HasSuffix(got[0], "voucher.sql") {
		t.Errorf("Commands() = %q", got)
	}
}

// A pull request's .pit.yaml names the file, pit reads it on the
// reviewer's machine: a link out of the repository is not followed.
func TestAPromotedScenarioDoesNotFollowALinkOutOfTheRepository(t *testing.T) {
	c := promotedConfig(t)
	secret := filepath.Join(t.TempDir(), "id_rsa")
	if err := os.WriteFile(secret, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	sc, err := data.Select(c, "voucher")
	if err != nil {
		t.Fatal(err)
	}
	file := sc.Steps[0].Restores[0].File
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, file); err != nil {
		t.Fatal(err)
	}
	f := &feeding{}
	err = (data.Commands{Runner: f}).Apply(t.Context(), sandbox(), sc, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "leads outside the repository") {
		t.Errorf("err = %v", err)
	}
	for _, fed := range f.fed {
		if strings.Contains(fed, "PRIVATE KEY") {
			t.Fatal("the file behind the link went into the sandbox")
		}
	}

	// A link inside the repository is only a link.
	inside := filepath.Join(filepath.Dir(file), "real.sql")
	if err := os.WriteFile(inside, []byte("-- inside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.sql", file); err != nil {
		t.Fatal(err)
	}
	f = &feeding{}
	if err := (data.Commands{Runner: f}).Apply(t.Context(), sandbox(), sc, io.Discard, io.Discard); err != nil || f.fed[0] != "-- inside\n" {
		t.Errorf("err = %v, fed %q", err, f.fed)
	}
}
