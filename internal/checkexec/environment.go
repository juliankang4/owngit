package checkexec

import (
	"os"
	"runtime"
	"slices"
	"strings"

	"owngit/internal/checkapi"
)

var hostEnvironmentNames = []string{
	"PATH", "HOME", "LANG", "TZ",
	"LC_ALL", "LC_COLLATE", "LC_CTYPE", "LC_MESSAGES", "LC_MONETARY", "LC_NUMERIC", "LC_TIME",
}

var unixEnvironmentNames = []string{"USER", "LOGNAME", "SHELL"}

var windowsEnvironmentNames = []string{
	"SystemRoot", "SystemDrive", "windir", "ComSpec", "PATHEXT",
	"USERPROFILE", "APPDATA", "LOCALAPPDATA", "ProgramData", "ProgramFiles", "ProgramFiles(x86)", "ProgramW6432",
	"CommonProgramFiles", "CommonProgramFiles(x86)", "CommonProgramW6432", "HOMEDRIVE", "HOMEPATH", "USERNAME",
	"NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE", "PROCESSOR_ARCHITEW6432", "OS",
}

// HostEnvironment returns the defined host payload environment and failure guidance.
// The caller owns the writable temporary directory and its cleanup.
func HostEnvironment(temporary string) ([]string, string) {
	return hostEnvironment(temporary)
}

func hostEnvironment(temporary string) ([]string, string) {
	names := append([]string(nil), hostEnvironmentNames...)
	if runtime.GOOS == "windows" {
		names = append(names, windowsEnvironmentNames...)
	} else {
		names = append(names, unixEnvironmentNames...)
	}
	environment := make([]string, 0, len(names)+4)
	var omitted []string
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		allowed := slices.ContainsFunc(names, func(allowed string) bool {
			return name == allowed || runtime.GOOS == "windows" && strings.EqualFold(name, allowed)
		})
		ownedName := name
		if runtime.GOOS == "windows" {
			ownedName = strings.ToUpper(name)
		}
		if allowed {
			environment = append(environment, entry)
		} else if ownedName != "CI" && ownedName != "TMPDIR" && ownedName != "TEMP" && ownedName != "TMP" && safeEnvironmentName(name) {
			omitted = append(omitted, name)
		}
	}
	environment = append(environment, "CI=true", "TMPDIR="+temporary, "TEMP="+temporary, "TMP="+temporary)
	slices.Sort(omitted)
	omitted = slices.Compact(omitted)
	listed, clipped := checkapi.ClipText(strings.Join(omitted, ", "), 8<<10)
	if clipped {
		listed += " (additional names omitted)"
	}
	note := "[OwnGit did not pass parent environment variables"
	if listed != "" {
		note += ": " + listed
	}
	return environment, note + ". Set variables this command needs in the check command itself.]\n"
}

func safeEnvironmentName(name string) bool {
	upper := strings.ToUpper(name)
	for _, sensitive := range []string{"TOKEN", "SECRET", "PASSWORD", "KEY", "CREDENTIAL"} {
		if strings.Contains(upper, sensitive) {
			return false
		}
	}
	for index, char := range name {
		if char != '_' && (char < 'A' || char > 'Z') && (char < 'a' || char > 'z') && (index == 0 || char < '0' || char > '9') {
			return false
		}
	}
	return name != ""
}
