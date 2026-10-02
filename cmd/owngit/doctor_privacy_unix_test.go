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
