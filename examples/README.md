# Examples

Files to copy into a project that pit reviews. They are examples, not
libraries: read them, change the names to yours, check them in.

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
