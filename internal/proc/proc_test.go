package proc

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/errs"
)

func TestCommandString(t *testing.T) {
	tests := []struct {
		name string
		cmd  Command
		want string
	}{
		{"no arguments", Command{Name: "git"}, "git"},
		{"plain arguments", Command{Name: "git", Args: []string{"worktree", "list"}}, "git worktree list"},
		{
			name: "argument with spaces is quoted",
			cmd:  Command{Name: "git", Args: []string{"commit", "-m", "two words"}},
			want: `git commit -m "two words"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cmd.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOutputReturnsStdout(t *testing.T) {
	out, err := Exec{}.Output(t.Context(), Command{Name: "echo", Args: []string{"hello"}})
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "hello" {
		t.Errorf("Output = %q, want %q", got, "hello")
	}
}

func TestOutputPassesStdinThrough(t *testing.T) {
	c := Command{Name: "cat", Stdin: strings.NewReader("piped in")}

	out, err := Exec{}.Output(t.Context(), c)
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if got := string(out); got != "piped in" {
		t.Errorf("Output = %q, want %q", got, "piped in")
	}
}

func TestOutputRunsInDir(t *testing.T) {
	dir := t.TempDir()

	where := Command{Name: "pwd", Dir: dir}
	if goruntime.GOOS == "windows" {
		// Git's pwd would say /tmp/...: a directory of its own.
		where = Command{Name: "cmd", Args: []string{"/c", "cd"}, Dir: dir}
	}
	out, err := Exec{}.Output(t.Context(), where)
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	// The same directory, however it is spelled: macOS reports
	// /private/var for /var, Windows may give either name of one.
	want, _ := os.Stat(dir)
	if got, err := os.Stat(strings.TrimSpace(string(out))); err != nil || !os.SameFile(got, want) {
		t.Errorf("ran in %q, want %q", out, dir)
	}
}

func TestEnvReachesTheProcess(t *testing.T) {
	c := Command{Name: "sh", Args: []string{"-c", "echo $PIT_TEST_VALUE"}, Env: []string{"PIT_TEST_VALUE=set"}}

	out, err := Exec{}.Output(t.Context(), c)
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "set" {
		t.Errorf("Output = %q, want %q", got, "set")
	}
}

func TestStreamForwardsBothChannels(t *testing.T) {
	var stdout, stderr bytes.Buffer
	c := Command{Name: "sh", Args: []string{"-c", "echo out; echo err >&2"}}

	if err := (Exec{}).Stream(t.Context(), c, &stdout, &stderr); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "out" {
		t.Errorf("stdout = %q, want %q", got, "out")
	}
	if got := strings.TrimSpace(stderr.String()); got != "err" {
		t.Errorf("stderr = %q, want %q", got, "err")
	}
}

func TestMissingExecutableCarriesAHint(t *testing.T) {
	_, err := Exec{}.Output(t.Context(), Command{Name: "pit-does-not-exist"})
	if err == nil {
		t.Fatal("want an error for a missing executable")
	}
	if errs.Hint(err) == "" {
		t.Errorf("Hint() is empty for %v, want a next step", err)
	}
	if !strings.Contains(err.Error(), "pit-does-not-exist") {
		t.Errorf("error = %q, want it to name the executable", err)
	}
}

func TestNonZeroExitQuotesStderr(t *testing.T) {
	c := Command{Name: "sh", Args: []string{"-c", "echo 'the real reason' >&2; exit 3"}}

	_, err := Exec{}.Output(t.Context(), c)
	if err == nil {
		t.Fatal("want an error for a non-zero exit")
	}
	for _, want := range []string{"code 3", "the real reason"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, missing %q", err, want)
		}
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Error("the *exec.ExitError is no longer reachable through the chain")
	}
}

func TestStreamQuotesStderrTailOnFailure(t *testing.T) {
	var discard bytes.Buffer
	c := Command{Name: "sh", Args: []string{"-c", "echo 'streamed reason' >&2; exit 1"}}

	err := Exec{}.Stream(t.Context(), c, &discard, &discard)
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "streamed reason") {
		t.Errorf("error = %q, want the tail of stderr", err)
	}
}

func TestTailBufferKeepsOnlyTheEnd(t *testing.T) {
	const (
		chunk  = "abcdef"
		writes = 5
		limit  = 10
	)
	tb := &tailBuffer{limit: limit}

	for range writes {
		if _, err := tb.Write([]byte(chunk)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	written := strings.Repeat(chunk, writes)
	want := written[len(written)-limit:]
	if got := tb.String(); got != want {
		t.Errorf("String() = %q, want the last %d bytes %q", got, limit, want)
	}
}

func TestTailBufferKeepsEverythingBelowTheLimit(t *testing.T) {
	tb := &tailBuffer{limit: 100}

	if _, err := tb.Write([]byte("short")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := tb.String(); got != "short" {
		t.Errorf("String() = %q, want %q", got, "short")
	}
}
