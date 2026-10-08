package actions

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func commandReadFlags() int {
	return os.O_RDONLY | int(windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_OVERLAPPED)
}

func singleLink(file *os.File, _ os.FileInfo) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return err
	}
	if info.NumberOfLinks != 1 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("file must be regular with one link")
	}
	return nil
}
