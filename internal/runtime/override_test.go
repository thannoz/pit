package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/proc"

	"gopkg.in/yaml.v3"
)

func TestRenderOverridePublishesThePort(t *testing.T) {
	got, err := RenderOverride(Override{Service: "web", HostPort: 49580, ContainerPort: 80})
	if err != nil {
		t.Fatalf("RenderOverride: %v", err)
	}

	rendered := string(got)
	for _, want := range []string{"services:", "web:", `"49580:80"`, "ports: !override"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the override is missing %q:\n%s", want, rendered)
		}
	}
}

func TestRenderOverrideIsStable(t *testing.T) {
	// Map iteration is random; a file that reshuffles between runs
	// makes a diff of it useless.
	o := Override{
		Service: "web", HostPort: 49580, ContainerPort: 80,
		Env: map[string]string{"C": "3", "A": "1", "B": "2"},
	}

	first, err := RenderOverride(o)
	if err != nil {
		t.Fatalf("RenderOverride: %v", err)
	}
	for range 20 {
		again, err := RenderOverride(o)
		if err != nil {
			t.Fatalf("RenderOverride: %v", err)
		}
		if string(again) != string(first) {
			t.Fatalf("the output changed between runs:\n%s\n---\n%s", first, again)
		}
	}
	if a, b := strings.Index(string(first), "A:"), strings.Index(string(first), "C:"); a > b {
		t.Errorf("environment variables are not sorted:\n%s", first)
	}
}

