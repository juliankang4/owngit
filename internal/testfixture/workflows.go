package testfixture

import (
	"os"
	"path/filepath"
	"testing"
)

const SurfaceWorkflow = `name: Synthetic workflow
on:
  workflow_dispatch:
    inputs:
      enabled:
        type: boolean
        default: true
      count:
        type: number
        default: 2
      choice:
        type: choice
        options: [first, second]
        default: first
jobs:
  build:
    runs-on: ubuntu-latest
    env:
      TOKEN: ${{ secrets.TOKEN }}
    steps:
      - run: echo synthetic
  dependent:
    needs: build
    runs-on: ubuntu-latest
    steps:
      - run: echo dependent
`

// WriteWorkflow writes a synthetic workflow in a test-owned checkout.
func WriteWorkflow(t *testing.T, work, name, content string) {
	t.Helper()
	directory := filepath.Join(work, ".github", "workflows")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
