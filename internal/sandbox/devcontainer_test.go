package sandbox_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/sandbox"
)

// devProc stands in for docker where a dev container's commands run:
// it records them, and answers the few questions pit asks.
type devProc struct {
	mu sync.Mutex
	// ran are the commands, after the compose file flags.
	ran []string
	// label is the image's devcontainer.metadata.
	label string
	// created says the container has the marker of its create commands.
	created bool
	// fail makes a command that contains it fail.
	fail string
}

func (p *devProc) Stream(_ context.Context, c proc.Command, stdout, _ io.Writer) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	line := strings.Join(c.Args, " ")
	// What follows the last --file and its value.
	for i := len(c.Args) - 1; i >= 0; i-- {
		if c.Args[i] == "--file" {
			line = strings.Join(c.Args[i+2:], " ")
			break
		}
	}
	p.ran = append(p.ran, line)
	switch {
	case p.fail != "" && strings.Contains(line, p.fail):
		return errors.New("exit status 1")
	case strings.HasPrefix(line, "ps -q"):
		_, _ = io.WriteString(stdout, "c0ffee\n")
	case strings.HasPrefix(line, "inspect"):
		_, _ = io.WriteString(stdout, p.label+"\n")
	case strings.Contains(line, "test -e /tmp/.pit-created"):
		if !p.created {
			return errors.New("exit status 1")
		}
	case strings.Contains(line, "touch /tmp/.pit-created"):
		p.created = true
	}
	return nil
}

func (p *devProc) commands() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.ran)
}

const devJSON = `{
	// The app, as microsoft/vscode-remote-try-node has it.
	"image": "mcr.microsoft.com/devcontainers/javascript-node:1-18-bullseye",
	"postCreateCommand": "npm install",
	"updateContentCommand": "npm ci",
	"postStartCommand": "echo started",
	"features": {"ghcr.io/devcontainers/features/github-cli:1": {}},
}`

// devFixture is a pull request whose environment is a devcontainer.json.
func devFixture(t *testing.T, start string) (*sandbox.Manager, sandbox.UpRequest, *devProc) {
	t.Helper()
	m, req, _ := upFixture(t)
	pushToPullRequest(t, req.Repo.Root, 7, map[string]string{".devcontainer/devcontainer.json": devJSON})
	cfg, err := config.Parse([]byte("devcontainer:\n  file: .devcontainer/devcontainer.json\n  start: " + start + "\nweb:\n  service: dev\n  port: 3000\n"))
	if err != nil {
		t.Fatal(err)
	}
	req.Config = cfg
	p := &devProc{label: `[{"id": "node"}, {"remoteUser": "node"}]`}
	m.Proc = p
	return m, req, p
}