func TestRenderOverrideRejectsIncompleteInput(t *testing.T) {
	tests := []struct {
		name string
		in   Override
	}{
		{"no service", Override{HostPort: 1, ContainerPort: 1}},
		{"no host port", Override{Service: "web", ContainerPort: 80}},
		{"no container port", Override{Service: "web", HostPort: 49580}},
		{"relative env file", Override{Service: "web", HostPort: 1, ContainerPort: 1, EnvFile: "../.env"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := RenderOverride(tt.in); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func TestOverridePathIsBesideTheWorktree(t *testing.T) {
	// Inside the worktree it would show up in the reviewer's diff and
	// in their editor.
	path := OverridePath("/state/pit/acme-shop-c56680", 482)

	if filepath.Dir(path) != filepath.FromSlash("/state/pit/acme-shop-c56680") {
		t.Errorf("Dir = %q, want the repository directory", filepath.Dir(path))
	}
	if !strings.Contains(filepath.Base(path), "482") {
		t.Errorf("Base = %q, want it to name the pull request", filepath.Base(path))
	}
}

// TestComposeMergesTheOverrideAsIntended is the acceptance criterion
// for T-303. It runs `docker compose config`, which needs neither
// images nor a network -- only the Compose binary.
func TestComposeMergesTheOverrideAsIntended(t *testing.T) {
	requireCompose(t)

	dir := t.TempDir()
	base := filepath.Join(dir, "docker-compose.yml")
	writeTestFile(t, base, `services:
  web:
    image: nginx:alpine
    ports:
      - "8080:80"
    environment:
      BASE_VAR: from-base
      NODE_ENV: production
  db:
    image: postgres:16
`)

	override := filepath.Join(dir, "override.yml")
	if err := WriteOverride(override, Override{
		Service:       "web",
		HostPort:      49580,
		ContainerPort: 80,
		Env:           map[string]string{"NODE_ENV": "development", "PIT": "1"},
	}); err != nil {
		t.Fatalf("WriteOverride: %v", err)
	}

	out, err := proc.Exec{}.Output(t.Context(), proc.Command{
		Name: "docker",
		Args: []string{"compose", "--project-name", "pit-test-1", "-f", base, "-f", override, "config"},
		Dir:  dir,
	})
	if err != nil {
		t.Fatalf("docker compose config: %v\n%s", err, out)
	}
	merged := string(out)

	t.Run("the assigned port is published", func(t *testing.T) {
		if !strings.Contains(merged, `published: "49580"`) {
			t.Errorf("the merged configuration does not publish 49580:\n%s", merged)
		}
	})

	// This is the reason !override exists. Compose appends sequences,
	// so without it the base file's 8080 would still be bound and two
	// sandboxes of this project would collide on it.
	t.Run("the base port is gone", func(t *testing.T) {
		if strings.Contains(merged, `published: "8080"`) {
			t.Errorf("the base port is still published; !override did not take effect:\n%s", merged)
		}
	})

	t.Run("environment is merged, not replaced", func(t *testing.T) {
		for _, want := range []string{"BASE_VAR", "from-base", "PIT"} {
			if !strings.Contains(merged, want) {
				t.Errorf("the merged configuration is missing %q:\n%s", want, merged)
			}
		}
	})

	t.Run("an overridden variable wins", func(t *testing.T) {
		if !strings.Contains(merged, "development") {
			t.Errorf("NODE_ENV was not overridden:\n%s", merged)
		}
	})

	t.Run("untouched services survive", func(t *testing.T) {
		if !strings.Contains(merged, "postgres:16") {
			t.Errorf("the db service disappeared:\n%s", merged)
		}
	})
}

func requireCompose(t *testing.T) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping: runs docker compose")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("skipping: docker is not installed")
	}
	if _, err := (proc.Exec{}).Output(t.Context(), proc.Command{
		Name: "docker", Args: []string{"compose", "version"},
	}); err != nil {
		t.Skip("skipping: docker compose is not usable here")
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

// Two sandboxes of one repository share nothing the compose file fixes:
// a container's name, a port bound on the host.
func TestOverrideIsolatesNamesAndPorts(t *testing.T) {
	data, err := RenderOverride(Override{
		Service: "web", HostPort: 41234, ContainerPort: 80, Project: "pit-shop-1-7",
		Images:  map[string]string{"worker": "registry/worker:abc"},
		Renamed: []string{"web", "worker"}, Unpublished: []string{"db", "web"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Services map[string]struct {
			ContainerName string    `yaml:"container_name"`
			Image         string    `yaml:"image"`
			Ports         yaml.Node `yaml:"ports"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &f); err != nil {
		t.Fatalf("%v:\n%s", err, data)
	}
	web, worker, db := f.Services["web"], f.Services["worker"], f.Services["db"]
	if web.ContainerName != "pit-shop-1-7-web" || web.Ports.Tag != "!override" {
		t.Errorf("web %+v:\n%s", web, data)
	}
	if worker.ContainerName != "pit-shop-1-7-worker" || worker.Image != "registry/worker:abc" {
		t.Errorf("worker %+v:\n%s", worker, data)
	}
	if db.Ports.Tag != "!reset" || db.ContainerName != "" {
		t.Errorf("db %+v:\n%s", db, data)
	}
	if strings.Count(string(data), "\n  worker:") != 1 {
		t.Errorf("worker twice:\n%s", data)
	}
}

func TestDevcontainerPathIsBesideTheOverride(t *testing.T) {
	for slot, want := range map[string]string{"": "pr-482.devcontainer.yml", "base": "pr-482-base.devcontainer.yml", "check": "pr-482-check.devcontainer.yml"} {
		got := DevcontainerPathIn("/state/pit/acme-shop-c56680", 482, slot)
		if got != filepath.Join("/state/pit/acme-shop-c56680", want) {
			t.Errorf("slot %q: %q", slot, got)
		}
	}
}

// A secret is named in the file and never written into it: Compose
// reads its value from the environment of the command that starts the
// services, and any other command sees none, without a warning.
func TestOverrideNamesSecretsWithoutTheirValues(t *testing.T) {
	data, err := RenderOverride(Override{Service: "web", HostPort: 49580, ContainerPort: 80,
		Env: map[string]string{"NODE_ENV": "development"}, Secrets: []string{"STRIPE_KEY", "DB_PASSWORD"}})
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		`      NODE_ENV: "development"` + "\n" + `      DB_PASSWORD: "${PIT_SECRET_DB_PASSWORD:-}"` + "\n" + `      STRIPE_KEY: "${PIT_SECRET_STRIPE_KEY:-}"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the override lacks %q:\n%s", want, text)
		}
	}
	// Secrets alone are an environment too.
	data, _ = RenderOverride(Override{Service: "web", HostPort: 49580, ContainerPort: 80, Secrets: []string{"STRIPE_KEY"}})
	if !strings.Contains(string(data), "    environment:\n      STRIPE_KEY:") {
		t.Errorf("no environment:\n%s", data)
	}

	requireCompose(t)
	dir := t.TempDir()
	base := filepath.Join(dir, "docker-compose.yml")
	writeTestFile(t, base, "services:\n  web:\n    image: nginx:alpine\n    environment:\n      STRIPE_KEY: ${STRIPE_KEY:-from-the-project}\n")
	override := filepath.Join(dir, "override.yml")
	if err := WriteOverride(override, Override{Service: "web", HostPort: 49580, ContainerPort: 80, Secrets: []string{"STRIPE_KEY"}}); err != nil {
		t.Fatal(err)
	}
	config := func(env ...string) string {
		t.Helper()
		var stdout, stderr strings.Builder
		err := proc.Exec{}.Stream(t.Context(), proc.Command{
			Name: "docker", Args: []string{"compose", "--project-name", "pit-test-1", "-f", base, "-f", override, "config"}, Dir: dir, Env: env,
		}, &stdout, &stderr)
		if err != nil {
			t.Fatalf("docker compose config: %v", err)
		}
		if strings.Contains(stderr.String(), "not set") {
			t.Errorf("compose warns: %s", stderr.String())
		}
		return stdout.String()
	}
	if got := config("PIT_SECRET_STRIPE_KEY=sk_test_123", "STRIPE_KEY=the-reviewers-own"); !strings.Contains(got, "STRIPE_KEY: sk_test_123") {
		t.Errorf("with the value:\n%s", got)
	}
	if got := config(); !strings.Contains(got, `STRIPE_KEY: ""`) || strings.Contains(got, "sk_test") {
		t.Errorf("without it:\n%s", got)
	}
}
