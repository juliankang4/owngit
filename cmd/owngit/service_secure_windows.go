//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	trustedInstallerSID = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"
	systemSID           = "S-1-5-18"
	usersSID            = "S-1-5-32-545"
)

func platformServiceInstallPaths() (serviceInstallPaths, error) {
	programFiles, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	if err != nil {
		return serviceInstallPaths{}, fmt.Errorf("find Program Files: %w", err)
	}
	directory := filepath.Join(programFiles, "OwnGit")
	return serviceInstallPaths{
		Directory:  directory,
		Executable: filepath.Join(directory, "owngit.exe"),
		Temp:       filepath.Join(directory, "temp"),
	}, nil
}

func platformPrepareServiceInstall(paths serviceInstallPaths, keepExisting bool) (string, error) {
	_, err := os.Lstat(paths.Directory)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if keepExisting || err != nil {
		return "", nil
	}
	moved := paths.Directory + ".old-" + time.Now().UTC().Format("20060102T150405.000000000")
	return moved, os.Rename(paths.Directory, moved)
}

func platformPrepareServiceStorage(paths serviceInstallPaths) error {
	for _, path := range []struct {
		name      string
		directory bool
	}{{paths.Directory, true}, {paths.Temp, true}, {paths.Executable, false}} {
		if _, err := administratorControlledDescriptor(path.name, path.directory); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("refuse an unsafe OwnGit service path: %w", err)
		}
	}
	if err := os.MkdirAll(paths.Directory, 0o755); err != nil {
		return fmt.Errorf("create the OwnGit service folder: %w", err)
	}
	if err := setProtectedServiceACL(paths.Directory, true, true); err != nil {
		return fmt.Errorf("protect the OwnGit service folder: %w", err)
	}
	if err := os.MkdirAll(paths.Temp, 0o700); err != nil {
		return fmt.Errorf("create the OwnGit administrator temporary folder: %w", err)
	}
	if err := setProtectedServiceACL(paths.Temp, true, false); err != nil {
		return fmt.Errorf("protect the OwnGit administrator temporary folder: %w", err)
	}
	return nil
}

// platformReplaceServiceCopy atomically replaces the task executable from a
// locked source through the administrator-only temporary directory.
func platformReplaceServiceCopy(source string, paths serviceInstallPaths) error {
	input, err := openServiceSource(source)
	if err != nil {
		return fmt.Errorf("open this owngit.exe: %w", err)
	}
	defer input.Close()
	output, err := os.CreateTemp(paths.Temp, "owngit-*.exe")
	if err != nil {
		return fmt.Errorf("create the protected service copy: %w", err)
	}
	temporary := output.Name()
	defer os.Remove(temporary)
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return fmt.Errorf("copy owngit.exe into Program Files: %w", err)
	}
	if err := output.Sync(); err != nil {
		output.Close()
		return fmt.Errorf("finish the protected service copy: %w", err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close the protected service copy: %w", err)
	}
	if err := setProtectedServiceACL(temporary, false, true); err != nil {
		return fmt.Errorf("protect the service copy: %w", err)
	}
	from, err := windows.UTF16PtrFromString(temporary)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(paths.Executable)
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return fmt.Errorf("replace the OwnGit service copy: %w", err)
	}
	if err := verifyProtectedServiceACL(paths.Executable, false, true); err != nil {
		return fmt.Errorf("verify the OwnGit service copy: %w", err)
	}
	return nil
}

// openServiceSource holds the executable without write or delete sharing, so
// its path cannot be replaced while an elevated helper copies it.
func openServiceSource(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}

func protectedServiceDescriptor(directory, usersRead bool) (*windows.SECURITY_DESCRIPTOR, error) {
	inherit := ""
	if directory {
		inherit = "OICI"
	}
	sddl := "O:BAD:P" +
		"(A;" + inherit + ";FA;;;SY)" +
		"(A;" + inherit + ";FA;;;BA)"
	if usersRead {
		sddl += "(A;" + inherit + ";0x1200a9;;;BU)"
	}
	return windows.SecurityDescriptorFromString(sddl)
}

func setProtectedServiceACL(path string, directory, usersRead bool) error {
	descriptor, err := protectedServiceDescriptor(directory, usersRead)
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	enablePrivileges("SeRestorePrivilege", "SeTakeOwnershipPrivilege")
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner, nil, dacl, nil); err != nil {
		return err
	}
	return verifyProtectedServiceACL(path, directory, usersRead)
}

