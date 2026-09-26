package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"owngit/internal/service"
)

// fakeWindows stands in for schtasks, PowerShell and UAC, so that the
// Windows backend runs on every platform. It keeps the one task and the one
// firewall rule that the backend manages.
type fakeWindows struct {
	t          *testing.T
	calls      []string
	definition string // the task XML, "" when there is none
	state      string // TaskStateScript output
	firewall   string // FirewallShowScript output
	created    string // XML read from the file given to schtasks /Create
	failRun    bool
	runState   string // the state after schtasks /Run (default: Running)
	elevated   [][]string
	elevate    func([]string) (int, error)
	stopAsked  []string
	listening  bool // whether a server listens for the stop event
	git        bool
	winget     bool
	// owners maps folders to owner SIDs (default: the test account);
	// adminOwned counts what the Administrators group owns below them.
	owners       map[string]string
	adminOwned   map[string]int
	repositories string // the repository folder saved in the state
}

const fakeSystem = `C:\Windows\System32`

func (fake *fakeWindows) script(args []string) string {
	data, err := base64.StdEncoding.DecodeString(args[len(args)-1])
	if err != nil {
		fake.t.Fatalf("PowerShell without an encoded command: %q", args)
	}
	units := make([]uint16, len(data)/2)
	for index := range units {
		units[index] = binary.LittleEndian.Uint16(data[2*index:])
	}
	script := string(utf16.Decode(units))
	for name, text := range map[string]string{
		"definition": service.TaskDefinitionScript, "state": service.TaskStateScript,
		"firewall-show": service.FirewallShowScript, "firewall-allow": service.FirewallAllowScript,
		"firewall-remove": service.FirewallRemoveScript,
	} {
		if strings.HasSuffix(script, text) {
			return name
		}
	}
	fake.t.Fatalf("unknown script:\n%s", script)
	return ""
}

func (fake *fakeWindows) run(_ context.Context, name string, args ...string) ([]byte, error) {
	switch name {
	case fakeSystem + `\WindowsPowerShell\v1.0\powershell.exe`:
		script := fake.script(args)
		call := "powershell " + script
		if script == "firewall-allow" {
			call += " " + os.Getenv(service.FirewallProgramVariable)
		}
		fake.calls = append(fake.calls, call)
		switch script {
		case "definition":
			return []byte(fake.definition), nil
		case "state":
			if fake.definition == "" {
				return []byte("task not found"), errors.New("exit status 1")
			}
			return []byte(fake.state), nil
		case "firewall-show":
			return []byte(fake.firewall), nil
		case "firewall-allow":
			// Like New-NetFirewallRule, a second rule with the same name
			// fails unless the script removes the first one.
			remove := strings.Index(service.FirewallAllowScript, "Remove-NetFirewallRule -Name '"+service.FirewallRuleName+"'")
			if fake.firewall != "" && (remove < 0 || remove > strings.Index(service.FirewallAllowScript, "New-NetFirewallRule")) {
				return []byte("already exists"), errors.New("exit status 1")
			}
			fake.firewall = os.Getenv(service.FirewallProgramVariable) + "\n2\nTrue\n"
		case "firewall-remove":
			fake.firewall = ""
		}
		return nil, nil
	case fakeSystem + `\schtasks.exe`:
		fake.calls = append(fake.calls, "schtasks "+strings.Join(args, " "))
		switch args[0] {
		case "/Create":
			// Like schtasks, an existing task is replaced only with /F.
			if fake.definition != "" && args[len(args)-1] != "/F" {
				return []byte("ERROR: Cannot create a file when that file already exists."), errors.New("exit status 1")
			}
			data, err := os.ReadFile(args[4])
			if err != nil {
				fake.t.Fatal(err)
			}
			installed, err := service.ParseTask(data)
			if err != nil {
				fake.t.Fatalf("created task: %v", err)
			}
			if installed.User == "" {
				fake.t.Fatal("created task has no account")
			}
			text, _ := decodeUTF16ForTest(data)
			fake.created, fake.definition, fake.state = text, text, "3\n0"
		case "/Run":
			if fake.failRun {
				return []byte("ERROR: fake"), errors.New("exit status 1")
			}
			fake.state = cmp.Or(fake.runState, "4\n267009")
		case "/End":
			fake.state = "3\n1"
		case "/Delete":
			fake.definition = ""
		}
		return nil, nil
	}
	fake.t.Fatalf("unexpected command %s %q", name, args)
	return nil, nil
}

