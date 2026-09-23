package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/ui"
)

func newScenariosCmd(opts *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "scenarios",
		Short: "List the data states this repository declares",
		Long: `List the scenarios configured under data.scenarios.

A scenario is a named data state: the commands that put the database
into a state worth reviewing. Pass one to a review with
` + "`pit <pull request number> --scenario=<name>`" + `; without it, the
one marked as the default is loaded.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runScenarios(c, opts)
		},
	}
}

func runScenarios(c *cobra.Command, opts *globalOptions) error {
	// From the working directory, which walks up to the repository
	// root on its own. Not through currentRepo: reading which data
	// states a file declares has nothing to do with a hosting service,
	// and going that way would turn a missing git remote into a reason
	// not to print a table.
	cfg, path, err := config.LoadFrom(".")
	if err != nil {
		return err
	}

	out := ui.New(c.OutOrStdout(), c.ErrOrStderr())
	if opts.jsonOutput {
		return writeScenariosJSON(out, cfg)
	}
	return writeScenariosTable(out, cfg, path)
}

// scenarioRow is the shape `--json` promises. It is a type of its own
// rather than the internal one, so that renaming a field inside pit
// does not silently break someone's script.
type scenarioRow struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Extends     string   `json:"extends,omitempty"`
	Apply       []string `json:"apply,omitempty"`
	Default     bool     `json:"default,omitempty"`
}

func writeScenariosJSON(out *ui.Printer, c *config.Config) error {
	rows := make([]scenarioRow, 0, len(c.Data.Scenarios))
	for _, s := range c.Data.Scenarios {
		rows = append(rows, scenarioRow{
			Name:        s.Name,
			Description: s.Description,
			Extends:     s.Extends,
			Apply:       s.Apply,
			Default:     s.Name == c.Data.Default,
		})
	}

	enc := json.NewEncoder(out.Out())
	enc.SetIndent("", "  ")
	return enc.Encode(rows)
}

func writeScenariosTable(out *ui.Printer, c *config.Config, path string) error {
	scenarios := c.Data.Scenarios
	if len(scenarios) == 0 {
		out.Printf("No scenarios in %s.\n", path)
		out.Println("A scenario names a data state worth reviewing; add one under data.scenarios.")
		return nil
	}

	// The column only earns its place when something inherits: with a
	// flat list it is a dash on every line.
	showExtends := false
	for _, s := range scenarios {
		if s.Extends != "" {
			showExtends = true
		}
	}

	w := tabwriter.NewWriter(out.Out(), 0, 0, 2, ' ', 0)
	header := []string{"", "NAME", "DESCRIPTION"}
	if showExtends {
		header = []string{"", "NAME", "EXTENDS", "DESCRIPTION"}
	}
	// tabwriter buffers, so these cannot fail in a way worth checking
	// here; a broken pipe or a full disk surfaces at Flush below.
	_, _ = fmt.Fprintln(w, strings.Join(header, "\t"))

	for _, s := range scenarios {
		row := []string{defaultMark(s.Name == c.Data.Default), s.Name, orDash(s.Description)}
		if showExtends {
			row = []string{defaultMark(s.Name == c.Data.Default), s.Name, orDash(s.Extends), orDash(s.Description)}
		}
		_, _ = fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	if err := w.Flush(); err != nil {
		return errs.Wrap(err, "cannot write the listing")
	}

	// A marker nobody explained is a riddle, and which scenario a plain
	// `pit <nr>` loads is the first thing a reader wants from this
	// table.
	if c.Data.Default != "" {
		out.Printf("\n* is the default: `pit <pull request number>` loads %q.\n", c.Data.Default)
		return nil
	}
	out.Println("\nNo default is set, so a review starts with whatever state the services come up in.")
	out.Println("Set data.default, or pass --scenario for each review.")
	return nil
}

func defaultMark(isDefault bool) string {
	if isDefault {
		return "*"
	}
	return ""
}
