package config

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/glob"
	"github.com/thannoz/pit/internal/hooks"
)

// parseStrict decodes the file and rejects anything it does not
// recognise. A silently ignored field is worse than a rejected one: the
// author believes they configured something that has no effect.
func parseStrict(data []byte, file string) (*Config, *yaml.Node, error) {
	// The node tree is kept so that problems can point at a line.
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return nil, nil, syntaxError(err, file)
	}

	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, nil, syntaxError(err, file)
	}

	c.applyDefaults()
	return &c, &node, nil
}

// unknownField matches how yaml.v3 phrases a rejected key, which is
// accurate but talks about Go types the author has never seen.
var unknownField = regexp.MustCompile(`field (\S+) not found in type \S+`)

func syntaxError(err error, file string) error {
	msg := unknownField.ReplaceAllString(err.Error(), `unknown field "$1"`)
	msg = strings.TrimPrefix(msg, "yaml: ")
	msg = strings.TrimPrefix(msg, "unmarshal errors:\n  ")

	return errs.New("%s could not be read:\n  %s", file, msg).
		WithHint("check the spelling of the field names, and the indentation around them")
}

// validate checks that a parsed configuration means something. dir is
// where relative paths in it are resolved from.
func (c *Config) validate(node *yaml.Node, dir, file string) error {
	var p []Problem

	p = append(p, c.checkVersion(node)...)
	p = append(p, c.checkWeb(node)...)
	p = append(p, c.checkCompose(node, dir)...)
	p = append(p, c.checkHealthcheck(node)...)
	p = append(p, c.checkBuild(node)...)
	p = append(p, c.checkData(node)...)
	p = append(p, c.checkScenarioSnapshots(node, dir)...)
	p = append(p, c.checkCommands(node)...)
	p = append(p, c.checkIgnore(node)...)
	p = append(p, c.checkEnv(node, dir)...)

	if len(p) == 0 {
		return nil
	}
	return errs.Wrap(&InvalidError{File: file, Problems: p}, "").
		WithHint("fix the lines listed above; `pit init` writes a working file to compare against")
}

func (c *Config) checkVersion(node *yaml.Node) []Problem {
	if c.Version == Version {
		return nil
	}
	return []Problem{{
		Line: lineOf(node, "version"),
		Path: "version",
		Msg:  fmt.Sprintf("is %d, but this build of pit understands version %d", c.Version, Version),
		Hint: "upgrade pit, or set version back to " + fmt.Sprint(Version),
	}}
}

func (c *Config) checkWeb(node *yaml.Node) []Problem {
	var p []Problem

	if c.Web.Service == "" {
		p = append(p, Problem{
			Line: lineOf(node, "web"),
			Path: "web.service",
			Msg:  "is not set; pit needs to know which service a reviewer opens",
			Hint: "name one of the services from your compose file",
		})
	}
	if c.Web.Port <= 0 || c.Web.Port > 65535 {
		p = append(p, Problem{
			Line: lineOf(node, "web", "port"),
			Path: "web.port",
			Msg:  fmt.Sprintf("is %d; a port is between 1 and 65535", c.Web.Port),
			Hint: "use the port the service listens on inside its container, not the published one",
		})
	}
	return p
}

func (c *Config) checkCompose(node *yaml.Node, dir string) []Problem {
	var p []Problem

	for i, f := range c.Compose.Files {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			p = append(p, Problem{
				Line: lineOfIndex(node, i, "compose", "files"),
				Path: fmt.Sprintf("compose.files[%d]", i),
				Msg:  fmt.Sprintf("names %q, which does not exist", f),
				Hint: "paths are relative to " + FileName,
			})
		}
	}
	return p
}

