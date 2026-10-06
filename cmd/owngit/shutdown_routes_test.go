package main

import (
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"

	"owngit/internal/service"
)

// A manager that stops OwnGit sooner than its shutdown takes kills it during
// cleanup. The unit, agent and task use service.StopTimeout directly; the
// container file and the Homebrew formula are files, so this reads them.
func TestEveryServiceRouteOutwaitsTheShutdownDeadline(t *testing.T) {
	if service.StopTimeout < shutdownDeadline+10*time.Second {
		t.Fatalf("service.StopTimeout %s leaves under 10s over the shutdown deadline %s", service.StopTimeout, shutdownDeadline)
	}
	for path, pattern := range map[string]string{
		"../../packaging/container/compose.yaml":  `(?m)^\s+stop_grace_period:\s*(\d+)s\s*$`,
		"../../packaging/homebrew/owngit.rb.tmpl": `(?m)^\s+stop_timeout\s+(\d+)\s*$`,
	} {
		file, err := os.ReadFile(path)
		noErr(t, err)
		match := regexp.MustCompile(pattern).FindSubmatch(file)
		if match == nil {
			t.Fatalf("%s sets no stop wait in seconds", path)
		}
		seconds, _ := strconv.Atoi(string(match[1]))
		if wait := time.Duration(seconds) * time.Second; wait != service.StopTimeout {
			t.Fatalf("%s waits %s, want service.StopTimeout %s", path, wait, service.StopTimeout)
		}
	}
}
