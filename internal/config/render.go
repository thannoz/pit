package config

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"github.com/thannoz/pit/internal/errs"
)

// InitOptions is what `pit init` learned about a project.
type InitOptions struct {
	// ComposeFiles are the compose files, relative to the repository
	// root.
	ComposeFiles []string
	// WebService is the service a reviewer opens.
	WebService string
	// WebPort is the port it listens on inside its container.
	WebPort int
	// Services are the other services found, used to write useful
	// commented-out examples rather than generic ones.
	Services []string
	// Databases are the services with the image each runs, so that a
	// database is recognised by what it is and not only by its name.
	Databases []Database
}

// Render writes a .pit.yaml for a project that has none.
//
// The generated file is deliberately more comment than configuration.
// Someone meeting pit for the first time learns the format from the
// file in front of them, and the sections they will need later are
// already there in the right place, switched off.
func Render(o InitOptions) ([]byte, error) {
	if o.WebService == "" {
		return nil, errs.New("no web service was chosen")
	}
	if o.WebPort <= 0 || o.WebPort > 65535 {
		return nil, errs.New("%d is not a usable port", o.WebPort)
	}
	if len(o.ComposeFiles) == 0 {
		o.ComposeFiles = []string{DefaultComposeFile}
	}

	var b bytes.Buffer
	db := guessDatabase(o.Services)
	found, ok := FindDatabase("", o.Databases)
	if ok {
		db = found.Service
	}
	engine, known := EngineOf(found.Image)
	if !known {
		engine = Postgres
	}
	if err := initTemplate.Execute(&b, view{
		InitOptions: o,
		Version:     Version,
		DBService:   db,
		DBExample:   orElse(db, "db"),
		Engine:      engine,
		Recognised:  known,
		Snapshot:    SuggestSnapshot(orElse(db, "db"), engine),
		Check:       SuggestCheck(orElse(db, "db"), engine),
	}); err != nil {
		return nil, errs.Wrap(err, "cannot write the configuration")
	}
	return b.Bytes(), nil
}

type view struct {
	InitOptions
	Version int
	// DBService is the project's own database service, empty when none
	// was recognised.
	DBService string
	// DBExample is a name for the commented examples: the real one
	// where there is one, a plausible one otherwise.
	DBExample string
	// Snapshot are the snapshot commands for Engine, which is the
	// database the image showed when Recognised, and an example
	// otherwise.
	Snapshot Snapshot
	// Check are the commands pit migrate-check counts with, where pit
	// knows them for Engine.
	Check      Check
	Engine     Engine
	Recognised bool
}

// guessDatabase picks the project's database service so the data
// section names something real, and returns nothing when none of the
// services looks like one.
//
// The difference matters: a name inside a comment is an example and a
// wrong guess costs nothing, but a name written as configuration is a
// claim about this project, and pointing at a service that does not
// exist is worse than leaving the setting out.
func guessDatabase(services []string) string {
	known := []string{"db", "database", "postgres", "postgresql", "mysql", "mariadb", "mongo", "mongodb"}
	for _, s := range services {
		for _, k := range known {
			if strings.EqualFold(s, k) {
				return s
			}
		}
	}
	return ""
}

