package service

import "testing"

// On Windows a running program cannot be replaced: an archive update unpacks
// the new release into its own folder beside the current one, and npm stops
// a sign-in task that runs its file first.
func TestUpdateCommandOnWindows(t *testing.T) {
	archive := ClassifyExecutable(`C:\Users\you\Downloads\owngit_1.1.2_windows_amd64\owngit.exe`)
	npm := ClassifyExecutable(`C:\Users\you\AppData\Roaming\npm\node_modules\owngit\node_modules\owngit-win32-x64\bin\owngit.exe`)
	if archive.Route != RouteArchive || npm.Route != RouteNPM {
		t.Fatalf("routes %s and %s", archive.Route, npm.Route)
	}
	platform := Platform{GOOS: "windows", GOARCH: "amd64", Service: true}
	folder := `C:\Users\you\Downloads\owngit_1.1.3_windows_amd64`
	want := "Invoke-WebRequest 'https://github.com/juliankang4/owngit/releases/download/v1.1.3/owngit_1.1.3_windows_amd64.zip' -OutFile '" + folder + ".zip'; " +
		"Expand-Archive '" + folder + ".zip' '" + folder + "'; & '" + folder + `\owngit.exe' service install`
	if got := archive.UpdateCommand("1.1.3", platform); got != want {
		t.Errorf("archive:\n got %s\nwant %s", got, want)
	}
	if got := npm.UpdateCommand("1.1.3", platform); got != "npm install -g owngit@1.1.3; owngit service install" {
		t.Errorf("npm with a boot task: %s", got)
	}
	platform.ServiceRunsFile = true
	if got := npm.UpdateCommand("1.1.3", platform); got != "owngit service stop; npm install -g owngit@1.1.3; owngit service install" {
		t.Errorf("npm with a sign-in task: %s", got)
	}
	if got := archive.RemoveCommand("windows", false); got != `Remove-Item 'C:\Users\you\Downloads\owngit_1.1.2_windows_amd64\owngit.exe'` {
		t.Errorf("remove: %s", got)
	}
}
