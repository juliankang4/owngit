//go:build windows

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/testfixture"
)

// powerShellBlocks returns the ```powershell code blocks of a Markdown file.
func powerShellBlocks(content []byte) []string {
	var blocks []string
	for rest := string(bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))); ; {
		_, after, found := strings.Cut(rest, "```powershell\n")
		if !found {
			return blocks
		}
		block, remaining, found := strings.Cut(after, "```")
		if !found {
			return blocks
		}
		blocks = append(blocks, block)
		rest = remaining
	}
}

// The PowerShell commands that the documentation gives for making a password
// file on Windows run exactly as written, with Read-Host answered by the test
// and $HOME in a temporary folder, and make a file that OwnGit accepts with
// the password (and server line) it holds.
func TestWindowsDocumentedPasswordFileCommands(t *testing.T) {
	const password = "valid-docs-password-2841"
	for _, test := range []struct{ document, origin string }{
		{"OPERATIONS.md", ""},
		{"OPERATIONS.ko.md", ""},
		{"CODING_TOOLS.md", "https://owngit.example.test"},
		{"CODING_TOOLS.ko.md", "https://owngit.example.test"},
	} {
		t.Run(test.document, func(t *testing.T) {
			content, err := os.ReadFile(filepath.Join("..", "..", "docs", test.document))
			noErr(t, err)
			blocks := powerShellBlocks(content)
			if len(blocks) != 1 {
				t.Fatalf("%d PowerShell blocks, want 1", len(blocks))
			}
			testfixture.ForEachPowerShell(t, func(t *testing.T, shell string) {
				home := t.TempDir()
				// The preamble only answers Read-Host (a function takes precedence
				// over the cmdlet) and stops unless $HOME is the temporary folder.
				preamble := "$ErrorActionPreference = 'Stop'\n" +
					"if ($HOME -ne '" + home + "') { Write-Error ('HOME is ' + $HOME) }\n" +
					"function Read-Host { param([Parameter(Position = 0)] $Prompt, [switch] $AsSecureString) ConvertTo-SecureString '" + password + "' -AsPlainText -Force }\n"
				script := filepath.Join(t.TempDir(), "documented.ps1")
				noErr(t, os.WriteFile(script, []byte(preamble+blocks[0]), 0o600))
				command := exec.Command(shell, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script)
				volume := filepath.VolumeName(home)
				command.Env = append(os.Environ(), "USERPROFILE="+home, "HOMEDRIVE="+volume, "HOMEPATH="+strings.TrimPrefix(home, volume))
				output, err := command.CombinedOutput()
				if err != nil {
					t.Fatalf("the documented commands failed: %v\n%s", err, output)
				}
				path := filepath.Join(home, "owngit-password.txt")
				file, err := readPasswordFile(path)
				if err != nil {
					listing, _ := exec.Command("icacls", path).CombinedOutput()
					t.Fatalf("the documented file was refused: %v\n%s", err, listing)
				}
				if file.secret != password || file.origin != test.origin {
					t.Fatalf("read secret %q origin %q", file.secret, file.origin)
				}
			})
		})
	}
}
