package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
	elevated   [][]string
	elevate    func([]string) (int, error)
	stopAsked  []string
	listening  bool // whether a server listens for the stop event
	git        bool
	winget     bool
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
			fake.firewall = os.Getenv(service.FirewallProgramVariable) + "\n2\nTrue\n"
		case "firewall-remove":
			fake.firewall = ""
		}
		return nil, nil
	case fakeSystem + `\schtasks.exe`:
		fake.calls = append(fake.calls, "schtasks "+strings.Join(args, " "))
		switch args[0] {
		case "/Create":
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
			fake.state = "4\n267009"
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
	fake := &fakeWindows{t: t, git: true, winget: true}
	previousRunner, previousElevated, previousLook, previousStop := serviceRunner, runElevated, lookPath, signalServiceStop
	t.Cleanup(func() {
		serviceRunner, runElevated, lookPath, signalServiceStop = previousRunner, previousElevated, previousLook, previousStop
	})
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
		sid: "S-1-5-21-1-2-3-1001", system: fakeSystem, out: &out,
	}, &out
}

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
	fake.existing(t, service.ModeBootTask, "S-1-5-21-1-2-3-1001", testStateDir)
	fake.state = "4\n267009"
	fake.listening = true
	host, out := testTaskHost(service.Environment{Administrator: true})
	err := host.install("")
	if err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Fatalf("install with a failing elevated step: %v", err)
	}
	want := [][]string{{"service", "elevated-install", "--state-dir", testStateDir, "--attach", fmt.Sprint(os.Getpid())}}
	if !reflect.DeepEqual(fake.elevated, want) {
		t.Errorf("elevated %q, want %q", fake.elevated, want)
	}
	if got := strings.Count(out.String(), "Windows asks once for administrator approval to register the OwnGit task that starts at boot and allow OwnGit through Windows Firewall on private networks.\n"); got != 1 {
		t.Errorf("output:\n%s", out.String())
	}
	// The running server was asked to stop in order before the prompt.
	if !reflect.DeepEqual(fake.stopAsked, []string{testStateDir}) || slicesContainPrefix(fake.calls, "schtasks /End") {
		t.Errorf("stop asked %q, calls %q", fake.stopAsked, fake.calls)
	}

	fake.elevated = nil
	fake.elevate = func([]string) (int, error) { return 0, errElevationCancelled }
	out.Reset()
	if err := host.install(""); !errors.Is(err, errElevationCancelled) || !strings.Contains(out.String(), "Nothing changed") {
		t.Errorf("declined prompt: %v\n%s", err, out.String())
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
	fake.existing(t, service.ModeBootTask, "S-1-5-21-1-2-3-1001", testStateDir)
	fake.git = false
	host, out := testTaskHost(service.Environment{Administrator: true, Getenv: func(name string) string {
		return map[string]string{"SSH_CLIENT": "10.0.0.2 5000 22"}[name]
	}, NoDesktop: false})
	_ = host.install("")
	if len(fake.elevated) != 1 || !reflect.DeepEqual(fake.elevated[0][4:6], []string{"--headless", "--install-git"}) || !strings.Contains(out.String(), ", and install Git with winget.") {
		t.Errorf("elevated %q, output:\n%s", fake.elevated, out.String())
	}
	fake.winget, fake.elevated, fake.calls = false, nil, nil
	out.Reset()
	if err := host.install(""); err == nil || len(fake.elevated) != 0 || slicesContainPrefix(fake.calls, "schtasks") || !strings.Contains(out.String(), "https://git-scm.com/download/win") {
		t.Errorf("without winget: %v, elevated %q, calls %q\n%s", err, fake.elevated, fake.calls, out.String())
	}
}

// Without a desktop (SSH, not elevated) no prompt can appear: the command
// says what to do instead.
func TestTaskInstallWithoutADesktopExplains(t *testing.T) {
	fake := newFakeWindows(t)
	fake.existing(t, service.ModeBootTask, "S-1-5-21-1-2-3-1001", testStateDir)
	host, out := testTaskHost(service.Environment{Administrator: true, NoDesktop: true})
	if err := host.install(""); err == nil || len(fake.elevated) != 0 || !strings.Contains(out.String(), `"Run as administrator"`) {
		t.Errorf("%v, elevated %q\n%s", err, fake.elevated, out.String())
	}
}

// A standard account registers a sign-in task itself, without any prompt.
func TestTaskInstallForAStandardAccount(t *testing.T) {
	fake := newFakeWindows(t)
	fake.existing(t, service.ModeLogonTask, "S-1-5-21-1-2-3-1001", testStateDir)
	fake.failRun = true
	host, _ := testTaskHost(service.Environment{})
	if err := host.install(""); err == nil || !strings.Contains(err.Error(), "schtasks.exe /Run") {
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
	if err := host.install(""); err == nil || !strings.Contains(err.Error(), "another account") {
		t.Errorf("task of another account: %v", err)
	}
	fake.definition = strings.Replace(fake.definition, "owngit service install", "someone", 1)
	if err := host.install(""); !errors.Is(err, service.ErrForeignTask) {
		t.Errorf("foreign task: %v", err)
	}
	if len(fake.elevated) != 0 || slicesContainPrefix(fake.calls, "schtasks") {
		t.Errorf("changed something: %q %q", fake.elevated, fake.calls)
	}
}

// The elevated copy registers the task for its own executable, adds the
// firewall rule for it and starts the task, in that order.
func TestTaskElevatedInstall(t *testing.T) {
	fake := newFakeWindows(t)
	host, _ := testTaskHost(service.Environment{Administrator: true, Elevated: true})
	noErr(t, host.elevatedInstall(testStateDir, true, false))
	want := []string{"schtasks /Create", "powershell firewall-allow " + host.executable, "schtasks /End /TN \\OwnGit", "schtasks /Run /TN \\OwnGit"}
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
	// The temporary definition file is gone.
	path := strings.Fields(fake.calls[0])[5]
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("definition file %s stayed: %v", path, err)
	}
}

func TestTaskElevatedUninstallKeepsTheData(t *testing.T) {
	fake := newFakeWindows(t)
	fake.existing(t, service.ModeBootTask, "S-1-5-21-1-2-3-1001", testStateDir)
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
	fake.existing(t, service.ModeBootTask, "S-1-5-21-1-2-3-1001", testStateDir)
	fake.definition = strings.Replace(fake.definition, testStateDir, stateDir, -1)
	fake.state = "2\n0"
	host, out := testTaskHost(service.Environment{Administrator: true})
	noErr(t, host.status())
	for _, want := range []string{
		"Windows keeps the task queued", "until someone has signed in on this computer",
		"  Log:      " + service.TaskLogFile(stateDir), "  State:    " + stateDir,
		"no rule for this owngit.exe", "Setup is not complete.",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status lacks %q:\n%s", want, out.String())
		}
	}
}

func TestTaskStopAsksTheServerFirst(t *testing.T) {
	fake := newFakeWindows(t)
	fake.existing(t, service.ModeBootTask, "S-1-5-21-1-2-3-1001", testStateDir)
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
