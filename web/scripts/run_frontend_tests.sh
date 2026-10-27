#!/bin/sh
# Runs every frontend unit test in one go.
#
# The suite is deliberately framework-free: each src/**/*.test.ts is a plain
# script (node:assert + the module under test, no third-party imports) that
# node executes directly with --experimental-strip-types — the same command
# the test headers document for running one file by hand. This script only
# globs and loops so `npm test` / `make test` cover them all; a failing
# assert makes node exit non-zero, and every file still runs so one failure
# does not hide the next.
set -u
cd "$(dirname "$0")/.."

fail=0
for f in $(find src -name '*.test.ts' | sort); do
	echo "== $f"
	node --experimental-strip-types "$f" || fail=1
done

if [ "$fail" -ne 0 ]; then
	echo "frontend tests: FAILED" >&2
	exit 1
fi
echo "frontend tests: OK"