func (c *Config) checkHealthcheck(node *yaml.Node) []Problem {
	var p []Problem
	h := c.Healthcheck

	if h.ExpectStatus < 100 || h.ExpectStatus > 599 {
		p = append(p, Problem{
			Line: lineOf(node, "healthcheck", "expect_status"),
			Path: "healthcheck.expect_status",
			Msg:  fmt.Sprintf("is %d; an HTTP status is between 100 and 599", h.ExpectStatus),
		})
	}
	if h.Timeout.Duration() <= 0 {
		p = append(p, Problem{
			Line: lineOf(node, "healthcheck", "timeout"),
			Path: "healthcheck.timeout",
			Msg:  "must be positive",
		})
	}
	if h.Interval.Duration() <= 0 {
		p = append(p, Problem{
			Line: lineOf(node, "healthcheck", "interval"),
			Path: "healthcheck.interval",
			Msg:  "must be positive",
		})
	}
	if h.Interval.Duration() > 0 && h.Timeout.Duration() > 0 && h.Interval.Duration() > h.Timeout.Duration() {
		p = append(p, Problem{
			Line: lineOf(node, "healthcheck", "interval"),
			Path: "healthcheck.interval",
			Msg:  fmt.Sprintf("is %v, longer than the timeout of %v, so only one attempt would ever be made", h.Interval, h.Timeout),
		})
	}
	if !strings.Contains(h.URL, "{port}") {
		p = append(p, Problem{
			Line: lineOf(node, "healthcheck", "url"),
			Path: "healthcheck.url",
			Msg:  fmt.Sprintf("is %q, which has no {port} placeholder", h.URL),
			Hint: "pit chooses the published port per sandbox, so the URL has to contain {port}",
		})
	}
	return p
}

// checkBuild makes sure a prebuilt image can only ever be the commit
// under review.
//
// This is the one rule in the file that exists for correctness rather
// than convenience. An image named after a pull request is whatever a
// pipeline pushed last; pulling it would hand the reviewer a sandbox
// of some earlier commit, which looks exactly like the right one. With
// the commit in the name the worst case is a missing image, and a
// missing image means building.
func (c *Config) checkBuild(node *yaml.Node) []Problem {
	pattern := c.Build.Prebuilt
	if pattern == "" {
		return nil
	}

	var p []Problem
	if !strings.Contains(pattern, "{sha}") {
		p = append(p, Problem{
			Line: lineOf(node, "build", "prebuilt"),
			Path: "build.prebuilt",
			Msg:  "does not contain {sha}, so it can name an image of a different commit",
			Hint: "tag the image with the commit, as in ghcr.io/acme/shop-{service}:{sha}",
		})
	}
	if strings.Contains(pattern, "{pr}") {
		p = append(p, Problem{
			Line: lineOf(node, "build", "prebuilt"),
			Path: "build.prebuilt",
			Msg:  "contains {pr}, which names an image that changes under it as the pull request moves",
			Hint: "use {sha}, which names one commit and only that one",
		})
	}
	return p
}

