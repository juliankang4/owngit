package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"owngit/internal/doctor"
	"owngit/internal/service"
	"owngit/internal/webui"
)

// On Windows the checkup reads who owns the folders and which rules let
// devices reach the program on the listen port, and gives an
// administrator one repair for both: "owngit service install", whose one
// approval returns the folders and adds OwnGit's rule.
func TestDoctorReadsWindowsFoldersAndFirewall(t *testing.T) {
	fake := newFakeWindows(t)
	fake.owners[testStateDir] = administratorsSID
	fake.access = "2 7\n1\t4\t6\t7654\n" // a rule for public networks only
	host, _ := testTaskHost(service.Environment{Administrator: true})
	facts := doctor.Facts{GOOS: "windows", Running: true, SetupComplete: true, Listen: "0.0.0.0:7654", OtherDevices: true, Program: testServiceExecutable, Administrator: true}
	noErr(t, host.readDoctorFacts(&facts, []string{testStateDir, ""}))
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
	facts = doctor.Facts{GOOS: "windows", Running: true, SetupComplete: true, Listen: "0.0.0.0:7654", OtherDevices: true, Program: testServiceExecutable}
	err := host.readDoctorFacts(&facts, nil)
	if err == nil || !strings.Contains(err.Error(), "Access is denied.") {
		t.Errorf("unreadable firewall: %v", err)
	}
}

// The macOS application firewall is read with its own tool; a state it
// cannot read is an error.
func TestMacFirewall(t *testing.T) {
	previous := serviceRunner
	t.Cleanup(func() { serviceRunner = previous })
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
		serviceRunner = func(_ context.Context, name string, args ...string) ([]byte, error) {
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
		got, err := macFirewall("/x/owngit")
		if (err != nil) != test.fails || got != test.want {
			t.Errorf("%q: %+v, %v", test.global, got, err)
		}
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
// OwnGit; the JSON has the English sentence beside its code.
func TestDoctorCommandWithoutAServer(t *testing.T) {
	useFakeHealth(t)
	stateDir := filepath.Join(t.TempDir(), "state")
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
	if report.StateDir != stateDir || report.Running || report.Listen != "127.0.0.1:7654" || len(report.Findings) != 1 ||
		report.Findings[0].Code != string(webui.MsgDoctorNotRunning) || report.Findings[0].Message != "OwnGit is not running." || report.Findings[0].Repair != "owngit service install" {
		t.Errorf("report:\n%s", output)
	}
	text, err := captureStdout(func() error { return run([]string{"doctor", "--state-dir", stateDir}) })
	noErr(t, err)
	if !strings.Contains(text, "Problem: OwnGit is not running.\nRepair:  owngit service install\n") {
		t.Errorf("text:\n%s", text)
	}
}
