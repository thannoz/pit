# pit

> **P**ull request **I**n **T**est — a pit stop for code review.

Pull requests get reviewed as text diffs. Anything that only appears at
runtime — layout, error states, form validation, how a screen behaves with no
data — stays invisible to the reviewer.

`pit` brings up the running state of a pull request on the reviewer's own
machine: one command, isolated from their work in progress, with the right test
data loaded, and torn down without a trace by a second command. A review you can
operate, not just read.

Written in Go. Runs locally on Docker. No cloud account required.

![pit bringing up a pull request, listing what to look at, and removing it](examples/demo/demo.gif)

```console
$ pit 7
#7 "Show refunds, and prices with a thousands separator" by Demo
warning: this repository has no hosting service to ask for pull request details; the description above is the commit's own
  ✓ fetch      #7 at 84d9508a118f
  ✓ worktree   ~/.local/state/pit/pit-demo-teashop-83c553/pr-7
using #7's own .pit.yaml; it changes data
  ✓ build      web built  (1.5s)
  ✓ services   pit-pit-demo-teashop-83c553-7
  ✓ migrate    1 migration  (1.2s)
  ✓ data       scenario standard
  ✓ healthy    it answers

http://localhost:47374/

$ pit what 7
#7 Show refunds, and prices with a thousands separator
http://localhost:47374/ · 84d9508 into the default branch · scenario standard

Affected by this pull request (0 of 3 looked at):
    1. http://localhost:47374/orders  (main.go, money.go)
    2. http://localhost:47374/orders/1001  (main.go, money.go)
    3. http://localhost:47374/  (money.go)  likely
         ? money.go does not serve this address; main.go uses it
  !  migrations/002_refunds.sql changes the schema (ALTER TABLE orders ADD COLUMN IF NOT EXISTS refunded_cents integer NOT NULL DEFAULT 0); check the data that exists before it runs

No address found for these; look at them yourself:
  .pit.yaml  (config)
  fixtures/refunded.sql  (code)
```

