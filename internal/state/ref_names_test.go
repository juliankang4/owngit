package state

import "testing"

// Every ref name check in the state owners accepts a legal name such as
// "a./b" and refuses an illegal one.
func TestRefNameOwnersFollowGit(t *testing.T) {
	for name, legal := range map[string]bool{"a./b": true, "a/b.c": true, "a/.b": false, "a.lock/b": false, "a/b.": false} {
		if got := validBranchText(name); got != legal {
			t.Errorf("validBranchText(%q) = %v, want %v", name, got, legal)
		}
		if got := ValidateInitialBranch(name) == nil; got != legal {
			t.Errorf("ValidateInitialBranch(%q) = %v, want %v", name, got, legal)
		}
		if got := validImportRefName("refs/heads/" + name); got != legal {
			t.Errorf("validImportRefName(refs/heads/%s) = %v, want %v", name, got, legal)
		}
	}
}
