package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/runtime"
	"github.com/thannoz/pit/internal/sandbox"
	"github.com/thannoz/pit/internal/state"
)

func TestLogsNeedsASandbox(t *testing.T) {
	atRepo(t, "github.com", "acme", "shop")
	withManager(t)

	_, err := runCLI(t, "logs", "482")
	if err == nil {
		t.Fatal("want an error for a sandbox that does not exist")
	}
	if !strings.Contains(errs.Hint(err), "pit ls") {
		t.Errorf("hint = %q, want it to point at pit ls", errs.Hint(err))
	}
}

func TestLogsRejectsNonsense(t *testing.T) {
	atRepo(t, "github.com", "acme", "shop")
	withManager(t)

	for _, arg := range []string{"abc", "0", "-2"} {
		t.Run(arg, func(t *testing.T) {
			if _, err := runCLI(t, "logs", arg); err == nil {
				t.Errorf("logs %q was accepted", arg)
			}
		})
	}
}

func TestServiceDefaultsToTheOneAReviewerOpens(t *testing.T) {
	box := state.Sandbox{WebService: "web"}

	if got := serviceArg([]string{"482"}, box); got != "web" {
		t.Errorf("serviceArg = %q, want the recorded web service", got)
	}
	if got := serviceArg([]string{"482", "db"}, box); got != "db" {
		t.Errorf("serviceArg = %q, want the one that was asked for", got)
	}
}

func TestComposeCommandCarriesTheSandbox(t *testing.T) {
	// logs and shell are little more than this call; if it lost the
	// project name they would act on the wrong sandbox.
	box := state.Sandbox{
		Project:      "pit-acme-shop-c56680-482",
		ComposeFiles: []string{"docker-compose.yml", "override.yml"},
		Worktree:     "/state/pr-482",
	}

	cmd := runtime.ComposeCommand(sandbox.RuntimeSandbox(box), "logs", "--follow")

	joined := strings.Join(cmd.Args, " ")
	for _, want := range []string{"--project-name pit-acme-shop-c56680-482", "override.yml", "logs", "--follow"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q are missing %q", joined, want)
		}
	}
	if cmd.Dir != "/state/pr-482" {
		t.Errorf("Dir = %q, want the worktree", cmd.Dir)
	}
}

// TestInterruptionIsNotAFailure covers what `pit logs -f` is for: being
// interrupted is how it ends, not something to report.
func TestInterruptionIsNotAFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := ignoreInterrupt(ctx, errs.New("exit status 130")); err != nil {
		t.Errorf("err = %v, want the interruption swallowed", err)
	}
}

func TestARealFailureIsStillAFailure(t *testing.T) {
	boom := errs.New("the daemon is not running")

	if err := ignoreInterrupt(t.Context(), boom); err == nil {
		t.Error("a genuine failure was swallowed")
	}
}

func TestMissingExecutableIsRecognisedByExitCode(t *testing.T) {
	// An attached command writes its own message to the terminal, so
	// the exit code is all that reaches the error.
	tests := []struct {
		msg  string
		want bool
	}{
		{"docker exited with code 127: exit status 127", true},
		{"docker exited with code 126: exit status 126", true},
		{"docker exited with code 1: exit status 1", false},
		{"cannot connect to the Docker daemon", false},
	}

	for _, tt := range tests {
		t.Run(tt.msg, func(t *testing.T) {
			if got := missingExecutable(errs.New("%s", tt.msg)); got != tt.want {
				t.Errorf("missingExecutable(%q) = %v, want %v", tt.msg, got, tt.want)
			}
		})
	}
}

func TestNotRunningIsRecognised(t *testing.T) {
	// Telling someone their database has no shell when they mistyped
	// its name sends them looking in the wrong place.
	for _, msg := range []string{
		`service "db" is not running`,
		"no such service: db",
		"no container found for db",
	} {
		t.Run(msg, func(t *testing.T) {
			if !notRunning(errs.New("%s", msg)) {
				t.Errorf("notRunning(%q) = false, want true", msg)
			}
		})
	}
	if notRunning(errs.New("bash: not found")) {
		t.Error("a missing shell was mistaken for a missing service")
	}
}
