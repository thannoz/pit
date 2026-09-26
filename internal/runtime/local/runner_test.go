//go:build unix

package local

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/runtime"
)

// The test binary is also the supervisor, and a web process: pit runs
// itself for the one, and a project's app is the other. The web process
// is started by the supervisor and sees its variables too.
func TestMain(m *testing.M) {
	switch {
	case os.Getenv("PIT_TEST_SERVE") != "":
		fmt.Println("listening on", os.Getenv("PORT"), "as", os.Getenv("PIT_PROJECT"), os.Getenv("GREETING"))
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			fmt.Println("GET", r.URL.Path)
			_, _ = fmt.Fprintln(w, "hello from", os.Getenv("PORT"))
		})
		server := &http.Server{Addr: "127.0.0.1:" + os.Getenv("PORT"), ReadHeaderTimeout: time.Second}
		if err := server.ListenAndServe(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case os.Getenv("PIT_TEST_SUPERVISE") != "":
		if err := Supervise(context.Background(), os.Args[len(os.Args)-1]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close() //nolint:errcheck // only its number is wanted
	return l.Addr().(*net.TCPAddr).Port
}

// sandbox writes a Procfile and a plan, and returns the runner and the
// sandbox to run them with.
func sandbox(t *testing.T, procfile string) (Runner, runtime.Sandbox, int) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping: starts processes")
	}
	t.Setenv("PIT_TEST_SUPERVISE", "1")
	work := t.TempDir()
	procfile = strings.ReplaceAll(procfile, "$SELF", strconv.Quote(os.Args[0]))
	if err := os.WriteFile(filepath.Join(work, "Procfile"), []byte(procfile), 0o600); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	plan := filepath.Join(t.TempDir(), "pr-7.processes.json")
	if err := WritePlan(plan, Plan{Project: "pit-shop-7", Web: "web", Port: port, Env: map[string]string{"GREETING": "hi there"}}); err != nil {
		t.Fatal(err)
	}
	r := Runner{Root: t.TempDir(), Supervisor: []string{os.Args[0]}}
	s := runtime.Sandbox{Project: "pit-shop-7", Dir: work, Files: []string{filepath.Join(work, "Procfile"), plan}, Processes: true}
	t.Cleanup(func() { _ = r.Down(context.Background(), s, nil, nil) })
	return r, s, port
}

func probe(port int) runtime.Probe {
	return runtime.Probe{URL: "http://127.0.0.1:" + strconv.Itoa(port) + "/", ExpectStatus: 200, Timeout: 10 * time.Second, Interval: 50 * time.Millisecond}
}

