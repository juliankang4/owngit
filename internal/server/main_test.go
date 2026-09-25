package server

import (
	"fmt"
	"os"
	"testing"

	"owngit/internal/markdown"
	"owngit/internal/tailscale/tailscaletest"
)

// TestMain lets the test binary render Markdown in a child process, as the
// owngit binary does, and act as a fake tailscale command.
func TestMain(m *testing.M) {
	tailscaletest.RunIfFake()
	if markdown.IsChild(os.Args) {
		os.Exit(markdown.RunChild(os.Stdin, os.Stdout))
	}
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	markdown.SetHelper(executable)
	os.Exit(m.Run())
}
