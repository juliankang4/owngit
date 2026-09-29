package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"owngit/internal/doctor"
	"owngit/internal/service"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// On Windows the checkup reads who owns the folders and which rules let
// devices reach the program on the listen port, and gives an
// administrator one repair for both: "owngit service install", whose one
// approval returns the folders and adds OwnGit's rule.
func TestDoctorReadsWindowsFoldersAndFirewall(t *testing.T) {
	ctx := context.Background()
	fake := newFakeWindows(t)
	fake.owners[testStateDir] = administratorsSID
	fake.access = "2 7 0\n1\t4\t6\t7654\t*\n" // a rule for public networks only
	host, _ := testTaskHost(service.Environment{Administrator: true})
	facts := doctor.Facts{GOOS: "windows", Server: doctor.ServerRunning, SetupComplete: true, Listen: "0.0.0.0:7654", OtherDevices: true, Program: testServiceExecutable, Administrator: true}
	noErr(t, host.readDoctorFacts(ctx, &facts, []string{testStateDir, ""}))
	want := []webui.Finding{
		{Code: webui.MsgDoctorAdministratorsFolder, Args: []string{testStateDir}, Repair: "owngit service install"},
		{Code: webui.MsgDoctorWindowsRule, Args: []string{testServiceExecutable}, Repair: "owngit service install"},
	}
	if got := doctor.Diagnose(facts); !reflect.DeepEqual(got, want) {
		t.Errorf("findings %+v, want %+v", got, want)
	}
	if !reflect.DeepEqual(fake.calls, []string{"powershell firewall-show", "powershell firewall-access " + testServiceExecutable}) {
		t.Errorf("calls %q", fake.calls)
	}

	// A firewall that cannot be read is said so, never taken as open.
	fake.access = ""
	facts = doctor.Facts{GOOS: "windows", Server: doctor.ServerRunning, SetupComplete: true, Listen: "0.0.0.0:7654", OtherDevices: true, Program: testServiceExecutable}
	err := host.readDoctorFacts(ctx, &facts, nil)
	if err == nil || !strings.Contains(err.Error(), "Access is denied.") {
		t.Errorf("unreadable firewall: %v", err)
	}

	// A standard account's folder outside its profile is left to an
	// administrator, with no command.
	standard, _ := testTaskHost(service.Environment{})
	fake.owners[`D:\OwnGit\repos`] = administratorsSID
	facts = doctor.Facts{GOOS: "windows", Server: doctor.ServerRunning, SetupComplete: true, Listen: "127.0.0.1:7654"}
	noErr(t, standard.readDoctorFacts(ctx, &facts, []string{testStateDir, `D:\OwnGit\repos`}))
	if got := doctor.Diagnose(facts); len(got) != 2 || got[0].Code != webui.MsgDoctorAdministratorsFolder || got[0].Repair != "owngit service install" ||
		got[1].Code != webui.MsgDoctorAdministratorsFolderElsewhere || got[1].Repair != "" {
		t.Errorf("standard account: %+v", got)
	}

	// A folder whose owner cannot be read is a check that could not run.
	fake.ownerErrors[testStateDir] = errors.New("Access is denied.")
	facts = doctor.Facts{GOOS: "windows", Server: doctor.ServerRunning, SetupComplete: true, Listen: "127.0.0.1:7654"}
	noErr(t, host.readDoctorFacts(ctx, &facts, []string{testStateDir}))
	if got := doctor.Diagnose(facts); len(got) != 1 || got[0].Code != webui.MsgDoctorUncheckedOwner || !got[0].Unchecked || !strings.Contains(got[0].Args[0], "Access is denied.") {
		t.Errorf("unreadable owner: %+v", got)
	}
}

// The Settings page reports a repository folder it could not read as a
// check that could not run.
func TestServerDiagnosisSaysWhatItCouldNotRead(t *testing.T) {
	diagnosis := serverDiagnosis(t.TempDir(), "127.0.0.1:18961", false, func(context.Context) (string, error) {
		return "", errors.New("state closed")
	})
	got := diagnosis(context.Background())
	if runtime.GOOS == "windows" {
		t.Skip("the folder owners are read on this computer")
	}
	if want := []webui.Finding{{Code: webui.MsgDoctorUncheckedOwner, Args: []string{"state closed"}, Unchecked: true}}; !reflect.DeepEqual(got, want) {
		t.Errorf("findings %+v", got)
	}
}

