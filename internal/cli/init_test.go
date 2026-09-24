package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/errs"
)

const exampleCompose = `services:
  db:
    image: postgres:16
    expose: ["5432"]
  redis:
    image: redis:7
  api:
    image: node:22
    ports: ["4000:4000"]
  web:
    image: nginx:alpine
    ports: ["8080:80"]
`

// inProject runs fn with the working directory set to a fresh project.
func inProject(t *testing.T, compose string) string {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Chdir(dir)
	return dir
}

// runInit executes `pit init` with the given arguments and stdin.
func runInitCmd(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()

	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(append([]string{"init"}, args...))

	err := cmd.Execute()
	return out.String(), err
}

// TestInitProducesAValidConfiguration is the acceptance criterion for
// T-205: what init writes has to survive the validation from T-203.
func TestInitProducesAValidConfiguration(t *testing.T) {
	dir := inProject(t, exampleCompose)

	out, err := runInitCmd(t, "", "--service", "web", "--port", "80")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}

	c, err := config.Load(filepath.Join(dir, config.FileName))
	if err != nil {
		t.Fatalf("the generated file does not validate: %v", err)
	}
	if c.Web.Service != "web" || c.Web.Port != 80 {
		t.Errorf("Web = %+v, want {web 80}", c.Web)
	}
}

func TestInitAsksWhichServiceToOpen(t *testing.T) {
	dir := inProject(t, exampleCompose)

	// Answer "4" for web, then accept the suggested port.
	out, err := runInitCmd(t, "4\n\n")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}

	for _, want := range []string{"db", "redis", "api", "web", "postgres:16", "port 80"} {
		if !strings.Contains(out, want) {
			t.Errorf("the prompt does not mention %q:\n%s", want, out)
		}
	}

	c, err := config.Load(filepath.Join(dir, config.FileName))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Web.Service != "web" {
		t.Errorf("Service = %q, want web", c.Web.Service)
	}
	// The port was taken from the compose file rather than asked for
	// again.
	if c.Web.Port != 80 {
		t.Errorf("Port = %d, want the 80 from the compose file", c.Web.Port)
	}
}

func TestInitAcceptsAServiceByName(t *testing.T) {
	dir := inProject(t, exampleCompose)

	if _, err := runInitCmd(t, "api\n\n"); err != nil {
		t.Fatalf("init: %v", err)
	}

	c, err := config.Load(filepath.Join(dir, config.FileName))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Web.Service != "api" || c.Web.Port != 4000 {
		t.Errorf("Web = %+v, want {api 4000}", c.Web)
	}
}

func TestInitSkipsTheQuestionForASingleService(t *testing.T) {
	inProject(t, "services:\n  app:\n    image: nginx\n    ports: [\"3000:3000\"]\n")

	out, err := runInitCmd(t, "\n")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Only one service") {
		t.Errorf("want init to say it chose for us:\n%s", out)
	}
}

