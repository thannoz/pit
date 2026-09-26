package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/devcontainer"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/runtime"
	"github.com/thannoz/pit/internal/runtime/local"
	"github.com/thannoz/pit/internal/suggest"
	"github.com/thannoz/pit/internal/ui"
)

type initOptions struct {
	composeFiles []string
	service      string
	port         int
	force        bool
}

func newInitCmd(_ *globalOptions) *cobra.Command {
	o := &initOptions{}

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a .pit.yaml for this project",
		Long: `Read the project's compose file, ask which service a reviewer opens,
and write a .pit.yaml next to it.

The file is meant to be checked in: every reviewer uses the same one.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runInit(c, o)
		},
	}

	f := cmd.Flags()
	f.StringSliceVar(&o.composeFiles, "compose-file", nil, "compose file to use (repeatable; default docker-compose.yml)")
	f.StringVar(&o.service, "service", "", "the service a reviewer opens (skips the question)")
	f.IntVar(&o.port, "port", 0, "the port that service listens on inside its container")
	f.BoolVar(&o.force, "force", false, "overwrite an existing "+config.FileName)

	return cmd
}

func runInit(c *cobra.Command, o *initOptions) error {
	dir, err := os.Getwd()
	if err != nil {
		return errs.Wrap(err, "cannot determine the current directory")
	}

	out := ui.New(c.OutOrStdout(), c.ErrOrStderr())
	target := filepath.Join(dir, config.FileName)

	if _, err := os.Stat(target); err == nil && !o.force {
		return errs.New("%s already exists", config.FileName).
			WithHint("pass --force to overwrite it")
	}

	files := o.composeFiles
	var dev devFound
	if len(files) == 0 {
		name, ok := config.FindComposeFile(dir)
		if ok {
			files = []string{name}
		} else if dev, ok, err = findDevcontainer(dir); err != nil {
			return err
		} else if !ok {
			procfile, found := findProcfile(dir)
			if !found {
				return errs.New("there is no compose file in %s, no devcontainer.json and no Procfile", dir).
					WithHint("pit looks for %s, %s and %s; --compose-file names another",
						strings.Join(config.ComposeNames, ", "), strings.Join(devcontainer.Places, ", "), strings.Join(procfiles, ", "))
			}
			return initProcesses(c, o, out, dir, target, procfile)
		}
	}

	var services []runtime.Service
	if dev.file != "" {
		services = dev.services
		if o.service == "" {
			o.service = dev.service
		}
	} else if services, err = runtime.ReadServices(filepath.Join(dir, files[0])); err != nil {
		return err
	}

	service, port, err := chooseService(c, out, services, o)
	if err != nil {
		return err
	}

	opts := config.InitOptions{
		Devcontainer: dev.file,
		Start:        dev.start,
		ComposeFiles: files,
		WebService:   service,
		WebPort:      port,
		Services:     names(services),
		Databases:    databases(services),
	}
	data, err := config.Render(opts)
	if err != nil {
		return err
	}

	if err := os.WriteFile(target, data, 0o644); err != nil { //nolint:gosec // a configuration file is meant to be readable
		return errs.Wrap(err, "cannot write %s", config.FileName)
	}

	// Writing a file that pit itself would reject would be a poor first
	// impression, so the result is loaded back before saying it worked.
	if _, err := config.Load(target); err != nil {
		return errs.Wrap(err, "the generated %s does not validate", config.FileName).
			WithHint("this is a bug in pit; please report it")
	}

	out.Printf("Wrote %s: %s\n", config.FileName, opts.Summary())
	out.Printf("Check it in, then run `pit <pull request number>`.\n")
	return nil
}

// chooseService settles on the service and port, asking only for what
// was not given on the command line.
func chooseService(c *cobra.Command, out *ui.Printer, services []runtime.Service, o *initOptions) (string, int, error) {
	service := o.service
	if service == "" {
		var err error
		service, err = askService(c, out, services)
		if err != nil {
			return "", 0, err
		}
	}

	found, ok := find(services, service)
	if !ok {
		declared := names(services)
		e := errs.New("%q is not a service in the compose file", service)
		if near := suggest.Closest(service, declared); near != "" {
			return "", 0, e.WithHint("did you mean %q? the file declares: %s", near, strings.Join(declared, ", "))
		}
		return "", 0, e.WithHint("the file declares: %s", strings.Join(declared, ", "))
	}

	port := o.port
	if port == 0 {
		port = askPort(c, out, found)
	}
	if port <= 0 || port > 65535 {
		return "", 0, errs.New("%d is not a usable port", port).
			WithHint("use the port %s listens on inside its container", service)
	}
	return service, port, nil
}

func askService(c *cobra.Command, out *ui.Printer, services []runtime.Service) (string, error) {
	if len(services) == 1 {
		out.Printf("Only one service in the compose file: %s\n", services[0].Name)
		return services[0].Name, nil
	}
	if !interactive(c) {
		return "", errs.New("cannot ask which service a reviewer opens").
			WithHint("pass --service (the file declares: %s)", strings.Join(names(services), ", "))
	}

	out.Printf("Which service does a reviewer open in a browser?\n")
	for i, s := range services {
		out.Printf("  %d) %-16s %s\n", i+1, s.Name, describeService(s))
	}

	answer := prompt(c, out, fmt.Sprintf("Number or name [1-%d]: ", len(services)))
	if n, err := strconv.Atoi(answer); err == nil && n >= 1 && n <= len(services) {
		return services[n-1].Name, nil
	}
	if _, ok := find(services, answer); ok {
		return answer, nil
	}
	return "", errs.New("%q is not one of the services", answer).
		WithHint("answer with a number from 1 to %d, or a service name", len(services))
}

func askPort(c *cobra.Command, out *ui.Printer, s runtime.Service) int {
	suggested := 0
	if len(s.Ports) > 0 {
		suggested = s.Ports[0]
	}

	if !interactive(c) {
		return suggested
	}

	question := fmt.Sprintf("Which port does %s listen on inside its container", s.Name)
	if suggested > 0 {
		question += fmt.Sprintf(" [%d]", suggested)
	}
	answer := prompt(c, out, question+": ")

	if answer == "" {
		return suggested
	}
	if n, err := strconv.Atoi(answer); err == nil {
		return n
	}
	return 0
}

func describeService(s runtime.Service) string {
	var parts []string
	if s.Image != "" {
		parts = append(parts, s.Image)
	}
	switch len(s.Ports) {
	case 0:
	case 1:
		parts = append(parts, fmt.Sprintf("port %d", s.Ports[0]))
	default:
		parts = append(parts, fmt.Sprintf("ports %v", s.Ports))
	}
	return strings.Join(parts, ", ")
}

// interactive reports whether there is anyone to answer a question.
// Without this a scripted run would block forever on a prompt nobody
// sees, or -- worse -- read EOF and carry on with an empty answer.
//
// An *os.File is only worth asking when it is a terminal. /dev/null is
// a character device but there is nobody behind it. Anything that is
// not a file is a test or a pipe deliberately supplying answers.
func interactive(c *cobra.Command) bool {
	in := c.InOrStdin()
	if in == nil {
		return false
	}

	f, ok := in.(*os.File)
	if !ok {
		// A test or another reader handed in on purpose.
		return true
	}
	if term.IsTerminal(int(f.Fd())) {
		return true
	}

	info, err := f.Stat()
	if err != nil {
		return false
	}
	// A pipe or a redirected file is someone supplying answers
	// deliberately, and should be read. A character device that is not
	// a terminal is /dev/null: reading it returns EOF forever, and
	// carrying on with an empty answer would be worse than refusing.
	return info.Mode()&os.ModeCharDevice == 0
}

func prompt(c *cobra.Command, out *ui.Printer, question string) string {
	out.Printf("%s", question)

	line, err := lineReader(c.InOrStdin()).ReadString('\n')
	if err != nil && err != io.EOF {
		return ""
	}
	return strings.TrimSpace(line)
}

// readers keeps one buffered reader for each input, so that a command
// asking twice gets the second answer too: a reader of its own for each
// question would have buffered it away with the first.
var readers sync.Map

func lineReader(in io.Reader) *bufio.Reader {
	r, _ := readers.LoadOrStore(in, bufio.NewReader(in))
	return r.(*bufio.Reader)
}

func find(services []runtime.Service, name string) (runtime.Service, bool) {
	for _, s := range services {
		if s.Name == name {
			return s, true
		}
	}
	return runtime.Service{}, false
}

func names(services []runtime.Service) []string {
	out := make([]string, 0, len(services))
	for _, s := range services {
		out = append(out, s.Name)
	}
	return out
}

func databases(services []runtime.Service) []config.Database {
	out := make([]config.Database, 0, len(services))
	for _, s := range services {
		out = append(out, config.Database{Service: s.Name, Image: s.Image})
	}
	return out
}

// devFound is what pit init read of a devcontainer.json.
type devFound struct {
	file     string
	service  string
	start    string
	services []runtime.Service
}

// findDevcontainer reads the project's devcontainer.json, for a project
// without a compose file: the services it describes, the one worked in
// with the ports it forwards, and what could start the app.
func findDevcontainer(dir string) (devFound, bool, error) {
	file, ok := devcontainer.Find(dir)
	if !ok {
		return devFound{}, false, nil
	}
	f, err := devcontainer.Load(filepath.Join(dir, file), devcontainer.Vars{Workspace: dir, Basename: filepath.Base(dir)})
	if err != nil {
		return devFound{}, false, err
	}
	found := devFound{file: file, service: devcontainer.Service, start: startOf(dir)}
	if f.Compose() {
		found.service = f.Service
		for _, c := range f.ComposeFiles {
			declared, err := runtime.ReadServices(filepath.Join(dir, filepath.Dir(file), c))
			if err == nil {
				found.services = append(found.services, declared...)
			}
		}
	}
	for i, s := range found.services {
		if s.Name == found.service {
			found.services[i].Ports = append(f.Ports(), s.Ports...)
			return found, true, nil
		}
	}
	found.services = append([]runtime.Service{{Name: found.service, Image: f.Image, Ports: f.Ports()}}, found.services...)
	return found, true, nil
}

// startOf is what starts a project the way its own scripts say, where
// they say it plainly: a package.json's start or dev script.
func startOf(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return ""
	}
	var p struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(data, &p) != nil {
		return ""
	}
	switch {
	case p.Scripts["start"] != "":
		return "npm start"
	case p.Scripts["dev"] != "":
		return "npm run dev"
	}
	return ""
}

// procfiles are the Procfiles pit init looks for: the one for
// development first, where a project has both.
var procfiles = []string{"Procfile.dev", "Procfile"}

func findProcfile(dir string) (string, bool) {
	for _, name := range procfiles {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil && !info.IsDir() {
			return name, true
		}
	}
	return "", false
}

// devShellOf is the dev shell a project's processes run in: devenv's,
// if it has one, which is also a flake of sorts, or its flake's.
func devShellOf(dir string) string {
	for _, f := range []struct{ file, environment string }{
		{"devenv.nix", config.DevenvEnvironment},
		{"flake.nix", config.NixEnvironment},
	} {
		if info, err := os.Stat(filepath.Join(dir, f.file)); err == nil && !info.IsDir() {
			return f.environment
		}
	}
	return ""
}

// initProcesses writes a .pit.yaml for a project whose services are the
// processes of a Procfile.
func initProcesses(c *cobra.Command, o *initOptions, out *ui.Printer, dir, target, procfile string) error {
	procs, err := local.ReadProcfile(filepath.Join(dir, procfile))
	if err != nil {
		return err
	}
	var services []runtime.Service
	for _, p := range procs {
		services = append(services, runtime.Service{Name: p.Name})
	}
	service := o.service
	switch {
	case service != "":
		if _, ok := find(services, service); !ok {
			return errs.New("%q is not a process in %s", service, procfile).
				WithHint("it names: %s", strings.Join(names(services), ", "))
		}
	case slices.ContainsFunc(procs, func(p local.Process) bool { return p.Name == "web" }):
		service = "web"
	default:
		if service, err = askService(c, out, services); err != nil {
			return err
		}
	}
	opts := config.InitOptions{
		Processes:   procfile,
		Setup:       setupOf(dir),
		Environment: devShellOf(dir),
		WebService:  service,
		Services:    names(services),
	}
	data, err := config.Render(opts)
	if err != nil {
		return err
	}
	if err := os.WriteFile(target, data, 0o644); err != nil { //nolint:gosec // a configuration file is meant to be readable
		return errs.Wrap(err, "cannot write %s", config.FileName)
	}
	if _, err := config.Load(target); err != nil {
		return errs.Wrap(err, "the generated %s does not validate", config.FileName).
			WithHint("this is a bug in pit; please report it")
	}
	out.Printf("Wrote %s: %s\n", config.FileName, opts.Summary())
	out.Printf("The processes run on this machine, not in containers. Check it in, then run `pit <pull request number>`.\n")
	return nil
}

// setupOf is what installs a project's dependencies, where its lock
// file says plainly how.
func setupOf(dir string) []string {
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}
	var out []string
	switch {
	case has("pnpm-lock.yaml"):
		out = append(out, "pnpm install --frozen-lockfile")
	case has("yarn.lock"):
		out = append(out, "yarn install --frozen-lockfile")
	case has("package-lock.json"):
		out = append(out, "npm ci")
	case has("package.json"):
		out = append(out, "npm install")
	}
	if has("Gemfile") {
		out = append(out, "bundle install")
	}
	return out
}
