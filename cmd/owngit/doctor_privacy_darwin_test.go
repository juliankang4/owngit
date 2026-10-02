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
	root := t.TempDir()
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
