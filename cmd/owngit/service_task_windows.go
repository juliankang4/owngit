//go:build windows

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"owngit/internal/gitexec"
	"owngit/internal/service"
)

var (
	// inheritedEnvironment is captured before an elevated helper replaces its
	// own environment. Restricted copies need the caller's ordinary user
	// environment, not the administrator-only TEMP and module paths.
	inheritedEnvironment      = os.Environ()
	advapi32                  = windows.NewLazySystemDLL("advapi32.dll")
	procCreateRestrictedToken = advapi32.NewProc("CreateRestrictedToken")
	shell32                   = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteEx        = shell32.NewProc("ShellExecuteExW")
	kernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	procAttachConsole         = kernel32.NewProc("AttachConsole")
	procFreeConsole           = kernel32.NewProc("FreeConsole")
	wtsapi32                  = windows.NewLazySystemDLL("wtsapi32.dll")
	procWTSEnumerateProcesses = wtsapi32.NewProc("WTSEnumerateProcessesExW")
	procWTSFreeMemoryEx       = wtsapi32.NewProc("WTSFreeMemoryExW")
)

// copyVariable marks a copy of owngit that another one started (see
// runCopy). A copy never starts a further copy.
const copyVariable = "OWNGIT_STARTED_BY_OWNGIT"

func platformCurrentAccountSID() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}

func platformSystemDirectory() (string, error) { return windows.GetSystemDirectory() }

// wtsProcessInfo is WTS_PROCESS_INFO_EXW.
type wtsProcessInfo struct {
	session, process                   uint32
	name                               *uint16
	user                               *windows.SID
	threads, handles, pagefile, peakPF uint32
	workingSet, peakWorkingSet         uint32
	userTime, kernelTime               int64
}

// platformRequestingProcess returns the account and the program of the
// process that started this one, as Windows lists them for every process
// to an administrator. After a UAC prompt that is the OwnGit that asked,
// whatever account approved. parentProcess refuses a process that started
// after this one, so a reused process ID cannot stand in for the parent,
// and its open handle keeps the ID taken while it is looked up.
func platformRequestingProcess() (sid, program string, err error) {
	parent, err := parentProcess()
	if err != nil {
		return "", "", err
	}
	defer windows.CloseHandle(parent)
	id, err := windows.GetProcessId(parent)
	if err != nil {
		return "", "", err
	}
	name := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(name))
	if err := windows.QueryFullProcessImageName(parent, 0, &name[0], &size); err != nil {
		return "", "", fmt.Errorf("read the program of process %d: %w", id, err)
	}
	program = windows.UTF16ToString(name[:size])
	const anySession, levelOne = 0xFFFFFFFE, 1
	level := uint32(levelOne)
	var list *wtsProcessInfo
	var count uint32
	if ok, _, err := procWTSEnumerateProcesses.Call(0, uintptr(unsafe.Pointer(&level)), anySession, uintptr(unsafe.Pointer(&list)), uintptr(unsafe.Pointer(&count))); ok == 0 {
		return "", "", fmt.Errorf("list processes: %w", err)
	}
	defer procWTSFreeMemoryEx.Call(levelOne, uintptr(unsafe.Pointer(list)), uintptr(count))
	for _, process := range unsafe.Slice(list, count) {
		if process.process == id && process.user != nil {
			return process.user.String(), program, nil
		}
	}
	return "", "", fmt.Errorf("process %d has no account that Windows lists", id)
}

