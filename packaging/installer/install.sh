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

# Tools come from the system folders only, never from a folder earlier in
# the inherited PATH; that PATH is kept only to tell whether owngit is on it.
user_path=${PATH-}
PATH=/usr/bin:/bin:/usr/sbin:/sbin
export PATH

say() { printf '%s\n' "$*"; }
fail() {
	printf 'owngit install: %s\n' "$*" >&2
	exit 1
}

# system prints the fixed path of a system tool, or fails.
system() {
	for folder in /usr/bin /bin /usr/sbin /sbin; do
		if [ -x "$folder/$1" ]; then
			printf '%s\n' "$folder/$1"
			return 0
		fi
	done
	return 1
}

# quote prints a word as one POSIX shell word, quoted only when it has to
# be, as "owngit update" quotes paths, so a printed command can be copied.
quote() {
	case $1 in
	"" | *[!A-Za-z0-9_@%+=:,./-]*) printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")" ;;
	*) printf '%s' "$1" ;;
	esac
}

# changeable prints why another account can change FOLDER ($1), or nothing:
# it belongs to another account (not root), others or a shared group can
# write it, or an access list allows changes. HOLDS ($2) is 1 for the
# folder the installer creates entries in, where a sticky folder does not
# help, since others can still take a name that does not exist yet; above
# it a sticky folder such as /tmp is fine, since others cannot rename what
# is in it. The rule is the one OwnGit uses for its state folder.
changeable() {
	info=$(ls -ldn -- "$1") || {
		echo "it cannot be read"
		return
	}
	set -f
	# shellcheck disable=SC2086 # the fields of ls are wanted
	set -- "$1" "$2" $info
	set +f
	mode=$3 uid=$5 gid=$6
	if [ "$uid" != 0 ] && [ "$uid" != "$euid" ]; then
		echo "it belongs to another account"
		return
	fi
	case $2$mode in 0?????????[tT]*) return ;; esac
	case $mode in ????????w*)
		echo "every account can write it"
		return
		;;
	esac
	case $mode in ?????w*)
		if [ "$gid" != "$own_group" ] && ! { [ "$os" = darwin ] && { [ "$gid" = 0 ] || [ "$gid" = 80 ]; }; }; then
			echo "its group can write it"
			return
		fi
		;;
	esac
	case $mode in ??????????+*)
		# shellcheck disable=SC2010 # the access list lines of ls are read, not names
		if [ "$os" = linux ]; then
			echo "it has an access list"
		elif ls -lde -- "$1" | grep -Eq '^ *[0-9]+: .* allow .*(add_file|add_subdirectory|delete|writesecurity|chown)'; then
			echo "its access list lets others change it"
		fi
		;;
	esac
}

