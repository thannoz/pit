package sandbox

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/runtime"
)

func TestMergedIsWhatComposeMakesOfTwoFiles(t *testing.T) {
	earlier := runtime.Service{Name: "app", Context: ".", Ports: []int{3000}, DependsOn: []string{"db"}, Named: true}
	later := runtime.Service{Name: "app", Image: "app:dev", Ports: []int{3000, 9229}, DependsOn: []string{"cache"}, Publishes: true}
	want := runtime.Service{Name: "app", Image: "app:dev", Context: ".", Ports: []int{3000, 9229},
		DependsOn: []string{"db", "cache"}, Named: true, Publishes: true}
	if got := merged(earlier, later); !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	// A later file that says little keeps what the earlier one said.
	if got := merged(earlier, runtime.Service{Name: "app"}); got.Context != "." || !reflect.DeepEqual(got.DependsOn, []string{"db"}) || !got.Named {
		t.Errorf("got %+v", got)
	}
	// A build a later file takes away is gone.
	if got := merged(earlier, runtime.Service{Name: "app", Image: "golang", Unbuilt: true}); got.Context != "" {
		t.Errorf("context = %q", got.Context)
	}
	// The later image wins.
	if got := merged(runtime.Service{Image: "a"}, runtime.Service{Image: "b"}); got.Image != "b" {
		t.Errorf("image = %q", got.Image)
	}
}

// A file pit writes lives outside the worktree and names the context
// where it is; what a commit changes is still found in it.
func TestAGeneratedFileIsReadWhereItIs(t *testing.T) {
	worktree := t.TempDir()
	generated := filepath.Join(t.TempDir(), "pr-7.devcontainer.yml")
	body := "services:\n  dev:\n    build:\n      context: " + filepath.Join(worktree, "docker") + "\n"
	if err := os.WriteFile(generated, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := inWorktree(worktree, generated); got != generated {
		t.Errorf("inWorktree = %q", got)
	}
	if got := inWorktree(worktree, "compose.yml"); got != filepath.Join(worktree, "compose.yml") {
		t.Errorf("inWorktree = %q", got)
	}
	services, err := buildableServices(&config.Config{Compose: config.Compose{Files: []string{generated}}}, worktree)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 || services[0].dir != "docker" {
		t.Errorf("services = %+v", services)
	}
	if !touches([]string{"docker/Dockerfile"}, services[0].dir) || touches([]string{"src/app.js"}, services[0].dir) {
		t.Error("the context is not where the changes are looked for")
	}
}

func TestABuildTakenAwayIsNotBuilt(t *testing.T) {
	worktree := t.TempDir()
	for name, body := range map[string]string{
		"docker-compose.yml": "services:\n  web:\n    build: .\n  api:\n    build: ./api\n",
		"dev.yml":            "services:\n  web:\n    build: !reset null\n    image: golang\n  api:\n    command: sleep infinity\n",
	} {
		if err := os.WriteFile(filepath.Join(worktree, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	services, err := buildableServices(&config.Config{Compose: config.Compose{Files: []string{"docker-compose.yml", "dev.yml"}}}, worktree)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 || services[0] != (service{name: "api", dir: "api"}) {
		t.Errorf("services = %+v", services)
	}
}
