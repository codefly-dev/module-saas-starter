#!/usr/bin/env bash
# check-boundaries.sh — the boundary gate. Governed by the handbook's
# concepts/boundaries.md: nothing in this repo may name what sits above it, and one
# consumer's need is never a feature here.
#
# It greps every tracked text file for the words and paths this layer may not
# contain (scripts/boundaries.denylist: one extended regex per line, '#' comments)
# (case-insensitively: "Apollo" and "apollo" are the same leak) and FAILS on any hit in a file not listed in scripts/boundaries.baseline — the
# files that already carried a hit the day the gate landed. The baseline shrinks and
# never grows: clean a file, delete its line. A baselined file with no hits left is
# reported so its line gets removed. Do not add to the baseline to land a change.
#
# Exempt: this gate's own files, AGENTS.md and CLAUDE.md (they state the rule and so
# must name the forbidden words), and any line that says who *consumes* this repo —
# the one place the handbook allows naming a consumer.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

DENY=scripts/boundaries.denylist
BASE=scripts/boundaries.baseline
PATTERN="$(grep -vE '^[[:space:]]*(#|$)' "$DENY" | paste -sd'|' -)"
[ -n "$PATTERN" ] || { echo "check-boundaries: $DENY has no patterns" >&2; exit 2; }
touch "$BASE"

HITS="$(git ls-files -z \
  | grep -zvE '(^|/)(AGENTS|CLAUDE)\.md$|^scripts/(check-boundaries\.sh|boundaries\.(denylist|baseline))$' \
  | xargs -0 grep -niEI "($PATTERN)" -- 2>/dev/null \
  | grep -viE 'consum(er|ers|ed|es|ing)\b' || true)"

fail=0
FILES="$(printf '%s\n' "$HITS" | cut -d: -f1 | sort -u | sed '/^$/d')"
for f in $FILES; do
  if ! grep -qxF "$f" "$BASE"; then
    fail=1
    echo "check-boundaries: $f names something outside this repo's boundary:" >&2
    # awk, not grep|head: under pipefail, head closing early would SIGPIPE grep
    # and abort the loop, truncating the report and any baseline built from it.
    printf '%s\n' "$HITS" | awk -v p="$f:" 'index($0, p) == 1 && n < 5 { print "    " $0; n++ }' >&2
  fi
done
while read -r b; do
  [ -n "$b" ] || continue
  grep -qxF "$b" <<< "$FILES" || { fail=1; echo "check-boundaries: $b is baselined but clean now; delete its line from $BASE" >&2; }
done < "$BASE"

if [ "$fail" -ne 0 ]; then
  echo "check-boundaries: FAILED — see AGENTS.md (Boundaries) and the handbook's concepts/boundaries.md." >&2
  exit 1
fi
n=$(wc -l < "$BASE" | tr -d ' ')
echo "check-boundaries: clean outside the baseline ($n baselined file(s) still to fix)."
