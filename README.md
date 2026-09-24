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

## What it runs

Reviewing a pull request with `pit` means running that pull request's code on
your machine: its Dockerfiles are built, its services are started, and its
`.pit.yaml` decides how. That is the point of the tool, and it is worth being
explicit about — a branch you would not run is a branch `pit` cannot help you
review.

Commands in `.pit.yaml` that go through the `compose` shorthand run inside the
sandbox's own containers. A command without it runs on your machine, as you; when
a pull request adds one, `pit` shows it and asks before running it.

## Status

Early development — not usable yet. Full documentation will follow once the tool
does something worth documenting.

Outside contributions are not being accepted at this time.

## License

Apache License 2.0 — see [LICENSE](LICENSE).
