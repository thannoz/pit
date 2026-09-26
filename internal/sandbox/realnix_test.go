package sandbox_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/thannoz/pit/internal/runtime"
	"github.com/thannoz/pit/internal/runtime/runtimetest"
)

const realFlake = `{
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.05";
  outputs = { nixpkgs, ... }: {
    devShells = nixpkgs.lib.genAttrs [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ] (system:
      let pkgs = nixpkgs.legacyPackages.${system}; in {
        default = pkgs.mkShell {
          packages = [ pkgs.hello ];
          shellHook = ''export GREETING="hi from the flake"'';
        };
      });
  };
}
`

// With nix itself, where it is installed: CI has a job for it. The
// tool and the shell hook's variable reach the setup, and no lock file
// is left in the checkout.
func TestProcessesRunInARealFlake(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	if _, err := exec.LookPath("nix"); err != nil {
		if os.Getenv("PIT_REQUIRE_NIX") != "" {
			t.Fatal("PIT_REQUIRE_NIX is set, and nix is not installed")
		}
		t.Skip("skipping: nix is not installed")
	}
	yaml := "processes:\n  file: Procfile\n  environment: nix\n  setup:\n    - \"sh -c 'hello --greeting=\\\"$GREETING\\\" > hello.txt'\"\nweb:\n  service: web\n"
	m, req := processFixture(t, "web: hello\n", yaml)
	pushToPullRequest(t, req.Repo.Root, 7, map[string]string{"flake.nix": realFlake})
	req.Confirm = func(string) bool { return true }
	m.Runtime = runtime.Either{Compose: runtimetest.New("web"), Processes: runtimetest.New("web")}
	box, err := m.Up(t.Context(), req, &quietReporter{stderr: os.Stderr})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(box.Worktree, "hello.txt")); err != nil || string(got) != "hi from the flake\n" {
		t.Errorf("hello.txt = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(box.Worktree, "flake.lock")); !os.IsNotExist(err) {
		t.Errorf("a lock file was written: %v", err)
	}
}
