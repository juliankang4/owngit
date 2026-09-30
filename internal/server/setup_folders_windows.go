package server

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

func chooserFolder(entry fs.DirEntry) (bool, error) {
	// Windows ReadDir retains each entry's own attributes; Info does no I/O.
	info, err := entry.Info()
	if err != nil {
		return false, err
	}
	attributes, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return false, errors.New("unsupported directory entry attributes")
	}
	return attributes.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0, nil
}

func chooserHidden(path, name string) (bool, error) {
	if strings.HasPrefix(name, ".") {
		return true, nil
	}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	// GetFileAttributes returns the link's own attributes, not its target's.
	attributes, err := windows.GetFileAttributes(pointer)
	return attributes&windows.FILE_ATTRIBUTE_HIDDEN != 0, err
}

func platformFolderName(name string) bool {
	if len(utf16.Encode([]rune(name))) > 255 || strings.ContainsAny(name, `<>:"|?*`) || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return false
	}
	base, _, _ := strings.Cut(name, ".")
	base = strings.ToUpper(strings.TrimRight(base, " "))
	switch base {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return false
	}
	if len([]rune(base)) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) {
		last := []rune(base)[3]
		if (last >= '1' && last <= '9') || strings.ContainsRune("¹²³", last) {
			return false
		}
	}
	return true
}

func folderNotDirectory(err error) bool {
	return errors.Is(err, windows.ERROR_DIRECTORY)
}

func folderParent(path string) (string, bool) {
	parent := filepath.Dir(path)
	if parent == path {
		return "", true
	}
	return parent, false
}

func folderRoots(ctx context.Context) (folderResult, error) {
	if err := ctx.Err(); err != nil {
		return folderResult{}, err
	}
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return folderResult{}, err
	}
	result := folderResult{Folders: []folderEntry{}}
	for drive := 0; drive < 26; drive++ {
		if mask&(1<<drive) != 0 {
			path := string(rune('A'+drive)) + `:\`
			result.Folders = append(result.Folders, folderEntry{Name: path, Path: path})
		}
	}
	return result, nil
}
