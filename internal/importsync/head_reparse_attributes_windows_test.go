//go:build windows

package importsync

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// attributeFileInfo attaches native Windows attribute metadata to the shared
// fakeFileInfo, whose Sys is otherwise nil. The mode stays synthetic, so a
// guard that looked only at mode bits would accept a reparse point that looks
// like a direct regular file or directory.
type attributeFileInfo struct {
	fakeFileInfo
	attributes syscall.Win32FileAttributeData
}

func (f attributeFileInfo) Sys() any { return &f.attributes }

// TestDirectPathGuardsCheckWindowsReparseAttributes isolates the platform
// reparse attribute check from the mode guards. A real junction supplies the
// REPARSE_POINT metadata read from Lstat, while every FileInfo mode passed to
// the guards is synthetic. The fixture reuses the junction helper from
// head_windows_test.go; this metadata test complements those native
// end-to-end tests and is not by itself publication evidence.
func TestDirectPathGuardsCheckWindowsReparseAttributes(t *testing.T) {
	target := t.TempDir()
	junction := filepath.Join(target, "junction")
	makeJunction(t, junction, target)

	junctionAttributes := lstatAttributes(t, junction)
	require(t, junctionAttributes.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0,
		"junction attributes %#x are not a reparse point", junctionAttributes.FileAttributes)
	targetAttributes := lstatAttributes(t, target)
	require(t, targetAttributes.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT == 0,
		"direct target attributes %#x are a reparse point", targetAttributes.FileAttributes)

	reparseFile := attributeFileInfo{fakeFileInfo{mode: 0}, junctionAttributes}
	reparseDirectory := attributeFileInfo{fakeFileInfo{mode: os.ModeDir}, junctionAttributes}
	directFile := attributeFileInfo{fakeFileInfo{mode: 0}, targetAttributes}
	directDir := attributeFileInfo{fakeFileInfo{mode: os.ModeDir}, targetAttributes}

	require(t, !directRegularFile(junction, reparseFile),
		"regular mode with a reparse attribute passed directRegularFile")
	require(t, !directDirectory(junction, reparseDirectory),
		"directory mode with a reparse attribute passed directDirectory")
	require(t, directRegularFile(target, directFile), "non-reparse regular mode failed directRegularFile")
	require(t, directDirectory(target, directDir), "non-reparse directory mode failed directDirectory")
}

func lstatAttributes(t *testing.T, path string) syscall.Win32FileAttributeData {
	t.Helper()
	info, err := os.Lstat(path)
	noErr(t, err)
	attributes, ok := info.Sys().(*syscall.Win32FileAttributeData)
	require(t, ok && attributes != nil, "Lstat(%s) attributes type %T", path, info.Sys())
	return *attributes
}
