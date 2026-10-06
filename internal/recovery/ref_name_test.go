package recovery

import "testing"

func TestBackupRefNamesFollowGit(t *testing.T) {
	for name, legal := range map[string]bool{"refs/heads/a./b": true, "refs/heads/.hidden": false, "refs/heads/a\tb": false, "refs/heads//a": false, "refs/heads/a.lock/b": false} {
		if got := validRefName(name); got != legal {
			t.Errorf("validRefName(%q) = %v, want %v", name, got, legal)
		}
	}
}