This is the [demo](#try-it-on-the-demo), on a machine that has built its image
before. Between the steps, `pit` also shows what Docker Compose prints while it
builds and starts the services; those lines are left out here.

## Contents

- [Requirements](#requirements)
- [Install](#install)
- [Try it on the demo](#try-it-on-the-demo)
- [Use it on your project](#use-it-on-your-project)
- [Dev containers](#dev-containers)
- [Commands](#commands)
- [What to look at: `pit what`](#what-to-look-at-pit-what)
- [Data scenarios](#data-scenarios)
- [Snapshots](#snapshots)
- [`.pit.yaml` reference](#pityaml-reference)
- [Where pull requests come from](#where-pull-requests-come-from)
- [What stays where](#what-stays-where)
- [What it runs](#what-it-runs)
- [Status](#status)

## Requirements

- **macOS or Linux.**
- **git.**
- **Docker** with **Compose v2** (`docker compose`, not `docker-compose`),
  and the daemon running.
- **Go 1.27 or newer**, to install `pit`. There are no prebuilt binaries yet.
- **The GitHub CLI** (`gh`), logged in with `gh auth login`, for
  repositories on GitHub. Not needed for anything else — see
  [Where pull requests come from](#where-pull-requests-come-from).

The project you review needs a working compose file, or a `devcontainer.json`.
If `docker compose up` brings it up on your machine, `pit` can bring up its
pull requests.

## Install

```bash
go install github.com/thannoz/pit/cmd/pit@latest
```

This puts `pit` into `$(go env GOBIN)`, or `$(go env GOPATH)/bin` when that is
empty, usually `~/go/bin`. If your shell does not find it afterwards, add that
directory to your `PATH`. There are no releases yet, so `pit version` prints
`dev` for a build made this way.

Then check that everything `pit` needs is there:

```bash
pit doctor
```

`pit doctor` checks git, Docker, Compose, `gh`, the state directory and the
port range, and says what to do about anything that is missing. Outside a
project it also mentions that there is no `.pit.yaml` yet; that is expected.

## Try it on the demo

The repository has a small demo, a shop with a Go web service and a Postgres
database, and one pull request waiting for review. A script sets it up as a
git repository of its own. Nothing leaves your machine: its remote is a
directory next to it.

```bash
git clone https://github.com/thannoz/pit.git
pit/examples/demo/setup.sh ~/pit-demo
cd ~/pit-demo/teashop
```

Bring up pull request #7:

```bash
pit 7
```

The first run downloads the Go and Postgres images and builds the web service,
which can take a few minutes; later runs take seconds. `pit` prints a warning that there is no hosting
service to ask about the pull request. That is right for the demo, whose
remote is a local directory; `pit` reads the title from the commit instead.

At the end it prints the sandbox's URL. Open it, or let `pit` open it:

```bash
pit open 7
```

Now ask what the pull request changed, as links into the sandbox:

```bash
pit what 7
```

`pit` also notes that it is "using #7's own .pit.yaml": the pull request
changes `.pit.yaml`, and a pull request is reviewed with its own version of
it. This one adds refunds, and a scenario with data to show them. Load that
data instead of the default:

```bash
pit 7 --scenario refunded
pit what 7
```

The sandbox keeps running; only its data is replaced. Item 2 now links to
order 1002, the refunded one, because the scenario says which order to use.

Whatever you do in the sandbox from here on changes its data. Order a tea with
the form on the home page, and `pit ls` shows the scenario as
`refunded +edited`: the data is no longer what the scenario loads. To keep a
state worth coming back to, save it:

```bash
pit snap save 7 refunded-order
```

`pit` prints the snapshot's size and how long saving took, and its ID on
stdout. Change something, then put it back, exactly as it was saved:

```bash
pit snap restore 7 refunded-order
```

It asks first, since whatever is in the database now is replaced.
Snapshots are kept when the sandbox goes, but only on your machine. To make one
a scenario every reviewer can start from, promote it into the repository:

```bash
pit snap promote refunded-order
```

It writes the dump to `fixtures/refunded-order.sql` and adds a scenario of that
name to `.pit.yaml`; `git diff` shows the lines it added.

The containers, volumes, worktree and generated compose file go away with:

```bash
pit down 7
```

Apart from what `pit snap promote` wrote, your checkout of the demo was never
touched: the sandbox ran in a worktree of its own. The demo directory itself is
yours to delete, and so is the image Docker built (`docker image ls 'pit-*'`).

## Use it on your project

In your repository, next to the compose file:

```bash
pit init
```

It asks which service a reviewer opens in a browser, and on which port that
service listens inside its container, then writes `.pit.yaml`. Pass
`--service web --port 3000` to skip the questions. `pit` finds the compose
file the way Compose does (`compose.yaml`, `compose.yml`, `docker-compose.yaml`,
`docker-compose.yml`); for another, or several, pass `--compose-file` (once per
file). A project with no compose file but a `devcontainer.json` gets one that
runs its dev container; see [Dev containers](#dev-containers). The file
`pit init` writes explains every setting in comments, with commented-out
examples for migrations and scenarios; read it once.

A sandbox runs next to your own stack and next to other sandboxes, so `pit`
gives each one its own: the web service gets a port of its own, ports other
services publish on your machine, like `"5432:5432"`, are taken away (inside
the sandbox they are reached as before), and a fixed `container_name` gets the
sandbox's name in front of it. One thing to check yourself: the sandbox is a
fresh checkout, so a `.env` file that is not committed is not in it, and an
`env_file:` that points at one fails. Set what the services need in the
compose file, or for the web service under `env.set` in `.pit.yaml`.

Then add what makes a review useful, both described in
[Data scenarios](#data-scenarios):

- **Migrations**, under `data.migrate`, so the schema matches the pull
  request.
- **A scenario**, under `data.scenarios`, so the screens have something on
  them.

Check `.pit.yaml` in, and review a pull request by its number:

```bash
pit 482
```

A pull request is reviewed with its own `.pit.yaml` when it has one. It can
add a service or a scenario, and the review uses it. When a pull request
adds or changes a command that would run on your machine rather than in a
container, `pit` shows it and asks first. A pull request without the file,
such as one opened before `.pit.yaml` was merged, is reviewed with yours.

Running `pit 482` again later reuses the running sandbox. When the pull
request has a new commit, `pit` updates the sandbox in place and rebuilds only
what changed. The checkout moves to the new commit where it is, so a directory
a container mounts from it, like `./migrations` or `./src`, shows the new
files. A single mounted file, like `./nginx.conf:/etc/nginx/nginx.conf`, does
not: git replaces a changed file with a new one, and the container keeps the
old. `pit down 482` and `pit 482` start that service afresh.

## Dev containers

A project that describes its environment in `.devcontainer/devcontainer.json`
needs no compose file. `pit init` writes:

```yaml
devcontainer:
  file: .devcontainer/devcontainer.json
  start: npm start
web:
  service: dev
  port: 3000
```

`pit` runs the container the file describes, from its `image`, its `build`, or
its `dockerComposeFile`, with the pull request's checkout as the workspace. It
runs the lifecycle commands in it as the file's user: `onCreateCommand`,
`updateContentCommand` and `postCreateCommand` in a new container,
`updateContentCommand` again when the pull request has a new commit, and
`postStartCommand` each time. Then `devcontainer.start` starts the app. A dev
container is one to work in, and nothing in it starts the app by itself.
`pit init` takes the `start` or `dev` script of a `package.json`; for anything
else, write the command yourself. What it prints is in `pit logs`.

`pit` leaves out features, which it cannot install, and `initializeCommand`,
which would run on your machine; it says so when a file has them.

## Commands

| Command | What it does |
|---|---|
| `pit <n>` | Bring up pull request *n*, or update the sandbox that is running. `--scenario` picks the data, `--open` opens the browser when it is ready. |
| `pit what <n>` | List the addresses the change leads to, as links, and what clicking through them will not show. |
| `pit open <n>` | Open the sandbox in a browser. |
| `pit ls` | List every sandbox, from every repository, with what it is actually doing, and whether its data was changed since it was loaded. |
| `pit logs <n> [service]` | Show what a service says. Default: the one a reviewer opens. |
| `pit shell <n> [service] [-- cmd]` | A shell, or a command, inside a service. |
| `pit data reset <n>` | Load a scenario into a running sandbox again. |
| `pit snap save <n> [name]` | Save the data a sandbox is in. `--consistent` pauses the other services meanwhile. |
| `pit snap restore <n> <snapshot>` | Put a sandbox's data back into a saved state, by ID or name. |
| `pit snap ls` | List the snapshots of every repository: name, pull request, size, age. |
| `pit snap rm <snapshot>...` | Remove snapshots by ID or name. `--older-than=30d` removes every one older than that, after asking. |
| `pit snap promote <snapshot>` | Write a snapshot into the repository, with a scenario in `.pit.yaml` that loads it. |
| `pit scenarios` | List the data states this repository declares. |
| `pit timing <n>` | Show where the time went while a sandbox was built. |
| `pit down <n>` | Remove a sandbox: containers, volumes, worktree. `--all` removes every one. |
| `pit init` | Write a `.pit.yaml` for this project. |
| `pit doctor` | Check whether this machine can run `pit`. |
| `pit version` | Print the version. |

`what`, `ls`, `scenarios`, `timing`, `doctor`, `version`, `snap save`,
`snap ls` and `snap promote` print JSON with `--json`, for scripts. `-v` adds diagnostic logging
to any command, and `pit <command> --help` explains each one.

## What to look at: `pit what`

`pit what <n>` reads the pull request's diff and works out which addresses
of the running application it affects: the pages and endpoints a changed file
serves, and, for a changed helper or component, the ones that use it. It
follows the code by declaration, not by file. A new function next to an old one
does not make every page that imports the file "affected".

Each address comes with a certainty; an address without a label is certain:

- **certain:** the changed file serves it.
- **likely:** the change reaches it through other files, or the address has a
  part `pit` cannot turn into a link. The reasons are printed underneath.
- **uncertain:** something decides at runtime where requests go, such as
  middleware, a rewrite, or a base path set in code.

Below the addresses come the things clicking through will not show:
migrations (`!!` when destructive), endpoints that were removed or moved,
changed permission checks, and removed error handling. Files for which no
address was found are listed as well. A file `pit` cannot place is still a file
to review.

Addresses with placeholders, such as `/orders/{id}`, become links when the
scenario gives a value for them. See `params` under
[Data scenarios](#data-scenarios).

**Frameworks.** Addresses are found for:

- **Next.js**: App Router and Pages Router. `next.config` and middleware are
  read for what they do to addresses: a `basePath` is added, and an address a
  rewrite or middleware can change is marked uncertain.
- **Go**: `net/http`, chi, gin and gorilla/mux.
- **SvelteKit**: 2.x and the 3.0 prereleases, including form actions and
  `+server` methods.

Imports are followed in TypeScript and JavaScript, including Svelte, Vue and
Astro files, and in Go. `review.routes.framework` in `.pit.yaml` picks one
framework when guessing gets it wrong.

**Keeping track.** `pit what 482 --done 1,3` marks items as looked at, and
`--undone 3` takes a mark back. Items are also checked off on their own when the
web service's log shows a request for them. Most development servers log every
request; for one that does not, `pit` says so. When a new commit changes a file
that leads to an item already looked at, the item asks to be looked at again.

## Data scenarios

A scenario is a named data state, loaded after the migrations. It is a list of
commands, usually something that feeds a SQL file to the database:

```yaml
data:
  migrate:
    - "compose exec -T web npm run migrate"
  scenarios:
    - name: empty
      description: "Migrations only, no data"
    - name: standard
      description: "Three users, twenty products, five orders"
      apply:
        - "compose exec -T db psql -U app -d app -f /fixtures/standard.sql"
      params:
        id: "1001"          # /orders/{id} becomes /orders/1001
    - name: split-refund
      description: "An order refunded from two warehouses"
      extends: standard     # standard is loaded first
      apply:
        - "compose exec -T db psql -U app -d app -f /fixtures/split-refund.sql"
      params:
        id: "1042"
  default: standard
```

`pit 482 --scenario split-refund` loads one other than the default, also into a
sandbox that is already running: asking for a scenario is asking for its data,
so it replaces what is there without a question. When a pull request gets a
new commit, the sandbox keeps its data, and `pit` asks whether to load the
scenario again. `pit data reset 482` loads the scenario again on purpose,
after asking, because anything entered by hand is lost. `pit scenarios` lists
what the repository offers.

The commands run inside the containers, so the files they read must be there
too. Mount a directory of fixtures into the database service:

```yaml
# docker-compose.yml
services:
  db:
    image: postgres:17-alpine
    volumes:
      - ./fixtures:/fixtures:ro
```

Migrations run when the sandbox is set up and again for every new commit, so
they have to be safe to run twice, as most migration tools are. They run as
soon as the containers have started, which is not the same as the database
accepting connections. Give the database a compose `healthcheck` and the
service that migrates a `depends_on` with `condition: service_healthy`, or
wait in the command itself, as the demo's `.pit.yaml` does with `pg_isready`.

## Snapshots

A scenario is a state the repository describes. A snapshot is one you made by
using a sandbox: a cart with a voucher in it, an order half-way through a
refund. `pit snap save 482 cart-with-voucher` saves it; the name is optional.

`pit snap restore 482 cart-with-voucher` puts it back, after asking. A
snapshot can go into the sandbox of another pull request of the same
repository. One taken at another commit has that commit's schema, so the
migrations of the sandbox's commit run after it. `pit ls` shows the
snapshot as where the sandbox's data came from.

`pit` knows nothing about databases. A snapshot is whatever the repository's
save command writes to stdout, kept compressed under
`$XDG_STATE_HOME/pit/snapshots/`, and the restore command reads it back from
stdin:

```yaml
data:
  snapshot:
    save: >-
      compose exec -T db sh -c 'exec pg_dump -U "${POSTGRES_USER:-postgres}" --clean --if-exists "${POSTGRES_DB:-${POSTGRES_USER:-postgres}}"'
    restore: >-
      compose exec -T db sh -c 'U="${POSTGRES_USER:-postgres}"; D="${POSTGRES_DB:-$U}"; psql -q -v ON_ERROR_STOP=1 -U "$U" -d "$D" -c "SET client_min_messages TO warning" -c "DO \$\$ DECLARE s name; BEGIN FOR s IN SELECT nspname FROM pg_namespace WHERE nspname NOT LIKE \$p\$pg\_%\$p\$ AND nspname <> \$p\$information_schema\$p\$ LOOP EXECUTE format(\$f\$DROP SCHEMA %I CASCADE\$f\$, s); END LOOP; END \$\$" -c "CREATE SCHEMA public" && exec psql -q -o /dev/null -v ON_ERROR_STOP=1 -U "$U" -d "$D"'
    writes: >-
      compose exec -T db sh -c 'exec psql -At -U "${POSTGRES_USER:-postgres}" -d "${POSTGRES_DB:-${POSTGRES_USER:-postgres}}" -c "SELECT coalesce(sum(n_tup_ins + n_tup_upd + n_tup_del), 0) FROM pg_stat_user_tables"'
```

You do not have to write these yourself. Without them, `pit snap save` says
which lines to add, written for the database your compose file runs:
PostgreSQL, MySQL, MariaDB or MongoDB, recognised by the image. `pit init`
writes the same lines as a comment. They read the credentials from the
container's environment, so no password ends up in `.pit.yaml`. The `>-` keeps
the quotes in them as they are.

These commands restore exactly what was saved. They empty the database before
the dump goes in, not only what the dump names, so a table or collection
created after the snapshot is gone afterwards. For PostgreSQL that means
dropping the database's own schemas rather than the database, which keeps the
application's connections open. Commands you write yourself restore only as
exactly as they are written.

When a pull request brings its own `.pit.yaml` without snapshot commands, the
ones in your checkout's `.pit.yaml` are used.

The third command, `writes`, is optional. It prints a number that grows with
every write to the database: for PostgreSQL the rows written, as its
statistics count them; for MySQL, MariaDB and MongoDB their own counters.
Reading, saving a snapshot and the database's housekeeping leave it alone.
`pit` counts once the data is loaded, and `pit ls` counts again: when the
number grew, the sandbox shows `+edited` next to its scenario, because its
data is no longer something the scenario, or the snapshot, can bring back. An
update keeps that mark, and does not count its own migrations as edits.

What the count cannot tell, `pit ls` does not claim:

- MySQL, MariaDB and MongoDB start counting from zero when the database
  restarts. What was written before is then unknown, and `pit ls` says
  nothing about that sandbox until its data is loaded again.
- PostgreSQL can take up to ten seconds to count a write.
- An application that writes on its own, a session or a visit for every
  page, shows as edited as soon as a page is opened.

A project with several databases lists a pair of commands for each, named by
its service, and a snapshot then holds all of them, one file each. When pit
recognises more than one database, the lines it suggests are already this list:

```yaml
data:
  snapshot:
    - service: db
      save: "compose exec -T db pg_dump -U app app"
      restore: "compose exec -T db psql -U app -d app"
    - service: analytics
      save: "compose exec -T analytics mysqldump shop"
      restore: "compose exec -T analytics mysql"
```

The databases are saved one after the other. An application that writes to
both meanwhile can leave them describing different moments: an order in one
that the other has never heard of. `pit snap save 482 --consistent` pauses
every other service while the databases are saved, so that nothing writes in
between, and lets them go on afterwards, also when saving fails. To know which
services to keep running, it reads them from `service`, or from the
`compose exec <service>` of each save command.

`pit snap ls` lists the snapshots, newest first, and `pit snap rm` removes
them, by ID or name. `pit snap rm --older-than=30d` clears out old ones; it
lists them and asks first.

A snapshot stays on your machine. `pit snap promote cart-with-voucher` makes
it a scenario of the repository, which every reviewer can start from: the dump
goes to `fixtures/cart-with-voucher.sql`, or one file for each database, and
`.pit.yaml` gets a scenario that loads it. Only those lines are added; the
rest of the file keeps its comments and its formatting. `--as` names the
scenario, `--description` describes it, and `--dir` puts the files elsewhere.

```yaml
    - name: cart-with-voucher
      description: "Saved in #482 at 1a2b3c4"
      snapshot: fixtures/cart-with-voucher.sql
```

A scenario with `snapshot` is loaded by `data.snapshot.restore`, and then
`data.migrate` runs, since the dump has the schema of the commit it was saved
at. With several databases, `snapshot` maps each service to its file. Other
scenarios can extend it; it cannot extend one itself, because it replaces all
the data. Promoting again under the same name replaces the files, after
asking, which is how a scenario that no longer fits is renewed.

Review what it wrote and commit it. A dump is data: check that nothing in it
should stay out of the repository. Until the pull requests you review contain
that commit, `pit` loads a scenario their `.pit.yaml` does not have from yours,
and says so.

## `.pit.yaml` reference

`pit` looks for `.pit.yaml` from the current directory upwards; `--config`
names one explicitly. Unknown keys are errors, not silently ignored, and an
error in the file names its line.

Commands in `hooks`, `data.migrate` and `data.scenarios[].apply` are strings.
A command that starts with `compose` runs as this sandbox's `docker compose`,
with the project name and files filled in. Any other command runs on your
machine, in the sandbox's worktree.

| Key | Default | Meaning |
|---|---|---|
| `version` | `1` | Schema version. |
| `compose.files` | the one Compose finds | Compose files, relative to the repository root, in merge order. |
| `compose.services` | all | The services a review needs. What they depend on comes with them. |
| `devcontainer.file` | none | A `devcontainer.json`, in place of `compose.files`. See [Dev containers](#dev-containers). |
| `devcontainer.start` | none | The command that starts the app in the dev container, after its lifecycle commands. |
| `web.service` | *required* | The service a reviewer opens in a browser. |
| `web.port` | *required* | The port it listens on **inside** its container. `pit` picks the published port itself, one per sandbox, from 40000–49999. |
| `healthcheck.url` | `http://{host}:{port}/` | Polled until it answers. `{host}` becomes `localhost`, `{port}` the sandbox's published port. |
| `healthcheck.expect_status` | `200` | The status that means ready. Redirects are followed, so `/` sending you to `/login` counts as the login page's status. |
| `healthcheck.timeout` | `120s` | How long to keep trying. |
| `healthcheck.interval` | `2s` | How long to wait between tries. |
| `hooks.after_up` | none | Commands run once the services are up, before migrations and data. For installing dependencies and the like. |
| `build.prebuilt` | none | Image name with `{service}` and `{sha}`, e.g. `ghcr.io/acme/shop-{service}:{sha}`. When it can be pulled, the build is skipped. `{sha}` is required. See [`examples/`](examples/README.md). |
| `data.migrate` | none | Commands that bring the schema up to date, run before any scenario, at setup and for every new commit. `pit` counts them as "migrations". |
| `data.scenarios[].name` | *required* | What `--scenario` takes. |
| `data.scenarios[].description` | none | Shown by `pit scenarios`. |
| `data.scenarios[].extends` | none | A scenario to load first. |
| `data.scenarios[].apply` | none | Commands that produce this state. |
| `data.scenarios[].snapshot` | none | A dump to load with `data.snapshot.restore` before `apply`, relative to `.pit.yaml`; with several databases, a map from service to file. `pit snap promote` writes it. |
| `data.scenarios[].params` | none | Example values for placeholders in addresses: `id: "1001"` for `/orders/{id}`. |
| `data.default` | none | The scenario loaded when none is asked for. |
| `data.service` | from the images | The service holding the database, for when `pit` cannot tell from the images which one it is. |
| `data.snapshot.save` | none | A command that writes a dump of the database to stdout. See [Snapshots](#snapshots). |
| `data.snapshot.restore` | none | A command that reads such a dump from stdin and replaces the database with it. Set together with `save`. |
| `data.snapshot.writes` | none | A command that prints a number that grows with every write, for `pit ls` to show `+edited`. |
| `data.snapshot.service` | from the command | The service the commands work on, for `--consistent`, when it is not a `compose exec`. |
| `data.snapshot[]` | none | Instead, a list of `service`, `save`, `restore` and optionally `writes`, one for each of several databases. |
| `review.routes.framework` | `auto` | `auto`, `nextjs`, `go` or `sveltekit`. |
| `review.ignore` | none | Glob patterns for files that never belong on the checklist, e.g. `"**/*.test.ts"`. |
| `env.set` | none | Environment variables set on the web service, as a map: `NODE_ENV: development`. |

Two keys are accepted and checked but do not do anything yet. They belong to
features that are planned, not built: `data.production_like` and
`env.from_file`.

## Where pull requests come from

- **GitHub** (github.com and hosts named `github.*`): through `gh`, which
  supplies the title, author and state. `gh` has to be logged in.
- **Anywhere else**, such as GitLab, a company's own server, or a local
  directory: `pit` fetches the pull request's ref from `origin` directly, and
  reads the title and author from its commit. The ref is
  `refs/merge-requests/<n>/head` on GitLab and `refs/pull/<n>/head` elsewhere.
  The demo works this way. What a commit cannot say stays unknown: `pit ls`
  shows no branch, and `pit what` compares against `origin`'s default branch,
  which it calls "the default branch".

## What stays where

- **Your checkout is left alone.** Each sandbox runs in a git worktree of its
  own, so a review never touches the branch you are working on or your
  uncommitted changes.
- **Sandboxes don't collide.** Each one is its own Compose project, with its
  own published port, network and volumes. Several pull requests, of several
  repositories, can run side by side.
- **Everything `pit` keeps** lives under `$XDG_STATE_HOME/pit`, which is
  `~/.local/state/pit` by default: the sandbox list, worktrees and generated
  compose files.
- **`pit down` removes what the sandbox ran in:** containers, networks,
  volumes, the worktree and the files it generated. Images stay, like Docker's
  build cache, so that the next review of the project starts quickly; `docker
  image ls 'pit-*'` lists them. `pit ls` shows what exists; it also notices
  sandboxes whose containers were removed behind its back.

## What it runs

Reviewing a pull request with `pit` means running that pull request's code on
your machine: its Dockerfiles are built, its services are started, and its
`.pit.yaml` decides how. That is the point of the tool, and it is worth being
explicit about — a branch you would not run is a branch `pit` cannot help you
review.

Commands in `.pit.yaml` that go through the `compose` shorthand run inside the
sandbox's own containers. A command without it runs on your machine, as you; when
a pull request adds or changes one, `pit` shows it and asks before running it.

## Status

Early, and in use. The core works: sandboxes, data scenarios and the review
checklist, and saving, restoring and promoting snapshots. Comments back into the pull
request and prebuilt binaries are planned. Interfaces and the `.pit.yaml`
schema may still change before a first release.

Outside contributions are not being accepted at this time.

## License

Apache License 2.0 — see [LICENSE](LICENSE).
