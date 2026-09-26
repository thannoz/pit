package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
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
			lines[i] = "  snapshot:"
			for j := i + 1; strings.HasPrefix(lines[j], "  #   "); j++ {
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
	if want := config.SuggestSnapshot("store", config.MariaDB); !reflect.DeepEqual(c.Data.Snapshot, want) {
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

// For PostgreSQL the file suggests what pit migrate-check counts with;
// uncommented, it is what .pit.yaml then holds.
func TestInitWritesCheckCommandsForPostgres(t *testing.T) {
	dir := inProject(t, "services:\n  db:\n    image: postgres:17\n  web:\n    image: nginx\n    ports: [\"8080:80\"]\n")
	if _, err := runInitCmd(t, "", "--service", "web", "--port", "80"); err != nil {
		t.Fatalf("init: %v", err)
	}
	path := filepath.Join(dir, config.FileName)
	written, _ := os.ReadFile(path)
	lines := strings.Split(string(written), "\n")
	found := false
	for i, l := range lines {
		if strings.HasPrefix(l, "  # check:") {
			found = true
			lines[i] = "  check:"
			for j := i + 1; j < len(lines) && strings.HasPrefix(lines[j], "  #   "); j++ {
				lines[j] = "  " + strings.TrimPrefix(lines[j], "  # ")
			}
			break
		}
	}
	if !found {
		t.Fatalf("no check block:\n%s", written)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatalf("uncommented, the file does not validate: %v", err)
	}
	if want := config.SuggestCheck("db", config.Postgres); !reflect.DeepEqual(c.Data.Check, want) {
		t.Errorf("Check\n got %#v\nwant %#v", c.Data.Check, want)
	}
}

// For a database pit knows no counting commands for, none are suggested.
func TestInitSuggestsNoCheckItCannotWrite(t *testing.T) {
	dir := inProject(t, "services:\n  store:\n    image: mariadb:11\n  web:\n    image: nginx\n    ports: [\"8080:80\"]\n")
	if _, err := runInitCmd(t, "", "--service", "web", "--port", "80"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if written, _ := os.ReadFile(filepath.Join(dir, config.FileName)); strings.Contains(string(written), "# check:") {
		t.Errorf("a check for MariaDB:\n%s", written)
	}
}

// pit init finds the compose file under any name Compose reads, and
// writes the one it found into .pit.yaml.
func TestInitFindsTheComposeFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services:\n  web:\n    image: nginx\n    ports: [\"8080:80\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if _, err := runInitCmd(t, "", "--service", "web", "--port", "80"); err != nil {
		t.Fatalf("init: %v", err)
	}
	c, err := config.Load(filepath.Join(dir, config.FileName))
	if err != nil || len(c.Compose.Files) != 1 || c.Compose.Files[0] != "compose.yaml" {
		t.Errorf("%v, %v", c, err)
	}
}

func TestInitWithoutAComposeFile(t *testing.T) {
	t.Chdir(t.TempDir())
	_, err := runInitCmd(t, "", "--service", "web", "--port", "80")
	if err == nil || !strings.Contains(err.Error(), "there is no compose file in") || !strings.Contains(errs.Hint(err), "compose.yaml, compose.yml, docker-compose.yaml, docker-compose.yml") {
		t.Errorf("err = %v, hint %q", err, errs.Hint(err))
	}
}

// devProject is a project that has a devcontainer.json and no compose
// file.
func devProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
	return dir
}

func TestInitReadsADevcontainer(t *testing.T) {
	dir := devProject(t, map[string]string{
		".devcontainer/devcontainer.json": `{
			// microsoft/vscode-remote-try-node's, which forwards no port
			"image": "mcr.microsoft.com/devcontainers/javascript-node:1-18-bullseye",
			"portsAttributes": {"3000": {"label": "Hello Remote World"}},
			"postCreateCommand": "npm install"
		}`,
		"package.json": `{"scripts": {"start": "node server.js", "dev": "nodemon"}}`,
	})
	out, err := runInitCmd(t, "")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Wrote .pit.yaml: dev on port 3000, from .devcontainer/devcontainer.json") {
		t.Errorf("out = %s", out)
	}
	c, err := config.Load(filepath.Join(dir, config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if c.Devcontainer.File != ".devcontainer/devcontainer.json" || c.Devcontainer.Start != "npm start" ||
		c.Web.Service != "dev" || c.Web.Port != 3000 || len(c.Compose.Files) != 0 {
		t.Errorf("config = %+v", c)
	}
}

func TestInitReadsADevcontainerOfComposeFiles(t *testing.T) {
	dir := devProject(t, map[string]string{
		".devcontainer/devcontainer.json": `{"dockerComposeFile": "compose.yml", "service": "app", "forwardPorts": ["db:5432", 8001]}`,
		".devcontainer/compose.yml":       "services:\n  app:\n    build: .\n    ports: [\"8000:8000\"]\n  db:\n    image: postgres:16\n",
		"package.json":                    `{"scripts": {"dev": "vite"}}`,
	})
	if out, err := runInitCmd(t, ""); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	c, err := config.Load(filepath.Join(dir, config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	// The service worked in, the port the file forwards for it before
	// the one its compose file names -- not the database's -- and the
	// database found for the data section.
	if c.Web.Service != "app" || c.Web.Port != 8001 || c.Devcontainer.Start != "npm run dev" || c.Data.Service != "db" {
		t.Errorf("config = %+v", c)
	}
}

func TestInitWithADevcontainerAndAComposeFileTakesTheComposeFile(t *testing.T) {
	dir := devProject(t, map[string]string{
		".devcontainer/devcontainer.json": `{"image": "node"}`,
		"compose.yaml":                    "services:\n  web:\n    image: nginx\n",
	})
	if _, err := runInitCmd(t, "", "--port", "80"); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(filepath.Join(dir, config.FileName))
	if err != nil || c.Devcontainer.File != "" || c.Compose.Files[0] != "compose.yaml" {
		t.Errorf("%+v, %v", c, err)
	}
}

func TestInitWithABrokenDevcontainer(t *testing.T) {
	devProject(t, map[string]string{".devcontainer/devcontainer.json": `{"name": "nothing to run"}`})
	_, err := runInitCmd(t, "")
	if err == nil || !strings.Contains(err.Error(), "neither which image") {
		t.Errorf("err = %v", err)
	}
}

func TestStartOf(t *testing.T) {
	for body, want := range map[string]string{
		`{"scripts": {"start": "node ."}}`: "npm start",
		`{"scripts": {"dev": "vite"}}`:     "npm run dev",
		`{"scripts": {"test": "jest"}}`:    "",
		`not json`:                         "",
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := startOf(dir); got != want {
			t.Errorf("%s: got %q, want %q", body, got, want)
		}
	}
	if got := startOf(t.TempDir()); got != "" {
		t.Errorf("without a package.json: %q", got)
	}
}

func TestInitReadsAProcfile(t *testing.T) {
	dir := devProject(t, map[string]string{
		"Procfile":          "worker: node jobs.js\nweb: node index.js\n",
		"package.json":      `{"scripts": {"start": "node index.js"}}`,
		"package-lock.json": "{}",
	})
	out, err := runInitCmd(t, "")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	// The web process, without asking.
	if !strings.Contains(out, "Wrote .pit.yaml: web, from Procfile") || !strings.Contains(out, "not in containers") || strings.Contains(out, "Which service") {
		t.Errorf("out = %s", out)
	}
	c, err := config.Load(filepath.Join(dir, config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if c.Processes.File != "Procfile" || !reflect.DeepEqual(c.Processes.Setup, []string{"npm ci"}) || c.Web.Service != "web" {
		t.Errorf("config = %+v", c)
	}
}

func TestInitPrefersTheProcfileForDevelopment(t *testing.T) {
	dir := devProject(t, map[string]string{
		"Procfile":     "web: bundle exec puma -C config/puma.rb\n",
		"Procfile.dev": "css: bin/rails tailwindcss:watch\napp: bin/rails server\n",
		"Gemfile":      "source 'https://rubygems.org'\n",
		"yarn.lock":    "",
	})
	// No web process: which one it is, is asked.
	if out, err := runInitCmd(t, "2\n"); err != nil || !strings.Contains(out, "Which service does a reviewer open") {
		t.Errorf("err = %v\n%s", err, out)
	}
	if c, err := config.Load(filepath.Join(dir, config.FileName)); err != nil || c.Web.Service != "app" {
		t.Errorf("%+v, %v", c, err)
	}
	if _, err := runInitCmd(t, "", "--service", "mailer", "--force"); err == nil || !strings.Contains(err.Error(), `"mailer" is not a process in Procfile.dev`) {
		t.Errorf("err = %v", err)
	}
	if _, err := runInitCmd(t, "", "--service", "app", "--force"); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(filepath.Join(dir, config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if c.Processes.File != "Procfile.dev" || c.Web.Service != "app" ||
		!reflect.DeepEqual(c.Processes.Setup, []string{"yarn install --frozen-lockfile", "bundle install"}) {
		t.Errorf("config = %+v", c)
	}
}

func TestSetupOf(t *testing.T) {
	for files, want := range map[string][]string{
		"pnpm-lock.yaml,package.json": {"pnpm install --frozen-lockfile"},
		"package.json":                {"npm install"},
		"go.mod":                      nil,
	} {
		dir := t.TempDir()
		for _, f := range strings.Split(files, ",") {
			if err := os.WriteFile(filepath.Join(dir, f), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if got := setupOf(dir); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %q, want %q", files, got, want)
		}
	}
}

func TestInitWithNothingToRun(t *testing.T) {
	devProject(t, map[string]string{"README.md": "hello"})
	_, err := runInitCmd(t, "")
	if err == nil || !strings.Contains(err.Error(), "no devcontainer.json and no Procfile") || !strings.Contains(errs.Hint(err), "Procfile.dev, Procfile") {
		t.Errorf("err = %v, hint %q", err, errs.Hint(err))
	}
}
