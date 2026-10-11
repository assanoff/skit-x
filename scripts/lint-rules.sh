#!/usr/bin/env bash
# Project rules that golangci-lint has no linter for. Checks tracked files and
# new files that are not ignored yet, so a fresh file fails before `git add`;
# outside a git repository (a just-generated service) it checks every file.
# Generated code is skipped. The optional argument is a regexp of paths to skip.
#
# Usage: lint-rules.sh [exclude-regexp]
set -euo pipefail

exclude=${1:-'^$'}

list() {
	git ls-files --cached --others --exclude-standard '*.go' '*.go.tmpl' 2>/dev/null ||
		find . -type f \( -name '*.go' -o -name '*.go.tmpl' \) | sed 's|^\./||'
}

files=$(list | grep -Ev "$exclude" |
	xargs grep -L '^// Code generated .* DO NOT EDIT\.$' || true)
if [ -z "$files" ]; then
	exit 0
fi
fail=0

# No else: use an early return.
if grep -HnE '\}[[:space:]]*else\b' $files; then
	echo 'lint-rules: else — replace it with an early return'
	fail=1
fi

# A function written on one line, function literals included, fits in
# 80 columns with tab = 4.
awk '{ l = $0; gsub(/\t/, "    ", l) }
	/func[ (].*\{.*\}[)},;]*[[:space:]]*$/ && length(l) > 80 {
		print FILENAME ":" FNR ": " length(l) " columns"; bad = 1
	}
	END { exit bad }' $files || {
	echo 'lint-rules: one-line function over 80 columns — move the body to its own lines'
	fail=1
}

exit $fail