// The macOS application firewall is read with its own tool; a state it
// cannot read is an error.
func TestMacFirewall(t *testing.T) {
	previous := doctorTool
	t.Cleanup(func() { doctorTool = previous })
	for _, test := range []struct {
		global, blockAll, app string
		want                  doctor.Firewall
		fails                 bool
	}{
		{global: "Firewall is disabled. (State = 0)\n", want: doctor.Firewall{}},
		{global: "Firewall is enabled. (State = 1)\n", blockAll: "Firewall has block all state set to disabled.\n", app: "Incoming connection to /x/owngit is permitted.\n", want: doctor.Firewall{AppFirewall: true}},
		{global: "Firewall is enabled. (State = 1)\n", blockAll: "Firewall has block all state set to enabled.\n", app: "Incoming connection to /x/owngit is permitted.\n", want: doctor.Firewall{AppFirewall: true, BlockAll: true}},
		{global: "Firewall is enabled. (State = 1)\n", blockAll: "Firewall has block all state set to disabled.\n", app: "Incoming connection to /x/owngit is blocked.\n", want: doctor.Firewall{AppFirewall: true, AppBlocked: true}},
		{global: "something else\n", fails: true},
	} {
		doctorTool = func(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
			if name != socketFilter {
				t.Fatalf("ran %s", name)
			}
			switch args[0] {
			case "--getglobalstate":
				return []byte(test.global), nil
			case "--getblockall":
				return []byte(test.blockAll), nil
			case "--getappblocked":
				if args[1] != "/x/owngit" {
					t.Errorf("asked about %q", args[1])
				}
				return []byte(test.app), nil
			}
			return nil, errors.New("unknown option")
		}
		got, err := macFirewall(context.Background(), "/x/owngit")
		if (err != nil) != test.fails || !reflect.DeepEqual(got, test.want) {
			t.Errorf("%q: %+v, %v", test.global, got, err)
		}
	}
}

// ufw is read from its configuration file, within a size limit, and
// firewalld from its tool; an answer the checkup cannot read is an error,
// never a firewall that is off.
func TestLinuxFirewall(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the firewalld tool is checked as a root-controlled Linux program")
	}
	previous := doctorTool
	t.Cleanup(func() { doctorTool = previous })
	dir := t.TempDir()
	config := filepath.Join(dir, "ufw.conf")
	noErr(t, os.WriteFile(config, []byte("ENABLED=yes\n"), 0o600))
	// Any root-controlled program stands for firewall-cmd; the fake answers.
	tool, err := exec.LookPath("sh")
	noErr(t, err)
	tool, err = filepath.EvalSymlinks(tool)
	noErr(t, err)
	var answer []byte
	var answerErr error
	doctorTool = func(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
		if name != tool {
			t.Fatalf("ran %s", name)
		}
		if strings.HasPrefix(args[0], "--get-zone-of-interface=") {
			return []byte("home\n"), nil
		}
		return answer, answerErr
	}
	for _, test := range []struct {
		answer           string
		err              error
		firewalld, fails bool
	}{
		{answer: "running\n", firewalld: true},
		{answer: "not running\n", err: errors.New("exit status 252")},
		{answer: "", err: errors.New("dbus unavailable"), fails: true},
		{answer: "something else\n", fails: true},
		{answer: "not running\n", fails: true}, // exit status 0 with that answer
	} {
		answer, answerErr = []byte(test.answer), test.err
		got, err := linuxFirewall(context.Background(), "0.0.0.0", config, tool)
		if (err != nil) != test.fails || err == nil && (!got.UFW || got.Firewalld != test.firewalld) {
			t.Errorf("%q, %v: %+v, %v", test.answer, test.err, got, err)
		}
	}
	// No ufw, no firewalld.
	answer, answerErr = []byte("not running\n"), errors.New("exit status 252")
	if got, err := linuxFirewall(context.Background(), "0.0.0.0", filepath.Join(dir, "missing"), filepath.Join(dir, "no-firewall-cmd")); err != nil || got.UFW || got.Firewalld {
		t.Errorf("none: %+v, %v", got, err)
	}
	// A configuration file past the limit is not read.
	noErr(t, os.WriteFile(config, []byte(strings.Repeat("#", ufwConfigLimit)+"\nENABLED=yes\n"), 0o600))
	if _, err := linuxFirewall(context.Background(), "0.0.0.0", config, filepath.Join(dir, "no-firewall-cmd")); err == nil {
		t.Error("an oversized ufw.conf was read")
	}
}

