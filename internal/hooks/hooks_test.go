package hooks

import (
	"slices"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/errs"
)

func sandbox() Sandbox {
	return Sandbox{
		Project: "pit-acme-shop-482",
		Files:   []string{"docker-compose.yml", "docker-compose.pit.yml"},
		Dir:     "/state/pit/acme-shop-c56680/pr-482",
	}
}

// TestDocumentedHookExpands is the acceptance criterion for T-204: the
// line the documentation shows has to become the command it promises.
func TestDocumentedHookExpands(t *testing.T) {
	got, err := Expand("compose exec -T api npm run migrate", sandbox())
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}

	want := []string{
		"compose",
		"--project-name", "pit-acme-shop-482",
		"--file", "docker-compose.yml",
		"--file", "docker-compose.pit.yml",
		"exec", "-T", "api", "npm", "run", "migrate",
	}

	if got.Name != "docker" {
		t.Errorf("Name = %q, want docker", got.Name)
	}
	if !slices.Equal(got.Args, want) {
		t.Errorf("Args =\n  %v\nwant\n  %v", got.Args, want)
	}
	if got.Dir != sandbox().Dir {
		t.Errorf("Dir = %q, want the worktree", got.Dir)
	}
}

func TestSecondDocumentedHookExpands(t *testing.T) {
	got, err := Expand("compose exec -T db psql -U app -d app -f /fixtures/standard.sql", sandbox())
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}

	line := got.Name + " " + strings.Join(got.Args, " ")
	want := "docker compose --project-name pit-acme-shop-482 " +
		"--file docker-compose.yml --file docker-compose.pit.yml " +
		"exec -T db psql -U app -d app -f /fixtures/standard.sql"

	if line != want {
		t.Errorf("expanded to\n  %s\nwant\n  %s", line, want)
	}
}

func TestIsolationFlagsComeBeforeTheSubcommand(t *testing.T) {
	// Compose treats --project-name and --file as global flags and
	// rejects them after the subcommand.
	got, err := Expand("compose exec -T db true", sandbox())
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}

	project, file, sub := slices.Index(got.Args, "--project-name"), slices.Index(got.Args, "--file"), slices.Index(got.Args, "exec")
	if project < 0 || file < 0 || sub < 0 {
		t.Fatalf("args %v are missing an expected element", got.Args)
	}
	if project > sub || file > sub {
		t.Errorf("isolation flags are at %d and %d, after the subcommand at %d", project, file, sub)
	}
}

func TestNonComposeCommandsRunAsWritten(t *testing.T) {
	// Not every hook belongs in a container. A script in the repository
	// should run in the worktree, untouched.
	got, err := Expand("./scripts/seed.sh --fast", sandbox())
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}

	if got.Name != "./scripts/seed.sh" {
		t.Errorf("Name = %q, want the script", got.Name)
	}
	if !slices.Equal(got.Args, []string{"--fast"}) {
		t.Errorf("Args = %v, want the flag unchanged", got.Args)
	}
	if got.Dir != sandbox().Dir {
		t.Errorf("Dir = %q, want the worktree", got.Dir)
	}
}

func TestShellEscapeHatch(t *testing.T) {
	// No shell is involved, so anyone needing a pipe asks for one.
	got, err := Expand(`sh -c "psql < dump.sql | tee log"`, sandbox())
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}

	if got.Name != "sh" {
		t.Errorf("Name = %q, want sh", got.Name)
	}
	if want := []string{"-c", "psql < dump.sql | tee log"}; !slices.Equal(got.Args, want) {
		t.Errorf("Args = %v, want %v", got.Args, want)
	}
}

func TestExpandAllNamesTheFailingLine(t *testing.T) {
	// A list of hooks reporting only "unbalanced quote" leaves the
	// author hunting through the file.
	_, err := ExpandAll(AfterUp([]string{
		"compose exec -T db true",
		`compose exec -T db psql -c "select 1`,
	}), sandbox())

	if err == nil {
		t.Fatal("want an error for the broken line")
	}
	if !strings.Contains(err.Error(), "hooks.after_up entry 2") {
		t.Errorf("error = %q, want it to name the second hook", err)
	}
	if errs.Hint(err) == "" {
		t.Error("the error carries no hint")
	}
}

func TestExpandAllKeepsOrder(t *testing.T) {
	// Migrations before seeds: the order in the file is the order the
	// author needs.
	cmds, err := ExpandAll(AfterUp([]string{"a", "b", "c"}), sandbox())
	if err != nil {
		t.Fatalf("ExpandAll: %v", err)
	}

	for i, want := range []string{"a", "b", "c"} {
		if cmds[i].Name != want {
			t.Errorf("command %d is %q, want %q", i, cmds[i].Name, want)
		}
	}
}

