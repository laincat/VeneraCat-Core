#!/bin/sh
# Squashes the whole history into one commit without changing a single file.
#
#   scripts/squash.sh [--push]
#
# The published history of this repository is one commit, amended as the project
# grows. Doing that by hand is easy to get wrong in a way nobody notices: a
# rebase that drops a file still leaves a plausible-looking commit. So this
# script proves the new commit has exactly the same tree as the old one, and
# only then offers to push.
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

if [ -n "$(git status --porcelain)" ]; then
	printf 'squash: the working tree has changes; commit or stash them first\n' >&2
	exit 1
fi

branch=$(git symbolic-ref --short HEAD 2>/dev/null || echo "")
if [ "$branch" != "main" ]; then
	printf 'squash: on %s, expected main\n' "${branch:-a detached HEAD}" >&2
	exit 1
fi

before_commit=$(git rev-parse HEAD)
before_tree=$(git rev-parse HEAD^{tree})
base=$(git rev-list --max-parents=0 HEAD)
count=$(git rev-list --count HEAD)

if [ "$before_commit" = "$base" ]; then
	printf 'squash: already a single commit %s\n' "$(git rev-parse --short HEAD)"
else
	author_name=$(git log -1 --format=%an "$base")
	author_email=$(git log -1 --format=%ae "$base")
	printf 'squash: folding %s commits into one\n' "$count"
	git reset --quiet --soft "$base"
	# The amend keeps the base commit's author; the committer is whoever runs this,
	# which is nobody at all when git has no identity configured.
	if ! git -c user.name="$author_name" -c user.email="$author_email" \
		commit --quiet --amend --no-edit --date="$(git log -1 --format=%aD "$base")"; then
		printf 'squash: the amend failed; restoring %s\n' "$(git rev-parse --short "$before_commit")" >&2
		git reset --quiet --hard "$before_commit"
		exit 1
	fi
	after_tree=$(git rev-parse HEAD^{tree})
	if [ "$after_tree" != "$before_tree" ]; then
		printf 'squash: the tree changed (%s -> %s); restoring %s\n' \
			"$before_tree" "$after_tree" "$(git rev-parse --short "$before_commit")" >&2
		git reset --quiet --hard "$before_commit"
		exit 1
	fi
	printf 'squash: tree unchanged (%s)\n' "$before_tree"
fi

printf 'squash: HEAD is now %s\n' "$(git rev-parse HEAD)"

if [ $# -ge 1 ] && [ "$1" = "--push" ]; then
	git push --force-with-lease origin main
	printf 'squash: pushed %s\n' "$(git rev-parse HEAD)"
else
	printf 'squash: not pushed; run: git push --force-with-lease origin main\n'
fi
