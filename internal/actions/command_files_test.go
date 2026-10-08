package actions

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCommandFiles(t *testing.T) {
	type fileCase struct {
		name      string
		contents  map[string]string
		windows   bool
		mutate    func(*testing.T, *commandFiles)
		wantError string
		check     func(*testing.T, commandChanges)
	}
	tests := []fileCase{
		{name: "assignments multiline CRLF and BOM", contents: map[string]string{"GITHUB_ENV": "\uFEFFVALUE=first\r\nMULTI<<END\r\nline one\r\nline two\r\nEND\r\nVALUE=last\r\n", "GITHUB_OUTPUT": "cache-hit=false\nempty=\nresult<<EOF\na=b\nEOF\n", "GITHUB_PATH": "/one\r\n/two\n", "GITHUB_STEP_SUMMARY": "ignored summary"}, check: func(t *testing.T, changes commandChanges) {
			if changes.env["VALUE"] != "last" || changes.env["MULTI"] != "line one\nline two" || changes.outputs["result"] != "a=b" || changes.outputs["cache-hit"] != "false" || len(changes.paths) != 2 || changes.notes[0].Code != "note.summary" {
				t.Fatal(changes)
			}
		}},
		{name: "windows assignment order and case sensitive outputs", windows: true, contents: map[string]string{"GITHUB_ENV": "VALUE=first\nValue=second\nvalue<<END\nlast\nEND\n", "GITHUB_OUTPUT": "VALUE=first\nValue=second\nvalue=last\n"}, check: func(t *testing.T, changes commandChanges) {
			if len(changes.env) != 1 || changes.env["value"] != "last" || len(changes.outputs) != 3 || changes.outputs["VALUE"] != "first" || changes.outputs["Value"] != "second" || changes.outputs["value"] != "last" {
				t.Fatal(changes)
			}
		}},
		{name: "unix environment names remain distinct", contents: map[string]string{"GITHUB_ENV": "VALUE=first\nValue=second\nvalue=last\n"}, check: func(t *testing.T, changes commandChanges) {
			if len(changes.env) != 3 || changes.env["VALUE"] != "first" || changes.env["Value"] != "second" || changes.env["value"] != "last" {
				t.Fatal(changes)
			}
		}},
		{name: "protected names ignored", contents: map[string]string{"GITHUB_ENV": "GITHUB_SHA=x\nrunner_TEMP=x\nNODE_OPTIONS=x\nVALUE=ok\n"}, check: func(t *testing.T, changes commandChanges) {
			if len(changes.env) != 1 || changes.env["VALUE"] != "ok" || len(changes.notes) != 3 {
				t.Fatal(changes)
			}
		}},
		{name: "summary at byte limit", contents: map[string]string{"GITHUB_STEP_SUMMARY": strings.Repeat("x", maxCommandFileBytes)}, check: func(t *testing.T, changes commandChanges) {
			if len(changes.notes) != 1 {
				t.Fatal(changes)
			}
		}},
		{name: "assignment containing marker", contents: map[string]string{"GITHUB_ENV": "VALUE=x<<text\n"}, check: func(t *testing.T, changes commandChanges) {
			if changes.env["VALUE"] != "x<<text" {
				t.Fatal(changes)
			}
		}},
		{name: "empty multiline value", contents: map[string]string{"GITHUB_OUTPUT": "empty<<END\nEND\n"}, check: func(t *testing.T, changes commandChanges) {
			if value, ok := changes.outputs["empty"]; !ok || value != "" {
				t.Fatal(changes)
			}
		}},
		{name: "missing delimiter", contents: map[string]string{"GITHUB_ENV": "VALUE<<END\nnever closed\n"}, wantError: "GITHUB_ENV"},
		{name: "empty delimiter", contents: map[string]string{"GITHUB_ENV": "VALUE<<\n"}, wantError: "GITHUB_ENV"},
		{name: "bad assignment", contents: map[string]string{"GITHUB_OUTPUT": "not an assignment\n"}, wantError: "GITHUB_OUTPUT"},
		{name: "bad environment name", contents: map[string]string{"GITHUB_ENV": "BAD-NAME=x\n"}, wantError: "GITHUB_ENV"},
		{name: "environment value bound", contents: map[string]string{"GITHUB_ENV": "VALUE=" + strings.Repeat("x", maxEnvironmentValue+1)}, wantError: "GITHUB_ENV"},
		{name: "invalid UTF-8", contents: map[string]string{"GITHUB_OUTPUT": "value=\xff\n"}, wantError: "GITHUB_OUTPUT"},
		{name: "NUL", contents: map[string]string{"GITHUB_PATH": "/bad\x00path\n"}, wantError: "GITHUB_PATH"},
		{name: "file replaced", wantError: "GITHUB_OUTPUT", mutate: func(t *testing.T, files *commandFiles) {
			path := files.paths["GITHUB_OUTPUT"]
			if err := os.Rename(path, path+".old"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("value=x\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "hard link", wantError: "GITHUB_OUTPUT", mutate: func(t *testing.T, files *commandFiles) {
			if err := os.Link(files.paths["GITHUB_OUTPUT"], filepath.Join(t.TempDir(), "linked")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symbolic link", wantError: "GITHUB_OUTPUT", mutate: func(t *testing.T, files *commandFiles) {
			path := files.paths["GITHUB_OUTPUT"]
			if err := os.Rename(path, path+".old"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(path+".old", path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "directory replaces file", wantError: "GITHUB_OUTPUT", mutate: func(t *testing.T, files *commandFiles) {
			path := files.paths["GITHUB_OUTPUT"]
			if err := os.Rename(path, path+".old"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "command directory replaced", wantError: "command directory", mutate: func(t *testing.T, files *commandFiles) {
			path := files.root.Name()
			if err := os.Rename(path, path+".old"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, name := range commandFileNames {
		tests = append(tests, fileCase{name: "oversized " + name, contents: map[string]string{name: strings.Repeat("x", maxCommandFileBytes+1)}, wantError: name})
	}
	if runtime.GOOS != "windows" {
		tests = append(tests, fileCase{name: "FIFO", wantError: "GITHUB_OUTPUT", mutate: func(t *testing.T, files *commandFiles) {
			path := files.paths["GITHUB_OUTPUT"]
			if err := os.Rename(path, path+".old"); err != nil {
				t.Fatal(err)
			}
			if err := makeFIFO(path); err != nil {
				t.Fatal(err)
			}
		}})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, err := prepareActionsDirectory(filepath.Join(t.TempDir(), "actions"))
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			files, err := prepareCommandFiles(root, 1)
			if err != nil {
				t.Fatal(err)
			}
			defer files.root.Close()
			for name, value := range test.contents {
				if err := os.WriteFile(files.paths[name], []byte(value), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if test.mutate != nil {
				test.mutate(t, files)
			}
			changes, err := files.read(test.windows)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("got %+v, %v", changes, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if test.check != nil {
				test.check(t, changes)
			}
		})
	}
}
