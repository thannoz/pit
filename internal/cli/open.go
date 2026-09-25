package cli

import (
	"context"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/ui"
)

// openInBrowser shows a URL in whatever the system uses for one; a
// variable so that no test opens a browser on the machine it runs on.
var openInBrowser = systemBrowser

// systemBrowser opens a URL the way the system does. A failure is worth
// mentioning but not worth failing over: the URL is on screen either
// way.
func systemBrowser(ctx context.Context, out *ui.Printer, url string) {
	name, args := browserCommand(url)
	if name == "" {
		out.Warnf("do not know how to open a browser on %s", runtime.GOOS)
		return
	}
	if _, err := (proc.Exec{}).Output(ctx, proc.Command{Name: name, Args: args}); err != nil {
		out.Warnf("could not open a browser: %v", err)
	}
}

func browserCommand(url string) (string, []string) {
	switch runtime.GOOS {
	case "darwin":
		return "open", []string{url}
	case "linux":
		return "xdg-open", []string{url}
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		return "", nil
	}
}

func newOpenCmd(_ *globalOptions) *cobra.Command {
	var record bool
	cmd := &cobra.Command{
		Use:   "open <pull request number>",
		Short: "Open a sandbox in a browser",
		Long: `Open a sandbox's URL in whatever this system uses for one.

The URL is printed either way, so this still tells you something when
there is no browser to open.

--record opens it in a Chrome pit watches instead, and writes down what
you do there -- the pages you open, what you type, what you press --
until you close it. The next pit note keeps the steps, and the comment
carries them: as steps for a person, and as a recipe pit replay does
again on another machine.`,
		Example: `  pit open 482
  pit open 482 --record`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			box, err := sandboxFor(c, args[0])
			if err != nil {
				return err
			}

			out := ui.New(c.OutOrStdout(), c.ErrOrStderr())
			out.Notef("%s", box.Describe())
			out.Println(box.URL)
			if record {
				if err := notOnBase(c, "a recording"); err != nil {
					return err
				}
				return recordSandbox(c, out, box)
			}
			openInBrowser(c.Context(), out, box.URL)
			return nil
		},
	}
	cmd.Flags().BoolVar(&record, "record", false, "open a browser pit watches, and record what you do in it")
	return cmd
}
