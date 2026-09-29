#!/bin/sh
# OwnGit in a container on Proxmox VE.
#
#   /usr/bin/curl --proto '=https' --proto-redir '=https' -fsSL https://owngit.app/proxmox.sh | /bin/sh
#   /usr/bin/curl --proto '=https' --proto-redir '=https' -fsSL https://owngit.app/proxmox.sh | /bin/sh -s -- --repositories /tank/owngit
#
# Run as root on a Proxmox VE host. It creates an unprivileged Debian 13
# container, runs the release's install.sh inside it, which checks the
# archive against SHA256SUMS and installs the service as the installer does
# everywhere, and can give OwnGit a folder of this host for the
# repositories. Nothing from the OwnGit release runs on the host itself.
# It never changes an existing container: an ID or name in use stops it,
# and for OwnGit's own container it says how to update instead.
# OWNGIT_RELEASES replaces the release address (https only), for a mirror.
#
# Everything runs from main, called on the last line, so a shell that reads
# this script from a pipe has read all of it before anything runs.

set -eu

PATH=/usr/sbin:/usr/bin:/sbin:/bin
export PATH

# The container's repository folder is the one OwnGit's setup suggests to
# the owngit account, whose home is /var/lib/owngit.
home=/var/lib/owngit
inner=$home/OwnGit-Repositories
tag=owngit
# install.sh puts the program here for root. pct exec runs commands with
# a PATH that does not include /usr/local/bin, so commands name it whole.
program=/usr/local/bin/owngit

say() { printf '%s\n' "$*"; }
fail() {
	printf 'owngit proxmox: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<'EOF'
Usage: proxmox.sh [options]

Creates an unprivileged Debian 13 container with OwnGit on this Proxmox VE host.

  --id N                 container ID (default: the next free ID)
  --hostname NAME        container name (default owngit)
  --storage NAME         storage of the container's disk (default local-lvm or local-zfs)
  --disk GB              disk size (default 8)
  --cores N              CPU cores (default 2)
  --memory MB            memory (default 1024)
  --bridge NAME          network bridge (default vmbr0)
  --ip ADDRESS/PREFIX    fixed IPv4 address, such as 192.168.1.50/24 (default: DHCP)
  --gateway ADDRESS      IPv4 gateway, needed with --ip
  --repositories FOLDER  keep the repositories in FOLDER on this host, which
                         must be new or empty and whose parent must exist
                         (default: inside the container)
  --template VOLUME      a Debian 13 template you have, such as
                         local:vztmpl/debian-13-standard_13.6-1_amd64.tar.zst
                         (default: the newest debian-13-standard)
  --version X.Y.Z        install this OwnGit release (default: the latest)
EOF
}

# matches reports whether a whole value matches an extended regular
# expression.
matches() { printf '%s\n' "$1" | grep -Eqx -- "$2"; }

# config_value prints the value of KEY ($2) in the container config file
# $1, from its current settings only, not its snapshots.
config_value() {
	awk -v key="$2:" '/^\[/ { exit } $1 == key { sub(/^[^:]*:[ \t]*/, ""); print; exit }' "$1"
}

# is_owngit reports whether the container config file $1 has OwnGit's tag.
is_owngit() {
	config_value "$1" tags | awk -v tag="$tag" '{ n = split($0, words, /[;, ]+/); for (i = 1; i <= n; i++) if (words[i] == tag) found = 1 } END { exit !found }'
}

# already_there explains how to update OwnGit's existing container $1 and
# ends the run.
already_there() {
	say "OwnGit's container $1 ($(config_value "$2" hostname)) already exists, so nothing was changed."
	# /etc/pve/local holds the containers of this node.
	[ -f "/etc/pve/local/lxc/$1.conf" ] ||
		say "It is on the node $(basename "$(dirname "$(dirname "$2")")"); run the commands below there."
	say "To update OwnGit in it, run \"pct exec $1 -- $program update\", which prints the update command,"
	say "and run that command in the container's shell, which \"pct enter $1\" opens."
	say "If the container is stopped, start it first with \"pct start $1\"."
	exit 0
}