// Each checkup tool ends with the caller's context, and one that floods
// its output ends there; both are errors. A normal tool's output is
// returned.
func TestRunBounded(t *testing.T) {
	if mode := os.Getenv("OWNGIT_TEST_DOCTOR_TOOL"); mode != "" {
		switch mode {
		case "echo":
			fmt.Print("running")
		case "sleep":
			time.Sleep(time.Minute)
		case "flood":
			chunk := strings.Repeat("x", 4096)
			for {
				fmt.Print(chunk)
			}
		}
		os.Exit(0)
	}
	helper := func(mode string) []string {
		return append(os.Environ(), "OWNGIT_TEST_DOCTOR_TOOL="+mode)
	}
	args := []string{"-test.run=^TestRunBounded$"}
	output, err := runBounded(context.Background(), helper("echo"), os.Args[0], args...)
	if err != nil || string(output) != "running" {
		t.Errorf("normal tool: %q, %v", output, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := runBounded(ctx, helper("sleep"), os.Args[0], args...); err == nil || !strings.Contains(err.Error(), "did not finish") || time.Since(started) > 10*time.Second {
		t.Errorf("sleeping tool: %v after %s", err, time.Since(started))
	}
	started = time.Now()
	if _, err := runBounded(context.Background(), helper("flood"), os.Args[0], args...); err == nil || !strings.Contains(err.Error(), "printed more than") || time.Since(started) > 10*time.Second {
		t.Errorf("flooding tool: %v after %s", err, time.Since(started))
	}
}

func TestUFWEnabled(t *testing.T) {
	for config, want := range map[string]bool{
		"# comment\nENABLED=yes\nLOGLEVEL=low\n": true,
		"ENABLED=\"yes\"\n":                      true,
		"ENABLED=no\n":                           false,
		"":                                       false,
	} {
		if got := ufwEnabled(config); got != want {
			t.Errorf("%q: %v", config, got)
		}
	}
}

// Without a server, the checkup says so and names the command that starts
// OwnGit; the JSON has the English sentence beside its code. When the
// state's server does not run but something answers at its address, that
// program keeps OwnGit from listening there. The state listens on its own
// address, and the health check is a fake.
func TestDoctorCommandWithoutAServer(t *testing.T) {
	health := useFakeHealth(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	_, err := captureStdout(func() error {
		return run([]string{"network", "set", "--state-dir", stateDir, "--listen", "127.0.0.1:18961"})
	})
	noErr(t, err)
	output, err := captureStdout(func() error { return run([]string{"doctor", "--state-dir", stateDir, "--json"}) })
	noErr(t, err)
	var report struct {
		StateDir string `json:"state_dir"`
		Running  bool   `json:"running"`
		Listen   string `json:"listen"`
		Findings []struct {
			Code, Message, Repair string
		} `json:"findings"`
	}
	noErr(t, json.Unmarshal([]byte(output), &report))
	if report.StateDir != stateDir || report.Running || report.Listen != "127.0.0.1:18961" || len(report.Findings) != 1 ||
		report.Findings[0].Code != string(webui.MsgDoctorNotRunning) || report.Findings[0].Message != "OwnGit is not running." || report.Findings[0].Repair != "owngit service install" {
		t.Errorf("report:\n%s", output)
	}
	text, err := captureStdout(func() error { return run([]string{"doctor", "--state-dir", stateDir}) })
	noErr(t, err)
	if !strings.Contains(text, "Problem: OwnGit is not running.\nRepair:  owngit service install\n") {
		t.Errorf("text:\n%s", text)
	}
	health.answering = true
	text, err = captureStdout(func() error { return run([]string{"doctor", "--state-dir", stateDir}) })
	noErr(t, err)
	if !strings.Contains(text, "Problem: OwnGit is not running for this state directory, and another program answers at 127.0.0.1:18961") || strings.Contains(text, "No problem found") {
		t.Errorf("another program:\n%s", text)
	}
	for _, host := range health.checked {
		if host != "127.0.0.1:18961" {
			t.Errorf("checked %s", host)
		}
	}
}

// A state whose server runs but does not answer, and one that something
// else holds, are said so, never taken as running. The health check is a
// fake that does not answer.
func TestDoctorCommandTrustsTheStateOnly(t *testing.T) {
	useFakeHealth(t)
	codes := func(stateDir string) []string {
		t.Helper()
		output, err := captureStdout(func() error { return run([]string{"doctor", "--state-dir", stateDir, "--json"}) })
		noErr(t, err)
		var report struct {
			Running  bool `json:"running"`
			Findings []struct {
				Code string `json:"code"`
			} `json:"findings"`
		}
		noErr(t, json.Unmarshal([]byte(output), &report))
		if report.Running {
			t.Errorf("reported as running:\n%s", output)
		}
		var codes []string
		for _, finding := range report.Findings {
			codes = append(codes, finding.Code)
		}
		return codes
	}
	runningDir := filepath.Join(t.TempDir(), "state")
	served := startServed(t, runningDir)
	if got := codes(runningDir); !reflect.DeepEqual(got, []string{string(webui.MsgDoctorSilent)}) {
		t.Errorf("running without an answer: %q", got)
	}
	served.stop()

	stateDir := filepath.Join(t.TempDir(), "held")
	_, err := captureStdout(func() error {
		return run([]string{"network", "set", "--state-dir", stateDir, "--listen", "127.0.0.1:18962"})
	})
	noErr(t, err)
	release, err := state.AcquireOfflineLock(stateDir)
	noErr(t, err)
	defer release()
	if got := codes(stateDir); !reflect.DeepEqual(got, []string{string(webui.MsgDoctorUncheckedServer)}) {
		t.Errorf("held state directory: %q", got)
	}
}