func TestUpRunsADevContainer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, p := devFixture(t, "npm start")
	rep := &quietReporter{}
	box, err := m.Up(t.Context(), req, rep)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}

	// The compose file pit wrote comes first, then the override.
	if len(box.ComposeFiles) != 2 || !strings.HasSuffix(box.ComposeFiles[0], "pr-7.devcontainer.yml") {
		t.Fatalf("compose files = %q", box.ComposeFiles)
	}
	written, err := os.ReadFile(box.ComposeFiles[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"image: mcr.microsoft.com/devcontainers/javascript-node:1-18-bullseye", "source: " + box.Worktree, "target: /workspaces/"} {
		if !strings.Contains(string(written), want) {
			t.Errorf("the compose file has no %q:\n%s", want, written)
		}
	}

	// As the image's user, in the workspace: the create commands, then
	// the start ones, then the app.
	ws := "/workspaces/" + filepath.Base(req.Repo.Root)
	want := []string{
		"ps -q dev",
		"inspect --format {{index .Config.Labels \"devcontainer.metadata\"}} c0ffee",
		"exec -T dev test -e /tmp/.pit-created",
		"exec -T -u node -w " + ws + " dev /bin/sh -c npm ci",
		"exec -T -u node -w " + ws + " dev /bin/sh -c npm install",
		"exec -T -u node -w " + ws + " dev /bin/sh -c echo started",
		"exec -T dev touch /tmp/.pit-created",
		"exec -T -d -u node -w " + ws + " dev /bin/sh -c exec >>/tmp/pit-start.log 2>&1; npm start",
	}
	if got := p.commands(); !slices.Equal(got, want) {
		t.Errorf("ran\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	i := slices.Index(rep.begun, "dev container")
	if i < 0 || rep.begun[i-1] != "services" || rep.steps[i] != "3 commands, started: npm start" {
		t.Errorf("steps = %q / %q", rep.begun, rep.steps)
	}
	if !slices.ContainsFunc(rep.notes, func(n string) bool {
		return strings.Contains(n, "leaves out the .devcontainer/devcontainer.json's features (ghcr.io/devcontainers/features/github-cli:1)")
	}) {
		t.Errorf("notes = %q", rep.notes)
	}

	// Down takes the file away with the rest.
	if err := m.Down(t.Context(), box, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(box.ComposeFiles[0]); !os.IsNotExist(err) {
		t.Errorf("the compose file is still there: %v", err)
	}
}

func TestUpdatingADevContainerStartsItOver(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, p := devFixture(t, "npm start")
	if _, err := m.Up(t.Context(), req, &quietReporter{}); err != nil {
		t.Fatalf("Up: %v", err)
	}
	advancePullRequest(t, req.Repo.Root, 7)
	p.ran = nil
	rep := &quietReporter{}
	if _, err := m.Up(t.Context(), req, rep); err != nil {
		t.Fatalf("Up: %v", err)
	}
	var lifecycle []string
	for _, c := range p.commands() {
		if strings.HasPrefix(c, "exec") || strings.HasPrefix(c, "restart") {
			lifecycle = append(lifecycle, c)
		}
	}
	ws := "/workspaces/" + filepath.Base(req.Repo.Root)
	want := []string{
		"exec -T dev test -e /tmp/.pit-created",
		"restart dev",
		"exec -T -u node -w " + ws + " dev /bin/sh -c npm ci",
		"exec -T -u node -w " + ws + " dev /bin/sh -c echo started",
		"exec -T -d -u node -w " + ws + " dev /bin/sh -c exec >>/tmp/pit-start.log 2>&1; npm start",
	}
	if !slices.Equal(lifecycle, want) {
		t.Errorf("ran\n  %s\nwant\n  %s", strings.Join(lifecycle, "\n  "), strings.Join(want, "\n  "))
	}
	if i := slices.Index(rep.begun, "dev container"); i < 0 || rep.steps[i] != "2 commands, started: npm start" {
		t.Errorf("steps = %q / %q", rep.begun, rep.steps)
	}
}

func TestADevContainerThatFailsIsUndone(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, p := devFixture(t, "npm start")
	p.fail = "npm install"
	_, err := m.Up(t.Context(), req, &quietReporter{})
	if err == nil || !strings.Contains(err.Error(), "the dev container's postCreateCommand failed") {
		t.Fatalf("err = %v", err)
	}
	if hint := errs.Hint(err); !strings.Contains(hint, "it ran npm install") {
		t.Errorf("hint = %q", hint)
	}
	generated, _ := filepath.Glob(filepath.Join(m.StateDir, "*", "*.devcontainer.yml"))
	if len(generated) != 0 {
		t.Errorf("left behind: %q", generated)
	}

	// The start command's failure is its own.
	m, req, p = devFixture(t, "npm start")
	p.fail = "npm start"
	if _, err := m.Up(t.Context(), req, &quietReporter{}); err == nil || !strings.Contains(err.Error(), "cannot start the app in dev") {
		t.Errorf("err = %v", err)
	}
}

func TestADevContainerWithoutAStartCommandSaysItWaits(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, p := devFixture(t, `""`)
	rep := &quietReporter{}
	if _, err := m.Up(t.Context(), req, rep); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if !slices.ContainsFunc(rep.notes, func(n string) bool { return strings.Contains(n, "the dev container only waits") }) {
		t.Errorf("notes = %q", rep.notes)
	}
	if slices.ContainsFunc(p.commands(), func(c string) bool { return strings.Contains(c, "-d") }) {
		t.Errorf("something was started: %q", p.commands())
	}
}

func TestUpRunsADevContainerOfComposeFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, fake := upFixture(t)
	pushToPullRequest(t, req.Repo.Root, 7, map[string]string{
		".devcontainer/devcontainer.json": `{"dockerComposeFile": ["../docker-compose.yml", "extend.yml"], "service": "app", "runServices": ["app"], "workspaceFolder": "/workspace"}`,
		".devcontainer/extend.yml":        "services:\n  app:\n    command: sleep infinity\n",
		"docker-compose.yml":              "services:\n  app:\n    build: .\n    depends_on: [db]\n  db:\n    image: postgres\n  mail:\n    image: mailhog\n",
	})
	cfg, err := config.Parse([]byte("devcontainer:\n  file: .devcontainer/devcontainer.json\nweb:\n  service: app\n  port: 3000\n"))
	if err != nil {
		t.Fatal(err)
	}
	req.Config = cfg
	p := &devProc{label: "<no value>"}
	m.Proc = p
	box, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if len(box.ComposeFiles) != 4 || box.ComposeFiles[0] != filepath.Join(box.Worktree, "docker-compose.yml") ||
		box.ComposeFiles[1] != filepath.Join(box.Worktree, ".devcontainer/extend.yml") ||
		!strings.HasSuffix(box.ComposeFiles[2], "pr-7.devcontainer.yml") {
		t.Errorf("compose files = %q", box.ComposeFiles)
	}
	// runServices, and what they depend on; not the mail catcher.
	var up []string
	for _, c := range fake.Calls() {
		if c.Method == "Up" {
			up = c.Services
		}
	}
	if !slices.Equal(up, []string{"app", "db"}) {
		t.Errorf("started %q", up)
	}
	// The compose files start the app; pit only asks who to run as.
	if got := p.commands(); !slices.Equal(got, []string{
		"ps -q app",
		"inspect --format {{index .Config.Labels \"devcontainer.metadata\"}} c0ffee",
		"exec -T app test -e /tmp/.pit-created",
		"exec -T app touch /tmp/.pit-created",
	}) {
		t.Errorf("ran %q", got)
	}
}

// What a new commit makes pit build: the dev container's image when its
// devcontainer.json or its Dockerfile changed, and nothing for the app's
// code, which the container has mounted.
func TestUpdatingADevContainerRebuildsWhatChanged(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, fake := upFixture(t)
	pushToPullRequest(t, req.Repo.Root, 7, map[string]string{
		".devcontainer/devcontainer.json": `{"build": {"dockerfile": "../docker/Dockerfile", "context": "../docker"}}`,
		"docker/Dockerfile":               "FROM node:22\n",
		"src/app.js":                      "1\n",
	})
	cfg, err := config.Parse([]byte("devcontainer:\n  file: .devcontainer/devcontainer.json\n  start: node src/app.js\nweb:\n  service: dev\n  port: 3000\n"))
	if err != nil {
		t.Fatal(err)
	}
	req.Config = cfg
	m.Proc = &devProc{label: "<no value>"}
	builds := func() (out []string) {
		for _, c := range fake.Calls() {
			if c.Method == "Build" {
				out = append(out, strings.Join(c.Services, ",")+";")
			}
		}
		return out
	}
	if _, err := m.Up(t.Context(), req, &quietReporter{}); err != nil {
		t.Fatalf("Up: %v", err)
	}
	for _, step := range []struct {
		files map[string]string
		want  []string
	}{
		{map[string]string{"src/app.js": "2\n"}, []string{"dev;"}},
		{map[string]string{"docker/Dockerfile": "FROM node:24\n"}, []string{"dev;", "dev;"}},
		// Outside the context, and still what the image is built by.
		{map[string]string{".devcontainer/devcontainer.json": `{"build": {"dockerfile": "../docker/Dockerfile", "context": "../docker", "args": {"V": "2"}}}`}, []string{"dev;", "dev;", "dev;"}},
	} {
		pushToPullRequest(t, req.Repo.Root, 7, step.files)
		if _, err := m.Up(t.Context(), req, &quietReporter{}); err != nil {
			t.Fatalf("Up: %v", err)
		}
		if got := builds(); !slices.Equal(got, step.want) {
			t.Errorf("after %v: builds = %q, want %q", step.files, got, step.want)
		}
	}
}
