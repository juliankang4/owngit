package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/service"
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
		Current, Latest, Route, Program, Command, Start string
		Newer                                           bool
	}
	noErr(t, json.Unmarshal([]byte(output), &answer))
	install, err := detectInstall()
	noErr(t, err)
	platform := updatePlatform(install, serviceState{})
	want, start := install.UpdateCommand("99.0.0", platform), install.StartAfterUpdate("99.0.0", platform)
	if answer.Current != version.Version || answer.Latest != "99.0.0" || !answer.Newer || answer.Route != string(install.Route) || answer.Command != want || answer.Start != start {
		t.Fatalf("answer %+v, want command %q and start %q", answer, want, start)
	}

	// Without a service the owner starts OwnGit again: in place, or, after
	// a Windows archive update, from the new folder.
	next := "Then restart OwnGit where it runs.\n"
	if start != "" {
		next = "Then start OwnGit from " + start + ".\n"
	}
	output, err = captureStdout(func() error { return run([]string{"update"}) })
	noErr(t, err)
	if !strings.HasPrefix(output, "OwnGit 99.0.0 is available (this is "+version.Version+")") || want != "" && !strings.Contains(output, "OwnGit does not run it for you:\n  "+want+"\n"+next) {
		t.Fatalf("output:\n%s", output)
	}

	for _, tc := range []struct {
		tag, message string
		older        bool
	}{
		{"v" + version.Version, "OwnGit " + version.Version + " is the latest release.\n", false},
		{"v0.0.1", "The update source reports OwnGit 0.0.1, older than this OwnGit " + version.Version + ". No downgrade is offered.\n", true},
	} {
		tag = tc.tag
		output, err = captureStdout(func() error { return run([]string{"update"}) })
		if err != nil || output != tc.message {
			t.Errorf("%s: %q, %v", tc.tag, output, err)
		}
		output, err = captureStdout(func() error { return run([]string{"update", "--json"}) })
		noErr(t, err)
		var result struct {
			Current, Reported, Latest, Command string
			Newer                              bool
			SourceOlder                        bool `json:"source_older"`
		}
		noErr(t, json.Unmarshal([]byte(output), &result))
		if result.Current != version.Version || result.Reported != tc.tag[1:] || result.SourceOlder != tc.older || result.Newer || result.Command != "" || tc.older && result.Latest != "" || !tc.older && result.Latest != version.Version {
			t.Errorf("%s: JSON result %+v", tc.tag, result)
		}
	}

	releaseCheckEndpoint = "http://127.0.0.1:0/owngit-tests-never-contact-github"
	if _, err := captureStdout(func() error { return run([]string{"update"}) }); err == nil || !strings.Contains(err.Error(), "could not ask GitHub") {
		t.Fatalf("failed check: %v", err)
	}
	for _, tc := range []struct {
		args []string
		code string
	}{
		{[]string{"--json", "--qa-unknown"}, "invalid_arguments"},
		{[]string{"unexpected", "--json"}, "invalid_arguments"},
		{[]string{"--json"}, "release_check_failed"},
	} {
		err := run(append([]string{"update"}, tc.args...))
		if errorCode(err) != tc.code {
			t.Errorf("update %v: error %v, want %s", tc.args, err, tc.code)
		}
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

// A pacman package other than the release's own gets no command and no
// guess; root gets none either, because makepkg refuses root.
func TestPacmanWithoutACommandSaysWhy(t *testing.T) {
	other := service.ClassifyExecutable("/usr/bin/owngit").OwnedBy("owngit-git")
	if got := noUpdateCommand(other, "9.9.9"); got != "Installed by the pacman package owngit-git; update it the way you installed it." {
		t.Errorf("other package: %q", got)
	}
	release := service.ClassifyExecutable("/usr/bin/owngit").OwnedBy(service.ReleasePackage)
	if got := noUpdateCommand(release, "9.9.9"); !strings.HasPrefix(got, "makepkg does not run as root.") {
		t.Errorf("root: %q", got)
	}
}
