//go:build !windows

package checkrun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeDocker writes a Docker client stand-in that runs script with the
// arguments it was given.
func fakeDocker(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "docker")
	noErr(t, os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o700))
	return path
}

// A download reads the Docker client settings from an empty private folder,
// so no stored registry login or credential helper takes part.
func TestImageDownloadUsesNoStoredRegistryCredentials(t *testing.T) {
	envelope := t.TempDir()
	record := filepath.Join(t.TempDir(), "record")
	t.Setenv("DOCKER_CONFIG", filepath.Join(t.TempDir(), "owner-config"))
	docker := fakeDocker(t, `{ echo "config=$DOCKER_CONFIG"; ls -A "$DOCKER_CONFIG"; echo "args=$*"; } > `+record)
	noErr(t, pullContainerImage(context.Background(), docker, "unix:///docker.sock", envelope, "registry.example/checks:1"))
	content, err := os.ReadFile(record)
	noErr(t, err)
	want := "config=" + filepath.Join(envelope, "docker-config") + "\nargs=--host unix:///docker.sock pull --quiet registry.example/checks:1\n"
	if string(content) != want {
		t.Fatalf("download ran with\n%s\nwant\n%s", content, want)
	}
}

// A failed download, such as a full disk, is an error naming the image, and
// a cancelled one returns promptly.
func TestImageDownloadFailureAndCancellation(t *testing.T) {
	failing := fakeDocker(t, `echo "write /var/lib/docker/tmp: no space left on device" >&2; exit 1`)
	err := pullContainerImage(context.Background(), failing, "unix:///docker.sock", t.TempDir(), "registry.example/checks:1")
	if err == nil || !strings.Contains(err.Error(), "no space left on device") || !strings.Contains(err.Error(), "registry.example/checks:1") {
		t.Fatalf("failed download error=%v", err)
	}

	slow := fakeDocker(t, `sleep 30`)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = pullContainerImage(ctx, slow, "unix:///docker.sock", t.TempDir(), "registry.example/checks:1")
	if err == nil || time.Since(started) > 10*time.Second {
		t.Fatalf("cancelled download error=%v after %s", err, time.Since(started))
	}
}
