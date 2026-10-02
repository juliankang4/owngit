//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"owngit/internal/doctor"
	"owngit/internal/webui"
)

func TestDoctorReportsRepositoryRootSeparately(t *testing.T) {
	private := t.TempDir()
	if findings := repositoryPrivacyFindings(doctorSubject{repositories: private}); len(findings) != 0 {
		t.Fatalf("private root findings=%+v", findings)
	}
	for _, withRepository := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "private child"}[withRepository], func(t *testing.T) {
			root := t.TempDir()
			noErr(t, os.Chmod(root, 0o777))
			var ids []string
			if withRepository {
				noErr(t, os.Mkdir(filepath.Join(root, "private.git"), 0o700))
				ids = []string{"private"}
			}
			before, err := os.Stat(root)
			noErr(t, err)
			findings := repositoryPrivacyFindings(doctorSubject{
				server: doctor.ServerRunning, setupComplete: true, repositories: root, repositoryIDs: ids,
			})
			if len(findings) != 1 || findings[0].Code != webui.MsgDoctorRepositoryRootShared || findings[0].Args[0] != root || findings[0].Repair == "" {
				t.Fatalf("findings=%+v", findings)
			}
			after, err := os.Stat(root)
			noErr(t, err)
			if before.Mode() != after.Mode() {
				t.Fatalf("doctor changed root mode from %v to %v", before.Mode(), after.Mode())
			}
		})
	}
}

func TestDoctorReportsRepositoryRootInspectionFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-folder")
	noErr(t, os.WriteFile(path, []byte("unchanged"), 0o600))
	findings := repositoryPrivacyFindings(doctorSubject{repositories: path})
	if len(findings) != 1 || findings[0].Code != webui.MsgDoctorRepositoryRootUnchecked || !findings[0].Unchecked || findings[0].Repair != "" {
		t.Fatalf("findings=%+v", findings)
	}
	content, err := os.ReadFile(path)
	noErr(t, err)
	if string(content) != "unchanged" {
		t.Fatalf("doctor changed root fixture: %q", content)
	}
}

func TestDoctorDoesNotFollowRepositoryLink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside.git")
	noErr(t, os.Mkdir(target, 0o700))
	noErr(t, os.Chmod(target, 0o777))
	noErr(t, os.Symlink(target, filepath.Join(root, "linked.git")))
	findings := repositoryPrivacyFindings(doctorSubject{repositories: root, repositoryIDs: []string{"linked"}})
	if len(findings) != 1 || findings[0].Code != webui.MsgDoctorUncheckedOwner || !findings[0].Unchecked || findings[0].Repair != "" {
		t.Fatalf("findings=%+v", findings)
	}
	info, err := os.Stat(target)
	noErr(t, err)
	if info.Mode().Perm() != 0o777 {
		t.Fatalf("doctor changed link target mode to %o", info.Mode().Perm())
	}
}

func TestDoctorGuidesLinkedHookRecovery(t *testing.T) {
	for _, entry := range []string{"hooks", "hooks/update"} {
		t.Run(entry, func(t *testing.T) {
			root := t.TempDir()
			repository := filepath.Join(root, "sample.git")
			noErr(t, os.Mkdir(repository, 0o700))
			external := t.TempDir()
			if entry == "hooks" {
				noErr(t, os.Symlink(external, filepath.Join(repository, "hooks")))
			} else {
				noErr(t, os.Mkdir(filepath.Join(repository, "hooks"), 0o700))
				noErr(t, os.Symlink(filepath.Join(external, "outside"), filepath.Join(repository, "hooks", "update")))
			}
			findings := repositoryPrivacyFindings(doctorSubject{repositories: root, repositoryIDs: []string{"sample"}})
			if len(findings) != 1 || findings[0].Code != webui.MsgDoctorRepositoryHooksUnsafe || findings[0].Args[0] != "sample" || findings[0].Args[1] != entry || findings[0].Repair == "" {
				t.Fatalf("findings=%+v", findings)
			}
			if !strings.Contains(findings[0].Repair, filepath.Join(repository, entry)) || !strings.Contains(findings[0].Repair, repository+".") {
				t.Fatalf("repair=%q", findings[0].Repair)
			}
		})
	}
}

func TestDoctorReportsExposedRepositoryFoldersWithoutChangingThem(t *testing.T) {
	root := t.TempDir()
	private := filepath.Join(root, "private.git")
	exposed := filepath.Join(root, "exposed.git")
	noErr(t, os.Mkdir(private, 0o700))
	noErr(t, os.Mkdir(exposed, 0o700))
	noErr(t, os.Chmod(private, 0o700))
	noErr(t, os.Chmod(exposed, 0o777))

	subject := doctorSubject{
		server: doctor.ServerRunning, setupComplete: true, repositories: root,
		repositoryIDs: []string{"private", "exposed", "missing"},
	}
	findings := repositoryPrivacyFindings(subject)
	if len(findings) != 1 || findings[0].Code != webui.MsgDoctorRepositoriesShared ||
		len(findings[0].Args) != 2 || findings[0].Args[0] != "1" || findings[0].Args[1] != root {
		t.Fatalf("findings=%+v", findings)
	}
	if !strings.Contains(findings[0].Repair, exposed) {
		t.Fatalf("repair does not name the exposed repository: %q", findings[0].Repair)
	}
	if runtime.GOOS == "darwin" && !strings.Contains(findings[0].Repair, "chmod -RN") {
		t.Fatalf("macOS repair does not clear ACLs: %q", findings[0].Repair)
	}
	info, err := os.Stat(exposed)
	noErr(t, err)
	if info.Mode().Perm() != 0o777 {
		t.Fatalf("doctor changed permissions to %o", info.Mode().Perm())
	}
}
