#!/usr/bin/env bash
# Prints a release's notes: README.md's download table and nothing else, as
# the README has it. Its links are releases/latest/download/<fixed name>, so
# the table on every release, old ones included, fetches the newest files.
#
#   bash scripts/release-notes.sh
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

echo "## Download"
echo
# The table alone, from <table> to </table>.
awk '/^<table>/ { p = 1 } p { print } /^<\/table>/ && p { exit }' "$root/README.md"
