#!/bin/sh
# Prepares the build context of the OwnGit container image from release
# archives: checks the two Linux archives against the release's SHA256SUMS
# and unpacks each into its own folder beside the Dockerfile's inputs.
#
#   packaging/container/context.sh RELEASE_DIR CONTEXT_DIR
#
# RELEASE_DIR holds SHA256SUMS and the archives owngit_VERSION_linux_amd64.tar.gz
# and owngit_VERSION_linux_arm64.tar.gz, as "tools/release build" writes them
# or a GitHub Release carries them. CONTEXT_DIR must not exist yet. Then:
#
#   docker buildx build --platform linux/amd64,linux/arm64 \
#     --build-arg VERSION=... --build-arg REVISION=... \
#     -f packaging/container/Dockerfile CONTEXT_DIR
set -eu

main() {
	if [ "$#" -ne 2 ]; then
		echo "usage: context.sh RELEASE_DIR CONTEXT_DIR" >&2
		exit 2
	fi
	release=$1
	context=$2
	if [ -e "$context" ]; then
		echo "context.sh: $context already exists" >&2
		exit 1
	fi
	sums="$release/SHA256SUMS"
	mkdir "$context"
	for arch in amd64 arm64; do
		# Exactly one archive of this architecture must be listed.
		lines=$(grep -E "^[0-9a-f]{64}  owngit_[0-9]+\.[0-9]+\.[0-9]+_linux_${arch}\.tar\.gz\$" "$sums" || true)
		if [ "$(printf '%s\n' "$lines" | grep -c .)" -ne 1 ]; then
			echo "context.sh: $sums does not list exactly one linux/$arch archive" >&2
			exit 1
		fi
		name=${lines#*  }
		(cd "$release" && printf '%s\n' "$lines" | sha256sum --check --strict --quiet)
		mkdir "$context/linux_$arch"
		tar -xzf "$release/$name" -C "$context/linux_$arch" owngit LICENSE THIRD_PARTY_NOTICES
		echo "unpacked $name into $context/linux_$arch"
	done
}

main "$@"
