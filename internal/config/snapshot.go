package config

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/thannoz/pit/internal/errs"
)

// Configured reports whether both snapshot commands are set. Validation
// has already refused one without the other.
func (s Snapshot) Configured() bool { return s.Save != "" && s.Restore != "" }

// Engine is a database whose own dump tools pit can write the snapshot
// commands for.
type Engine string

// The engines pit recognises by their image.
const (
	Postgres Engine = "PostgreSQL"
	MySQL    Engine = "MySQL"
	MariaDB  Engine = "MariaDB"
	MongoDB  Engine = "MongoDB"
)

// engineImages are the images each engine is recognised by: the
// official ones and the common images built on them, which keep their
// tools and their environment variables.
var engineImages = map[Engine][]string{
	Postgres: {"postgres", "postgis", "pgvector", "timescaledb", "timescaledb-ha"},
	MySQL:    {"mysql"},
	MariaDB:  {"mariadb"},
	MongoDB:  {"mongo"},
}

// EngineOf recognises a database by the image a service runs:
// "postgres:17-alpine", "docker.io/library/mysql:8.4",
// "postgis/postgis:16-3.4". A service built from its own Dockerfile has
// no image to go by.
func EngineOf(image string) (Engine, bool) {
	name := path.Base(image)
	if i := strings.IndexAny(name, ":@"); i >= 0 {
		name = name[:i]
	}
	for _, e := range []Engine{Postgres, MySQL, MariaDB, MongoDB} {
		if slices.Contains(engineImages[e], strings.ToLower(name)) {
			return e, true
		}
	}
	return "", false
}

// SuggestSnapshot writes the two commands for a database service.
//
// They read the credentials from the container's own environment --
// POSTGRES_USER, MYSQL_ROOT_PASSWORD -- rather than having them written
// out. That works wherever the variables come from, an env_file
// included, and it keeps passwords out of .pit.yaml. The fallbacks are
// the images' own defaults.
func SuggestSnapshot(service string, e Engine) Snapshot {
	inside := func(script string) string {
		return fmt.Sprintf("compose exec -T %s sh -c '%s'", service, script)
	}
	switch e {
	case Postgres:
		const user, db = `"${POSTGRES_USER:-postgres}"`, `"${POSTGRES_DB:-${POSTGRES_USER:-postgres}}"`
		return Snapshot{
			Save:    inside("exec pg_dump -U " + user + " --clean --if-exists " + db),
			Restore: inside("exec psql -q -v ON_ERROR_STOP=1 -U " + user + " -d " + db),
		}
	case MySQL:
		// Without --set-gtid-purged=OFF, a server that records GTIDs --
		// MySQL 9 does by default -- writes a dump it refuses to read
		// back into itself.
		const login = `export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"; exec `
		return Snapshot{
			Save:    inside(login + `mysqldump -uroot --set-gtid-purged=OFF --databases "$MYSQL_DATABASE"`),
			Restore: inside(login + `mysql -uroot`),
		}
	case MariaDB:
		// MariaDB's images accept both spellings of the variables, and
		// from 11 on ship only the mariadb names of the tools.
		const login = `export MYSQL_PWD="${MARIADB_ROOT_PASSWORD:-$MYSQL_ROOT_PASSWORD}"; exec `
		return Snapshot{
			Save:    inside(login + `mariadb-dump -uroot --databases "${MARIADB_DATABASE:-$MYSQL_DATABASE}"`),
			Restore: inside(login + `mariadb -uroot`),
		}
	case MongoDB:
		const auth = `${MONGO_INITDB_ROOT_USERNAME:+--username "$MONGO_INITDB_ROOT_USERNAME" --password "$MONGO_INITDB_ROOT_PASSWORD" --authenticationDatabase admin}`
		return Snapshot{
			Save:    inside("exec mongodump --archive --quiet " + auth),
			Restore: inside("exec mongorestore --archive --drop --quiet " + auth),
		}
	}
	return Snapshot{}
}

// Database is a compose service that may hold a project's data.
type Database struct {
	Service string
	// Image is what it runs; empty when it is built from source.
	Image string
}

// FindDatabase picks the service a snapshot would dump: the one
// data.service names, or else the one whose image is a database pit
// knows. Two such services is a guess pit does not make.
func FindDatabase(configured string, services []Database) (Database, bool) {
	if configured != "" {
		for _, s := range services {
			if s.Service == configured {
				return s, true
			}
		}
		return Database{Service: configured}, true
	}
	var found []Database
	for _, s := range services {
		if _, ok := EngineOf(s.Image); ok {
			found = append(found, s)
		}
	}
	if len(found) != 1 {
		return Database{}, false
	}
	return found[0], true
}

// SnapshotCommands returns the commands that save and restore a
// sandbox's data, or, when the repository has none, an error that says
// what to add: the lines themselves where pit recognises the database,
// the shape of them where it does not.
func (c *Config) SnapshotCommands(services []Database) (Snapshot, error) {
	if c.Data.Snapshot.Configured() {
		return c.Data.Snapshot, nil
	}
	err := errs.New("%s does not say how to save a sandbox's data", FileName)

	db, found := FindDatabase(c.Data.Service, services)
	engine, known := EngineOf(db.Image)
	if !found || !known {
		example := SuggestSnapshot(orElse(db.Service, "db"), Postgres)
		return Snapshot{}, err.WithHint(
			"add data.snapshot with two commands: save writes a dump of the database to stdout, "+
				"restore reads one back from stdin. For PostgreSQL:\n\n%s", snapshotYAML(example))
	}
	return Snapshot{}, err.WithHint("add this to %s; %s is %s:\n\n%s",
		FileName, db.Service, engine, snapshotYAML(SuggestSnapshot(db.Service, engine)))
}

// snapshotYAML writes the commands as they go into .pit.yaml. A folded
// scalar takes them as they are: they hold both kinds of quote, which
// either quoted YAML style would have to escape.
func snapshotYAML(s Snapshot) string {
	return "  data:\n" +
		"    snapshot:\n" +
		"      save: >-\n" +
		"        " + s.Save + "\n" +
		"      restore: >-\n" +
		"        " + s.Restore
}
