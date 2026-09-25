package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	owngitchecks "owngit/integrations/skills/owngit-checks"
)

func TestSkillPrintMatchesTheShippedFile(t *testing.T) {
	shipped, err := os.ReadFile(filepath.Join("..", "..", "integrations", "skills", "owngit-checks", "SKILL.md"))
	noErr(t, err)
	output, err := captureStdout(func() error { return skillCommand([]string{"--print"}) })
	noErr(t, err)
	if output != string(shipped) || !bytes.Equal(owngitchecks.Skill, shipped) {
		t.Fatal("the embedded skill differs from integrations/skills/owngit-checks/SKILL.md")
	}
}

func runSkillInstall(t *testing.T, arguments ...string) skillInstallResult {
	t.Helper()
	output, err := captureStdout(func() error { return skillCommand(append([]string{"--install"}, arguments...)) })
	noErr(t, err)
	var result skillInstallResult
	noErr(t, json.Unmarshal([]byte(output), &result))
	return result
}

func TestSkillInstallKeepsTheOwnersEdits(t *testing.T) {
	skills := filepath.Join(t.TempDir(), "agents", "skills")
	target := filepath.Join(skills, "owngit-checks", "SKILL.md")

	if result := runSkillInstall(t, skills); !result.OK || result.Status != "installed" || result.Path != target {
		t.Fatalf("first install=%+v", result)
	}
	installed, err := os.ReadFile(target)
	noErr(t, err)
	if !bytes.Equal(installed, owngitchecks.Skill) {
		t.Fatal("the installed skill differs from the shipped bytes")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(target)
		noErr(t, err)
		if info.Mode().Perm() != 0o644 {
			t.Fatalf("installed mode=%v", info.Mode().Perm())
		}
	}
	if result := runSkillInstall(t, skills); result.Status != "already_current" || result.Previous != "" {
		t.Fatalf("repeat install=%+v", result)
	}

	edited := append(append([]byte{}, owngitchecks.Skill...), []byte("\nLocal rule: run the linter first.\n")...)
	noErr(t, os.WriteFile(target, edited, 0o644))
	_, err = captureStdout(func() error { return skillCommand([]string{"--install", skills}) })
	if got := commandErrorCode(err); got != "skill_modified" {
		t.Fatalf("install over an edited skill err=%v code=%q", err, got)
	}
	if content, err := os.ReadFile(target); err != nil || !bytes.Equal(content, edited) {
		t.Fatal("a refused install changed the edited skill")
	}
	if entries, err := os.ReadDir(filepath.Dir(target)); err != nil || len(entries) != 1 {
		t.Fatalf("a refused install left files: %v err=%v", entries, err)
	}

	result := runSkillInstall(t, skills, "--replace")
	if result.Status != "replaced" || result.Previous == "" || filepath.Dir(result.Previous) != filepath.Dir(target) {
		t.Fatalf("replace=%+v", result)
	}
	if kept, err := os.ReadFile(result.Previous); err != nil || !bytes.Equal(kept, edited) {
		t.Fatalf("the replaced skill was not kept: err=%v", err)
	}
	if content, err := os.ReadFile(target); err != nil || !bytes.Equal(content, owngitchecks.Skill) {
		t.Fatal("replace did not install the shipped skill")
	}
}

// A replacement whose copy cannot be kept leaves the edited file alone.
func TestSkillReplaceNeverOverwritesAnEarlierCopy(t *testing.T) {
	skills := t.TempDir()
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	target := filepath.Join(skills, "owngit-checks", "SKILL.md")
	noErr(t, os.MkdirAll(filepath.Dir(target), 0o755))
	noErr(t, os.WriteFile(target, []byte("first edit\n"), 0o644))
	if _, err := installSkill(skills, true, now); err != nil {
		t.Fatal(err)
	}
	noErr(t, os.WriteFile(target, []byte("second edit\n"), 0o644))
	if _, err := installSkill(skills, true, now); commandErrorCode(err) != "skill_install_failed" {
		t.Fatalf("replace with an existing copy err=%v", err)
	}
	if content, _ := os.ReadFile(target); string(content) != "second edit\n" {
		t.Fatalf("target=%q, want the second edit kept", content)
	}
	if kept, _ := os.ReadFile(target + ".previous-20260925T120000Z"); string(kept) != "first edit\n" {
		t.Fatalf("earlier copy=%q", kept)
	}
}

func TestSkillInstallRefusesALinkedTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need extra privileges on Windows")
	}
	skills := t.TempDir()
	elsewhere := filepath.Join(t.TempDir(), "notes.md")
	noErr(t, os.WriteFile(elsewhere, []byte("not a skill\n"), 0o644))
	noErr(t, os.MkdirAll(filepath.Join(skills, "owngit-checks"), 0o755))
	noErr(t, os.Symlink(elsewhere, filepath.Join(skills, "owngit-checks", "SKILL.md")))
	for _, replace := range []bool{false, true} {
		if _, err := installSkill(skills, replace, time.Now()); commandErrorCode(err) != "skill_target_invalid" {
			t.Fatalf("replace=%v err=%v", replace, err)
		}
	}
	if content, _ := os.ReadFile(elsewhere); string(content) != "not a skill\n" {
		t.Fatal("the install wrote through a symbolic link")
	}
}

func TestSkillFlagsRequireOneAction(t *testing.T) {
	for _, arguments := range [][]string{
		nil, {"--replace"}, {"--print", "--replace"}, {"--print", "--install", t.TempDir()},
	} {
		if _, err := captureStdout(func() error { return skillCommand(arguments) }); commandErrorCode(err) != "invalid_arguments" {
			t.Errorf("skill %v err=%v, want invalid_arguments", arguments, err)
		}
	}
}
