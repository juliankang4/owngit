package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"owngit/internal/auth"
	"owngit/internal/state"
)

func TestParseFlagsAndOperandsAcceptsOptionsAnywhere(t *testing.T) {
	for _, test := range []struct {
		arguments []string
		stateDir  string
		verbose   bool
		operands  []string
	}{
		{[]string{"host.test"}, "", false, []string{"host.test"}},
		{[]string{"host.test", "--state-dir", "dir"}, "dir", false, []string{"host.test"}},
		{[]string{"--state-dir", "dir", "host.test"}, "dir", false, []string{"host.test"}},
		{[]string{"host.test", "--verbose", "--state-dir=dir"}, "dir", true, []string{"host.test"}},
		{[]string{"a", "--verbose", "b"}, "", true, []string{"a", "b"}},
		{[]string{"--state-dir", "dir", "--", "--verbose", "b"}, "dir", false, []string{"--verbose", "b"}},
		{[]string{"a", "--", "--state-dir"}, "", false, []string{"a", "--state-dir"}},
		{nil, "", false, nil},
	} {
		flags := flag.NewFlagSet("test", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		stateDir := flags.String("state-dir", "", "")
		verbose := flags.Bool("verbose", false, "")
		operands, err := parseFlagsAndOperands(flags, test.arguments)
		noErr(t, err)
		if *stateDir != test.stateDir || *verbose != test.verbose || !slices.Equal(operands, test.operands) {
			t.Errorf("%q: state-dir=%q verbose=%v operands=%q, want %q %v %q", test.arguments, *stateDir, *verbose, operands, test.stateDir, test.verbose, test.operands)
		}
	}
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	if _, err := parseFlagsAndOperands(flags, []string{"host.test", "--unknown"}); err == nil {
		t.Fatal("an unknown option after an operand was accepted")
	}
}

// isolateDefaultState points the default state directory at a temporary
// folder, so a command that ignored --state-dir could not reach real state,
// and returns that default path.
func isolateDefaultState(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("AppData", filepath.Join(home, "appdata"))
	return defaultStateDir()
}

// The usage line is "owngit approve-host <host> [options]", so options after
// the host name apply.
func TestApproveHostAcceptsOptionsAfterTheHost(t *testing.T) {
	defaultState := isolateDefaultState(t)
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	store, err := state.Open(context.Background(), stateDir)
	noErr(t, err)
	accessHash, _ := auth.HashPassword("shared-password")
	adminHash, _ := auth.HashPassword("admin-password")
	noErr(t, store.CompleteSetup(context.Background(), filepath.Join(root, "repositories"), "password", accessHash, adminHash, true))
	noErr(t, store.Close())

	noErr(t, runCommand("approve-host", []string{"after.test", "--state-dir", stateDir}))
	result := commandJSON(t, func() error {
		return run([]string{"approve-host", "before.test", "--state-dir", stateDir, "--json"})
	})
	if result["ok"] != true || result["host"] != "before.test" || result["restart_required"] != true {
		t.Fatalf("approve-host JSON result: %v", result)
	}
	for _, arguments := range [][]string{
		{"one.test", "--state-dir", stateDir, "two.test"},
		{"--state-dir", stateDir},
	} {
		if err := runCommand("approve-host", arguments); err == nil || !strings.Contains(err.Error(), "exactly one host name") {
			t.Errorf("%q: err=%v, want the one-host refusal", arguments, err)
		}
	}

	store, err = state.Open(context.Background(), stateDir)
	noErr(t, err)
	defer store.Close()
	hosts, err := store.TrustedHosts(context.Background())
	noErr(t, err)
	if !slices.Contains(hosts, "after.test") || !slices.Contains(hosts, "before.test") || slices.Contains(hosts, "one.test") {
		t.Fatalf("trusted hosts=%v", hosts)
	}
	if _, err := os.Stat(defaultState); !os.IsNotExist(err) {
		t.Fatalf("the default state directory was touched: %v", err)
	}
}

func TestJSONErrorsRespectRawFlagOrder(t *testing.T) {
	isolateDefaultState(t)
	for _, command := range [][]string{
		{"network", "show"}, {"network", "set"}, {"network", "reset"},
		{"tailscale", "status"}, {"tailscale", "on"}, {"tailscale", "off"},
		{"backup"}, {"restore"}, {"doctor"}, {"update"},
		{"forget-check-container"}, {"reset-admin"}, {"approve-host", "host.test"}, {"setup-link"},
	} {
		for _, tc := range []struct {
			arguments []string
			json      bool
		}{
			{[]string{"extra", "--json"}, true},
			{[]string{"--json", "extra"}, true},
			{[]string{"--json", "extra", "--json=false"}, false},
			{[]string{"--json=1", "extra", "--json=false"}, false},
			{[]string{"extra", "--", "--json"}, false},
			{[]string{"--unknown", "--json"}, true},
		} {
			arguments := append(append([]string{}, command...), tc.arguments...)
			err := run(arguments)
			if err == nil || (errorCode(err) != "") != tc.json || tc.json && errorCode(err) != "invalid_arguments" {
				t.Errorf("%v: error %v, JSON=%v", arguments, err, tc.json)
			}
			if tc.json {
				output, captureErr := captureStdout(func() error { reportError(os.Stdout, err); return nil })
				noErr(t, captureErr)
				var result struct {
					OK    bool `json:"ok"`
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				if json.Unmarshal([]byte(output), &result) != nil || result.OK || result.Error.Code != "invalid_arguments" {
					t.Errorf("%v: invalid JSON error %q", arguments, output)
				}
			}
		}
	}
}

// setup-link and reset-admin take no operands. A stray operand used to end
// option parsing silently, so options after it, including --state-dir, were
// ignored and the command used the default state directory.
func TestStateCommandsRefuseOperands(t *testing.T) {
	defaultState := isolateDefaultState(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	passwordFile := filepath.Join(t.TempDir(), "password")
	noErr(t, os.WriteFile(passwordFile, []byte("new-admin-password\n"), 0o600))
	noErr(t, state.ProtectPrivatePath(passwordFile, false))
	for command, arguments := range map[string][]string{
		"setup-link":  {"http://127.0.0.1:7654", "--state-dir", stateDir, "--no-open"},
		"reset-admin": {"extra", "--state-dir", stateDir, "--password-file", passwordFile},
	} {
		err := runCommand(command, arguments)
		if err == nil || !strings.Contains(err.Error(), "takes no positional arguments") {
			t.Errorf("%s %q: err=%v, want the operand refusal", command, arguments, err)
		}
	}
	for _, tc := range []struct {
		command string
		args    []string
	}{
		{"setup-link", []string{"--state-dir", passwordFile, "--json"}},
		{"reset-admin", []string{"--state-dir", passwordFile, "--password-file", passwordFile, "--json"}},
		{"approve-host", []string{"host.test", "--state-dir", passwordFile, "--json"}},
	} {
		if err := runCommand(tc.command, tc.args); errorCode(err) != "state_unavailable" {
			t.Errorf("%s %v: error %v, want state_unavailable", tc.command, tc.args, err)
		}
	}
	for _, path := range []string{stateDir, defaultState} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s was touched: %v", path, err)
		}
	}
}
