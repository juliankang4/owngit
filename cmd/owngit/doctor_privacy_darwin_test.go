//go:build darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"owngit/internal/doctor"
	"owngit/internal/webui"
)

func TestDoctorReportsRepositoryWriteACL(t *testing.T) {
	t.Setenv("TMPDIR", "/tmp")
	root := t.TempDir()
	makeDoctorFixtureReachable(t, root)
	noErr(t, os.Chmod(root, 0o755))
	path := filepath.Join(root, "shared.git")
	noErr(t, os.Mkdir(path, 0o700))
	noErr(t, exec.Command("chmod", "+a", "everyone allow add_file,add_subdirectory,delete_child", path).Run())
	findings := repositoryPrivacyFindings(doctorSubject{
		server: doctor.ServerRunning, setupComplete: true, repositories: root, repositoryIDs: []string{"shared"},
	})
	if len(findings) != 1 || findings[0].Code != webui.MsgDoctorRepositoriesShared || findings[0].Args[0] != "1" {
		t.Fatalf("findings=%+v", findings)
	}
}

func TestDoctorReportsRepositoryBelowSearchACL(t *testing.T) {
	t.Setenv("TMPDIR", "/tmp")
	parent := t.TempDir()
	makeDoctorFixtureReachable(t, parent)
	noErr(t, os.Chmod(parent, 0o700))
	noErr(t, exec.Command("chmod", "+a", "everyone allow search", parent).Run())
	root := filepath.Join(parent, "repositories")
	noErr(t, os.Mkdir(root, 0o777))
	noErr(t, os.Chmod(root, 0o777))

	findings := repositoryPrivacyFindings(doctorSubject{repositories: root})
	if len(findings) != 1 || findings[0].Code != webui.MsgDoctorRepositoryRootShared {
		t.Fatalf("findings=%+v", findings)
	}
}

func TestDoctorReportsRepositoryBelowSharedGroupSearch(t *testing.T) {
	t.Setenv("TMPDIR", "/tmp")
	parent := t.TempDir()
	makeDoctorFixtureReachable(t, parent)
	noErr(t, os.Chown(parent, -1, os.Getegid()))
	noErr(t, os.Chmod(parent, 0o750))
	root := filepath.Join(parent, "repositories")
	noErr(t, os.Mkdir(root, 0o777))
	noErr(t, os.Chmod(root, 0o777))

	findings := repositoryPrivacyFindings(doctorSubject{repositories: root})
	if len(findings) != 1 || findings[0].Code != webui.MsgDoctorRepositoryRootShared {
		t.Fatalf("findings=%+v", findings)
	}
}
