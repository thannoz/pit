# Examples

Files to copy into a project that pit reviews. They are examples, not
libraries: read them, change the names to yours, check them in.

## `demo/`

The Teashop: a Go web service and a Postgres database, with a
`.pit.yaml` that migrates the schema and offers three data scenarios.
`setup.sh` turns it into a git repository of its own with one pull
request, #7, which adds refunds, a migration and a scenario to show
them:

```bash
examples/demo/setup.sh ~/pit-demo
cd ~/pit-demo/teashop
pit 7
```

The repository's remote is a directory next to it, so nothing leaves
your machine. The walk-through is in the
[README](../README.md#try-it-on-the-demo).

`demo.tape` records the GIF at the top of the README with
[vhs](https://github.com/charmbracelet/vhs): `vhs examples/demo/demo.tape`
from the repository's root.

## `github-actions/prebuild-images.yml`

Builds an image per service for every pull request and pushes it to the
GitHub container registry, so that setting up a review pulls the image
instead of building it.

Two things have to agree, and nothing checks them for you:

| Where | What |
|---|---|
| the workflow's matrix | `image: ghcr.io/acme/shop-web` |
| `.pit.yaml` | `build.prebuilt: "ghcr.io/acme/shop-{service}:{sha}"` |

An image pit cannot find is not an error — it builds that service
instead. That is what makes the workflow safe to adopt gradually, and
also what makes a typo in either file invisible: everything still
works, just as slowly as before.

`{sha}` is required in the pattern. An image tagged with a pull request
number is whatever was pushed last, which can be an older commit, and a
sandbox of the wrong commit looks exactly like a sandbox of the right
one.
