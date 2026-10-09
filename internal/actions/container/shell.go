package container

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"owngit/internal/actions"
)

func shellCommand(script actions.Script) ([]string, bool, error) {
	file := "/owngit/scripts/" + filepath.Base(script.Path)
	if script.Shell == "" {
		return []string{file}, true, nil
	}
	words, err := actions.ShellWords(script.Shell)
	if err != nil {
		return nil, false, err
	}
	language := strings.ToLower(path.Base(words[0]))
	if language == "cmd" || language == "cmd.exe" {
		return nil, false, fmt.Errorf("workflow.shell_unavailable: cmd requires Windows: %w", exec.ErrNotFound)
	}
	if language == "pwsh" || language == "powershell" {
		name := filepath.Join("scripts", filepath.Base(script.Path))
		input, err := script.ActionsRoot.Open(name)
		if err != nil {
			return nil, false, err
		}
		value, readErr := io.ReadAll(io.LimitReader(input, (64<<10)+1))
		if err := errors.Join(readErr, input.Close()); err != nil {
			return nil, false, err
		}
		if len(value) > 64<<10 {
			return nil, false, fmt.Errorf("workflow.limit: script exceeds 64 KiB")
		}
		output, err := script.ActionsRoot.OpenFile(name+".ps1", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, false, err
		}
		_, err = output.WriteString("\uFEFF$ErrorActionPreference = 'stop'\n" + string(value) + "\nif (Test-Path -LiteralPath variable:\\LASTEXITCODE) { exit $LASTEXITCODE }\n")
		if err := errors.Join(err, output.Close()); err != nil {
			return nil, false, err
		}
		file += ".ps1"
	}
	switch script.Shell {
	case "bash":
		return []string{"bash", "--noprofile", "--norc", "-eo", "pipefail", file}, false, nil
	case "sh":
		return []string{"sh", "-e", file}, false, nil
	case "python":
		return []string{"python", file}, false, nil
	case "pwsh", "powershell":
		return []string{script.Shell, "-command", ". '" + file + "'"}, false, nil
	}
	placeholder := false
	for index := 1; index < len(words); index++ {
		placeholder = placeholder || strings.Contains(words[index], "{0}")
		words[index] = strings.ReplaceAll(words[index], "{0}", file)
	}
	if !placeholder {
		return nil, false, fmt.Errorf("workflow.shell: custom shell must contain {0}")
	}
	return words, false, nil
}
