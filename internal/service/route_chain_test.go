package service

import (
	"path/filepath"
	"strings"
	"testing"
)

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

// PowerShell ends a single-quoted string at U+0027 and at U+2018 to U+201B,
// so every one of them is doubled inside the quotes.
func TestPowerShellQuote(t *testing.T) {
	for word, want := range map[string]string{
		`C:\Users\you\owngit.exe`:    `'C:\Users\you\owngit.exe'`,
		`C:\Data\O'Brien\owngit.exe`: `'C:\Data\O''Brien\owngit.exe'`,
		"C:\\Data\\O\u2019Brien":     "'C:\\Data\\O\u2019\u2019Brien'",
		"\u2018\u201a\u201b":         "'\u2018\u2018\u201a\u201a\u201b\u201b'",
		`C:\$(calc)\a;b&c` + "`x":    "'C:\\$(calc)\\a;b&c`x'",
		// A control or direction character never reaches the terminal raw.
		"C:\\a\u202eb\\\u201c$`\"": "\"C:\\a$([char]0x202E)b\\`\u201c`$```\"\"",
		"a\u0085b":                 "\"a$([char]0x0085)b\"",
	} {
		if got := PowerShellQuote(word); got != want {
			t.Errorf("%q: %q, want %q", word, got, want)
		}
	}
}

// Without a service, a Windows archive update puts the new program in a new
// folder, so the owner starts that one; every other route restarts in place.
func TestStartAfterUpdate(t *testing.T) {
	archive := Install{Route: RouteArchive, Executable: "C:/Users/you/owngit_1.1.2_windows_amd64/owngit.exe"}
	windows := Platform{GOOS: "windows", GOARCH: "amd64"}
	if got := archive.StartAfterUpdate("1.1.3", windows); !strings.HasSuffix(filepath.ToSlash(got), "owngit_1.1.3_windows_amd64/owngit.exe") {
		t.Errorf("windows archive: %q", got)
	}
	windows.Service = true
	if got := archive.StartAfterUpdate("1.1.3", windows); got != "" {
		t.Errorf("with a service: %q", got)
	}
	if got := archive.StartAfterUpdate("1.1.3", Platform{GOOS: "linux", GOARCH: "amd64"}); got != "" {
		t.Errorf("linux archive: %q", got)
	}
	if got := (Install{Route: RouteNPM}).StartAfterUpdate("1.1.3", Platform{GOOS: "windows", GOARCH: "amd64"}); got != "" {
		t.Errorf("npm: %q", got)
	}
}
