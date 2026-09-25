package devcontainer

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/proc"
)

// Compose itself reads what pit writes: the entrypoint with its "$",
// the command taken away, the worktree mounted, a volume of its own.
func TestComposeTakesTheGeneratedFile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs docker compose")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("skipping: docker is not installed")
	}
	if _, err := (proc.Exec{}).Output(t.Context(), proc.Command{Name: "docker", Args: []string{"compose", "version"}}); err != nil {
		t.Skip("skipping: docker compose is not usable here")
	}

	worktree := t.TempDir()
	base := filepath.Join(worktree, "docker-compose.yml")
	if err := os.WriteFile(base, []byte("services:\n  web:\n    build: .\n    command: sleep infinity\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := Parse([]byte(`{"dockerComposeFile": "../docker-compose.yml", "service": "web",
		"mounts": ["source=cache,target=/cache,type=volume"], "containerEnv": {"P": "$HOME"}}`), vars)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Translate(f, Input{Path: ".devcontainer/devcontainer.json", Worktree: worktree, Start: "npm start"})
	if err != nil {
		t.Fatal(err)
	}
	generated := filepath.Join(t.TempDir(), "pr-7.devcontainer.yml")
	if err := os.WriteFile(generated, s.Compose, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := proc.Exec{}.Output(t.Context(), proc.Command{
		Name: "docker",
		Args: []string{"compose", "--project-name", "pit-dev-7", "-f", filepath.Join(worktree, s.Files[0]), "-f", generated, "config"},
		Dir:  worktree,
	})
	if err != nil {
		t.Fatalf("docker compose config: %v\n%s", err, out)
	}
	merged := string(out)
	for _, want := range []string{
		"tail -f " + LogFile + " & wait $$!",
		"P: $$HOME",
		"name: pit-dev-7_cache",
		"target: /cache",
	} {
		if !strings.Contains(merged, want) {
			t.Errorf("no %q in\n%s", want, merged)
		}
	}
	if strings.Contains(merged, "sleep infinity") {
		t.Errorf("the compose file's command is still there:\n%s", merged)
	}
}
