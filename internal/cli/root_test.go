package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/errs"
)

// run executes the root command with args and captures its output.
func run(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	var out, errOut bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)

	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

func TestNoArgumentsShowsHelp(t *testing.T) {
	out, _, err := run(t)
	if err != nil {
		t.Fatalf("no arguments: %v", err)
	}
	for _, want := range []string{"Usage:", "version", "--verbose"} {
		if !strings.Contains(out, want) {
			t.Errorf("help output is missing %q:\n%s", want, out)
		}
	}
}

func TestUnknownCommandFails(t *testing.T) {
	_, _, err := run(t, "definitely-not-a-command")
	if err == nil {
		t.Fatal("unknown command: got nil error, want one")
	}
}

func TestGlobalFlagsAreRegistered(t *testing.T) {
	cmd := newRootCmd()

	tests := []struct {
		name      string
		shorthand string
	}{
		{"verbose", "v"},
		{"config", ""},
		{"json", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := cmd.PersistentFlags().Lookup(tt.name)
			if f == nil {
				t.Fatalf("flag --%s is not registered", tt.name)
			}
			if f.Shorthand != tt.shorthand {
				t.Errorf("--%s shorthand = %q, want %q", tt.name, f.Shorthand, tt.shorthand)
			}
			if f.Usage == "" {
				t.Errorf("--%s has no usage text", tt.name)
			}
		})
	}
}

func TestGlobalFlagsReachSubcommands(t *testing.T) {
	// --json is declared on the root; it has to take effect on a
	// subcommand, which is the whole point of a persistent flag.
	out, _, err := run(t, "version", "--json")
	if err != nil {
		t.Fatalf("version --json: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("stdout = %q, want JSON", out)
	}
}

func TestErrorsDoNotPrintUsage(t *testing.T) {
	// SilenceUsage: a runtime failure should not dump the full help.
	out, errOut, err := run(t, "version", "extra")
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(out+errOut, "Usage:") {
		t.Errorf("usage was printed for a runtime error:\nstdout %q\nstderr %q", out, errOut)
	}
}

func TestUnknownCommandCarriesAHint(t *testing.T) {
	err := Run([]string{"definitely-not-a-command"})
	if err == nil {
		t.Fatal("want an error")
	}
	if got := errs.Hint(err); got == "" {
		t.Errorf("Hint() is empty for %v, want a next step", err)
	}
}

func TestUnknownFlagCarriesAHint(t *testing.T) {
	err := Run([]string{"version", "--definitely-not-a-flag"})
	if err == nil {
		t.Fatal("want an error")
	}
	if got := errs.Hint(err); got == "" {
		t.Errorf("Hint() is empty for %v, want a next step", err)
	}
}

// TestCobraStillSaysUnknownCommand pins an assumption we cannot express
// in the type system: Run() recognises an unknown command by cobra's
// wording, because cobra exposes no typed error for it. If cobra ever
// rephrases the message, this fails and points at the place to fix
// rather than letting the hint quietly disappear.
func TestCobraStillSaysUnknownCommand(t *testing.T) {
	_, _, err := run(t, "definitely-not-a-command")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.HasPrefix(err.Error(), unknownCommandPrefix) {
		t.Errorf("cobra now says %q; it no longer starts with %q, so the "+
			"detection in Run() needs updating", err, unknownCommandPrefix)
	}
}
