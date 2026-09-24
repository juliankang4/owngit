package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/gitexec"
	"owngit/internal/repository"
	"owngit/internal/state"
)

// One repository that cannot be prepared no longer stops the server: it stays
// locked and is reported in the log while the other repository is served.
func TestServeStartsWhenOneRepositoryCannotBePrepared(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	repositoriesRoot := filepath.Join(root, "repositories")
	noErr(t, os.Mkdir(repositoriesRoot, 0o700))
	store, err := state.Open(ctx, stateDir)
	noErr(t, err)
	noErr(t, store.CompleteSetup(ctx, repositoriesRoot, "open", "", "synthetic-admin-hash", true))
	runner, err := gitexec.New("", filepath.Join(stateDir, "runtime"))
	noErr(t, err)
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: repositoriesRoot}
	for _, name := range []string{"healthy", "broken"} {
		_, err := manager.Create(ctx, name, "")
		noErr(t, err)
	}
	broken, err := manager.Path("broken")
	noErr(t, err)
	// Git refuses to overwrite a key with several values, so preparing this
	// repository fails on every attempt.
	if output, err := exec.Command("git", "--git-dir", broken, "config", "--local", "--add", "transfer.hideRefs", "refs/other/").CombinedOutput(); err != nil {
		t.Fatalf("break fixture config: %v\n%s", err, output)
	}
	noErr(t, store.Close())

	instance := startServed(t, stateDir)
	defer instance.stop()
	status := func(name string) int {
		t.Helper()
		response, err := http.Get(instance.url + "/git/" + name + ".git/info/refs?service=git-upload-pack")
		noErr(t, err)
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		return response.StatusCode
	}
	if got := status("healthy"); got != http.StatusOK {
		t.Fatalf("healthy repository status=%d\n%s", got, instance.log())
	}
	if got := status("broken"); got != http.StatusServiceUnavailable {
		t.Fatalf("unprepared repository status=%d\n%s", got, instance.log())
	}
	if !strings.Contains(instance.log(), `repository "broken" could not be prepared and stays locked`) {
		t.Fatalf("the preparation failure was not logged:\n%s", instance.log())
	}
}
