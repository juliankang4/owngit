package server

import (
	"fmt"
	"os"
	"testing"

	"owngit/internal/markdown"
)

// TestMain lets the test binary render Markdown in a child process, as the
// owngit binary does.
func TestMain(m *testing.M) {
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
