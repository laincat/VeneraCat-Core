#!/bin/sh
# Repository hygiene checks:
#   1. every X.md has an X.en.md and the other way round;
#   2. each file of a pair links to the other near the top;
#   3. the root holds only the files listed in ROOT_ALLOWED.
# Third-party licence texts (LICENSE-*.txt) are not Markdown and are ignored.
set -eu
cd "$(dirname "$0")/.."

ROOT_ALLOWED=".git .github .gitattributes .gitignore AGENTS.md AGENTS.en.md LICENSE README.md README.en.md go.mod go.sum
apple attribution aiapi backup challenge comic docs host magpierouting magpieplugin mobile plugin scripts subscription version video build"

fail=0
err() { printf 'check-docs: %s\n' "$*" >&2; fail=1; }

for f in $(git ls-files --cached --others --exclude-standard '*.md'); do
	case "$f" in
	*.en.md)
		zh="${f%.en.md}.md"
		[ -f "$zh" ] || err "$f has no Chinese counterpart $zh"
		head -n 5 "$f" | grep -q "](\(\./\)\{0,1\}$(basename "$zh"))" || err "$f does not link to $(basename "$zh") near the top"
		;;
	*)
		en="${f%.md}.en.md"
		[ -f "$en" ] || err "$f has no English counterpart $en"
		head -n 5 "$f" | grep -q "](\(\./\)\{0,1\}$(basename "$en"))" || err "$f does not link to $(basename "$en") near the top"
		;;
	esac

	# A pair that links correctly can still say different things: the Chinese page gains
	# a table row and the English one does not. Counting rows and headings catches that
	# without comparing prose, which cannot be automated.
	case "$f" in
	*.en.md) ;;
	*)
		en="${f%.md}.en.md"
		[ -f "$en" ] || continue
		rows=$(grep -c '^|' "$f" 2>/dev/null || true)
		enrows=$(grep -c '^|' "$en" 2>/dev/null || true)
		[ "$rows" = "$enrows" ] || err "$f has ${rows:-0} table rows, $en has ${enrows:-0}"
		heads=$(grep -c '^## ' "$f" 2>/dev/null || true)
		enheads=$(grep -c '^## ' "$en" 2>/dev/null || true)
		[ "$heads" = "$enheads" ] || err "$f has ${heads:-0} headings, $en has ${enheads:-0}"
		;;
	esac
done

for entry in $(ls -A); do
	case " $(echo $ROOT_ALLOWED) " in
	*" $entry "*) ;;
	*) err "unexpected file in the repository root: $entry" ;;
	esac
done

[ "$fail" -eq 0 ] && echo "check-docs: ok"
exit "$fail"
