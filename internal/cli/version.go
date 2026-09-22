package cli

import (
	"encoding/json"
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// Set via -ldflags at build time; see the Makefile.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// buildInfo is what `pit version --json` emits. The Go version and
// platform are included because they are the first thing worth knowing
// about a bug report.
type buildInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"buildDate"`
	GoVersion string `json:"goVersion"`
	Platform  string `json:"platform"`
}

func currentBuild() buildInfo {
	return buildInfo{
		Version:   version,
		Commit:    commit,
		BuildDate: date,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
}

func (b buildInfo) String() string {
	return fmt.Sprintf("pit %s (%s, built %s, %s, %s)",
		b.Version, b.Commit, b.BuildDate, b.GoVersion, b.Platform)
}

func newVersionCmd(opts *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version, commit and build date",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			b := currentBuild()
			if opts.jsonOutput {
				enc := json.NewEncoder(c.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(b)
			}
			_, err := fmt.Fprintln(c.OutOrStdout(), b)
			return err
		},
	}
}
