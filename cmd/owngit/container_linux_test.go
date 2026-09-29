package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"owngit/internal/service"
	"owngit/internal/state"
)

// useRouteRecord makes this test run as the program of the container image
// when content is "container".
func useRouteRecord(t *testing.T, content string) {
	t.Helper()
	record := filepath.Join(t.TempDir(), "install-route")
	noErr(t, os.WriteFile(record, []byte(content), 0o644))
	previous := routeRecord
	routeRecord = record
	t.Cleanup(func() { routeRecord = previous })
}

// The image's route record makes the update command the container update,
// which recreates the container, so nothing is left to restart.
func TestContainerImageUpdatesByRecreatingTheContainer(t *testing.T) {
	useRouteRecord(t, "container\n")
	install, err := detectInstall()
	if container, containerErr := inContainerImage(); err != nil || containerErr != nil || install.Route != service.RouteContainer || !container {
		t.Fatalf("install %+v, %v, %v", install, err, containerErr)
	}
	command, start, restart := dashboardUpdateCommand(false)("99.0.0")
	if command != service.ContainerUpdateCommand || start != "" || restart {
		t.Fatalf("dashboard update: %q, start %q, restart %v", command, start, restart)
	}
	useRouteRecord(t, "archive\n")
	if container, err := inContainerImage(); container || err != nil {
		t.Fatalf("another record made this the container image: %v", err)
	}
}

// A route record that exists but cannot be read leaves the route unknown,
// which install detection reports instead of taking another route.
func TestUnreadableRouteRecordIsAnError(t *testing.T) {
	record := t.TempDir() // a folder, which no account can read as a file
	previous := routeRecord
	routeRecord = record
	t.Cleanup(func() { routeRecord = previous })
	if _, err := detectInstall(); err == nil || !strings.Contains(err.Error(), "install route record") {
		t.Fatalf("detectInstall: %v", err)
	}
	if _, err := captureStdout(func() error { return run([]string{"update"}) }); err == nil || !strings.Contains(err.Error(), "install route record") {
		t.Fatalf("owngit update: %v", err)
	}
	if command, start, restart := dashboardUpdateCommand(false)("99.0.0"); command != "" || start != "" || restart {
		t.Fatalf("dashboard update: %q, %q, %v", command, start, restart)
	}
}

// In the container image the setup link names localhost, which a browser
// on the computer running the container opens, and says what to use from
// another device; the container's own addresses are left out.
func TestContainerSetupLinkNamesLocalhost(t *testing.T) {
	useRouteRecord(t, "container")
	stateDir := filepath.Join(t.TempDir(), "state")
	store, err := state.Open(context.Background(), stateDir)
	noErr(t, err)
	defer store.Close()
	noErr(t, store.UpdateNetwork(context.Background(), state.NetworkUpdate{Settings: state.NetworkSettings{Listen: "0.0.0.0:7654"}}))
	bases, tunnel, err := setupBases(context.Background(), store)
	noErr(t, err)
	if want := []string{"http://localhost:7654"}; !reflect.DeepEqual(bases, want) || tunnel != "" {
		t.Fatalf("setupBases = %q, tunnel %q; want %q", bases, tunnel, want)
	}
	var out bytes.Buffer
	_, err = issueAndShowSetupLink(context.Background(), store, "", &out, true)
	noErr(t, err)
	for _, want := range []string{"From another device, use that computer's name or address in place of localhost", "  http://localhost:7654/setup#"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("setup link output lacks %q:\n%s", want, out.String())
		}
	}
	// Not on a terminal, as in "docker logs", only the setup file is named.
	out.Reset()
	_, err = issueAndShowSetupLink(context.Background(), store, "", &out, false)
	noErr(t, err)
	if strings.Contains(out.String(), "#") || strings.Contains(out.String(), "http") {
		t.Fatalf("setup link written off a terminal:\n%s", out.String())
	}
}
