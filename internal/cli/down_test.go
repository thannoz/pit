package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/runtime"
	"github.com/thannoz/pit/internal/workspace"
)

// atRepo makes the commands believe they were run in a repository.
func atRepo(t *testing.T, host, owner, name string) workspace.Identity {
	t.Helper()

	id := workspace.Identity{Host: host, Owner: owner, Name: name}
	previous := currentRepo
	currentRepo = func(context.Context) (workspace.Repo, error) {
		return workspace.Repo{Root: t.TempDir(), Identity: id}, nil
	}
	t.Cleanup(func() { currentRepo = previous })
	return id
}

// runCLIWithInput is runCLI with an answer ready for a prompt.
func runCLIWithInput(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()

	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)

	err := cmd.Execute()
	return out.String(), err
}

func TestDownNeedsToKnowWhatToRemove(t *testing.T) {
	withManager(t)

	_, err := runCLI(t, "down")
	if err == nil {
		t.Fatal("want an error without a number or --all")
	}
	if !strings.Contains(errs.Hint(err), "--all") {
		t.Errorf("hint = %q, want it to mention --all", errs.Hint(err))
	}
}

func TestDownRefusesANumberTogetherWithAll(t *testing.T) {
	withManager(t)

	_, err := runCLI(t, "down", "482", "--all")
	if err == nil {
		t.Fatal("want an error for a contradictory invocation")
	}
}

