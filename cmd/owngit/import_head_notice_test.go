package main

import (
	"errors"
	"strings"
	"testing"
)

func TestImportRunReportsUnconfirmedHEADAsDivergence(t *testing.T) {
	result := `{"ok":true,"run":{"status":"complete","refs_divergent":1},"status":{"refs":[{"name":"refs/heads/main","state":"tracked"},{"name":"refs/heads/release","state":"tracked"}],"refs_truncated":false}}`
	output, err := captureStdout(func() error { return printImportRun("project", []byte(result), false) })
	var exit *checkExit
	if !errors.As(err, &exit) || exit.code != importDivergedExit {
		t.Fatalf("HEAD divergence did not retain its exit status: %v", err)
	}
	for _, notice := range []string{
		"1 ref differs from the source and was left unchanged here:",
		"1 more not listed by name, such as HEAD or a name that differs only by case.",
	} {
		if !strings.Contains(output, notice) {
			t.Errorf("HEAD preservation notice missing %q: %s", notice, output)
		}
	}
}