# only_root reports whether folder $1 belongs to root and no group or
# other account can write it.
only_root() {
	# shellcheck disable=SC2046 # the owner and mode are two words
	set -- $(stat -c '%u %a' "$1")
	[ "$1" = 0 ] || return 1
	case $2 in *[2367]? | *[2367]) return 1 ;; esac
}

# check_folder refuses a repository folder that another account could
# redirect or fill: the folder and every folder on the way must be real
# folders that belong to root and that only root can change, and the
# folder must be missing or empty. A folder that exists gets a new owner,
# the container's owngit account, so nothing in it may be there yet. It
# runs before any work and again just before the folder changes owner.
check_folder() {
	only_root / || fail "another account can change /, and so redirect the repository folder"
	current="" rest=${1#/}
	while [ -n "$rest" ]; do
		current=$current/${rest%%/*}
		case $rest in */*) rest=${rest#*/} ;; *) rest="" ;; esac
		[ ! -L "$current" ] || fail "$current is a link; name the folder it leads to"
		if [ ! -e "$current" ]; then
			# Only the folder itself is created, so that a failed run can
			# remove everything it made.
			[ -z "$rest" ] || fail "$current does not exist; create the folders above the repository folder first"
			return 0
		fi
		[ -d "$current" ] || fail "$current is not a folder"
		only_root "$current" && continue
		[ -z "$rest" ] ||
			fail "another account can change $current, and so redirect the repository folder; choose a folder inside folders that only root can change"
		fail "another account owns or can write $current, and could put files in it before the container gets it; let only root change it with \"chown root:root $current && chmod go-w $current\", or choose a new folder"
	done
	[ -z "$(ls -A "$current")" ] ||
		fail "$current is not empty, and its content would get the container's owner; choose a new or empty folder, and bring repositories in through OwnGit (import or restore) after setup"
}

main() {
	ctid="" hostname=owngit storage="" disk=8 cores=2 memory=1024 bridge=vmbr0 ip=dhcp gateway="" folder="" template="" version=""
	while [ $# -gt 0 ]; do
		case $1 in
		-h | --help) usage && return 0 ;;
		--*=*)
			option=${1%%=*} value=${1#*=}
			shift
			set -- "$option" "$value" "$@"
			;;
		esac
		case $1 in
		--id | --hostname | --storage | --disk | --cores | --memory | --bridge | --ip | --gateway | --repositories | --template | --version)
			[ $# -ge 2 ] && [ -n "$2" ] || fail "$1 needs a value (see --help)"
			case $1 in
			--id) ctid=$2 ;;
			--hostname) hostname=$2 ;;
			--storage) storage=$2 ;;
			--disk) disk=$2 ;;
			--cores) cores=$2 ;;
			--memory) memory=$2 ;;
			--bridge) bridge=$2 ;;
			--ip) ip=$2 ;;
			--gateway) gateway=$2 ;;
			--repositories) folder=$2 ;;
			--template) template=$2 ;;
			--version) version=$2 ;;
			esac
			shift 2
			;;
		*) fail "unknown option $1 (see --help)" ;;
		esac
	done
	# Every value is checked here, so none can add an option to the
	# comma-separated settings that pct takes.
	[ -z "$ctid" ] || matches "$ctid" '[1-9][0-9]{2,8}' || fail "--id takes a number from 100, not $ctid"
	matches "$hostname" '[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?' || fail "--hostname takes letters, digits and inner hyphens, not $hostname"
	[ -z "$storage" ] || matches "$storage" '[A-Za-z][A-Za-z0-9._-]*' || fail "--storage takes a storage name, not $storage"
	for pair in "disk:$disk" "cores:$cores" "memory:$memory"; do
		matches "${pair#*:}" '[1-9][0-9]{0,6}' || fail "--${pair%%:*} takes a whole number, not ${pair#*:}"
	done
	matches "$bridge" '[A-Za-z0-9][A-Za-z0-9._-]{0,14}' || fail "--bridge takes a bridge name, not $bridge"
	octet='(25[0-5]|2[0-4][0-9]|1?[0-9]?[0-9])'
	address="$octet\\.$octet\\.$octet\\.$octet"
	if [ "$ip" = dhcp ]; then
		[ -z "$gateway" ] || fail "--gateway goes with --ip"
	else
		matches "$ip" "$address/([1-9]|[12][0-9]|3[0-2])" || fail "--ip takes an IPv4 address with its prefix, such as 192.168.1.50/24, not $ip"
		[ -n "$gateway" ] || fail "--ip needs --gateway, since the container has no route out without one"
		matches "$gateway" "$address" || fail "--gateway takes an IPv4 address, not $gateway"
	fi
	[ -z "$folder" ] || matches "$folder" '(/[A-Za-z0-9_+@-][A-Za-z0-9._+@-]*)+' ||
		fail "--repositories takes an absolute path of letters, digits and ._+@- whose parts do not start with a dot, such as /tank/owngit, not $folder"
	[ -z "$template" ] || matches "$template" '[A-Za-z][A-Za-z0-9._-]*:vztmpl/[A-Za-z0-9._+-]+' ||
		fail "--template takes a template volume such as local:vztmpl/debian-13-standard_13.6-1_amd64.tar.zst, not $template"
	if [ -n "$version" ]; then
		matches "$version" 'v?[0-9]+\.[0-9]+\.[0-9]+' || fail "--version takes a release number such as 1.1.3, not $version"
		version=${version#v}
	fi
	releases=${OWNGIT_RELEASES:-https://github.com/juliankang4/owngit/releases}
	matches "$releases" 'https://[^[:space:]]+' || fail "OWNGIT_RELEASES must be an https:// address, not $releases"

	[ "$(id -u)" = 0 ] || fail "run this as root on the Proxmox VE host"
	for tool in pct pveam pvesh pvesm lxc-info; do
		command -v "$tool" >/dev/null || fail "$tool is missing; run this on a Proxmox VE host"
	done
	[ -d "/sys/class/net/$bridge/bridge" ] || fail "this host has no bridge $bridge; name one with --bridge"

	# /etc/pve holds the settings of every container in the cluster.
	for config in /etc/pve/nodes/*/lxc/*.conf; do
		[ -f "$config" ] || continue
		[ "$(config_value "$config" hostname)" = "$hostname" ] || continue
		existing=$(basename "$config" .conf)
		is_owngit "$config" && already_there "$existing" "$config"
		fail "container $existing is already named $hostname; choose another name with --hostname"
	done
	if [ -n "$ctid" ]; then
		for config in /etc/pve/nodes/*/lxc/"$ctid".conf; do
			[ -f "$config" ] && is_owngit "$config" && already_there "$ctid" "$config"
		done
		pvesh get /cluster/nextid --vmid "$ctid" >/dev/null 2>&1 ||
			fail "ID $ctid is in use by another container or virtual machine; choose another with --id, or leave it out for the next free one"
	fi

	storages=$(pvesm status --content rootdir --enabled 1) || fail "could not list the storages with pvesm status"
	storages=$(printf '%s\n' "$storages" | awk 'NR > 1 { printf " %s", $1 }')
	if [ -z "$storage" ]; then
		for name in local-lvm local-zfs; do
			case "$storages " in *" $name "*) storage=$name && break ;; esac
		done
		[ -n "$storage" ] || fail "neither local-lvm nor local-zfs can hold container disks here; name one of these with --storage:$storages"
	else
		case "$storages " in *" $storage "*) ;; *) fail "storage $storage cannot hold container disks here; choose one of:$storages" ;; esac
	fi
	[ -z "$folder" ] || check_folder "$folder"

	# The standard template's download is checked by pveam against
	# Proxmox's signed template list.
	if [ -n "$template" ]; then
		[ -f "$(pvesm path "$template" 2>/dev/null)" ] || fail "there is no template $template"
	else
		arch=$(dpkg --print-architecture)
		pattern="debian-13-standard_[0-9][^_]*_${arch}\\.tar\\.zst"
		list=$(pveam list local) || fail "could not list the templates of the storage local with pveam list; name a template with --template"
		name=$(printf '%s\n' "$list" | awk '{ sub(/^local:vztmpl\//, "", $1); print $1 }' | grep -Ex "$pattern" | sort -V | tail -n 1 || true)
		if [ -z "$name" ]; then
			say "Downloading the Debian 13 template."
			pveam update >/dev/null || fail "could not update the template list; nothing was changed"
			list=$(pveam available --section system) || fail "could not read the template list; nothing was changed"
			name=$(printf '%s\n' "$list" | awk '{ print $2 }' | grep -Ex "$pattern" | sort -V | tail -n 1 || true)
			[ -n "$name" ] || fail "Proxmox lists no Debian 13 template for $arch; name one with --template"
			pveam download local "$name" || fail "could not download $name; nothing was changed"
		fi
		template=local:vztmpl/$name
	fi

	[ -n "$ctid" ] || ctid=$(pvesh get /cluster/nextid) || fail "could not get a free container ID"
	network="name=eth0,bridge=$bridge,ip=$ip"
	[ -z "$gateway" ] || network="$network,gw=$gateway"

	# From here on a failure removes the container this run created and
	# puts the repository folder back as it was. Neither holds anything of
	# the owner's yet.
	created="" made="" before=""
	trap 'undo $?' EXIT
	trap 'exit 1' HUP INT PIPE TERM
	say "Creating container $ctid ($hostname) from $template."
	pct create "$ctid" "$template" --unprivileged 1 --features nesting=1 --ostype debian \
		--hostname "$hostname" --tags "$tag" --onboot 1 \
		--cores "$cores" --memory "$memory" --swap 512 --rootfs "$storage:$disk" --net0 "$network" \
		--description "OwnGit. To update it: pct exec $ctid -- $program update" >/dev/null ||
		fail "could not create container $ctid"
	created=$ctid

	start_and_wait
	say "Installing Git and curl."
	inside apt-get update -q >/dev/null || fail "apt-get update failed in the container; check its network"
	inside apt-get install -y -q --no-install-recommends ca-certificates curl git >/dev/null ||
		fail "could not install Git and curl in the container"
	inside apt-get upgrade -y -q >/dev/null || fail "could not update the container's packages"

	# install.sh comes from the same release as the archive it checks and
	# runs only inside the container. Its output goes to a file first, so
	# that pct gives it no terminal and the setup link is shown once, at
	# the end.
	if [ -n "$version" ]; then script=$releases/download/v$version/install.sh; else script=$releases/latest/download/install.sh; fi
	say "Running $script in the container."
	# shellcheck disable=SC2016 # expanded by the container's shell
	inside env "OWNGIT_RELEASES=$releases" /bin/sh -c '
		url=$1
		shift
		file=$(mktemp)
		/usr/bin/curl --proto "=https" --proto-redir "=https" --tlsv1.2 -fsSL --retry 2 -o "$file" "$url" &&
			/bin/sh "$file" "$@" >"$file.log" 2>&1
		status=$?
		[ ! -f "$file.log" ] || cat "$file.log"
		rm -f "$file" "$file.log"
		exit $status' sh "$script" ${version:+--version "$version"} ||
		fail "the OwnGit installer did not finish in container $ctid"

	if [ -n "$folder" ]; then
		# The folder gets the owner that the container's owngit account
		# has on this host, which the unprivileged ID mapping decides.
		pid=$(lxc-info -n "$ctid" -p -H) || fail "could not find container $ctid's processes"
		owner=$(stat -c '%u:%g' "/proc/$pid/root$home") || fail "could not read the owner of $home in the container"
		inside install -d -o owngit -g owngit -m 0700 "$inner" || fail "could not create $inner in the container"
		say "Stopping the container to add $folder."
		pct shutdown "$ctid" --timeout 120 || fail "could not stop container $ctid"
		# The install took minutes; check the folder again right before it
		# changes owner and goes into the container.
		check_folder "$folder"
		if [ -d "$folder" ]; then
			before=$(stat -c '%u:%g %a' "$folder")
		else
			made=$folder
		fi
		install -d -m 0700 -o "${owner%:*}" -g "${owner#*:}" "$folder" || fail "could not prepare $folder"
		pct set "$ctid" --mp0 "$folder,mp=$inner,backup=0" || fail "could not add $folder to container $ctid"
		start_and_wait
	fi

	tries=0
	until inside "$program" health >/dev/null 2>&1; do
		tries=$((tries + 1))
		[ "$tries" -lt 60 ] || fail "OwnGit does not answer in container $ctid; see: pct exec $ctid -- journalctl -u owngit.service"
		sleep 1
	done
	created="" made="" before=""

	addresses=$(inside hostname -I | awk '{ for (i = 1; i <= NF; i++) if ($i !~ /:/) printf "%s%s", (n++ ? ", " : ""), $i }' || true)
	say ""
	say "OwnGit runs in container $ctid ($hostname) at ${addresses:-an address that the container did not report}, port 7654."
	say "It starts with the host, and the container runs it as a service."
	if [ -n "$folder" ]; then
		say "Repositories: keep the suggested folder $inner in setup. It is $folder on this host."
	fi
	if [ -t 1 ]; then
		# pct gives the command a terminal when this output is one, and
		# the setup link is shown only on a terminal.
		inside "$program" setup-link || say "To see the setup link, run: pct exec $ctid -- $program setup-link"
	else
		say "To see the setup link, run in a terminal on this host: pct exec $ctid -- $program setup-link"
	fi
	say "Other commands work the same way, such as: pct exec $ctid -- $program service status"
}