func verifyProtectedServiceACL(path string, directory, usersRead bool) error {
	actual, err := administratorControlledDescriptor(path, directory)
	if err != nil {
		return err
	}
	expected, err := protectedServiceDescriptor(directory, usersRead)
	if err != nil {
		return err
	}
	actualText := "<none>"
	if actual != nil {
		actualText = actual.String()
	}
	if strings.Replace(actualText, "D:PAI", "D:P", 1) != expected.String() {
		return fmt.Errorf("owner or access list is %s, want %s", actualText, expected.String())
	}
	return nil
}

// platformTrustedWinget accepts only the Store-signed App Installer package
// registered for this account. Registration supplies the install location;
// a similarly named Administrators-owned file is never searched or accepted.
func platformTrustedWinget() (string, error) {
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return "", err
	}
	const lookup = `$ErrorActionPreference = 'Stop'
Import-Module "$env:SystemRoot\System32\WindowsPowerShell\v1.0\Modules\Appx\Appx.psd1"
$package = @(Appx\Get-AppxPackage -Name Microsoft.DesktopAppInstaller -PackageTypeFilter Main | Where-Object { $_.PackageFamilyName -eq 'Microsoft.DesktopAppInstaller_8wekyb3d8bbwe' -and $_.SignatureKind -eq 'Store' -and $_.Status -eq 'Ok' })
if ($package.Count -ne 1) { exit 1 }
$package[0].InstallLocation`
	output, err := exec.Command(filepath.Join(system, `WindowsPowerShell\v1.0\powershell.exe`),
		"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", lookup).Output()
	if err != nil {
		return "", errors.New("no registered Store copy of App Installer was found")
	}
	packageDir := filepath.Clean(strings.TrimSpace(string(output)))
	programFiles, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	if err != nil {
		return "", err
	}
	windowsApps := filepath.Join(programFiles, "WindowsApps")
	if packageDir == "." || !strings.EqualFold(filepath.Dir(packageDir), windowsApps) {
		return "", errors.New("App Installer has an unexpected registered location")
	}
	winget := filepath.Join(packageDir, "winget.exe")
	for _, path := range []struct {
		name      string
		directory bool
	}{{windowsApps, true}, {packageDir, true}, {winget, false}} {
		if _, err := administratorControlledDescriptor(path.name, path.directory); err != nil {
			return "", err
		}
	}
	return winget, nil
}

