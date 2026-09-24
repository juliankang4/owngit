package main

import (
	"strings"
	"testing"
)

// helpCommands lists every command path. Leaves parse flags and must print
// their options; groups print the actions they accept.
var helpCommands = []struct {
	path string
	leaf bool
}{
	{"serve", true}, {"setup-link", true}, {"reset-admin", true}, {"approve-host", true},
	{"forget-check-container", true}, {"backup", true}, {"restore", true},
	{"pr", false}, {"pr create", true}, {"pr list", true}, {"pr show", true},
	{"pr review", false}, {"pr review request", true}, {"pr review submit", true}, {"pr review skip", true},
	{"pr merge", true},
	{"check", false}, {"check task", false}, {"check task new", true},
	{"check cycle", false}, {"check cycle reserve", true}, {"check cycle list", true},
	{"check run", true}, {"check status", true}, {"check log", true},
	{"check config", false}, {"check config show", true},
	{"helper-credential", false}, {"helper-credential create", true},
	{"helper-credential list", true}, {"helper-credential revoke", true},
	{"check-policy", false}, {"check-policy show", true}, {"check-policy set", true},
	{"check-policy enable", true}, {"check-policy disable", true},
	{"check-job", false}, {"check-job list", true}, {"check-job show", true}, {"check-job log", true},
	{"check-job cancel", true}, {"check-job rerun", true},
	{"runner-credential", false}, {"runner-credential issue", true},
	{"runner-credential list", true}, {"runner-credential revoke", true},
	{"runner", true},
	{"import", false}, {"import add", true}, {"import refresh", true}, {"import status", true},
	{"import history", true}, {"import cancel", true}, {"import schedule", true},
	{"import credentials", true}, {"import resolve", true},
}

func TestEveryCommandPrintsHelpAndSucceeds(t *testing.T) {
	for _, command := range helpCommands {
		for _, flag := range []string{"-h", "--help"} {
			arguments := append(strings.Fields(command.path), flag)
			output, err := captureStdout(func() error { return run(arguments) })
			if err != nil {
				t.Errorf("owngit %s %s returned an error: %v", command.path, flag, err)
				continue
			}
			if !strings.HasPrefix(output, "Usage: owngit "+command.path) {
				t.Errorf("owngit %s %s printed %q, want its usage", command.path, flag, output)
			}
			if command.leaf && !strings.Contains(output, "\nOptions:\n  --") {
				t.Errorf("owngit %s %s did not list its options: %q", command.path, flag, output)
			}
		}
	}
}

func TestHelpCommandListCoversTopLevelUsage(t *testing.T) {
	var usage strings.Builder
	printUsage(&usage)
	listed := strings.TrimSuffix(strings.TrimPrefix(strings.Fields(usage.String())[2], "["), "]")
	covered := map[string]bool{"version": true}
	for _, command := range helpCommands {
		covered[command.path] = true
	}
	for _, name := range strings.Split(listed, "|") {
		if !covered[name] {
			t.Errorf("command %q from the top-level usage has no help test", name)
		}
	}
}

func TestFlagErrorsStillFail(t *testing.T) {
	for _, command := range helpCommands {
		if !command.leaf {
			continue
		}
		arguments := append(strings.Fields(command.path), "--no-such-flag")
		if _, err := captureStdout(func() error { return run(arguments) }); err == nil {
			t.Errorf("owngit %s --no-such-flag succeeded", command.path)
		}
	}
}