func decodeUTF16ForTest(data []byte) (string, error) {
	if len(data) < 2 || data[0] != 0xff || data[1] != 0xfe {
		return "", errors.New("no UTF-16 byte order mark")
	}
	units := make([]uint16, (len(data)-2)/2)
	for index := range units {
		units[index] = binary.LittleEndian.Uint16(data[2+2*index:])
	}
	return string(utf16.Decode(units)), nil
}

func newFakeWindows(t *testing.T) *fakeWindows {
	fake := &fakeWindows{t: t, git: true, winget: true, owners: map[string]string{}, adminOwned: map[string]int{}}
	previousRunner, previousElevated, previousLook, previousStop := serviceRunner, runElevated, lookPath, signalServiceStop
	previousOwner, previousGive, previousRoot, previousPoll := ownerOf, giveOwnership, repositoryRootWithoutAdminRights, taskPollInterval
	t.Cleanup(func() {
		serviceRunner, runElevated, lookPath, signalServiceStop = previousRunner, previousElevated, previousLook, previousStop
		ownerOf, giveOwnership, repositoryRootWithoutAdminRights, taskPollInterval = previousOwner, previousGive, previousRoot, previousPoll
	})
	taskPollInterval = 10 * time.Millisecond
	ownerOf = func(path string) (string, error) {
		if owner, found := fake.owners[path]; found {
			return owner, nil
		}
		return testSID, nil
	}
	giveOwnership = func(root, sid string, check func(string, string) error) (int, int, error) {
		fake.calls = append(fake.calls, "give "+root)
		owner, _ := ownerOf(root)
		if err := check(root, owner); err != nil {
			return 0, 0, err
		}
		changed := fake.adminOwned[root]
		if owner == administratorsSID {
			changed++
		}
		return changed, 0, nil
	}
	repositoryRootWithoutAdminRights = func(string) string { return fake.repositories }
	serviceRunner = fake.run
	runElevated = func(arguments []string) (int, error) {
		fake.elevated = append(fake.elevated, arguments)
		if fake.elevate != nil {
			return fake.elevate(arguments)
		}
		return 1, nil
	}
	lookPath = func(name string) (string, error) {
		if name == "git" && fake.git || name == "winget" && fake.winget {
			return `C:\Tools\` + name + ".exe", nil
		}
		return "", errors.New("not found")
	}
	signalServiceStop = func(stateDir string) (bool, error) {
		fake.stopAsked = append(fake.stopAsked, stateDir)
		if fake.listening {
			fake.state = "3\n0"
		}
		return fake.listening, nil
	}
	return fake
}

func testTaskHost(environment service.Environment) (*taskHost, *bytes.Buffer) {
	environment.Windows = true
	if environment.Getenv == nil {
		environment.Getenv = func(string) string { return "" }
	}
	var out bytes.Buffer
	return &taskHost{
		env: environment, executable: `C:\Program Files\OwnGit\owngit.exe`,
		sid: testSID, system: fakeSystem, out: &out,
	}, &out
}

const testSID = "S-1-5-21-1-2-3-1001"

// An existing task keeps its state directory, so these tests need no
// Windows path from filepath.Abs.
func (fake *fakeWindows) existing(t *testing.T, mode service.Mode, sid, stateDir string) {
	t.Helper()
	definition, err := service.RenderTask(service.TaskPlan{
		Mode: mode, Executable: `C:\Program Files\OwnGit\owngit.exe`, StateDir: stateDir,
		UserSID: sid, Conhost: fakeSystem + `\conhost.exe`,
	})
	noErr(t, err)
	fake.definition, fake.state = definition, "3\n0"
}

const testStateDir = `C:\Users\you\My Files\AppData\Roaming\owngit`

// An administrator gets one UAC prompt, announced in one line, for a copy
// of owngit that registers the task and the firewall rule for itself.
func TestTaskInstallAsksOnceForAdministratorApproval(t *testing.T) {
	fake := newFakeWindows(t)
	fake.existing(t, service.ModeBootTask, testSID, testStateDir)
	fake.state = "4\n267009"
	fake.listening = true
	host, out := testTaskHost(service.Environment{Administrator: true})
	err := host.install("", nil)
	if err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Fatalf("install with a failing elevated step: %v", err)
	}
	want := [][]string{{"service", "elevated-install", "--state-dir", testStateDir, "--headless=false", "--attach", fmt.Sprint(os.Getpid())}}
	if !reflect.DeepEqual(fake.elevated, want) {
		t.Errorf("elevated %q, want %q", fake.elevated, want)
	}
	if got := strings.Count(out.String(), "Windows asks once for administrator approval to register the OwnGit task that starts at boot and allow OwnGit through Windows Firewall on private networks.\n"); got != 1 {
		t.Errorf("output:\n%s", out.String())
	}
	// The server keeps running until the approved step stops it, so a
	// declined prompt changes nothing.
	fake.elevated = nil
	fake.elevate = func([]string) (int, error) { return 0, errElevationCancelled }
	out.Reset()
	if err := host.install("", nil); !errors.Is(err, errElevationCancelled) || !strings.Contains(out.String(), "Nothing changed") {
		t.Errorf("declined prompt: %v\n%s", err, out.String())
	}
	if len(fake.stopAsked) != 0 || slicesContainPrefix(fake.calls, "schtasks") || fake.state != "4\n267009" {
		t.Errorf("the server was touched before approval: stop asked %q, calls %q, state %q", fake.stopAsked, fake.calls, fake.state)
	}
}

func slicesContainPrefix(values []string, prefix string) bool {
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

// Git that is missing is installed inside the same prompt when winget
// exists; without winget the command says where to get Git and changes
// nothing.
func TestTaskInstallGit(t *testing.T) {
	fake := newFakeWindows(t)
	fake.existing(t, service.ModeBootTask, testSID, testStateDir)
	fake.git = false
	host, out := testTaskHost(service.Environment{Administrator: true, Getenv: func(name string) string {
		return map[string]string{"SSH_CLIENT": "10.0.0.2 5000 22"}[name]
	}, NoDesktop: false})
	_ = host.install("", nil)
	if len(fake.elevated) != 1 || !reflect.DeepEqual(fake.elevated[0][4:6], []string{"--headless=false", "--install-git"}) || !strings.Contains(out.String(), ", and install Git with winget.") {
		t.Errorf("elevated %q, output:\n%s", fake.elevated, out.String())
	}
	fake.winget, fake.elevated, fake.calls = false, nil, nil
	out.Reset()
	if err := host.install("", nil); err == nil || len(fake.elevated) != 0 || slicesContainPrefix(fake.calls, "schtasks") || !strings.Contains(out.String(), "https://git-scm.com/download/win") {
		t.Errorf("without winget: %v, elevated %q, calls %q\n%s", err, fake.elevated, fake.calls, out.String())
	}
}

// Without a desktop (SSH, not elevated) no prompt can appear: the command
// says what to do instead.
func TestTaskInstallWithoutADesktopExplains(t *testing.T) {
	fake := newFakeWindows(t)
	fake.existing(t, service.ModeBootTask, testSID, testStateDir)
	host, out := testTaskHost(service.Environment{Administrator: true, NoDesktop: true})
	if err := host.install("", nil); err == nil || len(fake.elevated) != 0 || !strings.Contains(out.String(), `"Run as administrator"`) {
		t.Errorf("%v, elevated %q\n%s", err, fake.elevated, out.String())
	}
}

// A standard account registers a sign-in task itself, without any prompt.
func TestTaskInstallForAStandardAccount(t *testing.T) {
	fake := newFakeWindows(t)
	fake.existing(t, service.ModeLogonTask, testSID, testStateDir)
	fake.failRun = true
	host, _ := testTaskHost(service.Environment{})
	if err := host.install("", nil); err == nil || !strings.Contains(err.Error(), "schtasks.exe /Run") {
		t.Fatalf("install: %v", err)
	}
	if len(fake.elevated) != 0 {
		t.Errorf("a standard account was prompted: %q", fake.elevated)
	}
	installed, err := service.ParseTask([]byte(fake.created))
	noErr(t, err)
	if installed.Mode != service.ModeLogonTask || installed.StateDir != testStateDir || installed.Executable != host.executable {
		t.Errorf("registered %+v", installed)
	}
	if slicesContainPrefix(fake.calls, "powershell firewall-allow") {
		t.Errorf("a standard account changed the firewall: %q", fake.calls)
	}
}

func TestTaskInstallRefusesOtherTasks(t *testing.T) {
	fake := newFakeWindows(t)
	fake.existing(t, service.ModeBootTask, "S-1-5-21-9-9-9-1001", testStateDir)
	host, _ := testTaskHost(service.Environment{Administrator: true})
	if err := host.install("", nil); err == nil || !strings.Contains(err.Error(), "another account") {
		t.Errorf("task of another account: %v", err)
	}
	fake.definition = strings.Replace(fake.definition, "owngit service install", "someone", 1)
	if err := host.install("", nil); !errors.Is(err, service.ErrForeignTask) {
		t.Errorf("foreign task: %v", err)
	}
	if len(fake.elevated) != 0 || slicesContainPrefix(fake.calls, "schtasks") {
		t.Errorf("changed something: %q %q", fake.elevated, fake.calls)
	}
}

// The elevated copy stops the running server, gives the account its
// folders back, registers the task for its own executable, adds the
// firewall rule for it and starts the task, in that order.
func TestTaskElevatedInstall(t *testing.T) {
	fake := newFakeWindows(t)
	fake.existing(t, service.ModeBootTask, testSID, testStateDir)
	fake.firewall = `C:\Old\owngit.exe` + "\n2\nTrue\n"
	fake.state, fake.listening = "4\n267009", true
	fake.repositories = `D:\Repositories`
	fake.owners[fake.repositories] = administratorsSID
	fake.adminOwned[fake.repositories] = 41
	host, out := testTaskHost(service.Environment{Administrator: true, Elevated: true})
	noErr(t, host.elevatedInstall(testStateDir, true, false))
	if !reflect.DeepEqual(fake.stopAsked, []string{testStateDir}) {
		t.Errorf("stop asked %q", fake.stopAsked)
	}
	want := []string{
		"powershell definition", "powershell state", "powershell state",
		"give " + testStateDir, "give " + fake.repositories,
		"schtasks /Create", "powershell firewall-allow " + host.executable, "schtasks /End /TN \\OwnGit", "schtasks /Run /TN \\OwnGit",
	}
	if len(fake.calls) != len(want) {
		t.Fatalf("calls %q, want %q", fake.calls, want)
	}
	for index := range want {
		if !strings.HasPrefix(fake.calls[index], want[index]) {
			t.Errorf("call %d = %q, want %q", index, fake.calls[index], want[index])
		}
	}
	installed, err := service.ParseTask([]byte(fake.created))
	noErr(t, err)
	if installed.Mode != service.ModeBootTask || !installed.Headless || installed.User != host.sid || !strings.Contains(fake.created, "<LogonType>S4U</LogonType>") {
		t.Errorf("registered %+v:\n%s", installed, fake.created)
	}
	if !strings.Contains(out.String(), `Your account is now the owner of 42 files and folders in D:\Repositories that belonged to the Administrators group.`) {
		t.Errorf("output:\n%s", out.String())
	}
	// The temporary definition file is gone.
	path := strings.Fields(fake.calls[5])[5]
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("definition file %s stayed: %v", path, err)
	}
}

func TestTaskElevatedUninstallKeepsTheData(t *testing.T) {
	fake := newFakeWindows(t)
	fake.existing(t, service.ModeBootTask, testSID, testStateDir)
	fake.firewall = `C:\Program Files\OwnGit\owngit.exe` + "\n2\nTrue\n"
	host, _ := testTaskHost(service.Environment{Administrator: true, Elevated: true})
	noErr(t, host.elevatedUninstall())
	if fake.definition != "" || fake.firewall != "" {
		t.Errorf("task %q or rule %q stayed", fake.definition, fake.firewall)
	}
	for _, call := range fake.calls {
		if strings.Contains(call, testStateDir) {
			t.Errorf("uninstall touched the state: %s", call)
		}
	}
}

// A task that Windows keeps queued gets the one line that explains it.
func TestTaskStatusExplainsAQueuedTask(t *testing.T) {
	fake := newFakeWindows(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	address := freeLoopbackAddress(t)
	_, err := runNetwork(t, "set", "--state-dir", stateDir, "--listen", address)
	noErr(t, err)
	fake.existing(t, service.ModeBootTask, testSID, testStateDir)
	fake.definition = strings.Replace(fake.definition, testStateDir, stateDir, -1)
	fake.state = "2\n0"
	host, out := testTaskHost(service.Environment{Administrator: true})
	noErr(t, host.status())
	for _, want := range []string{
		"Windows keeps the task queued", "until someone has signed in on this computer",
		"  Log:      " + service.TaskLogFile(stateDir), "  State:    " + stateDir,
		"Setup is not complete.",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status lacks %q:\n%s", want, out.String())
		}
	}
	// A server on 127.0.0.1 needs no firewall rule, so there is no warning.
	if strings.Contains(out.String(), "Firewall") {
		t.Errorf("a firewall line for a server on this computer only:\n%s", out.String())
	}
	_, port, _ := strings.Cut(address, ":")
	_, err = runNetwork(t, "set", "--state-dir", stateDir, "--listen", "0.0.0.0:"+port)
	noErr(t, err)
	out.Reset()
	noErr(t, host.status())
	if !strings.Contains(out.String(), "  Firewall: no rule for this owngit.exe, so other devices may be blocked") {
		t.Errorf("no firewall warning for a server on every address:\n%s", out.String())
	}
}

func TestTaskStopAsksTheServerFirst(t *testing.T) {
	fake := newFakeWindows(t)
	fake.existing(t, service.ModeBootTask, testSID, testStateDir)
	fake.state, fake.listening = "4\n267009", true
	host, out := testTaskHost(service.Environment{Administrator: true})
	noErr(t, host.control("stop"))
	if slicesContainPrefix(fake.calls, "schtasks /End") || !strings.Contains(out.String(), "starts again at the next boot") {
		t.Errorf("calls %q\n%s", fake.calls, out.String())
	}
	// A server that does not listen for the request is ended.
	fake.state, fake.listening, fake.calls = "4\n267009", false, nil
	noErr(t, host.control("stop"))
	if !slicesContainPrefix(fake.calls, "schtasks /End") {
		t.Errorf("calls %q", fake.calls)
	}
}

// Without a desktop that a person sees, serve opens no browser and only
// logs the setup file's path. As a service it never waits for answers on
// a console, and it writes its log to --log-file.
func TestServeWithoutADesktopOpensNothing(t *testing.T) {
	previousProbe, previousInteractive := probeEnvironment, interactiveSetup
	t.Cleanup(func() { probeEnvironment, interactiveSetup = previousProbe, previousInteractive })
	probeEnvironment = func() service.Environment {
		return service.Environment{Getenv: func(string) string { return "" }, Windows: true, NoDesktop: true}
	}
	// A console that looks interactive, as Windows gives a task.
	interactiveSetup = func() bool { return true }
	dir := t.TempDir()
	stateDir, logFile := filepath.Join(dir, "state"), filepath.Join(dir, "logs", "service.log")
	var opened []string
	logs := make(chan string, 100)
	logf := func(format string, arguments ...any) { logs <- fmt.Sprintf(format, arguments...) }
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- serveWithContext(ctx, []string{"--state-dir", stateDir, "--listen", "127.0.0.1:0", "--service", "--log-file", logFile},
			func(target string) error { opened = append(opened, target); return nil }, logf)
	}()
	var seen []string
	for listening := false; !listening; {
		select {
		case line := <-logs:
			seen = append(seen, line)
			listening = strings.HasPrefix(line, "OwnGit listening on")
		case err := <-result:
			t.Fatalf("serve returned: %v\n%s", err, strings.Join(seen, "\n"))
		}
	}
	cancel()
	noErr(t, <-result)
	if len(opened) != 0 {
		t.Errorf("opened %q without a desktop", opened)
	}
	if !slicesContainPrefix(seen, "owner setup file: "+filepath.Join(stateDir, "owner-setup.html")) {
		t.Errorf("the setup file path was not logged:\n%s", strings.Join(seen, "\n"))
	}
	if _, err := os.Stat(logFile); err != nil {
		t.Errorf("no log file: %v", err)
	}
}

func TestFlagGiven(t *testing.T) {
	for arguments, want := range map[string]bool{
		"--service":                 true,
		"-service":                  true,
		"--service=true":            true,
		"--service=false":           false,
		"--state-dir x --no-open":   false,
		"-- --service":              false,
		"--state-dir C:\\service x": false,
	} {
		if got := flagGiven(strings.Fields(arguments), "service"); got != want {
			t.Errorf("flagGiven(%q) = %v", arguments, got)
		}
	}
}

// A reinstall keeps the headless choice of the installed task, whatever
// the session, and only --headless changes it; a new install detects it.
func TestTaskInstallKeepsTheHeadlessChoice(t *testing.T) {
	ssh := func(name string) string {
		return map[string]string{"SSH_CONNECTION": "10.0.0.2 5000 10.0.0.3 22"}[name]
	}
	for _, test := range []struct {
		name      string
		installed *bool
		getenv    func(string) string
		flag      *bool
		want      string
	}{
		{"new over SSH", nil, ssh, nil, "--headless=true"},
		{"new on the desktop", nil, nil, nil, "--headless=false"},
		{"headless kept on the desktop", ptr(true), nil, nil, "--headless=true"},
		{"desktop kept over SSH", ptr(false), ssh, nil, "--headless=false"},
		{"changed by the option", ptr(true), nil, ptr(false), "--headless=false"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeWindows(t)
			if test.installed != nil {
				fake.existing(t, service.ModeBootTask, testSID, testStateDir)
				fake.definition = strings.Replace(fake.definition, "--headless=false", "--headless="+fmt.Sprint(*test.installed), 1)
			}
			host, _ := testTaskHost(service.Environment{Administrator: true, Getenv: test.getenv})
			stateDir := ""
			if test.installed == nil {
				// A new install needs an absolute Windows path.
				if runtime.GOOS != "windows" {
					t.Skip("a new task needs a Windows path")
				}
				stateDir = filepath.Join(t.TempDir(), "state")
			}
			_ = host.install(stateDir, test.flag)
			if len(fake.elevated) != 1 || fake.elevated[0][4] != test.want {
				t.Errorf("elevated %q, want %s", fake.elevated, test.want)
			}
		})
	}
}

// A standard account is never offered winget, which needs administrator
// rights; the command points to Git for Windows and changes nothing.
func TestTaskInstallGitForAStandardAccount(t *testing.T) {
	fake := newFakeWindows(t)
	fake.existing(t, service.ModeLogonTask, testSID, testStateDir)
	fake.git = false
	host, out := testTaskHost(service.Environment{})
	if err := host.install("", nil); err == nil || slicesContainPrefix(fake.calls, "schtasks") || len(fake.elevated) != 0 {
		t.Errorf("%v, calls %q, elevated %q", err, fake.calls, fake.elevated)
	}
	if !strings.Contains(out.String(), "https://git-scm.com/download/win") || strings.Contains(out.String(), "install Git with winget") {
		t.Errorf("output:\n%s", out.String())
	}
}

// Declining the prompt of an uninstall leaves the server running.
func TestTaskUninstallDeclinedChangesNothing(t *testing.T) {
	fake := newFakeWindows(t)
	fake.existing(t, service.ModeBootTask, testSID, testStateDir)
	fake.state, fake.listening = "4\n267009", true
	fake.elevate = func([]string) (int, error) { return 0, errElevationCancelled }
	host, out := testTaskHost(service.Environment{Administrator: true})
	if err := host.uninstall(); !errors.Is(err, errElevationCancelled) || !strings.Contains(out.String(), "Nothing changed") {
		t.Errorf("%v\n%s", err, out.String())
	}
	if len(fake.stopAsked) != 0 || slicesContainPrefix(fake.calls, "schtasks") || fake.definition == "" {
		t.Errorf("stop asked %q, calls %q", fake.stopAsked, fake.calls)
	}
}

// An uninstall with administrator rights still says where the
// repositories are, read by a copy without those rights.
func TestTaskElevatedUninstallNamesTheRepositories(t *testing.T) {
	fake := newFakeWindows(t)
	fake.existing(t, service.ModeBootTask, testSID, testStateDir)
	fake.repositories = `D:\Repositories`
	host, out := testTaskHost(service.Environment{Administrator: true, Elevated: true})
	noErr(t, host.uninstall())
	if fake.definition != "" {
		t.Error("the task stayed")
	}
	if !strings.Contains(out.String(), `The state stays in `+testStateDir+` and the repositories in D:\Repositories.`) {
		t.Errorf("output:\n%s", out.String())
	}
}

// Installing on a Windows that keeps the task queued says so at once
// instead of waiting for the server.
func TestTaskInstallStopsWaitingForAQueuedTask(t *testing.T) {
	fake := newFakeWindows(t)
	fake.existing(t, service.ModeLogonTask, testSID, testStateDir)
	fake.runState = "2\n0"
	host, out := testTaskHost(service.Environment{})
	started := time.Now()
	err := host.install("", nil)
	if err == nil || !strings.Contains(out.String(), queuedTaskMessage) || strings.Contains(out.String(), "did not answer") {
		t.Errorf("%v\n%s", err, out.String())
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Errorf("waited %s", elapsed)
	}
}

// A server that could not start is reported with its error at once by
// install, start and status, and the log is named only when it has
// something in it.
func TestTaskReportsWhyTheServerDidNotStart(t *testing.T) {
	fake := newFakeWindows(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	_, err := runNetwork(t, "set", "--state-dir", stateDir, "--listen", freeLoopbackAddress(t))
	noErr(t, err)
	fake.existing(t, service.ModeBootTask, testSID, testStateDir)
	fake.definition = strings.Replace(fake.definition, testStateDir, stateDir, -1)
	previous := waitForService
	t.Cleanup(func() { waitForService = previous })
	waitForService = func(string, time.Duration) (string, error) {
		return "", errServeFailed{"protect state directory: Access is denied."}
	}
	host, out := testTaskHost(service.Environment{Administrator: true})
	if err := host.control("start"); err == nil || !strings.Contains(out.String(), "OwnGit could not start: protect state directory: Access is denied.\n") || strings.Contains(out.String(), "See the log") {
		t.Errorf("%v\n%s", err, out.String())
	}
	noErr(t, os.MkdirAll(filepath.Dir(service.TaskLogFile(stateDir)), 0o700))
	noErr(t, os.WriteFile(service.TaskLogFile(stateDir), []byte("error: protect state directory\n"), 0o600))
	out.Reset()
	_ = host.control("start")
	if !strings.Contains(out.String(), "See the log: "+service.TaskLogFile(stateDir)) {
		t.Errorf("output:\n%s", out.String())
	}
	recordServeError(stateDir, errors.New("protect state directory: Access is denied."))
	fake.state = "3\n1"
	out.Reset()
	noErr(t, host.status())
	if !strings.Contains(out.String(), "  Last error: protect state directory: Access is denied.\n") {
		t.Errorf("status:\n%s", out.String())
	}
}

// The error that ends serve reaches --log-file before the file closes, and
// a server that its supervisor started again says so there.
func TestServeLogsItsErrorToTheLogFile(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "service.log")
	t.Setenv(restartedVariable, "3")
	err := serveWithContext(context.Background(), []string{"--state-dir", filepath.Join(dir, "state"), "--listen", "not an address", "--no-open", "--service", "--log-file", logFile},
		func(string) error { return nil }, log.Printf)
	if err == nil {
		t.Fatal("serve with a broken listen address started")
	}
	content, readErr := os.ReadFile(logFile)
	noErr(t, readErr)
	for _, want := range []string{"started again after the server exited with status 3", "error: " + err.Error()} {
		if !strings.Contains(string(content), want) {
			t.Errorf("log lacks %q:\n%s", want, content)
		}
	}
}

// Elevated, "owngit service status", "start", "stop" and "restart" read
// the state only as a copy without administrator rights; other commands
// that use the state do the same, except the service's own serve.
func TestElevatedStateCommandsRunWithoutAdminRights(t *testing.T) {
	fake := newFakeWindows(t)
	previousProbe, previousRun, previousSystem, previousSID := probeEnvironment, runWithoutAdminRights, windowsSystemDirectory, currentAccountSID
	t.Cleanup(func() {
		probeEnvironment, runWithoutAdminRights, windowsSystemDirectory, currentAccountSID = previousProbe, previousRun, previousSystem, previousSID
	})
	probeEnvironment = func() service.Environment {
		return service.Environment{Getenv: func(string) string { return "" }, Windows: true, Administrator: true, Elevated: true}
	}
	windowsSystemDirectory = func() (string, error) { return fakeSystem, nil }
	currentAccountSID = func() (string, error) { return testSID, nil }
	var copies [][]string
	runWithoutAdminRights = func(arguments []string) (int, error) {
		copies = append(copies, arguments)
		return 0, nil
	}
	for _, action := range []string{"status", "start", "stop", "restart"} {
		noErr(t, taskServiceCommand(action, nil))
	}
	if handled, err := stateCommandWithoutAdminRights("network", []string{"show"}); !handled || err != nil {
		t.Errorf("network show: %v %v", handled, err)
	}
	if handled, _ := stateCommandWithoutAdminRights("serve", []string{"--service"}); handled {
		t.Error("the service's serve was handed to a copy")
	}
	want := [][]string{{"service", "status"}, {"service", "start"}, {"service", "stop"}, {"service", "restart"}, {"network", "show"}}
	if !reflect.DeepEqual(copies, want) || len(fake.calls) != 0 {
		t.Errorf("copies %q, calls %q", copies, fake.calls)
	}
}

// A standard account is told who can give it a folder of the
// Administrators group; an administrator's single prompt says it will.
func TestTaskInstallNamesFoldersOfTheAdministrators(t *testing.T) {
	fake := newFakeWindows(t)
	fake.existing(t, service.ModeLogonTask, testSID, testStateDir)
	fake.owners[testStateDir] = administratorsSID
	fake.failRun = true
	host, out := testTaskHost(service.Environment{})
	_ = host.install("", nil)
	if !strings.Contains(out.String(), `An administrator can make your account its owner with: icacls "`+testStateDir+`" /setowner "*`+testSID+`" /T /C`) {
		t.Errorf("standard account:\n%s", out.String())
	}

	fake = newFakeWindows(t)
	fake.existing(t, service.ModeBootTask, testSID, testStateDir)
	fake.owners[testStateDir] = administratorsSID
	host, out = testTaskHost(service.Environment{Administrator: true})
	_ = host.install("", nil)
	if !strings.Contains(out.String(), "Windows asks once for administrator approval to register the OwnGit task that starts at boot, allow OwnGit through Windows Firewall on private networks, and make your account the owner of "+testStateDir+".\n") {
		t.Errorf("administrator:\n%s", out.String())
	}
}

// The elevated step gives back only folders of the Administrators group or
// the account, never a folder of another account, a drive or a folder of
// Windows, and says so when such a folder is not the account's.
func TestGiveFolderToAccount(t *testing.T) {
	windows := func(name string) string {
		return map[string]string{
			"SystemRoot": `C:\Windows`, "ProgramFiles": `C:\Program Files`, "ProgramFiles(x86)": `C:\Program Files (x86)`,
			"ProgramData": `C:\ProgramData`, "USERPROFILE": `C:\Users\you`, "SystemDrive": "C:",
		}[name]
	}
	for _, test := range []struct {
		folder, owner string
		given         bool
		line          string
	}{
		{`C:\OwnGit\state`, administratorsSID, true, `Your account is now the owner of 1 files and folders in C:\OwnGit\state`},
		{`C:\OwnGit\state`, testSID, true, ""},
		{`C:\OwnGit\state`, "S-1-5-21-9-9-9-1002", false, `OwnGit leaves the owner of C:\OwnGit\state as it is, because it belongs to another account.`},
		{`C:\Program Files\OwnGit\state`, administratorsSID, false, "because it is a folder of Windows or of installed programs."},
		{`C:\Windows\Temp\x`, administratorsSID, false, "because it is a folder of Windows or of installed programs."},
		{`D:\`, administratorsSID, false, "because it is a drive."},
		{`D:\`, testSID, false, ""},
		{`C:\Users\you`, testSID, false, ""},
		{`C:\ProgramData`, administratorsSID, false, "because it holds more than OwnGit's files."},
	} {
		fake := newFakeWindows(t)
		fake.owners[test.folder] = test.owner
		var changed bool
		giveOwnership = func(root, sid string, check func(string, string) error) (int, int, error) {
			if err := check(root, test.owner); err != nil {
				return 0, 0, err
			}
			changed = true
			return fake.adminOwned[root] + map[bool]int{true: 1}[test.owner == administratorsSID], 0, nil
		}
		host, out := testTaskHost(service.Environment{Administrator: true, Elevated: true, Getenv: windows})
		host.giveFolderToAccount(test.folder)
		if changed != test.given || test.line == "" && out.Len() != 0 || test.line != "" && !strings.Contains(out.String(), test.line) {
			t.Errorf("%s owned by %s: changed %v, output %q", test.folder, test.owner, changed, out.String())
		}
	}
}