func (c *Config) checkData(node *yaml.Node) []Problem {
	var p []Problem
	d := c.Data

	seen := map[string]bool{}
	for i, s := range d.Scenarios {
		switch {
		case s.Name == "":
			p = append(p, Problem{
				Line: lineOfIndex(node, i, "data", "scenarios"),
				Path: fmt.Sprintf("data.scenarios[%d].name", i),
				Msg:  "is empty; a scenario is selected by name",
			})
		case seen[s.Name]:
			p = append(p, Problem{
				Line: lineOfIndex(node, i, "data", "scenarios"),
				Path: fmt.Sprintf("data.scenarios[%d].name", i),
				Msg:  fmt.Sprintf("is %q, which is already used by an earlier scenario", s.Name),
			})
		}
		seen[s.Name] = true

		for _, key := range slices.Sorted(maps.Keys(s.Params)) {
			var msg, hint string
			switch {
			case key == "" || strings.ContainsAny(key, "/{}[] \t"):
				msg = fmt.Sprintf("has %q, which cannot be the name of a placeholder", key)
				hint = "use the name alone: slug for /partners/{slug} or /partners/[slug]"
			case s.Params[key] == "":
				msg = fmt.Sprintf("gives %s no value, which fills nothing", key)
				hint = "set a value that exists in this scenario's data, or remove the entry"
			default:
				continue
			}
			p = append(p, Problem{
				Line: lineOfIndex(node, i, "data", "scenarios"),
				Path: fmt.Sprintf("data.scenarios[%d].params", i),
				Msg:  msg,
				Hint: hint,
			})
		}

		if s.Extends != "" {
			if _, ok := c.Scenario(s.Extends); !ok {
				p = append(p, Problem{
					Line: lineOfIndex(node, i, "data", "scenarios"),
					Path: fmt.Sprintf("data.scenarios[%d].extends", i),
					Msg:  fmt.Sprintf("names %q, which is not a scenario", s.Extends),
					Hint: "known scenarios: " + c.knownScenarios(),
				})
			}
		}
	}

	p = append(p, c.checkExtends(node)...)

	if d.Default != "" {
		if _, ok := c.Scenario(d.Default); !ok {
			p = append(p, Problem{
				Line: lineOf(node, "data", "default"),
				Path: "data.default",
				Msg:  fmt.Sprintf("names %q, which is not a scenario", d.Default),
				Hint: "known scenarios: " + c.knownScenarios(),
			})
		}
	}

	// A retention time with nothing to retain is a setting that does
	// nothing, which is worse than a missing one: the author believes
	// they configured something.
	if !d.ProductionLike.TTL.IsZero() && d.ProductionLike.Fetch == "" {
		p = append(p, Problem{
			Line: lineOf(node, "data", "production_like", "ttl"),
			Path: "data.production_like.ttl",
			Msg:  "is set, but there is no fetch command to retain anything from",
			Hint: "add data.production_like.fetch, or remove the ttl",
		})
	}

	p = append(p, checkSnapshotParts(node, d.Snapshot.Parts)...)

	// Half a snapshot configuration is worse than none: save would
	// appear to work and restore would fail when it is needed most.
	if (d.Snapshot.Save == "") != (d.Snapshot.Restore == "") {
		missing, present := "restore", "save"
		if d.Snapshot.Save == "" {
			missing, present = "save", "restore"
		}
		p = append(p, Problem{
			Line: lineOf(node, "data", "snapshot"),
			Path: "data.snapshot." + missing,
			Msg:  fmt.Sprintf("is not set although %s is; snapshots need both", present),
			Hint: "save writes a dump to stdout, restore reads one from stdin",
		})
	}
	return p
}

// checkExtends reports inheritance that never reaches a base.
//
// A cycle cannot be seen by reading one scenario at a time, and it is
// the one mistake in this file that would otherwise make pit walk in
// circles rather than fail. Each cycle is reported once: every
// scenario in it is equally at fault, and three copies of the same
// finding read as three separate problems.
func (c *Config) checkExtends(node *yaml.Node) []Problem {
	var p []Problem
	reported := map[string]bool{}

	for i, s := range c.Data.Scenarios {
		if s.Extends == "" || reported[s.Name] {
			continue
		}

		var ce *CycleError
		if _, err := c.Chain(s.Name); !errors.As(err, &ce) {
			continue
		}
		for _, name := range ce.Path {
			reported[name] = true
		}

		p = append(p, Problem{
			Line: lineOfIndex(node, i, "data", "scenarios"),
			Path: fmt.Sprintf("data.scenarios[%d].extends", i),
			Msg:  fmt.Sprintf("never reaches a base: %s", strings.Join(ce.Path, " → ")),
			Hint: "extends has to point at a base; a scenario cannot build on something that already builds on it",
		})
	}
	return p
}

