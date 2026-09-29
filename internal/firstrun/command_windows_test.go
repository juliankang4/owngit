package firstrun

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// argumentsFile names, in the environment, the file where the test binary
// writes its arguments when it stands in for the program of a printed
// command.
const argumentsFile = "OWNGIT_TEST_ARGUMENTS_FILE"

func TestMain(m *testing.M) {
	if out := os.Getenv(argumentsFile); out != "" {
		data, err := json.Marshal(os.Args[1:])
		if err != nil || os.WriteFile(out, data, 0o600) != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// A command setup prints runs in Windows PowerShell 5.1, and in PowerShell 7
// when it is installed, as the program it names with exactly the arguments
// it names, whatever quotes or PowerShell syntax the program path and the
// state folder hold. Nothing in them runs as PowerShell.
func TestPrintedCommandRunsInPowerShell(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	shells := []string{"powershell.exe"}
	if pwsh, err := exec.LookPath("pwsh.exe"); err == nil {
		shells = append(shells, pwsh)
	}
	root := t.TempDir()
	for _, name := range []string{
		"own git",
		"it's here",
		"O\u2019Brien \u2018a\u201ab\u201bc",
		"$(New-Item marker) ; `x & [y]",
		"\u202edir",
	} {
		folder := filepath.Join(root, name)
		if err := os.Mkdir(folder, 0o700); err != nil {
			t.Fatal(err)
		}
		program := filepath.Join(folder, "owngit.exe")
		copyFile(t, self, program)
		stateDir := filepath.Join(root, "state "+name)
		command := commandLine(program, "serve", "--state-dir", stateDir)
		script := filepath.Join(root, "command.ps1")
		// Windows PowerShell 5.1 reads a script as UTF-8 only with a BOM.
		if err := os.WriteFile(script, []byte("\ufeff"+command+"\r\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, shell := range shells {
			out := filepath.Join(root, "arguments.json")
			_ = os.Remove(out)
			run := exec.Command(shell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script)
			run.Dir = root
			run.Env = append(os.Environ(), argumentsFile+"="+out)
			output, err := run.CombinedOutput()
			if err != nil {
				t.Fatalf("%s %s: %v\n%s", shell, command, err, output)
			}
			data, err := os.ReadFile(out)
			if err != nil {
				t.Fatalf("%s %s: the program did not run: %v\n%s", shell, command, err, output)
			}
			var got []string
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if want := []string{"serve", "--state-dir", stateDir}; !slices.Equal(got, want) {
				t.Errorf("%s %s: arguments %q, want %q", shell, command, got, want)
			}
			if _, err := os.Stat(filepath.Join(root, "marker")); err == nil {
				t.Fatalf("%s %s: a path ran as PowerShell", shell, command)
			}
		}
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	source, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	target, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(target, source); err != nil {
		target.Close()
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
}
