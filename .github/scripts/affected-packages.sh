#!/usr/bin/env bash
# Prints the Go packages whose tests a change can affect.
#
# Usage: affected-packages.sh [--full] BASE HEAD
#
# A changed file belongs to the package of its nearest enclosing package
# directory. A package is affected when its test binary depends on a package
# with a changed file. A package whose only changed files are its own
# *_test.go files affects only its own tests, because no other test binary
# contains them. A changed file outside every package directory, such as
# go.mod or a workflow, or under integrations/, can affect any test, so the
# script then prints "./..." alone. Otherwise it prints the affected import
# paths, one per line and sorted, or nothing when no test can see the change.
#
# A few tests also read files by path that their test binary does not show;
# they are declared below with what they read. A Markdown file outside every
# package selects only those readers, and an image under docs/images selects
# nothing, since no test reads one.
#
# --full prints the packages that keep their full flags on a pull request,
# which test-shard.sh turns into a -short run for the rest: every affected
# package that contains a changed file, or every affected package when a
# changed file belongs to a test helper package, whose code other test
# binaries compile into themselves. The full list is a subset of the normal
# list.
#
# Test binaries include different files on each system, so the packages and
# their dependencies are taken from Linux, Windows and macOS together.
# Renames count as a removal and an addition, so both paths are considered.
# The script needs bash 4 or later.
set -euo pipefail

full=
if [ "${1-}" = --full ]; then
	full=1
	shift
fi
[ $# -eq 2 ] || {
	echo "usage: $0 [--full] BASE HEAD" >&2
	exit 2
}
base=$1
head=$2
systems="linux windows darwin"

# Command substitutions rather than pipes, so a failing git or go command
# stops the script instead of selecting fewer packages.
files="$(git diff --no-renames --name-only -z "$base" "$head" | tr '\0' '\n')"
module="$(go list -m)"
# Test helper packages: other packages' test binaries compile them in, so a
# change here can change what those tests do, and --full then keeps every
# affected package out of the -short tier.
helper_packages="$module/internal/testfixture $module/internal/tailscale/tailscaletest"

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

# The tests that read files by path beyond their test binary, and what they
# read.
# internal/gitexec: TestUngatedLockReferencesStayWithinStorageVerificationPaths
# parses every non-test .go file of the module outside testdata and dot directories.
# tools/release: TestInstallerCommandsAreTheSafeForms reads every .md, .sh
# and .ps1 file outside testdata, node_modules and dot directories. Its other
# tests build ./cmd/owngit and compare the checked-in notices with those
# collected from its module graph and from the license files bundled in its
# packages, so every package ./cmd/owngit is built from counts as part of
# the tools/release test binary (below).
# internal/state: TestMacOSChangeChecksAgree compiles
# packaging/macos/ProtectedPath.swift; Swift changes select every package below.
# cmd/owngit: its document tests read docs/OPERATIONS.md, docs/CODING_TOOLS.md
# and their Korean versions.
gitexec=$module/internal/gitexec
release=$module/tools/release
owngit=$module/cmd/owngit

declare -A changed own_tests affected
helper_changed=
while read -r file; do
	[ -n "$file" ] || continue
	case $file in
	# The release tool reads and ships the files under integrations/ by path,
	# without importing their packages, so a change there selects everything
	# like a change outside every package.
	integrations/*)
		echo ./...
		exit 0
		;;
	esac
	case /$file in
	/internal/state/testdata/released/*) affected[$module/internal/recovery]=1 ;;
	*/testdata/* | */.*/*) ;;
	*.go) affected[$gitexec]=1 ;;
	*/node_modules/*) ;;
	*.md | *.sh | *.ps1) affected[$release]=1 ;;
	esac
	dir=$(dirname "$file")
	while [ -z "${package_of[$dir]-}" ] && [ "$dir" != . ]; do
		dir=$(dirname "$dir")
	done
	if [ -z "${package_of[$dir]-}" ]; then
		case /$file in
		*/testdata/* | */.*/* | */node_modules/*) ;;
		/docs/OPERATIONS.md | /docs/OPERATIONS.ko.md | /docs/CODING_TOOLS.md | /docs/CODING_TOOLS.ko.md)
			affected[$owngit]=1
			continue
			;;
		*.md | /docs/images/*) continue ;;
		esac
		echo ./...
		exit 0
	fi
	case " $helper_packages " in
	*" ${package_of[$dir]} "*) helper_changed=1 ;;
	esac
	case $file in
	*_test.go) own_tests[${package_of[$dir]}]=1 ;;
	*) changed[${package_of[$dir]}]=1 ;;
	esac
done <<<"$files"

# Each test binary "P.test" lists every package it contains; entries such as
# "P [P.test]" are packages compiled with P's test files. The tools/release
# test binary also counts every package ./cmd/owngit is built from.
for goos in $systems; do
	built="$(GOOS=$goos go list -f '{{.ImportPath}}' -deps ./cmd/owngit)"
	built=${built//$'\n'/,}
	tests="$(GOOS=$goos go list -test -f '{{.ImportPath}} {{join .Deps ","}}' ./...)"
	while read -r binary deps; do
		case $binary in *.test) ;; *) continue ;; esac
		pkg=${binary%.test}
		if [ "$pkg" = "$release" ]; then
			deps="$deps,$built"
		fi
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

# --full keeps only the packages that contain a changed file of their own,
# a product file or their own test files; the rest are affected only through a
# dependency, or through a file their tests read, and run with -short. A
# change to a test helper package appears in other packages' test binaries,
# so every affected package then runs in full.
for pkg in "${!affected[@]}"; do
	if [ -n "$full" ] && [ -z "$helper_changed" ] &&
		[ -z "${changed[$pkg]-}" ] && [ -z "${own_tests[$pkg]-}" ]; then
		continue
	fi
	echo "$pkg"
done | LC_ALL=C sort
