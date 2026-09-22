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
	if err := initTemplate.Execute(&b, view{
		InitOptions: o,
		Version:     Version,
		DBService:   guessDatabase(o.Services),
	}); err != nil {
		return nil, errs.Wrap(err, "cannot write the configuration")
	}
	return b.Bytes(), nil
}

type view struct {
	InitOptions
	Version   int
	DBService string
}

// guessDatabase picks a plausible database service so the commented-out
// data section names something real. A wrong guess inside a comment
// costs nothing; a generic placeholder teaches nothing.
func guessDatabase(services []string) string {
	known := []string{"db", "database", "postgres", "postgresql", "mysql", "mariadb", "mongo", "mongodb"}
	for _, s := range services {
		for _, k := range known {
			if strings.EqualFold(s, k) {
				return s
			}
		}
	}
	return "db"
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

# Commands to run once the services are up, before the healthcheck
# counts. Migrations belong here. The "compose" shorthand runs inside
# this sandbox; pit fills in the project name and files.
# hooks:
#   after_up:
#     - "compose exec -T {{ .WebService }} npm run migrate"

# The state of the database, which decides whether a reviewer sees the
# change or an empty screen. See the pit documentation on scenarios.
# data:
#   service: {{ .DBService }}
#
#   # Two commands are enough for any database: one that writes a dump
#   # to stdout, one that reads it back from stdin.
#   snapshot:
#     save:    "compose exec -T {{ .DBService }} pg_dump -U app --clean --if-exists app"
#     restore: "compose exec -T {{ .DBService }} psql -U app -d app"
#
#   scenarios:
#     - name: empty
#       description: "Migrations only, no data"
#     - name: standard
#       description: "Enough data to see the usual screens"
#       apply: ["compose exec -T {{ .DBService }} psql -U app -d app -f /fixtures/standard.sql"]
#   default: standard

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
