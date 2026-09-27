//go:build windows

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func TestProtectedServiceCopyAndFolders(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("setting the Administrators owner needs administrator rights")
	}
	paths := serviceInstallPaths{
		Directory: filepath.Join(t.TempDir(), "OwnGit"),
	}
	paths.Executable = filepath.Join(paths.Directory, "owngit.exe")
	paths.Temp = filepath.Join(paths.Directory, "temp")
	noErr(t, platformPrepareServiceStorage(paths))
	source, err := os.Executable()
	noErr(t, err)
	content, err := os.ReadFile(source)
	noErr(t, err)
	noErr(t, os.WriteFile(paths.Executable, content, 0o755))
	if err := platformReplaceServiceCopy(paths.Executable, paths); err == nil {
		t.Fatal("an unprotected service copy was protected in place")
	}
	noErr(t, platformReplaceServiceCopy(source, paths))
	noErr(t, verifyProtectedServiceACL(paths.Directory, true, true))
	noErr(t, verifyProtectedServiceACL(paths.Temp, true, false))
	noErr(t, verifyProtectedServiceACL(paths.Executable, false, true))
	assertMediumCannotReplace(t, paths.Executable)
}

func TestServiceStorageRefusesUnsafeExistingPaths(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("checking the elevated preparation needs administrator rights")
	}
	check := func(t *testing.T, directory string) {
		t.Helper()
		paths := serviceInstallPaths{Directory: directory, Executable: filepath.Join(directory, "owngit.exe"), Temp: filepath.Join(directory, "temp")}
		if err := platformPrepareServiceStorage(paths); err == nil {
			t.Fatal("an unsafe existing service path was protected in place")
		}
	}
	t.Run("user-writable folder", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "OwnGit")
		noErr(t, os.Mkdir(directory, 0o700))
		check(t, directory)
	})
	t.Run("folder junction", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target")
		junction := filepath.Join(root, "OwnGit")
		noErr(t, os.Mkdir(target, 0o700))
		if output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", junction, target).CombinedOutput(); err != nil {
			t.Fatalf("create test junction: %v: %s", err, output)
		}
		check(t, junction)
	})
}

func TestInstallCreatesFreshServiceFolder(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("creating protected storage needs administrator rights")
	}
	root := t.TempDir()
	directory := filepath.Join(root, "OwnGit")
	noErr(t, os.Mkdir(directory, 0o700))
	noErr(t, os.WriteFile(filepath.Join(directory, "owner.txt"), []byte("keep"), 0o600))
	moved, err := platformPrepareServiceInstall(serviceInstallPaths{Directory: directory})
	noErr(t, err)
	noErr(t, verifyProtectedServiceACL(directory, true, true))
	if _, err := os.Stat(filepath.Join(directory, "owner.txt")); moved == "" || !os.IsNotExist(err) {
		t.Fatalf("fresh folder reused old content: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(moved, "owner.txt")); err != nil || string(data) != "keep" {
		t.Fatalf("moved content=%q, error=%v", data, err)
	}
}

func TestInstalledServiceCopyRejectsMediumWrite(t *testing.T) {
	if os.Getenv("OWNGIT_TEST_INSTALLED_SERVICE") != "1" {
		t.Skip("set OWNGIT_TEST_INSTALLED_SERVICE=1 on an assigned Windows test VM")
	}
	paths, err := platformServiceInstallPaths()
	noErr(t, err)
	noErr(t, verifyProtectedServiceACL(paths.Directory, true, true))
	noErr(t, verifyProtectedServiceACL(paths.Temp, true, false))
	noErr(t, verifyProtectedServiceACL(paths.Executable, false, true))
	assertMediumCannotReplace(t, paths.Executable)
}

func assertMediumCannotReplace(t *testing.T, target string) {
	t.Helper()
	before, err := os.ReadFile(target)
	noErr(t, err)
	if len(before) == 0 {
		t.Fatal("the protected service copy is empty")
	}
	attacker := filepath.Join(t.TempDir(), "attacker.exe")
	noErr(t, os.WriteFile(attacker, []byte("synthetic attacker"), 0o600))
	token, err := withoutAdminToken()
	noErr(t, err)
	defer token.Close()
	command := exec.Command("cmd.exe", "/d", "/c", "copy", "/y", attacker, target)
	command.SysProcAttr = &syscall.SysProcAttr{Token: syscall.Token(token)}
	output, copyErr := command.CombinedOutput()
	if command.ProcessState == nil {
		t.Fatalf("the Medium-integrity copy did not start: %v: %s", copyErr, output)
	}
	if copyErr == nil {
		t.Fatalf("the Medium-integrity copy replaced the protected executable: %s", output)
	}
	after, err := os.ReadFile(target)
	noErr(t, err)
	if !bytes.Equal(after, before) {
		t.Fatal("the protected executable changed after the Medium-integrity copy attempt")
	}
}

func TestServiceSourceLockPreventsReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owngit.exe")
	noErr(t, os.WriteFile(path, []byte("original"), 0o600))
	locked, err := openServiceSource(path)
	noErr(t, err)
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err == nil {
		locked.Close()
		t.Fatal("a source executable was overwritten while held for UAC")
	}
	if err := os.Rename(path, path+".old"); err == nil {
		locked.Close()
		t.Fatal("a source executable was renamed while held for UAC")
	}
	noErr(t, locked.Close())
	noErr(t, os.WriteFile(path, []byte("replacement"), 0o600))
}

func TestAdministratorControlledPathRejectsAnOrdinaryFolder(t *testing.T) {
	if _, err := administratorControlledDescriptor(t.TempDir(), true); err == nil {
		t.Error("an ordinary user-owned folder passed the administrator-controlled path check")
	}
}

func TestRegisteredAppInstallerWinget(t *testing.T) {
	system, _ := windows.GetSystemDirectory()
	output, _ := exec.Command(filepath.Join(system, `WindowsPowerShell\v1.0\powershell.exe`), "-NoProfile", "-Command", `(Get-AppxPackage Microsoft.DesktopAppInstaller).InstallLocation`).Output()
	registered := strings.TrimSpace(string(output))
	if registered == "" {
		t.Skip("App Installer is not registered for this account")
	}
	winget, err := platformTrustedWinget()
	if err != nil || !strings.EqualFold(winget, filepath.Join(registered, "winget.exe")) {
		t.Fatalf("trusted winget = %q, %v; registered in %q", winget, err, registered)
	}
}

func TestRepositoryRootInternalServiceActionIsDispatched(t *testing.T) {
	err := serviceCommand([]string{"repository-root", "--not-a-flag"})
	if err == nil || strings.Contains(err.Error(), `unknown service command "repository-root"`) {
		t.Errorf("repository-root dispatch: %v", err)
	}
}

func TestExpandSystemPathUsesOnlyKnownSystemVariables(t *testing.T) {
	variables := map[string]string{"SystemRoot": `C:\Windows`, "ProgramFiles": `C:\Program Files`}
	got, err := expandSystemPath(`%SystemRoot%\System32;%PROGRAMFILES%\Tools`, variables)
	noErr(t, err)
	if want := `C:\Windows\System32;C:\Program Files\Tools`; got != want {
		t.Errorf("expanded path %q, want %q", got, want)
	}
	if _, err := expandSystemPath(`%USERPROFILE%\bin`, variables); err == nil {
		t.Error("a user environment variable was expanded into the system PATH")
	}
}

func TestServiceEnvironmentDoesNotInheritUserValues(t *testing.T) {
	paths, err := platformServiceInstallPaths()
	noErr(t, err)
	previousPath, hadPath := os.LookupEnv("PATH")
	previousModules, hadModules := os.LookupEnv("PSModulePath")
	noErr(t, os.Setenv("PATH", `C:\attacker`))
	noErr(t, os.Setenv("PSModulePath", `C:\attacker\Modules`))
	t.Cleanup(func() {
		if hadPath {
			_ = os.Setenv("PATH", previousPath)
		} else {
			_ = os.Unsetenv("PATH")
		}
		if hadModules {
			_ = os.Setenv("PSModulePath", previousModules)
		} else {
			_ = os.Unsetenv("PSModulePath")
		}
	})
	environment, err := platformServiceEnvironment(paths, []string{"OWNGIT_TEST=value"})
	noErr(t, err)
	joined := strings.Join(environment, "\n")
	if strings.Contains(strings.ToLower(joined), `c:\attacker`) {
		t.Errorf("inherited a user value:\n%s", joined)
	}
	for _, want := range []string{"Path=", "PSModulePath=", "TEMP=" + paths.Temp, "TMP=" + paths.Temp, "OWNGIT_TEST=value"} {
		if !strings.Contains(joined, want) {
			t.Errorf("environment lacks %q:\n%s", want, joined)
		}
	}
}
