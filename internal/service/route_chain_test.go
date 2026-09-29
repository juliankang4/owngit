package service

import "testing"

// A Windows command stops at the first failed step, as && does in a POSIX
// shell: each step runs only inside the "if ($?)" of the one before it.
func TestWindowsCommandStopsAtTheFirstFailure(t *testing.T) {
	npm := Install{Route: RouteNPM, Executable: "owngit.exe"}
	platform := Platform{GOOS: "windows", GOARCH: "amd64", Service: true}
	if got, want := npm.UpdateCommand("1.1.3", platform), "npm install -g owngit@1.1.3; if ($?) { owngit service install }"; got != want {
		t.Errorf("boot task:\n got %s\nwant %s", got, want)
	}
	platform.ServiceRunsFile = true
	if got, want := npm.UpdateCommand("1.1.3", platform), "owngit service stop; if ($?) { npm install -g owngit@1.1.3; if ($?) { owngit service install } }"; got != want {
		t.Errorf("sign-in task:\n got %s\nwant %s", got, want)
	}
	platform.Service = false
	if got := npm.UpdateCommand("1.1.3", platform); got != "npm install -g owngit@1.1.3" {
		t.Errorf("no service: %s", got)
	}
}