func TestInitRefusesToOverwrite(t *testing.T) {
	dir := inProject(t, exampleCompose)
	existing := filepath.Join(dir, config.FileName)
	if err := os.WriteFile(existing, []byte("# hand written\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := runInitCmd(t, "", "--service", "web", "--port", "80")
	if err == nil {
		t.Fatal("want an error rather than a silently overwritten file")
	}
	if !strings.Contains(errs.Hint(err), "--force") {
		t.Errorf("hint = %q, want it to mention --force", errs.Hint(err))
	}

	kept, _ := os.ReadFile(existing)
	if string(kept) != "# hand written\n" {
		t.Error("the existing file was overwritten anyway")
	}
}

func TestInitOverwritesWithForce(t *testing.T) {
	dir := inProject(t, exampleCompose)
	if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte("# old\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := runInitCmd(t, "", "--service", "web", "--port", "80", "--force"); err != nil {
		t.Fatalf("init --force: %v", err)
	}
	if _, err := config.Load(filepath.Join(dir, config.FileName)); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestInitRejectsAServiceThatIsNotThere(t *testing.T) {
	inProject(t, exampleCompose)

	_, err := runInitCmd(t, "", "--service", "nope", "--port", "80")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(errs.Hint(err), "web") {
		t.Errorf("hint = %q, want it to list the real services", errs.Hint(err))
	}
}

func TestInitSuggestsTheServiceThatWasMeant(t *testing.T) {
	inProject(t, exampleCompose)

	_, err := runInitCmd(t, "", "--service", "wbe", "--port", "80")
	if err == nil {
		t.Fatal("want an error")
	}
	if hint := errs.Hint(err); !strings.Contains(hint, `did you mean "web"?`) {
		t.Errorf("hint = %q, want the suggestion", hint)
	}
}

func TestInitWithoutACompose(t *testing.T) {
	t.Chdir(t.TempDir())

	_, err := runInitCmd(t, "", "--service", "web", "--port", "80")
	if err == nil {
		t.Fatal("want an error without a compose file")
	}
	if errs.Hint(err) == "" {
		t.Error("the error carries no hint")
	}
}

func TestInitGeneratesReadableGuidance(t *testing.T) {
	dir := inProject(t, exampleCompose)

	if _, err := runInitCmd(t, "", "--service", "web", "--port", "80"); err != nil {
		t.Fatalf("init: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, config.FileName))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	generated := string(data)

	// The sections a project will need next are present but switched
	// off, so the format is learned from the file rather than the docs.
	for _, want := range []string{"# hooks:", "# migrate:", "# env:", "# snapshot:", "# - name: standard"} {
		if !strings.Contains(generated, want) {
			t.Errorf("the generated file is missing %q", want)
		}
	}
	// The guessed database service is a real one from this project.
	if !strings.Contains(generated, "compose exec -T db") {
		t.Errorf("the data example does not name the project's own db service:\n%s", generated)
	}
}

func TestInitIsNotInteractiveWithoutAnswers(t *testing.T) {
	// A scripted run must fail with an explanation rather than block on
	// a question nobody will see.
	inProject(t, exampleCompose)

	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(devNull(t))
	cmd.SetArgs([]string{"init"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("want an error when there is nobody to ask")
	}
	if !strings.Contains(errs.Hint(err), "--service") {
		t.Errorf("hint = %q, want it to point at --service", errs.Hint(err))
	}
}

func devNull(t *testing.T) *os.File {
	t.Helper()

	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("opening %s: %v", os.DevNull, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// TestInitWritesAUsableScenario is the acceptance criterion for T-409.
func TestInitWritesAUsableScenario(t *testing.T) {
	dir := inProject(t, exampleCompose)

	if _, err := runInitCmd(t, "", "--service", "web", "--port", "80"); err != nil {
		t.Fatalf("init: %v", err)
	}

	c, err := config.Load(filepath.Join(dir, config.FileName))
	if err != nil {
		t.Fatalf("the generated file does not validate: %v", err)
	}

	if len(c.Data.Scenarios) != 1 || c.Data.Scenarios[0].Name != "empty" {
		t.Fatalf("Scenarios = %+v, want one called empty", c.Data.Scenarios)
	}
	if c.Data.Scenarios[0].Description == "" {
		t.Error("the scenario has no description, so the listing would show a dash")
	}
	// It has to be reachable without editing anything: a scaffold that
	// needs a change before it works is a comment with extra steps.
	if c.Data.Default != "empty" {
		t.Errorf("Default = %q, want the scenario that was written", c.Data.Default)
	}
	if len(c.Data.Scenarios[0].Apply) != 0 {
		t.Errorf("Apply = %v, want no commands; a generated command would name files that do not exist",
			c.Data.Scenarios[0].Apply)
	}
	// The one that needs project knowledge stays switched off.
	if c.Data.Service != "db" {
		t.Errorf("Service = %q, want the project's own database service", c.Data.Service)
	}
}

func TestInitScenarioIsListedRightAway(t *testing.T) {
	// End to end, because that is how someone meets it: set up a
	// project, ask what it offers.
	inProject(t, exampleCompose)

	if _, err := runInitCmd(t, "", "--service", "web", "--port", "80"); err != nil {
		t.Fatalf("init: %v", err)
	}

	out, _, err := run(t, "scenarios")
	if err != nil {
		t.Fatalf("pit scenarios: %v", err)
	}
	if !lineWith(out, "* ", "empty", "Migrations only") {
		t.Errorf("the generated scenario is not listed as the default:\n%s", out)
	}
}

func TestInitLeavesOutADatabaseThatIsNotThere(t *testing.T) {
	// A name in a comment is an example; a name written as
	// configuration is a claim about this project.
	dir := inProject(t, "services:\n  app:\n    image: nginx\n    ports: [\"8080:80\"]\n")

	if _, err := runInitCmd(t, "", "--service", "app", "--port", "80"); err != nil {
		t.Fatalf("init: %v", err)
	}

	c, err := config.Load(filepath.Join(dir, config.FileName))
	if err != nil {
		t.Fatalf("the generated file does not validate: %v", err)
	}
	if c.Data.Service != "" {
		t.Errorf("Service = %q, but this project has no database service", c.Data.Service)
	}
	if c.Data.Default != "empty" {
		t.Errorf("Default = %q; the scenario is useful with or without a database", c.Data.Default)
	}
}

// A database is recognised by its image, whatever the service is
// called, and the snapshot commands in the comments are the ones for
// that database: uncommented, they are what .pit.yaml then holds.
func TestInitWritesSnapshotCommandsForTheDatabase(t *testing.T) {
	dir := inProject(t, "services:\n  store:\n    image: mariadb:11\n  web:\n    image: nginx\n    ports: [\"8080:80\"]\n")
	if _, err := runInitCmd(t, "", "--service", "web", "--port", "80"); err != nil {
		t.Fatalf("init: %v", err)
	}
	path := filepath.Join(dir, config.FileName)
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "These are for store, which runs MariaDB") {
		t.Errorf("no word of MariaDB:\n%s", written)
	}

	// Uncomment the snapshot block, as someone setting it up would.
	lines := strings.Split(string(written), "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "  # snapshot:") {
			for j := i; j < i+5; j++ {
				lines[j] = "  " + strings.TrimPrefix(lines[j], "  # ")
			}
			break
		}
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatalf("uncommented, the file does not validate: %v", err)
	}
	if c.Data.Service != "store" {
		t.Errorf("Service = %q, want store", c.Data.Service)
	}
	if want := config.SuggestSnapshot("store", config.MariaDB); c.Data.Snapshot != want {
		t.Errorf("Snapshot\n got %#v\nwant %#v", c.Data.Snapshot, want)
	}
}

func TestInitSnapshotExampleWithoutADatabase(t *testing.T) {
	dir := inProject(t, "services:\n  app:\n    image: nginx\n    ports: [\"8080:80\"]\n")
	if _, err := runInitCmd(t, "", "--service", "app", "--port", "80"); err != nil {
		t.Fatalf("init: %v", err)
	}
	written, _ := os.ReadFile(filepath.Join(dir, config.FileName))
	if !strings.Contains(string(written), "An example for PostgreSQL") || strings.Contains(string(written), "which runs") {
		t.Errorf("the example claims to know the database:\n%s", written)
	}
}
