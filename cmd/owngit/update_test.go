package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/version"
)

// "owngit update" reports the newer release and the command for this
// install, and runs nothing; a failed check is an error, not "up to date".
func TestUpdateCommandPrintsTheCommandAndRunsNothing(t *testing.T) {
	tag := "v99.0.0"
	endpoint := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"tag_name":"` + tag + `","draft":false,"prerelease":false}`))
	}))
	defer endpoint.Close()
	previous := releaseCheckEndpoint
	releaseCheckEndpoint = endpoint.URL
	defer func() { releaseCheckEndpoint = previous }()

	output, err := captureStdout(func() error { return run([]string{"update", "--json"}) })
	noErr(t, err)
	var answer struct {
		Current, Latest, Route, Program, Command string
		Newer                                    bool
	}
	noErr(t, json.Unmarshal([]byte(output), &answer))
	install := detectInstall()
	want := install.UpdateCommand("99.0.0", updatePlatform(install, serviceState{}))
	if answer.Current != version.Version || answer.Latest != "99.0.0" || !answer.Newer || answer.Route != string(install.Route) || answer.Command != want {
		t.Fatalf("answer %+v, want command %q", answer, want)
	}

	output, err = captureStdout(func() error { return run([]string{"update"}) })
	noErr(t, err)
	if !strings.HasPrefix(output, "OwnGit 99.0.0 is available (this is "+version.Version+")") || want != "" && !strings.Contains(output, "OwnGit does not run it for you:\n  "+want+"\nThen restart OwnGit where it runs.\n") {
		t.Fatalf("output:\n%s", output)
	}

	tag = "v" + version.Version
	output, err = captureStdout(func() error { return run([]string{"update"}) })
	if err != nil || output != "OwnGit "+version.Version+" is the latest release.\n" {
		t.Fatalf("up to date: %q, %v", output, err)
	}

	releaseCheckEndpoint = "http://127.0.0.1:0/owngit-tests-never-contact-github"
	if _, err := captureStdout(func() error { return run([]string{"update"}) }); err == nil || !strings.Contains(err.Error(), "could not ask GitHub") {
		t.Fatalf("failed check: %v", err)
	}
}

// Uninstall is never run bare in tests; its help runs nothing, and an
// unknown option is refused before anything changes.
func TestUninstallHelpAndOptionRefusal(t *testing.T) {
	for _, flag := range []string{"-h", "--help"} {
		output, err := captureStdout(func() error { return run([]string{"uninstall", flag}) })
		if err != nil || !strings.HasPrefix(output, "Usage: owngit uninstall") || !strings.Contains(output, "takes no options") {
			t.Errorf("uninstall %s = %q, %v", flag, output, err)
		}
	}
	for _, arguments := range [][]string{{"uninstall", "--no-such-flag"}, {"uninstall", "now"}} {
		if _, err := captureStdout(func() error { return run(arguments) }); err == nil {
			t.Errorf("%q succeeded", arguments)
		}
	}
}

// An uninstall that found no service still says where the data is, and says
// nothing about a state directory that does not exist.
func TestDataStaysLine(t *testing.T) {
	if line := dataStaysLine(filepath.Join(t.TempDir(), "none"), ""); line != "" {
		t.Errorf("missing state: %q", line)
	}
	stateDir := initializedState(t, true)
	repositories := savedRepositoryRoot(stateDir)
	if repositories == "" {
		t.Fatal("no repository folder saved")
	}
	if line := dataStaysLine(stateDir, repositories); line != "The state stays in "+stateDir+" and the repositories in "+repositories+"." {
		t.Errorf("line %q", line)
	}
}
