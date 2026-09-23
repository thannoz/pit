package runtime

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
)

// stubRunner records what was asked of it and answers from a table.
type stubRunner struct {
	replies map[string]string
	failAll bool
	// notLocal makes `docker image inspect` fail, the way the daemon
	// answers for an image nobody has pulled yet.
	notLocal bool
	calls    []proc.Command
}

func (s *stubRunner) Output(_ context.Context, c proc.Command) ([]byte, error) {
	s.calls = append(s.calls, c)
	if s.failAll {
		return nil, errors.New("docker said no")
	}
	if s.notLocal && len(c.Args) > 0 && c.Args[0] == "image" {
		return nil, errors.New("Error: No such image")
	}
	return []byte(s.replies[strings.Join(c.Args, " ")]), nil
}

func (s *stubRunner) Stream(_ context.Context, c proc.Command, _, _ io.Writer) error {
	s.calls = append(s.calls, c)
	if s.failAll {
		return errors.New("docker said no")
	}
	return nil
}

func testSandbox() Sandbox {
	return Sandbox{
		Project: "pit-acme-shop-482",
		Dir:     "/state/pit/acme-shop-c56680/pr-482",
		Files:   []string{"docker-compose.yml"},
	}
}

func TestUpIsolatesTheSandbox(t *testing.T) {
	r := &stubRunner{}

	if err := (Compose{Runner: r}).Up(t.Context(), testSandbox(), nil, io.Discard, io.Discard); err != nil {
		t.Fatalf("Up: %v", err)
	}

	cmd := r.calls[0]
	if cmd.Name != "docker" {
		t.Errorf("ran %q, want docker", cmd.Name)
	}
	if cmd.Dir != testSandbox().Dir {
		t.Errorf("ran in %q, want the worktree", cmd.Dir)
	}

	// The project name is what gives the sandbox its own network and
	// volumes; without it two pull requests share both.
	if !slices.Contains(cmd.Args, "--project-name") {
		t.Errorf("args %v carry no project name", cmd.Args)
	}
	if !slices.Contains(cmd.Args, "pit-acme-shop-482") {
		t.Errorf("args %v do not use the sandbox's project name", cmd.Args)
	}

	for _, want := range []string{"up", "--detach"} {
		if !slices.Contains(cmd.Args, want) {
			t.Errorf("args %v are missing %q", cmd.Args, want)
		}
	}
	// Not --build: what had to be built was built by Build, and
	// rebuilding here would undo the decision not to touch the rest.
	if slices.Contains(cmd.Args, "--build") {
		t.Errorf("args %v rebuild everything", cmd.Args)
	}
}

func TestBuildNamesOnlyTheServicesItWasGiven(t *testing.T) {
	r := &stubRunner{}

	if err := (Compose{Runner: r}).Build(t.Context(), testSandbox(), []string{"web"}, io.Discard, io.Discard); err != nil {
		t.Fatalf("Build: %v", err)
	}

	cmd := r.calls[0]
	if !slices.Contains(cmd.Args, "build") || !slices.Contains(cmd.Args, "web") {
		t.Errorf("args %v do not build web", cmd.Args)
	}
	if slices.Contains(cmd.Args, "up") {
		t.Errorf("args %v start the services as well", cmd.Args)
	}
}

