package checkworkflow

import "testing"

func TestBranchPatternFollowsGitRefNames(t *testing.T) {
	for pattern, legal := range map[string]bool{"a./b": true, "a./*": true, "a.*": true, "*": true, "a/.b": false, "a/.*": false, "a.lock/b": false, "a/b.": false, "a..*": false} {
		if got := validateBranchPattern(pattern) == nil; got != legal {
			t.Errorf("validateBranchPattern(%q) = %v, want %v", pattern, got, legal)
		}
	}
}
