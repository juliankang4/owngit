package version

import (
	"regexp"
	"testing"
)

// The test must not repeat the current value, so advancing the version only
// changes the single literal in version.go.
func TestVersionIsASemanticLiteral(t *testing.T) {
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(Version) {
		t.Fatalf("version %q is not a semantic version", Version)
	}
}
