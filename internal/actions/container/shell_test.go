package container

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"owngit/internal/actions"
)

func TestContainerShellCommand(t *testing.T) {
	const scriptPath = "/owngit/scripts/step-1.script"
	for _, test := range []struct {
		name, shell               string
		want                      []string
		implicit, prepared, fails bool
	}{
		{name: "implicit", want: []string{scriptPath}, implicit: true},
		{name: "bash", shell: "bash", want: []string{"bash", "--noprofile", "--norc", "-eo", "pipefail", scriptPath}},
		{name: "sh", shell: "sh", want: []string{"sh", "-e", scriptPath}},
		{name: "python", shell: "python", want: []string{"python", scriptPath}},
		{name: "pwsh", shell: "pwsh", want: []string{"pwsh", "-command", ". '" + scriptPath + ".ps1'"}, prepared: true},
		{name: "powershell template", shell: "powershell -File {0}", want: []string{"powershell", "-File", scriptPath + ".ps1"}, prepared: true},
		{name: "relative template", shell: "./tool -x '{0}'", want: []string{"./tool", "-x", scriptPath}},
		{name: "quoted argument", shell: "sh -e {0} 'two words'", want: []string{"sh", "-e", scriptPath, "two words"}},
		{name: "no placeholder", shell: "sh -e", fails: true},
		{name: "unterminated", shell: "sh -e '{0}", fails: true},
		{name: "empty executable", shell: "'' {0}", fails: true},
		{name: "cmd", shell: "cmd", fails: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.Mkdir(filepath.Join(directory, "scripts"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "scripts", "step-1.script"), []byte("Write-Output '한글'"), 0o600); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			command, implicit, err := shellCommand(actions.Script{Shell: test.shell, Path: filepath.Join(directory, "scripts", "step-1.script"), ActionsRoot: root})
			if test.fails {
				if err == nil {
					t.Fatal(command)
				}
				return
			}
			if err != nil || implicit != test.implicit || !slices.Equal(command, test.want) {
				t.Fatalf("command=%v implicit=%v err=%v", command, implicit, err)
			}
			if test.prepared {
				value, err := root.ReadFile(filepath.Join("scripts", "step-1.script.ps1"))
				if err != nil || !strings.HasPrefix(string(value), "\uFEFF$ErrorActionPreference = 'stop'\n") || !strings.Contains(string(value), "Write-Output '한글'") || !strings.Contains(string(value), "exit $LASTEXITCODE") {
					t.Fatalf("prepared=%q err=%v", value, err)
				}
			}
		})
	}
}
