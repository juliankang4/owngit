package testfixture

import (
	"strconv"
	"strings"
)

// gitClientSettings turn off Git's automatic maintenance. Without them a
// commit, fetch, merge, rebase or am can start "git maintenance run --auto"
// in the background. Its repack removes empty object directories while the
// next command writes an object there, which fails with "unable to create
// temporary file", and the detached process can outlive the test.
var gitClientSettings = [][2]string{
	{"maintenance.auto", "false"},
	{"gc.auto", "0"},
}

// GitEnvironment returns env with Git's automatic maintenance turned off, for
// every Git command a test runs itself. The settings are added as
// GIT_CONFIG_COUNT entries after any env already has, so they apply whatever
// configuration files the command reads. env is not modified; pass
// os.Environ() for a command that would otherwise inherit the environment.
func GitEnvironment(env []string) []string {
	count := 0
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "GIT_CONFIG_COUNT="); ok {
			// The last entry wins, as for the command itself.
			count, _ = strconv.Atoi(value)
		}
	}
	result := make([]string, 0, len(env)+2*len(gitClientSettings)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, "GIT_CONFIG_COUNT=") {
			result = append(result, entry)
		}
	}
	for _, setting := range gitClientSettings {
		index := strconv.Itoa(count)
		result = append(result, "GIT_CONFIG_KEY_"+index+"="+setting[0], "GIT_CONFIG_VALUE_"+index+"="+setting[1])
		count++
	}
	return append(result, "GIT_CONFIG_COUNT="+strconv.Itoa(count))
}
