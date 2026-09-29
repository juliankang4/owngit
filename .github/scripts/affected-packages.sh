#!/usr/bin/env bash
# Prints the Go packages whose tests a change can affect.
#
# Usage: affected-packages.sh BASE HEAD
#
# A changed file belongs to the package of its nearest enclosing package
# directory. A package is affected when its test binary depends on a package
# with a changed file. A package whose only changed files are its own
# *_test.go files affects only its own tests, because no other test binary
# contains them. A changed file outside every package directory, such as
# go.mod, a workflow or a document that tests read, can affect any test, so
# the script then prints "./..." alone. Otherwise it prints the affected
# import paths, one per line and sorted, or nothing when no test can see the
# change.
#
# Test binaries include different files on each system, so the packages and
# their dependencies are taken from Linux, Windows and macOS together.
# Renames count as a removal and an addition, so both paths are considered.
# The script needs bash 4 or later.
set -euo pipefail

[ $# -eq 2 ] || {
	echo "usage: $0 BASE HEAD" >&2
	exit 2
}
base=$1
head=$2
systems="linux windows darwin"

# Command substitutions rather than pipes, so a failing git or go command
# stops the script instead of selecting fewer packages.
files="$(git diff --no-renames --name-only -z "$base" "$head" | tr '\0' '\n')"
module="$(go list -m)"

# Package directories relative to the repository root, which is the module
# root, mapped to their import paths.
declare -A package_of
for goos in $systems; do
	packages="$(GOOS=$goos go list ./...)"
	while read -r path; do
		if [ "$path" = "$module" ]; then
			package_of[.]=$path
		else
			package_of[${path#"$module"/}]=$path
		fi
	done <<<"$packages"
done

declare -A changed own_tests
while read -r file; do
	[ -n "$file" ] || continue
	dir=$(dirname "$file")
	while [ -z "${package_of[$dir]-}" ]; do
		if [ "$dir" = . ]; then
			echo ./...
			exit 0
		fi
		dir=$(dirname "$dir")
	done
	case $file in
	*_test.go) own_tests[${package_of[$dir]}]=1 ;;
	*) changed[${package_of[$dir]}]=1 ;;
	esac
done <<<"$files"

# Each test binary "P.test" lists every package it contains; entries such as
# "P [P.test]" are packages compiled with P's test files.
declare -A affected
for goos in $systems; do
	tests="$(GOOS=$goos go list -test -f '{{.ImportPath}} {{join .Deps ","}}' ./...)"
	while read -r binary deps; do
		case $binary in *.test) ;; *) continue ;; esac
		pkg=${binary%.test}
		if [ -n "${changed[$pkg]-}" ] || [ -n "${own_tests[$pkg]-}" ]; then
			affected[$pkg]=1
			continue
		fi
		IFS=, read -ra list <<<"$deps"
		for dep in ${list[@]+"${list[@]}"}; do
			if [ -n "${changed[${dep%% *}]-}" ]; then
				affected[$pkg]=1
				break
			fi
		done
	done <<<"$tests"
done

for pkg in "${!affected[@]}"; do
	echo "$pkg"
done | LC_ALL=C sort
