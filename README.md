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

The project you review needs a working `docker-compose.yml`. If
`docker compose up` brings it up on your machine, `pit` can bring up its pull
requests.

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

Whatever you do in the sandbox from here on changes its data. To keep a state
worth coming back to, save it:

```bash
pit snap save 7 refunded-order
```

`pit` prints the snapshot's size and how long saving took, and its ID on
stdout. Change something, then put it back, exactly as it was saved:

```bash
pit snap restore 7 refunded-order
```

It asks first, since whatever is in the database now is replaced.
Snapshots are kept when the sandbox goes. The containers, volumes, worktree
and generated compose file go away with:

```bash
pit down 7
```

Your checkout of the demo was never touched: the sandbox ran in a worktree of
its own. The demo directory itself is yours to delete, and so is the image
Docker built (`docker image ls 'pit-*'`).

## Use it on your project

In your repository, next to the compose file:

```bash
pit init
```

It asks which service a reviewer opens in a browser, and on which port that
service listens inside its container, then writes `.pit.yaml`. Pass
`--service web --port 3000` to skip the questions. `pit` reads
`docker-compose.yml` unless told otherwise; for a `compose.yaml`, or several
files, pass `--compose-file` (once per file). The file `pit init` writes
explains every setting in comments, with commented-out examples for
migrations and scenarios; read it once.

Before the first review, check three things in your compose file, because a
sandbox runs next to your own stack and next to other sandboxes:

- **Published ports.** `pit` replaces the web service's published port with
  one of its own. Ports other services publish, like `"5432:5432"` for the
  database, are kept, and a second sandbox, or your own running stack, would
  collide on them. A service that only other containers talk to does not need
  a published port.
- **`container_name`.** A fixed container name exists only once. Leave it out.
- **Untracked files.** The sandbox is a fresh checkout. A `.env` file that is
  not committed is not in it, and an `env_file:` that points at one fails.
  Set what the services need in the compose file, or for the web service under
  `env.set` in `.pit.yaml`.

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
what changed.

## Commands

| Command | What it does |
|---|---|
| `pit <n>` | Bring up pull request *n*, or update the sandbox that is running. `--scenario` picks the data, `--open` opens the browser when it is ready. |
| `pit what <n>` | List the addresses the change leads to, as links, and what clicking through them will not show. |
| `pit open <n>` | Open the sandbox in a browser. |
| `pit ls` | List every sandbox, from every repository, with what it is actually doing. |
| `pit logs <n> [service]` | Show what a service says. Default: the one a reviewer opens. |
| `pit shell <n> [service] [-- cmd]` | A shell, or a command, inside a service. |
| `pit data reset <n>` | Load a scenario into a running sandbox again. |
| `pit snap save <n> [name]` | Save the data a sandbox is in. |
| `pit snap restore <n> <snapshot>` | Put a sandbox's data back into a saved state, by ID or name. |
| `pit scenarios` | List the data states this repository declares. |
| `pit timing <n>` | Show where the time went while a sandbox was built. |
| `pit down <n>` | Remove a sandbox: containers, volumes, worktree. `--all` removes every one. |
| `pit init` | Write a `.pit.yaml` for this project. |
| `pit doctor` | Check whether this machine can run `pit`. |
| `pit version` | Print the version. |

`what`, `ls`, `scenarios`, `timing`, `doctor` and `version` print
JSON with `--json`, for scripts. `-v` adds diagnostic logging to any command,
and `pit <command> --help` explains each one.

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
ones in your checkout's `.pit.yaml` are used. Listing and removing snapshots
are planned; until then, they are files in that directory.

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
| `compose.files` | `[docker-compose.yml]` | Compose files, relative to the repository root, in merge order. |
| `compose.services` | all | The services a review needs. What they depend on comes with them. |
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
| `data.scenarios[].params` | none | Example values for placeholders in addresses: `id: "1001"` for `/orders/{id}`. |
| `data.default` | none | The scenario loaded when none is asked for. |
| `data.service` | from the images | The service holding the database, for when `pit` cannot tell from the images which one it is. |
| `data.snapshot.save` | none | A command that writes a dump of the database to stdout. See [Snapshots](#snapshots). |
| `data.snapshot.restore` | none | A command that reads such a dump from stdin and replaces the database with it. Set together with `save`. |
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
checklist, and saving and restoring snapshots. Comments back into the pull
request and prebuilt binaries are planned. Interfaces and the `.pit.yaml`
schema may still change before a first release.

Outside contributions are not being accepted at this time.

## License

Apache License 2.0 — see [LICENSE](LICENSE).