# inside runs a command in the new container as root, with a clean
# environment and no input, so that it cannot read the rest of a script
# that comes through a pipe.
inside() {
	pct exec "$ctid" -- /usr/bin/env -i PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
		HOME=/root LANG=C.UTF-8 DEBIAN_FRONTEND=noninteractive "$@" </dev/null
}

# start_and_wait starts the new container and waits until systemd has
# booted it and it has a network route.
start_and_wait() {
	pct start "$ctid" || fail "could not start container $ctid"
	inside timeout 120 systemctl is-system-running --wait >/dev/null 2>&1 || true
	tries=0
	until inside ip route show default 2>/dev/null | grep -q .; do
		tries=$((tries + 1))
		[ "$tries" -lt 60 ] || fail "container $ctid got no network route; check --bridge, --ip and --gateway"
		sleep 1
	done
}

# undo removes what a run that ends with status $1 other than 0 created:
# the container, and the repository folder when this run made it, or else
# the folder's old owner and mode.
undo() {
	[ "$1" != 0 ] || return 0
	# The run may end because its terminal or reader went away. Then a
	# message fails, and a message or another signal must not stop the
	# removal, which inherits the ignored signals.
	set +e
	trap '' HUP INT PIPE TERM
	if [ -n "$created" ]; then
		say "Removing container $created, which this run created."
		pct stop "$created" >/dev/null 2>&1 || true
		pct destroy "$created" >/dev/null || say "Could not remove container $created; remove it with: pct destroy $created"
	fi
	if [ -n "$made" ]; then
		rmdir "$made" 2>/dev/null || say "$made stays, since it is not empty."
	elif [ -n "$before" ]; then
		if ! chown "${before% *}" "$folder" || ! chmod "${before#* }" "$folder"; then
			say "Could not give $folder back its owner ${before% *} and mode ${before#* }."
		fi
	fi
}

main "$@"