func TestPullAsksDockerForTheImageByName(t *testing.T) {
	// By name, not by service: this runs before the override that
	// would tell Compose what a service's image is.
	r := &stubRunner{notLocal: true}

	err := (Compose{Runner: r}).Pull(t.Context(), testSandbox(), "ghcr.io/acme/shop-web:abc123", io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	cmd := r.calls[len(r.calls)-1]
	if cmd.Name != "docker" || !slices.Contains(cmd.Args, "pull") {
		t.Errorf("ran %q %v, want a docker pull", cmd.Name, cmd.Args)
	}
	if !slices.Contains(cmd.Args, "ghcr.io/acme/shop-web:abc123") {
		t.Errorf("args %v do not name the image", cmd.Args)
	}
	if slices.Contains(cmd.Args, "compose") {
		t.Errorf("args %v go through compose, which cannot resolve this yet", cmd.Args)
	}
}

func TestProjectNameComesBeforeTheSubcommand(t *testing.T) {
	// Compose treats --project-name as a global flag; after the
	// subcommand it is rejected.
	r := &stubRunner{}
	if err := (Compose{Runner: r}).Up(t.Context(), testSandbox(), nil, io.Discard, io.Discard); err != nil {
		t.Fatalf("Up: %v", err)
	}

	args := r.calls[0].Args
	project, sub := slices.Index(args, "--project-name"), slices.Index(args, "up")
	if project < 0 || sub < 0 {
		t.Fatalf("args %v are missing an expected element", args)
	}
	if project > sub {
		t.Errorf("--project-name is at %d, after the subcommand at %d", project, sub)
	}
}

func TestDownRemovesVolumes(t *testing.T) {
	// A sandbox that leaves its database behind is not a sandbox: the
	// next review would inherit the last one's data.
	r := &stubRunner{}

	if err := (Compose{Runner: r}).Down(t.Context(), testSandbox(), io.Discard, io.Discard); err != nil {
		t.Fatalf("Down: %v", err)
	}

	for _, want := range []string{"down", "--volumes", "--remove-orphans"} {
		if !slices.Contains(r.calls[0].Args, want) {
			t.Errorf("args %v are missing %q", r.calls[0].Args, want)
		}
	}
}

func TestEachComposeFileIsPassed(t *testing.T) {
	r := &stubRunner{}
	s := testSandbox()
	s.Files = []string{"docker-compose.yml", "docker-compose.pit.yml"}

	if err := (Compose{Runner: r}).Up(t.Context(), s, nil, io.Discard, io.Discard); err != nil {
		t.Fatalf("Up: %v", err)
	}

	args := r.calls[0].Args
	if got := strings.Count(strings.Join(args, " "), "--file"); got != 2 {
		t.Errorf("args %v carry %d --file flags, want 2", args, got)
	}
}

func TestServices(t *testing.T) {
	r := &stubRunner{replies: map[string]string{
		"compose --project-name pit-acme-shop-482 --file docker-compose.yml config --services": "db\nredis\napi\nweb\n",
	}}

	got, err := Compose{Runner: r}.Services(t.Context(), testSandbox())
	if err != nil {
		t.Fatalf("Services: %v", err)
	}
	if want := []string{"db", "redis", "api", "web"}; !slices.Equal(got, want) {
		t.Errorf("Services() = %v, want %v", got, want)
	}
}

func TestPort(t *testing.T) {
	tests := []struct {
		name  string
		reply string
		want  string
	}{
		{"ipv4", "0.0.0.0:41482\n", "41482"},
		{"ipv6", "[::]:41482\n", "41482"},
		{"bare port", "41482\n", "41482"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &stubRunner{replies: map[string]string{
				"compose --project-name pit-acme-shop-482 --file docker-compose.yml port web 3000": tt.reply,
			}}

			got, err := Compose{Runner: r}.Port(t.Context(), testSandbox(), "web", 3000)
			if err != nil {
				t.Fatalf("Port: %v", err)
			}
			if got != tt.want {
				t.Errorf("Port() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPortOnAServiceThatPublishesNothing(t *testing.T) {
	r := &stubRunner{replies: map[string]string{}} // empty answer

	_, err := Compose{Runner: r}.Port(t.Context(), testSandbox(), "worker", 3000)
	if err == nil {
		t.Fatal("want an error when nothing is published")
	}
	if errs.Hint(err) == "" {
		t.Error("the error carries no hint")
	}
}

func TestFailuresCarryHints(t *testing.T) {
	r := &stubRunner{failAll: true}
	c := Compose{Runner: r}

	tests := map[string]func() error{
		"Up":       func() error { return c.Up(t.Context(), testSandbox(), nil, io.Discard, io.Discard) },
		"Down":     func() error { return c.Down(t.Context(), testSandbox(), io.Discard, io.Discard) },
		"Services": func() error { _, err := c.Services(t.Context(), testSandbox()); return err },
	}

	for name, call := range tests {
		t.Run(name, func(t *testing.T) {
			err := call()
			if err == nil {
				t.Fatal("want an error")
			}
			if errs.Hint(err) == "" {
				t.Errorf("%v carries no hint", err)
			}
		})
	}
}

func TestPullSkipsTheRegistryForAnImageThatIsAlreadyHere(t *testing.T) {
	// pit only ever asks for names that carry the commit, and a given
	// commit's image does not change. Confirming that with the
	// registry costs a round trip per service and answers nothing.
	r := &stubRunner{}

	err := (Compose{Runner: r}).Pull(t.Context(), testSandbox(), "ghcr.io/acme/shop-web:abc123", io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	for _, c := range r.calls {
		if slices.Contains(c.Args, "pull") {
			t.Errorf("asked the registry for an image that is already here: %v", c.Args)
		}
	}
}
