//go:build !windows

package state

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
)

// PrivateDirectoryFix returns, but never runs, the shell command that makes a
// directory private to this account. Recursive mode also repairs everything
// already below a hosted repository folder.
func PrivateDirectoryFix(path string, recursive bool) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("%s is not a plain directory", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", errors.New("directory owner is unavailable")
	}
	operand := shellQuote(operandPath(path))
	var commands []string
	if int(stat.Uid) != os.Geteuid() {
		recurse := ""
		if recursive {
			recurse = "-R "
		}
		commands = append(commands, fmt.Sprintf("sudo chown %s%d:%d %s", recurse, os.Geteuid(), os.Getegid(), operand))
	}
	if runtime.GOOS == "darwin" {
		flag := "-N"
		if recursive {
			flag = "-RN"
		}
		commands = append(commands, "chmod "+flag+" "+operand)
	}
	if recursive {
		commands = append(commands, "chmod -R go-rwx "+operand)
	} else {
		commands = append(commands, "chmod go-w "+operand)
	}
	return strings.Join(commands, " && "), nil
}