// orElse keeps the commented examples readable when nothing was
// recognised: they need a name, and a made-up one in a comment teaches
// more than an empty gap.
func orElse(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

var initTemplate = template.Must(template.New("pit.yaml").Parse(
	`# How pit brings this project up for a review.
# Written by ` + "`pit init`" + `. Check it in: every reviewer uses this file.

version: {{ .Version }}

compose:
  files:
{{- range .ComposeFiles }}
    - {{ . }}
{{- end }}
  # Only these services, and whatever they depend on. Leaving it out
  # starts the whole project, which is right until one grows a queue
  # worker and a mail catcher that no review ever looks at.
  # services:
  #   - {{ .WebService }}

# The service a reviewer opens, and the port it listens on inside its
# container. pit chooses the published port itself, one per sandbox, so
# two pull requests can run side by side.
web:
  service: {{ .WebService }}
  port: {{ .WebPort }}

# How pit knows the sandbox is ready. Without this it would hand over a
# URL that answers with a connection refused.
healthcheck:
  url: "{{ "http://{host}:{port}/" }}"
  expect_status: 200
  timeout: 120s
  interval: 2s

# Commands to run once the services are up, before the schema is
# migrated and the data loaded -- whatever a project needs before
# either can work. The "compose" shorthand runs inside this sandbox;
# pit fills in the project name and files.
# hooks:
#   after_up:
#     - "compose exec -T {{ .WebService }} npm ci"

# Images a pipeline has already built for this commit. When one can be
# pulled, pit skips the build entirely. The commit has to appear in the
# name: an image tagged with the pull request number is whatever was
# pushed last, and that may be an older commit.
# build:
#   prebuilt: "ghcr.io/your-org/your-repo-{service}:{sha}"

# The state of the database, which decides whether a reviewer sees the
# change or an empty screen.
data:
{{- if .DBService }}
  service: {{ .DBService }}
{{- end }}

  # Bringing the schema up to date is a step of its own, so that a
  # migration that fails is reported as a migration and not as a hook.
  # migrate:
  #   - "compose exec -T {{ .WebService }} npm run migrate"

  # A scenario is a name for a data state, so that two reviewers can
  # talk about the same screen. "empty" is the state the migrations
  # leave behind: it needs no commands, which is why it works in every
  # project and is a safe place to start.
  scenarios:
    - name: empty
      description: "Migrations only, no data"

    # Copy this, fill in what puts the project into a state worth
    # reviewing, and make it the default below. The commands run
    # inside the sandbox; "compose" stands for this sandbox's own
    # docker compose, project name and files already filled in.
    # - name: standard
    #   description: "Enough data to see the usual screens"
    #   apply:
    #     - "compose exec -T {{ .DBExample }} psql -U app -d app -f /fixtures/standard.sql"
    #   # Values from this data for the placeholders in addresses, so
    #   # that /orders/{id} becomes a link that opens an order.
    #   params:
    #     id: "1001"

  # What a review loads when no --scenario is given. The command
  # "pit scenarios" lists what this file offers.
  default: empty

  # Two commands are enough for any database: one that writes a dump
  # to stdout, one that reads it back from stdin.
{{- if .Recognised }}
  # These are for {{ .DBExample }}, which runs {{ .Engine }}. They read
  # the credentials from the container's own environment.
{{- else }}
  # An example for PostgreSQL.
{{- end }}
  # snapshot:
  #   save: >-
  #     {{ .Snapshot.Save }}
  #   restore: >-
  #     {{ .Snapshot.Restore }}
{{- if .Snapshot.Writes }}
  #   # Counts the writes, so that "pit ls" can say when the data was
  #   # changed since it was loaded.
  #   writes: >-
  #     {{ .Snapshot.Writes }}
{{- end }}
{{- if not .Check.Empty }}

  # What "pit migrate-check" counts before and after a pull request's
  # migrations, to say what they take away, and the locks it watches
  # while they run: the tables of the public schema.
  # check:
  #   rows: >-
  #     {{ .Check.Rows }}
  #   columns: >-
  #     {{ .Check.Columns }}
  #   locks: >-
  #     {{ .Check.Locks }}
{{- end }}

# Environment for the sandbox's services. from_file points at a template
# checked into the repository -- never a real .env, and never a secret.
# env:
#   from_file: .env.pit.example
#   set:
#     NODE_ENV: development
`))

// Summary describes what Render produced, for pit init to print.
func (o InitOptions) Summary() string {
	return fmt.Sprintf("%s on port %d, from %s",
		o.WebService, o.WebPort, strings.Join(o.ComposeFiles, ", "))
}
