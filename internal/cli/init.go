package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/runtime"
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
	if len(files) == 0 {
		files = []string{config.DefaultComposeFile}
	}

	services, err := runtime.ReadServices(filepath.Join(dir, files[0]))
	if err != nil {
		return err
	}

	service, port, err := chooseService(c, out, services, o)
	if err != nil {
		return err
	}

	opts := config.InitOptions{
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
