#!/bin/sh
# OwnGit installer for Linux and macOS.
#
#   curl -fsSL https://owngit.app/install.sh | sh
#   curl -fsSL https://owngit.app/install.sh | sh -s -- --version 1.1.3 --no-service
#
# It downloads the release archive for this computer over HTTPS, checks it
# against the SHA256SUMS file of the same release, puts owngit in place and
# runs "owngit service install". Nothing on this computer changes before the
# archive matches SHA256SUMS. OWNGIT_RELEASES replaces the release address
# (https only), for a mirror.
#
# Everything runs from main, called on the last line, so a shell that reads
# this script from a pipe has read all of it before anything runs.

set -eu

say() { printf '%s\n' "$*"; }
fail() {
	printf 'owngit install: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<'EOF'
Usage: install.sh [--version X.Y.Z] [--to PATH] [--no-service]

  --version X.Y.Z  install this release instead of the latest one
  --to PATH        where the owngit program goes (default ~/.local/bin/owngit,
                   or /usr/local/bin/owngit for root)
  --no-service     install the program only; register and start nothing
EOF
}

main() {
	version="" target="" service=1
	while [ $# -gt 0 ]; do
		case $1 in
		--version | --to)
			[ $# -ge 2 ] || fail "$1 needs a value (see --help)"
			if [ "$1" = --version ]; then version=$2; else target=$2; fi
			shift 2
			;;
		--version=*) version=${1#--version=} && shift ;;
		--to=*) target=${1#--to=} && shift ;;
		--no-service) service=0 && shift ;;
		-h | --help) usage && return 0 ;;
		*) fail "unknown option $1 (see --help)" ;;
		esac
	done
	version=${version#v}
	if [ -n "$version" ] && ! printf '%s\n' "$version" | grep -Eqx '[0-9]+\.[0-9]+\.[0-9]+'; then
		fail "--version takes a release number such as 1.1.3, not $version"
	fi
	releases=${OWNGIT_RELEASES:-https://github.com/juliankang4/owngit/releases}
	case $releases in
	https://*) ;;
	*) fail "OWNGIT_RELEASES must be an https:// address, not $releases" ;;
	esac

	case $(uname -s) in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*) fail "no OwnGit release archive exists for $(uname -s); build OwnGit from source" ;;
	esac
	case $(uname -m) in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) fail "no OwnGit release archive exists for $(uname -m); build OwnGit from source" ;;
	esac
	# A shell under Rosetta reports x86_64 on an Apple silicon Mac.
	if [ "$os" = darwin ] && [ "$(sysctl -n hw.optional.arm64 2>/dev/null || true)" = 1 ]; then
		arch=arm64
	fi
	[ "$os/$arch" != darwin/amd64 ] || fail "OwnGit for macOS needs an Apple silicon Mac; on this Mac build OwnGit from source"

	command -v curl >/dev/null 2>&1 || fail "curl is needed to download OwnGit"
	command -v tar >/dev/null 2>&1 || fail "tar is needed to unpack OwnGit"
	if command -v sha256sum >/dev/null 2>&1; then
		digest_of() { sha256sum "$1" | cut -d ' ' -f 1; }
	elif command -v shasum >/dev/null 2>&1; then
		digest_of() { shasum -a 256 "$1" | cut -d ' ' -f 1; }
	else
		fail "sha256sum or shasum is needed to check the download"
	fi

	if [ -z "$target" ]; then
		if [ "$(id -u)" = 0 ]; then target=/usr/local/bin/owngit; else target=$HOME/.local/bin/owngit; fi
	fi
	case $target in /*) ;; *) target=$(pwd)/$target ;; esac
	[ ! -d "$target" ] || fail "--to names the program file, such as $target/owngit, not a folder"
	dir=$(dirname "$target")

	tmp=$(mktemp -d)
	trap 'rm -f "$tmp/SHA256SUMS" "$tmp/archive.tar.gz" "$tmp/owngit"; rmdir "$tmp"' EXIT
	fetch() {
		curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL --retry 2 -o "$2" "$1" ||
			fail "could not download $1; nothing was changed"
	}

	# The digest and the archive name come from the release's SHA256SUMS.
	# Without --version, the name of the latest release's archive gives
	# its version, so both downloads are from the same release.
	if [ -n "$version" ]; then
		fetch "$releases/download/v$version/SHA256SUMS" "$tmp/SHA256SUMS"
		pattern="owngit_${version}_${os}_${arch}\\.tar\\.gz"
	else
		fetch "$releases/latest/download/SHA256SUMS" "$tmp/SHA256SUMS"
		pattern="owngit_[0-9]+\\.[0-9]+\\.[0-9]+_${os}_${arch}\\.tar\\.gz"
	fi
	line=$(grep -Ex "[0-9a-f]{64}  $pattern" "$tmp/SHA256SUMS" || true)
	case $line in
	"" | *"
"*) fail "the release's SHA256SUMS does not list one archive for $os/$arch; nothing was changed" ;;
	esac
	digest=${line%%  *}
	name=${line#*  }
	version=${name#owngit_}
	version=${version%%_*}

	fetch "$releases/download/v$version/$name" "$tmp/archive.tar.gz"
	actual=$(digest_of "$tmp/archive.tar.gz")
	[ "$actual" = "$digest" ] ||
		fail "$name does not match the release's SHA256SUMS (got $actual, expected $digest); nothing was changed"
	tar -xzf "$tmp/archive.tar.gz" -C "$tmp" owngit || fail "could not unpack owngit from $name; nothing was changed"

	# sudo only when this account cannot write the folder (or the nearest
	# folder that exists, when it has to be made). A copy that sudo puts
	# there belongs to root, as a service that root installed requires.
	existing=$dir
	while [ ! -d "$existing" ]; do existing=$(dirname "$existing"); done
	sudo=""
	if [ ! -w "$existing" ]; then
		command -v sudo >/dev/null 2>&1 ||
			fail "this account cannot write $existing; run the installer as root, or choose a folder you can write with --to"
		sudo=sudo
		say "This account cannot write $existing, so sudo puts owngit there."
	fi
	# A file that cannot be read counts as different and is replaced.
	if [ -f "$target" ] && [ "$(digest_of "$target" 2>/dev/null)" = "$(digest_of "$tmp/owngit")" ]; then
		say "OwnGit $version is already at $target."
	else
		# The new file takes the old one's place in one rename, so the
		# program at the path is always whole; a running OwnGit keeps
		# running the old file until it restarts.
		staged=$dir/.owngit-$version-$$
		$sudo mkdir -p "$dir" || fail "could not create $dir"
		if ! $sudo install -m 0755 "$tmp/owngit" "$staged" || ! $sudo mv -f "$staged" "$target"; then
			$sudo rm -f "$staged"
			fail "could not put owngit at $target"
		fi
		say "Installed OwnGit $version at $target."
	fi

	case ":$PATH:" in
	*":$dir:"*)
		found=$(command -v owngit || true)
		[ "$found" = "$target" ] || [ -z "$found" ] || say "Note: \"owngit\" on your PATH is $found, not this one."
		;;
	*) say "$dir is not on your PATH, so run OwnGit as $target, or add $dir to PATH." ;;
	esac

	if [ "$service" = 0 ]; then
		say "Run it now with: $target serve"
		say "Or run it as a service that starts by itself: $target service install"
		return 0
	fi
	"$target" service install ||
		fail "\"$target service install\" did not finish. OwnGit $version stays at $target; after fixing what it reported, run that command again."
}

main "$@"
