package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/thannoz/pit/internal/errs"
)

// Restore is a dump a scenario puts into the sandbox, and the command
// that reads it.
type Restore struct {
	// Service is the database it goes into; empty for a project with
	// one database that does not name it.
	Service string
	// File is the dump: absolute when the configuration was read from
	// a file, otherwise as written.
	File string
	// Command is data.snapshot's restore command for that service.
	Command string
	// Root is the directory File has to stay inside: that of the
	// .pit.yaml. Empty when the configuration was not read from a file.
	Root string
}

// Open opens the dump, and refuses one that is not inside Root once its
// links are followed. The file can come from a pull request's
// .pit.yaml, and pit reads it on the reviewer's machine and hands it to
// that pull request's containers: a link to ~/.ssh would be a way out
// of the sandbox.
func (r Restore) Open() (*os.File, error) {
	if r.Root != "" {
		real, err := filepath.EvalSymlinks(r.File)
		if err != nil {
			return nil, err
		}
		root, err := filepath.EvalSymlinks(r.Root)
		if err != nil {
			return nil, err
		}
		if rel, err := filepath.Rel(root, real); err != nil || !filepath.IsLocal(rel) {
			return nil, errs.New("%s leads outside the repository, to %s", filepath.Base(r.File), real).
				WithHint("a scenario loads files of the repository only; a link out of it is not followed")
		}
	}
	return os.Open(r.File)
}

// Restores pairs the files a scenario's snapshot names with the restore
// commands of data.snapshot. A scenario without a snapshot has none.
//
// The commands are the ones a snapshot is restored with, not ones of
// the scenario's own: a promoted snapshot is a snapshot that was put in
// the repository, and reading it back is the same act as before.
func (c *Config) Restores(s Scenario) ([]Restore, error) {
	files := s.Snapshot.Each()
	if len(files) == 0 {
		return nil, nil
	}
	parts := c.Data.Snapshot.Each()
	if len(parts) == 0 {
		return nil, errs.New("scenario %q loads a snapshot, but data.snapshot has no restore command to load it with", s.Name).
			WithHint("add data.snapshot's save and restore commands; `pit snap save` names the ones for your database")
	}

	var out []Restore
	for _, f := range files {
		part, err := restoreFor(s.Name, f, parts)
		if err != nil {
			return nil, err
		}
		file := f.File
		if c.Dir != "" {
			file = filepath.Join(c.Dir, filepath.FromSlash(f.File))
		}
		out = append(out, Restore{Service: part.Service, File: file, Command: part.Restore, Root: c.Dir})
	}
	return out, nil
}

// restoreFor finds the restore command for one file: the only one there
// is for a file that names no service, otherwise its service's.
func restoreFor(scenario string, f SnapshotFile, parts []SnapshotPart) (SnapshotPart, error) {
	if f.Service == "" {
		if len(parts) > 1 {
			return SnapshotPart{}, errs.New("scenario %q loads one file, but data.snapshot restores %d databases", scenario, len(parts)).
				WithHint("name the file for each service: snapshot: {%s: <file>, ...}", parts[0].Service)
		}
		return parts[0], nil
	}
	i := slices.IndexFunc(parts, func(p SnapshotPart) bool { return p.Service == f.Service })
	if i < 0 {
		var known []string
		for _, p := range parts {
			if p.Service != "" {
				known = append(known, p.Service)
			}
		}
		e := errs.New("scenario %q loads a file into %s, which data.snapshot has no restore command for", scenario, f.Service)
		if len(known) == 0 {
			return SnapshotPart{}, e.WithHint("data.snapshot names no service; give the file as a plain path")
		}
		return SnapshotPart{}, e.WithHint("data.snapshot restores %s", strings.Join(known, ", "))
	}
	return parts[i], nil
}

// checkScenarioSnapshots checks what a scenario's snapshot names: files
// inside the repository that exist, and restore commands for each.
//
// Checked when the file is read, because the alternative is a sandbox
// that is built for minutes and then cannot load its data.
func (c *Config) checkScenarioSnapshots(node *yaml.Node, dir string) []Problem {
	var p []Problem
	for i, s := range c.Data.Scenarios {
		if s.Snapshot.IsZero() {
			continue
		}
		at := lineOfIndex(node, i, "data", "scenarios")
		path := fmt.Sprintf("data.scenarios[%d].snapshot", i)

		// Whatever it extends would be loaded first and then replaced,
		// which is time spent on nothing and a chain that says
		// something untrue.
		if s.Extends != "" {
			p = append(p, Problem{
				Line: at,
				Path: fmt.Sprintf("data.scenarios[%d].extends", i),
				Msg:  fmt.Sprintf("is set, but a snapshot replaces all the data, so what %q puts there would be gone", s.Extends),
				Hint: "remove extends; a scenario can extend this one instead",
			})
		}

		for _, f := range s.Snapshot.Each() {
			if msg := outside(f.File); msg != "" {
				p = append(p, Problem{Line: at, Path: path, Msg: msg,
					Hint: "give a path relative to the directory of .pit.yaml, like fixtures/" + s.Name + ".sql"})
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f.File))); err != nil {
				p = append(p, Problem{Line: at, Path: path,
					Msg:  fmt.Sprintf("names %s, which does not exist", f.File),
					Hint: "`pit snap promote` writes it; check that it is committed"})
			}
		}

		if _, err := c.Restores(Scenario{Name: s.Name, Snapshot: s.Snapshot}); err != nil {
			p = append(p, Problem{Line: at, Path: path, Msg: strings.TrimPrefix(err.Error(), fmt.Sprintf("scenario %q ", s.Name)),
				Hint: errs.Hint(err)})
		}
	}
	return p
}

// outside says what is wrong with a path that does not stay inside the
// repository, or nothing.
func outside(file string) string {
	switch {
	case file == "":
		return "names an empty path"
	case filepath.IsAbs(file) || strings.HasPrefix(file, "/"):
		return fmt.Sprintf("names %s, an absolute path; a scenario is shared, and the path has to be one on every machine", file)
	case !filepath.IsLocal(filepath.FromSlash(file)):
		return fmt.Sprintf("names %s, which is outside the repository", file)
	}
	return ""
}