// knownScenarios lists the configured names for a hint, without
// repeating a name that appears twice -- the duplicate is already being
// reported as its own problem, and echoing it here reads as a bug.
func (c *Config) knownScenarios() string {
	var names []string
	seen := map[string]bool{}
	for _, s := range c.Data.Scenarios {
		if s.Name != "" && !seen[s.Name] {
			names = append(names, s.Name)
			seen[s.Name] = true
		}
	}
	if len(names) == 0 {
		return "none are configured"
	}
	return strings.Join(names, ", ")
}

// checkIgnore makes sure every ignore pattern can match something. A
// pattern with an unclosed bracket matches nothing and says nothing,
// which is found out as a checklist full of files that were supposed
// to be ignored.
func (c *Config) checkIgnore(node *yaml.Node) []Problem {
	var p []Problem
	for i, pattern := range c.Review.Ignore {
		if err := glob.Valid(pattern); err != nil {
			p = append(p, Problem{
				Line: lineOfIndex(node, i, "review", "ignore"),
				Path: fmt.Sprintf("review.ignore[%d]", i),
				Msg:  fmt.Sprintf("is %q, which is not a pattern that can match anything", pattern),
				Hint: "check the brackets; ** stands for any number of directories",
			})
		}
	}
	return p
}

// checkCommands makes sure every configured command can be run.
//
// A command with an unbalanced quote is accepted by YAML and fails
// halfway through a setup, with a worktree already made and containers
// already started. Finding it while reading the file costs nothing.
func (c *Config) checkCommands(node *yaml.Node) []Problem {
	var p []Problem

	for _, cmd := range c.commands() {
		if problem, bad := commandProblem(cmd.Line, cmd.Path, cmd.at(node)); bad {
			p = append(p, problem)
		}
	}
	return p
}

// commandProblem describes what is wrong with a configured command, if
// anything.
func commandProblem(line, path string, atLine int) (Problem, bool) {
	if strings.TrimSpace(line) == "" {
		return Problem{
			Line: atLine, Path: path,
			Msg:  "is empty; there is nothing to run",
			Hint: "remove the entry, or write the command it should run",
		}, true
	}
	if err := hooks.Check(line); err != nil {
		return Problem{
			Line: atLine, Path: path,
			Msg:  err.Error(),
			Hint: errs.Hint(err),
		}, true
	}
	return Problem{}, false
}

func (c *Config) checkEnv(node *yaml.Node, dir string) []Problem {
	if c.Env.FromFile == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, c.Env.FromFile)); err != nil {
		return []Problem{{
			Line: lineOf(node, "env", "from_file"),
			Path: "env.from_file",
			Msg:  fmt.Sprintf("names %q, which does not exist", c.Env.FromFile),
			Hint: "this is a template to be checked in, never a real .env",
		}}
	}
	return nil
}

// checkSnapshotParts checks the list form of data.snapshot: each entry
// names its service, once, and has both commands.
func checkSnapshotParts(node *yaml.Node, parts []SnapshotPart) []Problem {
	var p []Problem
	seen := map[string]bool{}
	for i, part := range parts {
		at := lineOfIndex(node, i, "data", "snapshot")
		path := fmt.Sprintf("data.snapshot[%d]", i)
		switch {
		case part.Service == "":
			p = append(p, Problem{Line: at, Path: path + ".service",
				Msg:  "is not set; with several databases, each entry names the service it saves",
				Hint: "service: db"})
		case seen[part.Service]:
			p = append(p, Problem{Line: at, Path: path + ".service",
				Msg:  fmt.Sprintf("%q is listed twice", part.Service),
				Hint: "one entry for each database service"})
		}
		seen[part.Service] = true
		for _, missing := range []struct{ name, value string }{{"save", part.Save}, {"restore", part.Restore}} {
			if missing.value == "" {
				p = append(p, Problem{Line: at, Path: path + "." + missing.name,
					Msg:  "is not set; snapshots need both commands",
					Hint: "save writes a dump to stdout, restore reads one from stdin"})
			}
		}
	}
	return p
}