func administratorControlledDescriptor(path string, directory bool) (*windows.SECURITY_DESCRIPTOR, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, err := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(handle)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return nil, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory {
		return nil, errors.New("unexpected file type")
	}
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, err
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil || owner.String() != trustedInstallerSID && owner.String() != administratorsSID {
		return nil, errors.New("path has an unexpected owner")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return nil, errors.New("path has no inspectable access list")
	}
	trustedWriters := map[string]bool{trustedInstallerSID: true, administratorsSID: true, systemSID: true}
	const writeAccess = windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_EA |
		windows.FILE_WRITE_ATTRIBUTES | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER |
		windows.GENERIC_WRITE | windows.GENERIC_ALL | 0x40 // FILE_DELETE_CHILD for a directory
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil || ace == nil {
			return nil, errors.New("path access list cannot be inspected")
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		const accessAllowedCallbackACEType = 0x09
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE && ace.Header.AceType != accessAllowedCallbackACEType {
			return nil, errors.New("path has an unsupported access entry")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if ace.Mask&writeAccess != 0 && !trustedWriters[sid] {
			return nil, fmt.Errorf("path grants write access to %s", sid)
		}
	}
	return descriptor, nil
}

// platformServiceEnvironment returns the complete environment of a process
// started while the installer has administrator rights. It contains the
// machine PATH, system PowerShell modules and an administrator-only TEMP/TMP,
// with no value from the user's persistent environment.
func platformServiceEnvironment(paths serviceInstallPaths, extra []string) ([]string, error) {
	windowsDir, err := windows.KnownFolderPath(windows.FOLDERID_Windows, 0)
	if err != nil {
		return nil, err
	}
	programFiles, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	if err != nil {
		return nil, err
	}
	programFilesX86, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFilesX86, 0)
	if err != nil {
		return nil, err
	}
	programData, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		return nil, err
	}
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return nil, err
	}
	machinePath := system + ";" + windowsDir + ";" + filepath.Join(system, "Wbem") + ";" + filepath.Join(system, "WindowsPowerShell", "v1.0")
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`, registry.QUERY_VALUE)
	if err == nil {
		defer key.Close()
		if value, _, valueErr := key.GetStringValue("Path"); valueErr == nil {
			if expanded, expandErr := expandSystemPath(value, map[string]string{
				"SystemRoot": windowsDir, "windir": windowsDir, "SystemDrive": filepath.VolumeName(windowsDir),
				"ProgramFiles": programFiles, "ProgramW6432": programFiles, "ProgramFiles(x86)": programFilesX86,
			}); expandErr == nil {
				machinePath = expanded
			}
		}
	}
	environment := []string{
		"ALLUSERSPROFILE=" + programData,
		"ComSpec=" + filepath.Join(system, "cmd.exe"),
		"OS=Windows_NT",
		"Path=" + machinePath,
		"PATHEXT=.COM;.EXE;.BAT;.CMD",
		"ProgramData=" + programData,
		"ProgramFiles=" + programFiles,
		"ProgramFiles(x86)=" + programFilesX86,
		"ProgramW6432=" + programFiles,
		"PSModulePath=" + filepath.Join(programFiles, "WindowsPowerShell", "Modules") + ";" + filepath.Join(system, "WindowsPowerShell", "v1.0", "Modules"),
		"SystemDrive=" + filepath.VolumeName(windowsDir),
		"SystemRoot=" + windowsDir,
		"TEMP=" + paths.Temp,
		"TMP=" + paths.Temp,
		"windir=" + windowsDir,
	}
	for _, entry := range extra {
		if name, value, found := strings.Cut(entry, "="); !found || name == "" || strings.ContainsAny(name, "\x00=") || strings.ContainsRune(value, '\x00') {
			return nil, fmt.Errorf("invalid administrator environment entry %q", entry)
		}
		environment = append(environment, entry)
	}
	return environment, nil
}

func expandSystemPath(value string, variables map[string]string) (string, error) {
	var expanded strings.Builder
	for len(value) > 0 {
		start := strings.IndexByte(value, '%')
		if start < 0 {
			expanded.WriteString(value)
			break
		}
		expanded.WriteString(value[:start])
		value = value[start+1:]
		end := strings.IndexByte(value, '%')
		if end < 0 {
			return "", errors.New("machine PATH has an unmatched percent sign")
		}
		name := value[:end]
		replacement := ""
		for candidate, content := range variables {
			if strings.EqualFold(candidate, name) {
				replacement = content
				break
			}
		}
		if replacement == "" {
			return "", fmt.Errorf("machine PATH uses unsupported variable %%%s%%", name)
		}
		expanded.WriteString(replacement)
		value = value[end+1:]
	}
	return expanded.String(), nil
}

func platformApplyServiceEnvironment(environment []string) error {
	os.Clearenv()
	for _, entry := range environment {
		name, value, _ := strings.Cut(entry, "=")
		if err := os.Setenv(name, value); err != nil {
			return err
		}
	}
	return nil
}

func platformRunWithEnvironment(ctx context.Context, environment []string, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Env = append([]string(nil), environment...)
	return command.CombinedOutput()
}

// platformRunAttachedWithEnvironment runs a program on this console and
// returns its exit code.
func platformRunAttachedWithEnvironment(environment []string, name string, args ...string) (int, error) {
	command := exec.Command(name, args...)
	command.Env = append([]string(nil), environment...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := command.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	return 0, err
}

// platformGitOnServicePath reports whether git.exe is in a folder of the
// machine or user PATH saved in the registry. A task gets that PATH, while
// a terminal keeps the one from when it was opened.
func platformGitOnServicePath() bool {
	paths := ""
	for root, path := range map[registry.Key]string{registry.LOCAL_MACHINE: `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`, registry.CURRENT_USER: "Environment"} {
		if key, err := registry.OpenKey(root, path, registry.QUERY_VALUE); err == nil {
			value, _, _ := key.GetStringValue("Path")
			key.Close()
			paths += ";" + value
		}
	}
	paths, _ = registry.ExpandString(paths)
	for _, folder := range filepath.SplitList(paths) {
		if info, err := os.Stat(filepath.Join(folder, "git.exe")); filepath.IsAbs(folder) && err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}
