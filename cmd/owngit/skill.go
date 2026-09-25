package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	owngitchecks "owngit/integrations/skills/owngit-checks"
	"owngit/internal/apiclient"
	"owngit/internal/version"
)

// maximumInstalledSkillBytes bounds how much of an existing SKILL.md is read
// to compare it and to keep a copy.
const maximumInstalledSkillBytes = 1 << 20

type skillInstallResult struct {
	OK bool `json:"ok"`
	// Status is installed, already_current, or replaced.
	Status string `json:"status"`
	Path   string `json:"path"`
	// Previous is the copy of the replaced file, kept beside it.
	Previous string `json:"previous,omitempty"`
}

func skillCommand(arguments []string) error {
	flags := newCheckFlagSet("skill")
	install := flags.String("install", "", "skills `directory` of a coding tool; the skill is written to DIR/"+owngitchecks.Name+"/SKILL.md")
	printSkill := flags.Bool("print", false, "print the skill shipped with this binary")
	replace := flags.Bool("replace", false, "with --install, replace a changed SKILL.md after keeping a copy beside it")
	if err := parseCheckFlags(flags, arguments); err != nil {
		return err
	}
	switch {
	case *printSkill && *install == "" && !*replace:
		if _, err := os.Stdout.Write(owngitchecks.Skill); err != nil {
			return &apiclient.Error{Code: "output_failed", Message: "The skill could not be written.", Cause: err}
		}
		return nil
	case *install != "" && !*printSkill:
		result, err := installSkill(*install, *replace, time.Now())
		if err != nil {
			return err
		}
		return writeJSONValue(result)
	default:
		return cliProblem("invalid_arguments", "skill requires --install DIR or --print. --replace applies only to --install.")
	}
}

// installSkill writes the shipped skill to directory/owngit-checks/SKILL.md.
// An existing file with other content may hold the owner's edits, so it is
// replaced only with replace set, and only after a copy of it was written
// beside it. A symbolic link or other non-regular target is never written.
func installSkill(directory string, replace bool, now time.Time) (skillInstallResult, error) {
	skillDirectory := filepath.Join(directory, owngitchecks.Name)
	target := filepath.Join(skillDirectory, "SKILL.md")
	if err := os.MkdirAll(skillDirectory, 0o755); err != nil {
		return skillInstallResult{}, &apiclient.Error{Code: "skill_install_failed", Message: "The skill directory could not be created.", Cause: err}
	}
	if info, err := os.Lstat(skillDirectory); err != nil || !info.IsDir() {
		return skillInstallResult{}, cliProblem("skill_target_invalid", skillDirectory+" is not a directory.")
	}
	existing, err := readInstalledSkill(target)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := writeSkillFile(skillDirectory, target); err != nil {
			return skillInstallResult{}, err
		}
		return skillInstallResult{OK: true, Status: "installed", Path: target}, nil
	case err != nil:
		return skillInstallResult{}, err
	case bytes.Equal(existing, owngitchecks.Skill):
		return skillInstallResult{OK: true, Status: "already_current", Path: target}, nil
	case !replace:
		return skillInstallResult{}, cliProblem("skill_modified",
			target+" differs from the skill shipped with owngit "+version.Version+" and may hold your edits, so it was left unchanged. "+
				"Compare it with owngit skill --print. To install the shipped skill, rerun with --replace, which first keeps the current file beside it.")
	}
	previous := target + ".previous-" + now.UTC().Format("20060102T150405Z")
	if err := writeExclusive(previous, existing); err != nil {
		return skillInstallResult{}, &apiclient.Error{Code: "skill_install_failed", Message: "The current skill could not be kept, so it was left unchanged.", Cause: err}
	}
	if err := writeSkillFile(skillDirectory, target); err != nil {
		return skillInstallResult{}, err
	}
	return skillInstallResult{OK: true, Status: "replaced", Path: target, Previous: previous}, nil
}

// readInstalledSkill reads an existing regular SKILL.md without following a
// symbolic link.
func readInstalledSkill(target string) ([]byte, error) {
	info, err := os.Lstat(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, os.ErrNotExist
		}
		return nil, &apiclient.Error{Code: "skill_install_failed", Message: "The installed skill could not be inspected.", Cause: err}
	}
	if !info.Mode().IsRegular() {
		return nil, cliProblem("skill_target_invalid", target+" is not a regular file, so it was left unchanged.")
	}
	file, err := os.Open(target)
	if err != nil {
		return nil, &apiclient.Error{Code: "skill_install_failed", Message: "The installed skill could not be read.", Cause: err}
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maximumInstalledSkillBytes+1))
	if err != nil {
		return nil, &apiclient.Error{Code: "skill_install_failed", Message: "The installed skill could not be read.", Cause: err}
	}
	if len(content) > maximumInstalledSkillBytes {
		return nil, cliProblem("skill_target_invalid", target+" is larger than 1 MiB, so it was left unchanged.")
	}
	return content, nil
}

// writeSkillFile writes the shipped skill to a temporary file in the same
// directory and renames it over target, so a reader never sees half a file.
func writeSkillFile(directory, target string) error {
	temporary, err := os.CreateTemp(directory, ".SKILL.md.*")
	if err != nil {
		return &apiclient.Error{Code: "skill_install_failed", Message: "The skill could not be written.", Cause: err}
	}
	name := temporary.Name()
	_, writeErr := temporary.Write(owngitchecks.Skill)
	closeErr := temporary.Close()
	if err := errors.Join(writeErr, closeErr, os.Chmod(name, 0o644)); err != nil {
		_ = os.Remove(name)
		return &apiclient.Error{Code: "skill_install_failed", Message: "The skill could not be written.", Cause: err}
	}
	if err := os.Rename(name, target); err != nil {
		_ = os.Remove(name)
		return &apiclient.Error{Code: "skill_install_failed", Message: "The skill could not be written.", Cause: err}
	}
	return nil
}

// writeExclusive creates path, refusing an existing file, and writes content.
func writeExclusive(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(content)
	return errors.Join(writeErr, file.Sync(), file.Close())
}
