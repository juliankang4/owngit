//go:build darwin

package state

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMacOSChangeChecksAgree(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "main.swift")
	noErr(t, os.WriteFile(main, []byte(`import Foundation
for path in CommandLine.arguments.dropFirst() {
    print(protectedStateDirectoryProblem(path) == nil ? "safe" : "changeable")
}
`), 0o600))
	run := func(t *testing.T, name string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", name, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	probe := filepath.Join(root, "change-check")
	run(t, "swiftc", filepath.Join("..", "..", "packaging", "macos", "ProtectedPath.swift"), main, "-o", probe)
	for _, test := range []struct {
		name       string
		mode       os.FileMode
		grant      string
		changeable bool
	}{
		{"private", 0o700, "", false},
		{"readable", 0o755, "", false},
		{"owner read-only", 0o500, "", false},
		{"world writer", 0o702, "", true},
		{"sticky state", 0o702 | os.ModeSticky, "", true},
		{"read ACL", 0o700, "everyone allow read,list", false},
		{"write data", 0o700, "everyone allow add_file", true},
		{"append data", 0o700, "everyone allow add_subdirectory", true},
		{"delete", 0o700, "everyone allow delete", true},
		{"delete child", 0o700, "everyone allow delete_child", true},
		{"write attributes", 0o700, "everyone allow writeattr", true},
		{"write extended attributes", 0o700, "everyone allow writeextattr", true},
		{"write security", 0o700, "everyone allow writesecurity", true},
		{"change owner", 0o700, "everyone allow chown", true},
		{"inheritable attributes", 0o700, "everyone allow writeattr,file_inherit,directory_inherit", true},
		{"denied attributes", 0o700, "everyone deny writeattr,writeextattr", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state")
			noErr(t, os.Mkdir(path, 0o700))
			noErr(t, os.Chmod(path, test.mode))
			if test.grant != "" {
				run(t, "chmod", "+a", test.grant, path)
			}
			file, err := os.Open(path)
			noErr(t, err)
			defer file.Close()
			info, err := file.Stat()
			noErr(t, err)
			goChangeable, err := stateFileChangeable(file, info)
			noErr(t, err)
			held, openErr := OpenStateDirectory(path)
			if held != nil {
				noErr(t, held.Close())
			}
			swift := run(t, probe, path)
			if swift != "safe" && swift != "changeable" {
				t.Fatalf("unexpected Swift classification: %q", swift)
			}
			swiftChangeable := swift == "changeable"
			if goChangeable != test.changeable || swiftChangeable != test.changeable || (openErr != nil) != test.changeable {
				t.Fatalf("held Go=%v, state open=%v, Swift=%v, want %v", goChangeable, openErr, swiftChangeable, test.changeable)
			}
		})
	}
}