// platformAccountProfile returns the profile folder that Windows records
// for the account sid.
func platformAccountProfile(sid string) (string, error) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList\`+sid, registry.QUERY_VALUE)
	if err != nil {
		return "", err
	}
	defer key.Close()
	value, _, err := key.GetStringValue("ProfileImagePath")
	if err != nil {
		return "", err
	}
	return registry.ExpandString(value)
}

// platformIsOwnGitState reports whether dir holds an OwnGit state database:
// a plain file with one name, opened without following a link. It runs
// with administrator rights, which read a folder private to another
// account only through backup semantics.
func platformIsOwnGitState(dir string) bool {
	enablePrivileges("SeBackupPrivilege")
	name, err := windows.UTF16PtrFromString(filepath.Join(dir, "owngit.sqlite"))
	if err != nil {
		return false
	}
	handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	var info windows.ByHandleFileInformation
	return windows.GetFileInformationByHandle(handle, &info) == nil &&
		info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) == 0 && info.NumberOfLinks == 1
}

// shellExecuteInfo is SHELLEXECUTEINFOW.
type shellExecuteInfo struct {
	size       uint32
	mask       uint32
	window     uintptr
	verb       *uint16
	file       *uint16
	parameters *uint16
	directory  *uint16
	show       int32
	instance   uintptr
	idList     uintptr
	class      *uint16
	classKey   uintptr
	hotKey     uint32
	icon       uintptr
	process    windows.Handle
}

// platformRunElevated starts this executable through UAC ("runas") with a
// hidden console, so that the elevated copy can attach to this console,
// and waits for it.
func platformRunElevated(arguments []string) (int, error) {
	executable, err := os.Executable()
	if err != nil {
		return 0, err
	}
	lockedExecutable, err := openServiceSource(executable)
	if err != nil {
		return 0, fmt.Errorf("hold this owngit.exe while administrator approval runs: %w", err)
	}
	defer lockedExecutable.Close()
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return 0, err
	}
	parameters, err := windows.UTF16PtrFromString(service.WindowsCommandLine(arguments))
	if err != nil {
		return 0, err
	}
	const seeMaskNoCloseProcess, seeMaskFlagNoUI = 0x40, 0x400
	info := shellExecuteInfo{mask: seeMaskNoCloseProcess | seeMaskFlagNoUI, verb: verb, file: file, parameters: parameters, show: windows.SW_HIDE}
	info.size = uint32(unsafe.Sizeof(info))
	if ok, _, callErr := procShellExecuteEx.Call(uintptr(unsafe.Pointer(&info))); ok == 0 {
		if errors.Is(callErr, windows.ERROR_CANCELLED) {
			return 0, errElevationCancelled
		}
		return 0, fmt.Errorf("start owngit with administrator rights: %w", callErr)
	}
	defer windows.CloseHandle(info.process)
	if _, err := windows.WaitForSingleObject(info.process, windows.INFINITE); err != nil {
		return 0, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.process, &code); err != nil {
		return 0, err
	}
	return int(code), nil
}

// attachToConsole moves this process to the console of process pid, so
// that an elevated copy prints where the owner typed the command. When it
// cannot, output goes to the hidden console of its own.
func attachToConsole(pid int) {
	procFreeConsole.Call()
	if ok, _, _ := procAttachConsole.Call(uintptr(uint32(pid))); ok == 0 {
		return
	}
	if output, err := os.OpenFile("CONOUT$", os.O_RDWR, 0); err == nil {
		os.Stdout, os.Stderr = output, output
		_ = windows.SetStdHandle(windows.STD_OUTPUT_HANDLE, windows.Handle(output.Fd()))
		_ = windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(output.Fd()))
	}
	if input, err := os.OpenFile("CONIN$", os.O_RDWR, 0); err == nil {
		os.Stdin = input
		_ = windows.SetStdHandle(windows.STD_INPUT_HANDLE, windows.Handle(input.Fd()))
	}
}

// Groups that UAC turns into deny-only groups for an administrator's
// filtered token, as far as a local account can hold them.
var adminGroupSIDs = []string{
	"S-1-5-32-544", // Administrators
	"S-1-5-32-547", // Power Users
	"S-1-5-32-548", // Account Operators
	"S-1-5-32-549", // Server Operators
	"S-1-5-32-550", // Print Operators
	"S-1-5-32-551", // Backup Operators
	"S-1-5-32-556", // Network Configuration Operators
	"S-1-5-32-569", // Cryptographic Operators
	"S-1-5-114",    // Local account and member of Administrators group
}

// withoutAdminToken returns a restricted copy of this process's token, as
// UAC gives an administrator when not elevated: the administrator groups
// only deny access, every privilege except SeChangeNotifyPrivilege is
// removed, and the integrity level is Medium. New objects are owned by the
// account itself and, when nothing is inherited, only the account and
// SYSTEM may use them, so repositories created by the server are owned by
// the account as Git expects.
func withoutAdminToken() (windows.Token, error) {
	var own windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ALL_ACCESS, &own); err != nil {
		return 0, fmt.Errorf("open the process token: %w", err)
	}
	defer own.Close()
	var disable []windows.SIDAndAttributes
	for _, text := range adminGroupSIDs {
		sid, err := windows.StringToSid(text)
		if err != nil {
			return 0, err
		}
		disable = append(disable, windows.SIDAndAttributes{Sid: sid})
	}
	const disableMaxPrivilege = 0x1
	var restricted windows.Token
	if ok, _, err := procCreateRestrictedToken.Call(uintptr(own), disableMaxPrivilege,
		uintptr(len(disable)), uintptr(unsafe.Pointer(&disable[0])), 0, 0, 0, 0, uintptr(unsafe.Pointer(&restricted))); ok == 0 {
		return 0, fmt.Errorf("create a restricted token: %w", err)
	}
	fail := func(step string, err error) (windows.Token, error) {
		restricted.Close()
		return 0, fmt.Errorf("%s: %w", step, err)
	}
	medium, err := windows.StringToSid("S-1-16-8192")
	if err != nil {
		return fail("medium integrity SID", err)
	}
	label := windows.Tokenmandatorylabel{Label: windows.SIDAndAttributes{Sid: medium, Attributes: windows.SE_GROUP_INTEGRITY}}
	if err := windows.SetTokenInformation(restricted, windows.TokenIntegrityLevel, (*byte)(unsafe.Pointer(&label)), label.Size()); err != nil {
		return fail("set Medium integrity", err)
	}
	user, err := own.GetTokenUser()
	if err != nil {
		return fail("read the account", err)
	}
	owner := struct{ owner *windows.SID }{user.User.Sid}
	if err := windows.SetTokenInformation(restricted, windows.TokenOwner, (*byte)(unsafe.Pointer(&owner)), uint32(unsafe.Sizeof(owner))); err != nil {
		return fail("make the account the owner of new objects", err)
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:(A;;GA;;;" + user.User.Sid.String() + ")(A;;GA;;;SY)")
	if err != nil {
		return fail("default access", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fail("default access", err)
	}
	defaultDACL := struct{ dacl *windows.ACL }{dacl}
	if err := windows.SetTokenInformation(restricted, windows.TokenDefaultDacl, (*byte)(unsafe.Pointer(&defaultDACL)), uint32(unsafe.Sizeof(defaultDACL))); err != nil {
		return fail("set the default access", err)
	}
	return restricted, nil
}

// platformRunWithoutAdminRights runs this executable with the arguments and
// a restricted token (withoutAdminToken) on this console, and returns its
// exit code.
func platformRunWithoutAdminRights(arguments []string) (int, error) {
	return runCopy(arguments, true, 0, nil, nil)
}

// platformRepositoryRootWithoutAdminRights returns the repository folder
// saved in stateDir, or "". A copy without administrator rights reads it,
// since opening the state with them could leave files that the service
// cannot use.
func platformRepositoryRootWithoutAdminRights(stateDir string) string {
	var output bytes.Buffer
	code, err := runCopy([]string{"service", "repository-root", "--state-dir", stateDir}, true, 0, &output, nil)
	if err != nil || code != 0 {
		return ""
	}
	return strings.TrimSpace(output.String())
}

// runCopy runs this executable with the arguments on this console, in a
// job that ends it when this process ends, and returns its exit code. With
// restricted it runs with withoutAdminToken. An interrupt or a console
// close is passed on as CTRL_BREAK and, when stop is set, as the stop event
// of the server. Standard output goes to output when it is not nil, and
// environment is added to the copy's environment.
func runCopy(arguments []string, restricted bool, stop windows.Handle, output io.Writer, environment []string) (int, error) {
	if os.Getenv(copyVariable) != "" {
		return 0, errors.New("this copy of owngit was started by another one and does not start a further copy")
	}
	executable, err := os.Executable()
	if err != nil {
		return 0, err
	}
	command := exec.Command(executable, arguments...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if output != nil {
		command.Stdout = output
	}
	baseEnvironment := os.Environ()
	if restricted {
		baseEnvironment = inheritedEnvironment
	}
	command.Env = append(append(append([]string(nil), baseEnvironment...), environment...), copyVariable+"=1")
	// Suspended until it is in the job, and in its own process group so
	// that it can get CTRL_BREAK.
	gitexec.ConfigureOwnedProcess(command)
	if restricted {
		token, err := withoutAdminToken()
		if err != nil {
			return 0, err
		}
		defer token.Close()
		command.SysProcAttr.Token = syscall.Token(token)
	}
	if err := command.Start(); err != nil {
		return 0, fmt.Errorf("start a copy of owngit: %w", err)
	}
	owner, err := gitexec.AttachOwnedProcess(command)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return 0, err
	}
	defer gitexec.CloseOwnedProcess(owner)
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(interrupts)
	for {
		select {
		case err := <-done:
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				return exit.ExitCode(), nil
			}
			return 0, err
		case <-interrupts:
			// Ask the copy to stop in order; the job ends whatever is left
			// when this process returns.
			_ = windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(command.Process.Pid))
			if stop != 0 {
				_ = windows.SetEvent(stop)
			}
			go func() {
				time.Sleep(taskStopTimeout)
				_ = gitexec.TerminateOwnedProcess(owner, 5*time.Second)
			}()
		}
	}
}

// Stop requests for the server that runs as the task use a named event.
// The parent with administrator rights creates it in the Global namespace
// with access for the account only; a server without that parent (the
// sign-in task) creates it in its own session.

func stopEventNames(stateDir string) []string {
	name := service.StopEventName(absoluteOrSame(stateDir))
	return []string{`Global\` + name, `Local\` + name}
}

func absoluteOrSame(path string) string {
	if absolute, err := filepath.Abs(path); err == nil {
		return absolute
	}
	return path
}

// createGlobalStopEvent creates the stop event of stateDir in the Global
// namespace. Only the account may set it; the Medium label lets the
// account's ordinary processes reach it.
func createGlobalStopEvent(stateDir string) (windows.Handle, error) {
	sid, err := platformCurrentAccountSID()
	if err != nil {
		return 0, err
	}
	const synchronizeAndModify = "0x100002"
	descriptor, err := windows.SecurityDescriptorFromString("D:(A;;GA;;;SY)(A;;" + synchronizeAndModify + ";;;" + sid + ")S:(ML;;NW;;;ME)")
	if err != nil {
		return 0, err
	}
	attributes := windows.SecurityAttributes{SecurityDescriptor: descriptor}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	name, err := windows.UTF16PtrFromString(stopEventNames(stateDir)[0])
	if err != nil {
		return 0, err
	}
	return createEvent(&attributes, name)
}

// createEvent creates a manual-reset event, or opens it when it exists.
// windows.CreateEvent reports an existing event as an error along with a
// valid handle.
func createEvent(attributes *windows.SecurityAttributes, name *uint16) (windows.Handle, error) {
	handle, err := windows.CreateEvent(attributes, 1, 0, name)
	if handle != 0 && errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		err = nil
	}
	return handle, err
}

// watchServiceStop calls stop when the stop event of stateDir is set, for
// a server started with --service. It returns a function that ends the
// watch.
func watchServiceStop(stateDir string, stop func(), logf func(string, ...any)) func() {
	names := stopEventNames(stateDir)
	var event windows.Handle
	global, _ := windows.UTF16PtrFromString(names[0])
	event, err := windows.OpenEvent(windows.SYNCHRONIZE, false, global)
	if err != nil {
		local, _ := windows.UTF16PtrFromString(names[1])
		if event, err = createEvent(nil, local); err != nil {
			logf("\"owngit service stop\" cannot reach this server (%v); it stops when its task ends", err)
			return func() {}
		}
	}
	finished, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		windows.CloseHandle(event)
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer windows.CloseHandle(event)
		index, err := windows.WaitForMultipleObjects([]windows.Handle{event, finished}, false, windows.INFINITE)
		if err == nil && index == windows.WAIT_OBJECT_0 {
			logf("stop requested by \"owngit service stop\"")
			stop()
		}
	}()
	return func() {
		_ = windows.SetEvent(finished)
		<-done
		windows.CloseHandle(finished)
	}
}

// platformSignalServiceStop sets the stop event of stateDir, if a server
// listens for one.
func platformSignalServiceStop(stateDir string) (bool, error) {
	var lastErr error
	for _, text := range stopEventNames(stateDir) {
		name, err := windows.UTF16PtrFromString(text)
		if err != nil {
			return false, err
		}
		event, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, name)
		if err != nil {
			lastErr = err
			continue
		}
		err = windows.SetEvent(event)
		windows.CloseHandle(event)
		return err == nil, err
	}
	return false, lastErr
}

// serveWithoutAdminRights runs "owngit serve --service" as a supervisor:
// it creates the stop event, runs the server as a copy of itself and starts
// it again after a failure, until "owngit service stop" or the end of the
// task. When this process has administrator rights, as an S4U task of an
// administrator does, the copy runs without them. The supervisor neither
// listens nor opens the state directory. handled is false in the copy,
// which serves.
func serveWithoutAdminRights(arguments []string) (handled bool, err error) {
	if os.Getenv(copyVariable) != "" {
		if probeEnvironment().Elevated {
			return true, errors.New("the server could not give up administrator rights, so it does not start")
		}
		return false, nil
	}
	stateDir := stateDirArgument(arguments)
	if stateDir == "" {
		return true, errors.New("serve --service needs --state-dir")
	}
	elevated := probeEnvironment().Elevated
	var stop windows.Handle
	if elevated {
		stop, err = createGlobalStopEvent(stateDir)
	} else {
		var name *uint16
		if name, err = windows.UTF16PtrFromString(stopEventNames(stateDir)[1]); err == nil {
			stop, err = createEvent(nil, name)
		}
	}
	if err != nil {
		return true, fmt.Errorf("create the stop event: %w", err)
	}
	defer windows.CloseHandle(stop)
	// The sign-in task starts "conhost.exe --headless", which starts this
	// process. When Task Scheduler ends the task it ends conhost only, so
	// the end of the parent counts as a stop request.
	stopWithParent(stop)
	delay := serviceRestartDelay
	var restarted []string
	for {
		started := time.Now()
		code, err := runCopy(append([]string{"serve"}, arguments...), elevated, stop, nil, restarted)
		if err != nil {
			return true, err
		}
		if code == 0 || stopRequested(stop, 0) {
			if code != 0 {
				return true, &checkExit{code: code, err: fmt.Errorf("the server exited with status %d", code)}
			}
			return true, nil
		}
		// Task Scheduler restarts a task only when it cannot start it, not
		// when its program fails later, so the supervisor does.
		if time.Since(started) > time.Minute {
			delay = serviceRestartDelay
		}
		if stopRequested(stop, delay) {
			return true, nil
		}
		// The supervisor does not open the state directory; the next server
		// writes the reason for its start to the log.
		restarted = []string{restartedVariable + "=" + strconv.Itoa(code)}
		delay = min(2*delay, time.Minute)
	}
}

// stopWithParent sets stop when the process that started this one exits.
func stopWithParent(stop windows.Handle) {
	parent, err := parentProcess()
	if err != nil {
		return
	}
	go func() {
		defer windows.CloseHandle(parent)
		if result, err := windows.WaitForSingleObject(parent, windows.INFINITE); err == nil && result == windows.WAIT_OBJECT_0 {
			_ = windows.SetEvent(stop)
		}
	}()
}

// parentProcess opens the process that started this one. A process with
// the parent's ID that started later took over the ID of a parent that has
// already exited, and is not returned.
func parentProcess() (windows.Handle, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(snapshot)
	own := windows.GetCurrentProcessId()
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if entry.ProcessID == own {
			parent, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, entry.ParentProcessID)
			if err != nil {
				return 0, err
			}
			if !startedBefore(parent, windows.CurrentProcess()) {
				windows.CloseHandle(parent)
				return 0, errors.New("the parent process has exited")
			}
			return parent, nil
		}
	}
	return 0, err
}

// startedBefore reports whether process first started before process
// second.
func startedBefore(first, second windows.Handle) bool {
	var firstCreated, secondCreated, exit, kernel, user windows.Filetime
	if windows.GetProcessTimes(first, &firstCreated, &exit, &kernel, &user) != nil ||
		windows.GetProcessTimes(second, &secondCreated, &exit, &kernel, &user) != nil {
		return false
	}
	return firstCreated.Nanoseconds() <= secondCreated.Nanoseconds()
}

// serviceRestartDelay is the first wait before a failed server starts
// again. It doubles while the server keeps failing, up to a minute.
const serviceRestartDelay = 5 * time.Second

// stopRequested waits up to wait for the stop event and reports whether it
// is set.
func stopRequested(stop windows.Handle, wait time.Duration) bool {
	result, err := windows.WaitForSingleObject(stop, uint32(wait/time.Millisecond))
	return err == nil && result == windows.WAIT_OBJECT_0
}