func TestExpandWithoutComposeFiles(t *testing.T) {
	s := sandbox()
	s.Files = nil

	got, err := Expand("compose ps", s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if slices.Contains(got.Args, "--file") {
		t.Errorf("args %v carry a --file flag although none is configured", got.Args)
	}
}

func TestExpandAllKeepsTheSpecificHint(t *testing.T) {
	// errs.Hint takes the outermost hint it finds, so a generic one
	// from wrapLine would be printed instead of the advice tokenize
	// already gives about the actual mistake.
	_, err := ExpandAll(AfterUp([]string{`compose exec -T db psql -c "select 1`}), sandbox())
	if err == nil {
		t.Fatal("want an error for the broken line")
	}

	if hint := errs.Hint(err); !strings.Contains(hint, "close the quote") {
		t.Errorf("hint = %q, want the one about the unbalanced quote", hint)
	}
}

func TestExpandAllNamesTheSettingTheLinesCameFrom(t *testing.T) {
	// The same machinery runs hooks and a scenario's fixtures. Saying
	// "entry 1" without saying where would send the author to the
	// wrong part of the file.
	list := List{
		Path:  `data.scenarios["standard"].apply`,
		Lines: []string{`psql -c "select 1`},
	}

	_, err := ExpandAll(list, sandbox())
	if err == nil {
		t.Fatal("want an error for the broken line")
	}
	if !strings.Contains(err.Error(), `data.scenarios["standard"].apply entry 1`) {
		t.Errorf("error = %q, want it to name the scenario's setting", err)
	}
}

func TestExecService(t *testing.T) {
	for line, want := range map[string]string{
		"compose exec -T db pg_dump -U app app":                    "db",
		"compose exec -T -u postgres db pg_dump":                   "db",
		"compose exec --user=postgres -T store pg_dump":            "store",
		"compose exec -e PGPASSWORD=x -w /tmp analytics mysqldump": "analytics",
		"compose exec --index 2 -T db sh -c 'pg_dump'":             "db",
		"compose run --rm db pg_dump":                              "",
		"pg_dump -h localhost":                                     "",
		"compose exec -T":                                          "",
		"compose exec 'db":                                         "",
	} {
		got, ok := ExecService(line)
		if got != want || ok != (want != "") {
			t.Errorf("ExecService(%q) = %q, %v; want %q", line, got, ok, want)
		}
	}
}

func TestExpandGivesTheEnvironment(t *testing.T) {
	env := []string{"PORT=40007"}
	for _, line := range []string{"npm run migrate", "compose exec -T web npm run migrate"} {
		c, err := Expand(line, Sandbox{Project: "p", Dir: "/wt", Env: env})
		if err != nil {
			t.Fatal(err)
		}
		if len(c.Env) != 1 || c.Env[0] != "PORT=40007" || c.Dir != "/wt" {
			t.Errorf("%s: %+v", line, c)
		}
	}
}

// A line that starts with kubectl is pointed at the sandbox, in a
// sandbox in Kubernetes; anywhere else it runs as written.
func TestExpandKubectl(t *testing.T) {
	flags := []string{"--kubeconfig", "/state/kube/config-kind", "--context", "kind-pit", "--namespace", "pit-shop-7"}
	s := Sandbox{Dir: "/wt", Kubectl: flags, Env: []string{"PIT_NAMESPACE=pit-shop-7"}}
	c, err := Expand("kubectl exec deploy/db -- psql -c 'select 1'", s)
	if err != nil {
		t.Fatal(err)
	}
	want := append(slices.Clone(flags), "exec", "deploy/db", "--", "psql", "-c", "select 1")
	if c.Name != "kubectl" || !slices.Equal(c.Args, want) || c.Dir != "/wt" || !slices.Equal(c.Env, s.Env) {
		t.Errorf("%+v", c)
	}
	// Expanding twice does not add the flags twice, nor does one line
	// write into another's: the flags may have room to spare.
	roomy := make([]string, len(flags), len(flags)+8)
	copy(roomy, flags)
	s.Kubectl = roomy
	first, _ := Expand("kubectl get pods", s)
	second, _ := Expand("kubectl logs deploy/web", s)
	if len(first.Args) != len(flags)+2 || first.Args[len(flags)] != "get" || second.Args[len(flags)] != "logs" {
		t.Errorf("%q, %q", first.Args, second.Args)
	}
	c, _ = Expand("kubectl get pods", Sandbox{Dir: "/wt"})
	if !slices.Equal(c.Args, []string{"get", "pods"}) {
		t.Errorf("%q", c.Args)
	}
}

// A sandbox of processes in a dev shell runs every command as written
// in it; the dev shell is not the pull request's to pick per line.
func TestExpandPutsTheDevShellInFront(t *testing.T) {
	wrap := make([]string, 0, 16)
	wrap = append(wrap, "nix", "develop", "/wt", "--command")
	s := Sandbox{Dir: "/wt", Env: []string{"PORT=40001"}, Wrap: wrap}
	first, err := Expand("npm run migrate", s)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := Expand("sh -c 'echo $PORT'", s)
	if first.Name != "nix" || !slices.Equal(first.Args, []string{"develop", "/wt", "--command", "npm", "run", "migrate"}) ||
		first.Dir != "/wt" || !slices.Equal(first.Env, s.Env) {
		t.Errorf("first = %+v", first)
	}
	// The dev shell has room to spare; one line does not write into
	// another's.
	if !slices.Equal(first.Args[3:], []string{"npm", "run", "migrate"}) || !slices.Equal(second.Args[3:], []string{"sh", "-c", "echo $PORT"}) {
		t.Errorf("first = %q, second = %q", first.Args, second.Args)
	}
	if !slices.Equal(wrap, []string{"nix", "develop", "/wt", "--command"}) {
		t.Errorf("wrap = %q", wrap)
	}
	// Without one, as written.
	if c, _ := Expand("npm run migrate", Sandbox{Dir: "/wt"}); c.Name != "npm" {
		t.Errorf("%+v", c)
	}
}