func TestDownRemovesOne(t *testing.T) {
	id := atRepo(t, "github.com", "acme", "shop")
	box := recorded(482, id.String(), id.Ref(), "feat/checkout", time.Minute)
	m, fake := withManager(t, box)

	out, err := runCLI(t, "down", "482")
	if err != nil {
		t.Fatalf("down: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Removed #482") {
		t.Errorf("output = %q, want it to confirm the removal", out)
	}
	if !contains(fake.Methods(), "Down") {
		t.Errorf("the runtime was never asked to stop: %v", fake.Methods())
	}

	f, err := m.Store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, still := f.Find(id.Ref(), 482); still {
		t.Error("the record survived the removal")
	}
}

func TestDownOnSomethingThatIsNotThere(t *testing.T) {
	id := atRepo(t, "github.com", "acme", "shop")
	withManager(t, recorded(482, id.String(), id.Ref(), "feat/checkout", time.Minute))

	_, err := runCLI(t, "down", "999")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(errs.Hint(err), "pit ls") {
		t.Errorf("hint = %q, want it to point at pit ls", errs.Hint(err))
	}
}

func TestDownRejectsNonsense(t *testing.T) {
	atRepo(t, "github.com", "acme", "shop")
	withManager(t)

	for _, arg := range []string{"abc", "-5", "0"} {
		t.Run(arg, func(t *testing.T) {
			if _, err := runCLI(t, "down", arg); err == nil {
				t.Errorf("down %q was accepted", arg)
			}
		})
	}
}

// TestDownKeepsTheRecordWhenRemovalFails is the reason the order
// matters: an entry pointing at containers that are still there is more
// useful than no entry at all.
func TestDownKeepsTheRecordWhenRemovalFails(t *testing.T) {
	id := atRepo(t, "github.com", "acme", "shop")
	m, fake := withManager(t, recorded(482, id.String(), id.Ref(), "feat/checkout", time.Minute))
	fake.Fail["Down"] = errors.New("cannot connect to the Docker daemon")

	_, err := runCLI(t, "down", "482")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(errs.Hint(err), "try again") {
		t.Errorf("hint = %q, want it to say the record was kept", errs.Hint(err))
	}

	f, _ := m.Store.Load()
	if _, still := f.Find(id.Ref(), 482); !still {
		t.Error("the record was dropped although the containers are still there")
	}
}

func TestDownAllAsksFirst(t *testing.T) {
	id := atRepo(t, "github.com", "acme", "shop")
	m, _ := withManager(t,
		recorded(482, id.String(), id.Ref(), "a", time.Minute),
		recorded(479, id.String(), id.Ref(), "b", time.Hour),
	)

	// Answering anything but yes must leave everything alone.
	out, err := runCLIWithInput(t, "n\n", "down", "--all")
	if err != nil {
		t.Fatalf("down --all: %v", err)
	}
	if !strings.Contains(out, "Nothing was removed") {
		t.Errorf("output = %q, want it to say nothing happened", out)
	}

	f, _ := m.Store.Load()
	if len(f.Sandboxes) != 2 {
		t.Errorf("%d sandboxes survived, want 2", len(f.Sandboxes))
	}
}

func TestDownAllListsWhatItWouldRemove(t *testing.T) {
	id := atRepo(t, "github.com", "acme", "shop")
	withManager(t,
		recorded(482, id.String(), id.Ref(), "a", time.Minute),
		recorded(479, id.String(), id.Ref(), "b", time.Hour),
	)

	out, _ := runCLIWithInput(t, "n\n", "down", "--all")
	for _, want := range []string{"#482", "#479", "containers, volumes and worktrees"} {
		if !strings.Contains(out, want) {
			t.Errorf("the confirmation is missing %q:\n%s", want, out)
		}
	}
}

func TestDownAllProceedsOnYes(t *testing.T) {
	id := atRepo(t, "github.com", "acme", "shop")
	m, _ := withManager(t,
		recorded(482, id.String(), id.Ref(), "a", time.Minute),
		recorded(479, id.String(), id.Ref(), "b", time.Hour),
	)

	if _, err := runCLIWithInput(t, "y\n", "down", "--all"); err != nil {
		t.Fatalf("down --all: %v", err)
	}

	f, _ := m.Store.Load()
	if len(f.Sandboxes) != 0 {
		t.Errorf("%d sandboxes survived, want none", len(f.Sandboxes))
	}
}

func TestDownAllWithYesSkipsTheQuestion(t *testing.T) {
	id := atRepo(t, "github.com", "acme", "shop")
	m, _ := withManager(t, recorded(482, id.String(), id.Ref(), "a", time.Minute))

	// No input at all: --yes has to be enough.
	if _, err := runCLI(t, "down", "--all", "--yes"); err != nil {
		t.Fatalf("down --all --yes: %v", err)
	}

	f, _ := m.Store.Load()
	if len(f.Sandboxes) != 0 {
		t.Errorf("%d sandboxes survived", len(f.Sandboxes))
	}
}

func TestDownAllWithNothingToDo(t *testing.T) {
	atRepo(t, "github.com", "acme", "shop")
	withManager(t)

	out, err := runCLI(t, "down", "--all")
	if err != nil {
		t.Fatalf("down --all: %v", err)
	}
	if !strings.Contains(out, "No sandboxes") {
		t.Errorf("output = %q", out)
	}
}

// TestDownRemovesTheGeneratedFiles checks the part of the teardown the
// runtime knows nothing about.
func TestDownRemovesTheGeneratedFiles(t *testing.T) {
	id := atRepo(t, "github.com", "acme", "shop")
	box := recorded(482, id.String(), id.Ref(), "feat/checkout", time.Minute)
	m, _ := withManager(t, box)

	repoDir := m.RepoDir(box)
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	override := runtime.OverridePath(repoDir, 482)
	if err := os.WriteFile(override, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := runCLI(t, "down", "482"); err != nil {
		t.Fatalf("down: %v", err)
	}

	if _, err := os.Stat(override); !os.IsNotExist(err) {
		t.Errorf("%s survived the teardown", filepath.Base(override))
	}
	if _, err := os.Stat(repoDir); !os.IsNotExist(err) {
		t.Errorf("the empty repository directory was left behind")
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func TestConfirmationReadsCorrectlyForOneAndMany(t *testing.T) {
	id := atRepo(t, "github.com", "acme", "shop")

	t.Run("one", func(t *testing.T) {
		withManager(t, recorded(482, id.String(), id.Ref(), "a", time.Minute))
		out, _ := runCLIWithInput(t, "n\n", "down", "--all")
		if !strings.Contains(out, "1 sandbox, with its") {
			t.Errorf("output reads wrong for a single sandbox:\n%s", out)
		}
	})

	t.Run("many", func(t *testing.T) {
		withManager(t,
			recorded(482, id.String(), id.Ref(), "a", time.Minute),
			recorded(479, id.String(), id.Ref(), "b", time.Hour),
		)
		out, _ := runCLIWithInput(t, "n\n", "down", "--all")
		if !strings.Contains(out, "2 sandboxes, with their") {
			t.Errorf("output reads wrong for several sandboxes:\n%s", out)
		}
	})
}
