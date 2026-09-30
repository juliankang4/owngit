package recovery

import (
	"strings"
	"testing"
)

// The notes after a restore name every kind of thing a backup does not
// carry.
func TestRestoreNotesNameWhatABackupLeavesOut(t *testing.T) {
	notes := strings.Join(RestoreNotes(), "\n")
	for _, kind := range []string{
		"Sign-ins and setup links", "Network settings", "Helper credentials", "Runner tokens", "Consent to run automatic checks",
		"Import credentials", "connection choices", "Import schedules", "Share links", "Scheduled backups", "backup history", "backup before an upgrade", "acknowledgement of plain HTTP", "Raw check logs",
		"recent pushes", "Server-wide settings",
	} {
		if !strings.Contains(notes, kind) {
			t.Errorf("restore notes do not name %q:\n%s", kind, notes)
		}
	}
}
