// Package config describes .pit.yaml: the file a repository checks in to
// tell pit how to bring itself up. This file holds the schema; finding
// and validating the file are separate steps.
package config

// Version is the schema version this build understands. It exists so
// that a future incompatible change can be rejected with an explanation
// rather than a parse error.
const Version = 1

// Config is the whole of .pit.yaml.
type Config struct {
	Version     int         `yaml:"version"`
	Compose     Compose     `yaml:"compose"`
	Web         Web         `yaml:"web"`
	Healthcheck Healthcheck `yaml:"healthcheck"`
	Hooks       Hooks       `yaml:"hooks"`
	Build       Build       `yaml:"build"`
	Data        Data        `yaml:"data"`
	Review      Review      `yaml:"review"`
	Env         Env         `yaml:"env"`

	// Dir is the directory the file was read from. The paths a scenario
	// loads are relative to it, and it is not the same directory for
	// every configuration pit reads: a pull request's is in its
	// worktree, the reviewer's in their checkout.
	Dir string `yaml:"-"`
}

// Compose says which compose files describe the services.
type Compose struct {
	// Files are relative to the repository root, in the order Compose
	// should merge them.
	Files []string `yaml:"files"`
	// Services are the ones a review needs. Whatever they depend on
	// comes with them, so this is a list of entry points rather than
	// of everything that will run. Empty means the whole project.
	//
	// It exists because a project of eight services usually has four
	// that decide what a screen looks like, and the rest -- a queue
	// worker, a mail catcher, an analytics sink -- cost minutes of
	// build and answer nothing a reviewer asked.
	Services []string `yaml:"services"`
}

// Web identifies the service a reviewer opens in a browser.
type Web struct {
	// Service is the compose service name.
	Service string `yaml:"service"`
	// Port is the port that service listens on inside its container.
	// The port it is published on is chosen by pit, not configured.
	Port int `yaml:"port"`
}

// Healthcheck says how pit decides the sandbox is ready. Without it pit
// would hand over a URL that answers with a connection refused.
type Healthcheck struct {
	// URL is polled until it answers. {host} and {port} are replaced
	// with the sandbox's own.
	URL string `yaml:"url"`
	// ExpectStatus is the HTTP status that counts as ready.
	ExpectStatus int `yaml:"expect_status"`
	// Timeout is how long to keep trying.
	Timeout Duration `yaml:"timeout"`
	// Interval is how long to wait between attempts.
	Interval Duration `yaml:"interval"`
}

// Hooks are commands the repository needs run at fixed points. They are
// free-form commands rather than an abstraction on purpose: projects
// differ too much for pit to be clever about migrations.
type Hooks struct {
	// AfterUp runs once the services are up, before the schema is
	// migrated and the data loaded. It is for whatever a project needs
	// before either can work -- installing dependencies, waiting for a
	// service that comes up slowly. Migrations have their own setting,
	// data.migrate.
	AfterUp []string `yaml:"after_up"`
}

// Build says where images may come from instead of being built here.
type Build struct {
	// Prebuilt is the name of an image a pipeline has already built,
	// with {service} and {sha} filled in. When it can be pulled, the
	// build is skipped entirely.
	//
	// The commit has to appear in the name. An image tagged by pull
	// request number is whatever a pipeline pushed last, which may be
	// an older commit -- and handing that to a reviewer would be a
	// review of code that is not under review. With the commit in the
	// name, a missing image simply means building, which is right.
	Prebuilt string `yaml:"prebuilt"`
}

// Data describes the state of the database, which is the part that
// decides whether a sandbox is useful or shows an empty screen.
// See docs/03-datenkonzept.md.
type Data struct {
	// Service is the compose service holding the database.
	Service string `yaml:"service"`
	// Migrate brings the schema up to date. It runs as a step of its
	// own, before any data is loaded, because a migration that fails
	// is a different problem from a hook that fails: it is the change
	// under review often enough to deserve being named.
	Migrate []string `yaml:"migrate"`
	// Snapshot says how to dump and restore that service.
	Snapshot Snapshot `yaml:"snapshot"`
	// Scenarios are the named states a reviewer can start from.
	Scenarios []Scenario `yaml:"scenarios"`
	// Default names the scenario used when none is asked for.
	Default string `yaml:"default"`
	// ProductionLike points at an existing anonymised dump. pit never
	// anonymises anything itself.
	ProductionLike ProductionLike `yaml:"production_like"`
}

// Snapshot holds the two commands that make snapshots work for any
// database: one that writes a dump to stdout, one that reads it back
// from stdin. pit needs to know nothing else about the database.
//
// A project with more than one database lists a pair for each, named by
// its service; a snapshot of it is then one part per service. The
// single form and the list are written differently in .pit.yaml and
// are read by UnmarshalYAML.
type Snapshot struct {
	// Save and Restore are the single form's commands, and Service,
	// optionally, the service they work on.
	Save    string
	Restore string
	Service string
	// Parts is the list form: one pair of commands for each database.
	Parts []SnapshotPart
}

// SnapshotPart is the commands for one of several databases.
type SnapshotPart struct {
	Service string `yaml:"service"`
	Save    string `yaml:"save"`
	Restore string `yaml:"restore"`
}

// Scenario is a named, reproducible data state.
type Scenario struct {
	// Name is what a reviewer passes to --scenario.
	Name string `yaml:"name"`
	// Description is shown when listing scenarios.
	Description string `yaml:"description"`
	// Extends names a scenario to apply first, so scenarios can build
	// on each other instead of repeating the base data.
	Extends string `yaml:"extends"`
	// Apply are the commands that produce this state.
	Apply []string `yaml:"apply"`
	// Snapshot names a dump in the repository to load, by the
	// restore commands of data.snapshot, before Apply runs. It is what
	// `pit snap promote` writes: a state someone clicked together once,
	// kept for everyone.
	Snapshot ScenarioSnapshot `yaml:"snapshot"`
	// Params are example values for the placeholders in the addresses
	// a review leads to: the slug in /partners/{slug}. They belong to a
	// scenario because they name data it loads -- an order that exists
	// in this state and not in the empty one.
	Params map[string]string `yaml:"params"`
}

// ScenarioSnapshot is the dump a scenario loads: one file, or with
// several databases one for each service. It is written as a path, or
// as a mapping from service to path, and read by UnmarshalYAML.
type ScenarioSnapshot struct {
	File  string
	Files []SnapshotFile
}

// SnapshotFile is the dump of one service, relative to the directory
// of the .pit.yaml.
type SnapshotFile struct {
	Service string
	File    string
}

// ProductionLike fetches a dump from a pipeline the team already has.
type ProductionLike struct {
	// Fetch writes the dump to stdout.
	Fetch string `yaml:"fetch"`
	// TTL is how long a fetched dump may be reused before it is fetched
	// again.
	TTL Duration `yaml:"ttl"`
}

// Review configures the checklist pit derives from a diff.
type Review struct {
	Routes Routes `yaml:"routes"`
	// Ignore are glob patterns for files that never belong on a
	// reviewer's checklist.
	Ignore []string `yaml:"ignore"`
}

// Routes selects how changed files are mapped to URLs.
type Routes struct {
	// Framework names the heuristic to use; "auto" lets pit guess.
	Framework string `yaml:"framework"`
}

// Env is the environment the sandbox's services get.
type Env struct {
	// FromFile is a template checked into the repository. It is a
	// template on purpose: pit must never read a real .env, and no
	// secret should ever reach pit's state.
	FromFile string `yaml:"from_file"`
	// Set overrides individual variables.
	Set map[string]string `yaml:"set"`
}
