package recovery

import (
	"fmt"
	"time"

	"owngit/internal/state"
)

// notRestored is what a backup never carries: authority, and how the
// backed-up computer ran. A restore starts each at its default, so it says
// what the owner does to have it again. It is the one list of what a
// restore leaves out; RestoreNotes reports it after every restore.
var notRestored = []string{
	"Sign-ins and setup links are not restored: everyone signs in again.",
	"Network settings are not restored: the listen address, base URL, allowed Hosts, trusted proxies, the public share address, Tailscale Serve and the acknowledgement of plain HTTP start at their defaults. " +
		"Set them again under Settings or with owngit network set and owngit tailscale on. Behind a proxy, save the public address and the proxy again before you start OwnGit, with owngit network set --base-url <address> --trusted-proxy <proxy address>, or HTTPS pages and Git are refused until you save them and restart.",
	"Helper credentials are not restored, so their old tokens are refused: create new ones on each repository's Helper credentials page or with owngit helper-credential create.",
	"Runner tokens are not restored, so their old tokens are refused: issue new ones on each repository's Runner tokens page or with owngit runner-credential issue.",
	"Consent to run automatic checks is not restored, and unfinished check jobs are marked interrupted: " +
		"turn checks on again on each repository's Automatic checks tab or with owngit check-policy enable.",
	"Import credentials and each source's connection choices and limits, such as plain HTTP or redirects, are not restored: " +
		"store the credentials again and turn on what a source needs on each repository's Import tab or with owngit import credentials and owngit import configure. " +
		"A source follows an upstream deletion only after a refresh has seen the ref again.",
	"Import schedules are not restored: turn scheduled refreshes on again on each repository's Import tab or with owngit import schedule.",
	"Share links are not restored: create new ones on each repository's Share links page or with owngit repo share create.",
	"Scheduled backups and the backup history, the recorded manual and scheduled backups with their results, are not restored: " +
		"set the schedule up again under Settings or with owngit backup schedule set. " +
		"Earlier backups are no longer listed, but their folders stay where they were written, and owngit backup verify and owngit restore still take them.",
	"The backup before an upgrade is on, as on a new installation: if it was turned off, turn it off again with owngit upgrade-backup off.",
	"Raw check logs and the recent pushes list are not restored; check results come back without their raw logs.",
}

// RestoreNotes says what a finished restore did not bring back and what
// to do about each: notRestored, then the server-wide settings that start
// at their defaults.
func RestoreNotes() []string {
	return append(append([]string(nil), notRestored...), serverSettingsNotice())
}

// serverSettingsNotice names the server-wide settings that a restore
// starts at their defaults, as on a new installation, and where to set
// them again. A backup does not carry them, whether they were stricter or
// looser than the defaults. Sizes use the units of Settings, where 1 GB is
// 1024 MB.
func serverSettingsNotice() string {
	limits, login := state.DefaultGitTransferLimits, state.DefaultLoginLimits
	return fmt.Sprintf("Server-wide settings start at their defaults, as on a new installation: a sign-in with the shared password lasts %s, "+
		"new repositories start on %s, one Git transfer may move %d GB and take %s, raw check logs are kept %s, "+
		"repositories that follow the server keep overwritten and deleted history, deleting a repository asks for its name, "+
		"%d wrong passwords within %s pause an address for %s, and a link from another site opens without the shared sign-in. "+
		"Git transfer slots and waits, browsing limits, check ceilings and repository maintenance are at their defaults, and unused object cleanup is off. "+
		"Each repository's own kept history choice and default branch protection come back with it. "+
		"Set them again under Settings or with owngit settings set. The administrator password check and the new release check are also at their defaults; set them under Settings.",
		plainDuration(state.DefaultGeneralSession.Length()), state.DefaultInitialBranch, limits.MaximumBytes>>30,
		plainDuration(limits.Operation), plainDuration(state.DefaultCheckLogRetention.Duration()),
		login.Attempts, plainDuration(login.Window), plainDuration(login.Pause))
}

// plainDuration writes a whole number of days, hours or minutes in words.
func plainDuration(duration time.Duration) string {
	for _, unit := range []struct {
		size time.Duration
		name string
	}{{24 * time.Hour, "day"}, {time.Hour, "hour"}, {time.Minute, "minute"}} {
		if count := duration / unit.size; count == 1 && duration == unit.size {
			return "1 " + unit.name
		} else if count > 1 && duration%unit.size == 0 {
			return fmt.Sprintf("%d %ss", count, unit.name)
		}
	}
	return duration.String()
}
