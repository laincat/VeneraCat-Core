#!/bin/sh
# Core's error text is English; localisation belongs to the clients.
#
#   scripts/check-error-text.sh
#
# This was a rule in AGENTS.md and nothing else, and it had already drifted: the
# Magpie prelude answered "这个插件没有提供签到" from inside Core, so an iOS user
# saw Chinese from the shared layer and a desktop user could see it too. A rule
# nobody can check is a preference; this makes it a check.
#
# Only error constructors are inspected: default names such as "线路1" are data a
# provider may see, not a message to a person.
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

fail=0
for f in $(git ls-files '*.go' | grep -v '_test\.go$'); do
	# errors.New/fmt.Errorf calls whose literal contains a CJK character.
	hits=$(grep -nE '(errors\.New|fmt\.Errorf)\("[^"]*[一-鿿]' "$f" || true)
	if [ -n "$hits" ]; then
		printf '%s\n' "$hits" | while IFS= read -r line; do
			printf 'check-error-text: %s:%s\n' "$f" "$line" >&2
		done
		fail=1
	fi
done

# The Magpie prelude is JavaScript inside a Go string: same rule, different file.
for f in $(git ls-files 'magpieplugin/*.go' | grep -v '_test\.go$'); do
	hits=$(grep -nE 'new Error\("[^"]*[一-鿿]' "$f" || true)
	if [ -n "$hits" ]; then
		printf '%s\n' "$hits" | while IFS= read -r line; do
			printf 'check-error-text: %s:%s\n' "$f" "$line" >&2
		done
		fail=1
	fi
done

# The same rule for the JavaScript that ships inside Core: a prelude that answers
# in Chinese puts Chinese in front of every client.
for f in $(git ls-files '*.js' | grep -v testdata); do
	hits=$(grep -nE '(throw new Error|new Error)\("[^"]*[一-鿿]' "$f" || true)
	if [ -n "$hits" ]; then
		printf '%s
' "$hits" | while IFS= read -r line; do
			printf 'check-error-text: %s:%s
' "$f" "$line" >&2
		done
		fail=1
	fi
done

if [ "$fail" -ne 0 ]; then
	printf 'check-error-text: error text must be English; localise it in the client\n' >&2
	exit 1
fi
printf 'check-error-text: ok\n'
