#!/bin/sh
# Sets up pit's demo: the Teashop, in a git repository of its own, with
# one pull request waiting for review, #7. Nothing leaves your machine:
# the repository's remote is a directory next to it.
#
#   examples/demo/setup.sh [directory]    default: ./pit-demo
set -eu

here=$(cd "$(dirname "$0")" && pwd)
dir=${1:-pit-demo}
if [ -e "$dir" ]; then
	echo "$dir already exists; pass another directory, or remove it" >&2
	exit 1
fi
mkdir -p "$dir"
dir=$(cd "$dir" && pwd)

commit() {
	git -c user.name="Demo" -c user.email="demo@example.com" commit -q "$@"
}

git init -q --bare -b main "$dir/teashop.git"
git init -q -b main "$dir/teashop"
cp -R "$here/app/." "$dir/teashop/"
cd "$dir/teashop"
git remote add origin "$dir/teashop.git"
git add -A
commit -m "Teashop"
git push -q origin main

# The pull request lives where GitHub keeps them, refs/pull/7/head, so
# pit finds it the way it would on a real repository.
git switch -q -c show-refunds
git apply "$here/pr.patch"
git add -A
commit -m "Show refunds, and prices with a thousands separator"
git push -q origin HEAD:refs/pull/7/head
git switch -q main
git branch -q -D show-refunds

echo "Ready. Next:"
echo "  cd $dir/teashop"
echo "  pit 7"
