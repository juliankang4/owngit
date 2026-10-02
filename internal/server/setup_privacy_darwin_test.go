//go:build darwin

package server

import (
	"os"
	"os/exec"
	"testing"

	"owngit/internal/webui"
)

func TestMacOSSetupWarnsForWriteACL(t *testing.T) {
	app, _, root := newTestApp(t)
	noErr(t, os.Mkdir(root, 0o700))
	noErr(t, exec.Command("chmod", "+a", "everyone allow add_file,add_subdirectory,delete_child", root).Run())
	feedback := app.CheckRepositoryFolder(root)
	if len(feedback.Problems) != 0 || len(feedback.Warnings) != 1 || feedback.Warnings[0].Code != webui.MsgSetupStorageShared {
		t.Fatalf("folder check=%+v", feedback)
	}
}
