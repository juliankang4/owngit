#!/usr/bin/env bash
# Runs one shard of the Go tests of every package.
#
# Usage: test-shard.sh [--dry-run] SHARD SHARDS [GO TEST FLAGS...]
#
# The script lists the top-level tests, examples and fuzz tests of every
# package with "go test -list", sorts them by package and name, and numbers
# them from 0 across all packages. Test number i belongs to shard
# i % SHARDS + 1. Every shard lists the same tests,
# because it runs on the same commit and operating system, so every test runs
# in exactly one shard, a package with at least SHARDS tests is spread over all
# shards, and a test added later is picked up without changing this file.
#
# The listing compiles every package, including packages without tests, so a
# build failure fails every shard. The selected tests then run with one
# "go test -run '^(A|B|...)$' PACKAGE" per package: first the packages in
# $alone one at a time, then the rest several at once, the slowest first. The
# logs are printed afterwards in package order. The shard fails if any package
# fails. --dry-run prints the selected "package<TAB>test" lines and
# runs nothing. Logs stay in a new temporary directory, which is printed.
#
# The go test flags, such as -race, apply to the listing and to the runs. Do
# not pass them through GOFLAGS instead: tests that run the go command would
# inherit it, and tools/release fails with "-race requires cgo".
set -euo pipefail

usage() {
	echo "usage: $0 [--dry-run] SHARD SHARDS [GO TEST FLAGS...]" >&2
	exit 2
}

dry_run=
if [ "${1-}" = --dry-run ]; then
	dry_run=1
	shift
fi
[ $# -ge 2 ] || usage
shard=$1
shards=$2
shift 2
case "$shard$shards" in *[!0-9]* | '') usage ;; esac
[ "$shard" -ge 1 ] && [ "$shard" -le "$shards" ] || usage

# Packages running at the same time. Most tests wait on Git subprocesses, so
# this matches the four CPUs of the Linux and Windows runners.
parallel=4
# The slowest packages, started first so that none of them starts late and
# sets the length of the shard. Others follow by their number of tests.
slow_first="owngit/tools/release owngit/internal/importsync owngit/internal/server"
# Packages with tight timing (internal/gitexec gives a helper process 50 ms),
# run alone before the others so a loaded runner does not fail them.
alone="owngit/internal/gitexec"
# Packages run without -race. tools/release starts no goroutines and has no
# parallel tests, so the race detector has nothing to check there, but it slows
# the release tool's in-process archive building and hashing: 274 to 446 s per
# Linux shard with -race, against 161 to 272 s per shard on macOS and Windows
# without it.
no_race="owngit/tools/release"

work="$(mktemp -d)"

if ! go test "$@" -list '.*' ./... >"$work/list.out" 2>"$work/list.err"; then
	cat "$work/list.out" "$work/list.err"
	echo "Listing the tests failed." >&2
	exit 1
fi

# A package's names come before its "ok" line. go test runs benchmarks only
# with -bench, so they are left out.
tr -d '\r' <"$work/list.out" | awk '
	/^(Test|Example|Fuzz)[^ \t]*$/ { names[++n] = $0; next }
	/^ok[ \t]/ { for (i = 1; i <= n; i++) print $2 "\t" names[i]; n = 0 }
' | LC_ALL=C sort >"$work/all.tsv"

# Assign every test to one shard and check that the shard sizes add up to the
# total, so no test is lost or counted twice.
: >"$work/selected.tsv"
awk -F '\t' -v shard="$shard" -v shards="$shards" -v selected="$work/selected.tsv" '
	{ s = (NR - 1) % shards + 1; count[s]++; if (s == shard) print > selected }
	END {
		for (s = 1; s <= shards; s++) { line = line " " s "=" count[s] + 0; sum += count[s] }
		printf "Tests per shard:%s, total %d\n", line, NR
		if (sum != NR || NR == 0) { print "Shard counts do not add up." > "/dev/stderr"; exit 1 }
	}
' "$work/all.tsv"
echo "Shard $shard of $shards runs $(wc -l <"$work/selected.tsv" | tr -d ' ') tests."

if [ -n "$dry_run" ]; then
	cat "$work/selected.tsv"
	exit 0
fi

# One job per package: NNN.pkg holds the package, NNN.run the -run pattern,
# and jobs.txt the number of tests, the job, and the package. The largest
# package has about 60 names in a shard, well under the Windows command-line
# limit.
awk -F '\t' -v dir="$work" '
	function flush() {
		if (pkg == "") return
		file = dir "/" id ".run"; print "^(" run ")$" > file; close(file)
		file = dir "/" id ".pkg"; print pkg > file; close(file)
		print n, id, pkg > (dir "/jobs.txt")
	}
	$1 != pkg { flush(); pkg = $1; run = $2; n = 1; id = sprintf("%03d", ++jobs); next }
	{ run = run "|" $2; n++ }
	END { flush() }
' "$work/selected.tsv"

# Runs one job; a job without a status file counts as failed below. A slow
# runner can take more than Go's default 10 minutes for one package, so the
# limit is raised.
worker='
	dir=$SHARD_WORK id=$1
	shift
	pkg=$(cat "$dir/$id.pkg")
	flags=()
	for flag in "$@"; do
		case " $SHARD_NO_RACE " in *" $pkg "*) [ "$flag" = -race ] && continue ;; esac
		flags+=("$flag")
	done
	if go test ${flags[@]+"${flags[@]}"} -timeout 30m -run "$(cat "$dir/$id.run")" "$pkg" >"$dir/$id.log" 2>&1; then
		status=0
	else
		status=$?
	fi
	echo "$status $SECONDS ${flags[*]-}" >"$dir/$id.status"
'
export SHARD_WORK="$work" SHARD_NO_RACE="$no_race"
: >"$work/alone.txt"
: >"$work/queue.txt"
if [ -f "$work/jobs.txt" ]; then
	while read -r n id pkg; do
		case " $alone " in *" $pkg "*)
			echo "$id" >>"$work/alone.txt"
			continue
			;;
		esac
		rank=9
		i=1
		for slow in $slow_first; do
			[ "$slow" = "$pkg" ] && rank=$i
			i=$((i + 1))
		done
		echo "$rank $n $id" >>"$work/queue.txt"
	done <"$work/jobs.txt"
fi
while read -r id; do
	bash -c "$worker" _ "$id" "$@" </dev/null || true
done <"$work/alone.txt"
sort -k 1,1n -k 2,2nr "$work/queue.txt" | cut -d ' ' -f 3 |
	xargs -P "$parallel" -I '{}' bash -c "$worker" _ '{}' "$@" || true

failed=0
: >"$work/summary.txt"
for pkg_file in "$work"/*.pkg; do
	[ -e "$pkg_file" ] || continue
	id="${pkg_file%.pkg}"
	pkg="$(cat "$pkg_file")"
	tests="$(awk -F '|' '{ print NF }' "$id.run")"
	status=missing
	seconds=?
	flags=
	[ -f "$id.status" ] && read -r status seconds flags <"$id.status"
	if [ "$status" = 0 ]; then result=ok; else result=FAIL; failed=$((failed + 1)); fi
	echo "=== $pkg: $tests tests, $result"
	cat "$id.log" 2>/dev/null || true
	printf '%-4s %5ss %4s tests  %s %s\n' "$result" "$seconds" "$tests" "$pkg" "$flags" >>"$work/summary.txt"
done

echo
echo "Summary of shard $shard of $shards (logs in $work):"
cat "$work/summary.txt"
if [ "$failed" -ne 0 ]; then
	echo "$failed package(s) failed." >&2
	exit 1
fi