func states(t *testing.T, r Runner, s runtime.Sandbox) map[string]string {
	t.Helper()
	st, err := r.Status(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, x := range st {
		out[x.Service] = x.State + "/" + strconv.Itoa(x.ExitCode)
	}
	return out
}

func TestRunnerRunsAProcfile(t *testing.T) {
	// What pit runs with reaches the processes, under what the plan
	// sets.
	t.Setenv("PIT_TEST_INHERITED", "inherited")
	r, s, port := sandbox(t, "web: PIT_TEST_SERVE=1 exec $SELF\n# a comment\nworker: printf 'working on %s\\nfor %s\\n' $PORT $PIT_TEST_INHERITED; exec sleep 60\n")
	ctx := t.Context()
	var out strings.Builder
	if err := r.Up(ctx, s, nil, &out, nil); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if !strings.Contains(out.String(), fmt.Sprintf("web started on port %d", port)) ||
		!strings.Contains(out.String(), fmt.Sprintf("worker started on port %d", port+PortStep)) {
		t.Errorf("out = %q", out.String())
	}
	if err := r.WaitReady(ctx, s, "web", probe(port)); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	if got := states(t, r, s); got["web"] != "running/0" || got["worker"] != "running/0" {
		t.Errorf("states = %v", got)
	}

	// What they printed, with the environment the plan gave them.
	logs, err := r.Logs(ctx, s, "web", 10)
	if err != nil || !strings.Contains(string(logs), fmt.Sprintf("listening on %d as pit-shop-7 hi there", port)) || !strings.Contains(string(logs), "GET /") {
		t.Errorf("logs = %q, %v", logs, err)
	}
	// Two lines printed at once are two lines.
	if logs, _ := r.Logs(ctx, s, "worker", 10); string(logs) != fmt.Sprintf("working on %d\nfor inherited\n", port+PortStep) {
		t.Errorf("worker logs = %q", logs)
	}
	if logs, _ := r.Logs(ctx, s, "worker", 1); string(logs) != "for inherited\n" {
		t.Errorf("the last line = %q", logs)
	}
	if lines, err := r.LogsSince(ctx, s, "web", time.Now().Add(time.Hour)); err != nil || len(lines) != 0 {
		t.Errorf("lines from the future = %v, %v", lines, err)
	}
	if lines, _ := r.LogsSince(ctx, s, "web", time.Time{}); len(lines) < 2 || lines[0].At.IsZero() {
		t.Errorf("lines = %v", lines)
	}
	if got, err := r.Port(ctx, s, "worker", 0); err != nil || got != strconv.Itoa(port+PortStep) {
		t.Errorf("port = %q, %v", got, err)
	}
	if got, err := r.Services(ctx, s); err != nil || strings.Join(got, ",") != "web,worker" {
		t.Errorf("services = %v, %v", got, err)
	}

	// Frozen, and carrying on.
	if err := r.Pause(ctx, s, []string{"worker"}); err != nil {
		t.Fatal(err)
	}
	if got := states(t, r, s); got["worker"] != "paused/0" {
		t.Errorf("states = %v", got)
	}
	if err := r.Unpause(ctx, s, []string{"worker"}); err != nil {
		t.Fatal(err)
	}
	if got := states(t, r, s); got["worker"] != "running/0" {
		t.Errorf("states = %v", got)
	}

	// One stopped, the other not.
	if err := r.Stop(ctx, s, []string{"worker"}); err != nil {
		t.Fatal(err)
	}
	if got := states(t, r, s); got["worker"] != "exited/143" || got["web"] != "running/0" {
		t.Errorf("states = %v", got)
	}

	// Down takes everything away.
	st, _ := readState(r.dir(s))
	web := st.Processes["web"].PID
	if err := r.Down(ctx, s, nil, nil); err != nil {
		t.Fatal(err)
	}
	if alive(web) {
		t.Error("the web process is still running")
	}
	if _, err := os.Stat(r.dir(s)); !os.IsNotExist(err) {
		t.Errorf("the directory is still there: %v", err)
	}
	if st, err := r.Status(ctx, s); err != nil || len(st) != 0 {
		t.Errorf("status = %v, %v", st, err)
	}
}

func TestUpStartsTheProcessesAnew(t *testing.T) {
	r, s, port := sandbox(t, "web: PIT_TEST_SERVE=1 exec $SELF\nworker: exec sleep 60\n")
	ctx := t.Context()
	if err := r.Up(ctx, s, nil, &strings.Builder{}, nil); err != nil {
		t.Fatal(err)
	}
	first, _ := readState(r.dir(s))
	if err := r.Pause(ctx, s, []string{"worker"}); err != nil {
		t.Fatal(err)
	}
	// All of them again: what Up says it started is what runs now, and
	// nothing is paused any more.
	var said strings.Builder
	if err := r.Up(ctx, s, nil, &said, nil); err != nil {
		t.Fatal(err)
	}
	before, _ := readState(r.dir(s))
	if before.Processes["worker"].PID == first.Processes["worker"].PID ||
		!strings.Contains(said.String(), fmt.Sprintf("(pid %d)", before.Processes["worker"].PID)) {
		t.Errorf("first %+v, then %+v; said %q", first, before, said.String())
	}
	if got := states(t, r, s); got["worker"] != "running/0" {
		t.Errorf("states = %v", got)
	}
	// Only the web process, this time.
	if err := r.Up(ctx, s, []string{"web"}, &strings.Builder{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.WaitReady(ctx, s, "web", probe(port)); err != nil {
		t.Fatal(err)
	}
	after, _ := readState(r.dir(s))
	if after.Processes["web"].PID == before.Processes["web"].PID || len(after.Processes) != 1 {
		t.Errorf("before %+v, after %+v", before, after)
	}
	for name, ps := range before.Processes {
		if alive(ps.PID) {
			t.Errorf("the old %s is still running", name)
		}
	}
	if err := r.Up(ctx, s, []string{"mail"}, &strings.Builder{}, nil); err == nil || !strings.Contains(err.Error(), `no process "mail"`) {
		t.Errorf("err = %v", err)
	}
}

func TestWaitReadyStopsWhenTheProcessEnds(t *testing.T) {
	r, s, port := sandbox(t, "web: echo cannot find module express >&2; exit 3\n")
	if err := r.Up(t.Context(), s, nil, &strings.Builder{}, nil); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err := r.WaitReady(t.Context(), s, "web", probe(port))
	if err == nil || !strings.Contains(err.Error(), "web ended with code 3 before it answered") || !strings.Contains(err.Error(), "cannot find module express") {
		t.Errorf("err = %v", err)
	}
	if time.Since(started) > 5*time.Second {
		t.Errorf("it waited %v for a process that had ended", time.Since(started))
	}
	if got := states(t, r, s); got["web"] != "exited/3" {
		t.Errorf("states = %v", got)
	}
}

func TestWaitReadySaysWhatItPrinted(t *testing.T) {
	r, s, port := sandbox(t, "web: echo still thinking; exec sleep 60\n")
	if err := r.Up(t.Context(), s, nil, &strings.Builder{}, nil); err != nil {
		t.Fatal(err)
	}
	p := probe(port)
	p.Timeout = 300 * time.Millisecond
	err := r.WaitReady(t.Context(), s, "web", p)
	if err == nil || !strings.Contains(err.Error(), "web did not become ready") || !strings.Contains(err.Error(), "still thinking") {
		t.Errorf("err = %v", err)
	}
}

// A number in supervisor.pid that is not this sandbox's supervisor --
// after a reboot, it can be anything -- is left alone.
func TestAStrangerIsNotTheSupervisor(t *testing.T) {
	r, s, _ := sandbox(t, "web: exec sleep 60\n")
	stranger := exec.CommandContext(t.Context(), "sleep", "60")
	if err := stranger.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stranger.Process.Kill(); _ = stranger.Wait() })
	dir := r.dir(s)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, supervisorFile), []byte(strconv.Itoa(stranger.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, statusFile), []byte(`{"processes": {"web": {"pid": `+strconv.Itoa(stranger.Process.Pid)+`, "running": true}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := states(t, r, s); got["web"] != "exited/-1" {
		t.Errorf("states = %v", got)
	}
	if err := r.Down(t.Context(), s, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := stranger.Process.Signal(syscall.Signal(0)); err != nil {
		t.Errorf("the stranger was signalled: %v", err)
	}
	if err := r.Pause(t.Context(), s, []string{"web"}); err == nil {
		t.Error("paused a sandbox that is not running")
	}
}

func TestBuildAndPull(t *testing.T) {
	r := Runner{}
	if err := r.Build(t.Context(), runtime.Sandbox{}, nil, nil, nil); err != nil {
		t.Error(err)
	}
	if err := r.Pull(t.Context(), runtime.Sandbox{}, "shop:abc", nil, nil); err == nil || !strings.Contains(err.Error(), "shop:abc") {
		t.Errorf("err = %v", err)
	}
	if _, err := r.Services(t.Context(), runtime.Sandbox{Project: "p"}); err == nil {
		t.Error("services without a Procfile")
	}
	if err := r.Up(t.Context(), runtime.Sandbox{Project: "p"}, nil, nil, nil); err == nil {
		t.Error("up without a Procfile")
	}
}

// A process that does not end when asked is ended.
func TestAProcessThatIgnoresTheRequestIsKilled(t *testing.T) {
	previous := Grace
	Grace = 300 * time.Millisecond
	t.Cleanup(func() { Grace = previous })
	r, s, _ := sandbox(t, "web: trap '' TERM; echo stubborn; while true; do sleep 0.1; done\n")
	if err := r.Up(t.Context(), s, nil, &strings.Builder{}, nil); err != nil {
		t.Fatal(err)
	}
	st, _ := readState(r.dir(s))
	web := st.Processes["web"].PID
	started := time.Now()
	if err := r.Down(t.Context(), s, nil, nil); err != nil {
		t.Fatal(err)
	}
	if alive(web) {
		t.Error("it is still running")
	}
	if time.Since(started) > 5*time.Second {
		t.Errorf("it took %v", time.Since(started))
	}
}

// Stop waits for a process that takes its time to end.
func TestStopWaits(t *testing.T) {
	r, s, _ := sandbox(t, "web: trap 'sleep 0.3; exit 7' TERM; while true; do sleep 0.05; done\n")
	if err := r.Up(t.Context(), s, nil, &strings.Builder{}, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := r.Stop(t.Context(), s, []string{"web"}); err != nil {
		t.Fatal(err)
	}
	if got := states(t, r, s); got["web"] != "exited/7" {
		t.Errorf("states = %v", got)
	}
}

// A process that cannot start says why.
func TestAProcessThatCannotStart(t *testing.T) {
	r, s, _ := sandbox(t, "web: exec sleep 60\n")
	s.Dir = filepath.Join(s.Dir, "gone")
	err := r.Up(t.Context(), s, nil, &strings.Builder{}, nil)
	if err == nil || !strings.Contains(err.Error(), "web did not start") || !strings.Contains(err.Error(), "gone") {
		t.Errorf("err = %v", err)
	}
}

// A plan's Wrap is put in front of every process: the dev shell of the
// pull request's flake, which gives them its tools.
func TestProcessesRunInTheirDevShell(t *testing.T) {
	r, s, port := sandbox(t, "web: PIT_TEST_SERVE=1 exec $SELF\nworker: echo \"$WRAPPED\"; exec sleep 60\n")
	if err := WritePlan(s.Files[1], Plan{Project: "pit-shop-7", Web: "web", Port: port, Wrap: []string{"env", "WRAPPED=wrapped"}}); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := r.Up(ctx, s, nil, &strings.Builder{}, nil); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if err := r.WaitReady(ctx, s, "web", probe(port)); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	want := "wrapped\n"
	var logs []byte
	for range 100 {
		if logs, _ = r.Logs(ctx, s, "worker", 1); string(logs) == want {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if string(logs) != want {
		t.Errorf("worker logs = %q, want %q", logs, want)
	}
}

// Secrets reach the processes through the supervisor's environment:
// nothing pit writes down holds them.
func TestSecretsReachTheProcessesAndNoFile(t *testing.T) {
	r, s, port := sandbox(t, "web: PIT_TEST_SERVE=1 exec $SELF\nworker: echo \"$STRIPE_KEY\"; exec sleep 60\n")
	s.Secrets = map[string]string{"STRIPE_KEY": "sk_test_4242"}
	ctx := t.Context()
	if err := r.Up(ctx, s, nil, &strings.Builder{}, nil); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if err := r.WaitReady(ctx, s, "web", probe(port)); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	var logs []byte
	for range 100 {
		if logs, _ = r.Logs(ctx, s, "worker", 1); string(logs) == "sk_test_4242\n" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if string(logs) != "sk_test_4242\n" {
		t.Errorf("worker logs = %q", logs)
	}
	for _, dir := range []string{r.Root, filepath.Dir(s.Files[1])} {
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || strings.HasSuffix(path, ".log") {
				return nil
			}
			if data, _ := os.ReadFile(path); strings.Contains(string(data), "sk_test_4242") {
				t.Errorf("%s holds the secret", path)
			}
			return nil
		})
	}
}