# require_way refuses when another account can change FOLDER ($1), which
# exists, or anything on the path to it, which is walked as the system
# follows it later: every folder from / and every link on the way, and then
# the path each link names, from the folder the link is in (or from / for
# an absolute link). A link counts only if it belongs to this account or
# root; the folder it is in is checked before it. HOLDS ($2) and USE ($3)
# as for changeable: HOLDS is 1 when the folder gets entries.
require_way() {
	path=$1 holds=$2 use=$3 links=0 folder=/
	case $path in /*) rest=$path ;; *) rest=$PWD/$path ;; esac
	reason=$(changeable / 0)
	[ -z "$reason" ] || fail "another account can change / ($reason); $use"
	while [ -n "$rest" ]; do
		part=${rest%%/*}
		case $rest in */*) rest=${rest#*/} ;; *) rest="" ;; esac
		case $part in
		"" | .) continue ;;
		..)
			folder=$(dirname "$folder")
			continue
			;;
		esac
		next=${folder%/}/$part
		if [ -L "$next" ]; then
			links=$((links + 1))
			[ "$links" -le 40 ] || fail "$path goes through more than 40 links"
			set -f
			# shellcheck disable=SC2046 # the fields of ls are wanted
			set -- $(ls -ldn -- "$next")
			set +f
			[ "$3" = 0 ] || [ "$3" = "$euid" ] ||
				fail "another account can change $next (it is a link that belongs to another account); $use"
			link=$(readlink "$next") || fail "could not read the link $next"
			case $link in /*) folder=/ ;; esac
			rest=$link/$rest
			continue
		fi
		folder=$next
		reason=$(changeable "$folder" 0)
		[ -z "$reason" ] || fail "another account can change $folder ($reason); $use"
	done
	if [ "$holds" = 1 ]; then
		reason=$(changeable "$folder" 1)
		[ -z "$reason" ] || fail "another account can change $folder ($reason); $use"
	fi
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

	curl=$(system curl) || fail "curl is needed to download OwnGit"
	tar=$(system tar) || fail "tar is needed to unpack OwnGit"
	install=$(system install) || fail "install is needed to put OwnGit in place"
	mktemp=$(system mktemp) || fail "mktemp is needed for a private download folder"
	id=$(system id) || fail "id is needed to tell who runs the installer"
	if sha=$(system sha256sum); then
		digest_of() { "$sha" "$1" | cut -d ' ' -f 1; }
	elif sha=$(system shasum); then
		digest_of() { "$sha" -a 256 "$1" | cut -d ' ' -f 1; }
	else
		fail "sha256sum or shasum is needed to check the download"
	fi

	if [ -z "$target" ]; then
		if [ "$("$id" -u)" = 0 ]; then target=/usr/local/bin/owngit; else target=$HOME/.local/bin/owngit; fi
	fi
	case $target in /*) ;; *) target=$(pwd)/$target ;; esac
	[ ! -d "$target" ] || fail "--to names the program file, such as $target/owngit, not a folder"
	# A link belongs to whatever made it, such as npm's owngit in
	# /usr/local/bin, so the installer does not replace it with a program.
	[ ! -L "$target" ] ||
		fail "$target is a link to $(readlink "$target"), which another install may own; update that install its own way, or choose a regular file with --to"
	dir=$(dirname "$target")

	# Nobody but this account (and root) may be able to change the folders
	# the installer uses, or another account could swap the checked program
	# before it runs. A group counts as this account's own only when it is
	# its private group, named like the account.
	euid=$("$id" -u)
	own_group=none
	if [ "$euid" != 0 ] && [ "$("$id" -gn)" = "$("$id" -un)" ]; then own_group=$("$id" -g); fi
	existing=$dir
	while [ ! -d "$existing" ]; do existing=$(dirname "$existing"); done
	require_way "$existing" 1 "choose a folder that only you (or root) can change with --to"
	require_way "${TMPDIR:-/tmp}" 0 "set TMPDIR to a folder that only you can change"

	tmp=$("$mktemp" -d)
	trap 'rm -f "$tmp/SHA256SUMS" "$tmp/archive.tar.gz" "$tmp/owngit"; rmdir "$tmp"' EXIT
	fetch() {
		"$curl" --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL --retry 2 -o "$2" "$1" ||
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
	"$tar" -xzf "$tmp/archive.tar.gz" -C "$tmp" owngit || fail "could not unpack owngit from $name; nothing was changed"

	# sudo only when this account cannot write the folder (or the nearest
	# folder that exists, when it has to be made). A copy that sudo puts
	# there belongs to root, as a service that root installed requires.
	sudo=""
	if [ ! -w "$existing" ]; then
		sudo=$(system sudo) ||
			fail "this account cannot write $existing; run the installer as root, or choose a folder you can write with --to"
		say "This account cannot write $existing, so sudo puts owngit there."
	fi
	# A file that cannot be read counts as different and is replaced.
	if [ -f "$target" ] && [ "$(digest_of "$target" 2>/dev/null)" = "$(digest_of "$tmp/owngit")" ]; then
		say "OwnGit $version is already at $target."
	else
		# The new file takes the old one's place in one rename, so the
		# program at the path is always whole; a running OwnGit keeps
		# running the old file until it restarts.
		$sudo mkdir -p "$dir" || fail "could not create $dir"
		staged=$($sudo "$mktemp" "$dir/.owngit.XXXXXXXX") || fail "could not write in $dir"
		if ! $sudo "$install" -m 0755 "$tmp/owngit" "$staged" || ! $sudo mv -f "$staged" "$target"; then
			$sudo rm -f "$staged"
			fail "could not put owngit at $target"
		fi
		say "Installed OwnGit $version at $target."
	fi

	case ":$user_path:" in
	*":$dir:"*)
		found=$(PATH=$user_path && command -v owngit || true)
		[ "$found" = "$target" ] || [ -z "$found" ] || say "Note: \"owngit\" on your PATH is $found, not this one."
		;;
	*) say "$dir is not on your PATH, so run OwnGit as $(quote "$target"), or add $dir to PATH." ;;
	esac

	if [ "$service" = 0 ]; then
		say "Run it now with: $(quote "$target") serve"
		say "Or run it as a service that starts by itself: $(quote "$target") service install"
		return 0
	fi
	"$target" service install ||
		fail "owngit service install did not finish. OwnGit $version stays at $target; after fixing what it reported, run: $(quote "$target") service install"
}

main "$@"
