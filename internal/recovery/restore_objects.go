package recovery

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"owngit/internal/gitexec"
)

// RestoreReport names repositories whose history needs repair after restore.
type RestoreReport struct {
	ObjectWarnings []ObjectWarning `json:"object_warnings,omitempty"`
}

// ObjectWarning describes malformed objects without refusing the owner's history.
type ObjectWarning struct {
	ID      string `json:"id"`
	Message string `json:"message"`
}

const malformedObjectNotice = "This repository contains malformed Git objects that may display incorrectly; run git fsck --strict in a separate copy, repair the reported history, and import the repaired repository."

func (report *RestoreReport) checkObjects(ctx context.Context, runner *gitexec.Runner, repositoryStage, id string) error {
	if report == nil {
		return nil
	}
	arguments := []string{"--git-dir", "."}
	for _, warning := range gitexec.HistoricFormatWarnings() {
		arguments = append(arguments, "-c", "fsck."+warning+"=warn")
	}
	arguments = append(arguments, "fsck", "--strict", "--no-progress", "--no-dangling")
	var diagnostics objectDiagnostics
	result, err := runner.RunWithLimits(ctx, filepath.Join(repositoryStage, id+".git"), nil, gitexec.CommandLimits{Stderr: &diagnostics}, arguments...)
	diagnostics.finishLine()
	if err == nil {
		if diagnostics.unknown {
			log.Printf("Repository %s: git fsck diagnostic: %s", id, strings.TrimSpace(string(result.Stderr)))
		}
		return nil
	}
	known := !diagnostics.unknown && strings.TrimSpace(string(result.Stdout)) == ""
	// Git uses status 1 for loose object faults and 4 for packed object
	// faults. Only its semantic diagnostics are warnings: missing objects,
	// damaged packs, process failures and unknown diagnostics still fail.
	code, exited := gitexec.ExitCode(err)
	if exited && (code == 1 || code == 4 || code == 5) && known && diagnostics.malformed {
		report.ObjectWarnings = append(report.ObjectWarnings, ObjectWarning{ID: id, Message: malformedObjectNotice})
		return nil
	}
	return fmt.Errorf("git object check: %w", err)
}

// Diagnostics are classified from the complete stream, not the error sample.
// Memory is bounded by one line; unknown lines cannot excuse a failed check.
type objectDiagnostics struct {
	line      [64 << 10]byte
	used      int
	malformed bool
	unknown   bool
}

func (diagnostics *objectDiagnostics) Write(content []byte) (int, error) {
	length := len(content)
	for len(content) != 0 {
		end := bytes.IndexByte(content, '\n')
		part := content
		if end >= 0 {
			part = content[:end]
		}
		n := copy(diagnostics.line[diagnostics.used:], part)
		diagnostics.used += n
		if n != len(part) {
			diagnostics.unknown = true
		}
		if end < 0 {
			break
		}
		diagnostics.finishLine()
		content = content[end+1:]
	}
	return length, nil
}

func (diagnostics *objectDiagnostics) finishLine() {
	if diagnostics.used == 0 {
		return
	}
	line := string(diagnostics.line[:diagnostics.used])
	diagnostics.used = 0
	if strings.HasPrefix(line, "warning in ") || strings.HasPrefix(line, "notice: ") {
		return
	}
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "error" || fields[1] != "in" {
		diagnostics.unknown = true
		return
	}
	switch fields[2] {
	case "blob", "tree", "commit", "tag":
	default:
		diagnostics.unknown = true
	}
	if !strings.HasSuffix(fields[3], ":") || !validOID(strings.TrimSuffix(fields[3], ":")) ||
		!strings.HasSuffix(fields[4], ":") {
		diagnostics.unknown = true
	}
	diagnostics.malformed = true
}
