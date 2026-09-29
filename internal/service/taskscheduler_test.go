package service

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"
)

const testSID = "S-1-5-21-1111111111-2222222222-3333333333-1001"

func bootPlan() TaskPlan {
	return TaskPlan{
		Mode: ModeBootTask, Executable: `C:\Program Files\OwnGit\owngit.exe`,
		StateDir: `C:\Users\you\My Files\AppData\Roaming\owngit`, UserSID: testSID,
		Conhost: `C:\Windows\System32\conhost.exe`,
	}
}

func TestRenderBootTask(t *testing.T) {
	definition, err := RenderTask(bootPlan())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<?xml version="1.0" encoding="UTF-16"?>`,
		"<Source>owngit service install</Source>",
		"<BootTrigger><Enabled>true</Enabled></BootTrigger>",
		"<UserId>" + testSID + "</UserId>",
		"<LogonType>S4U</LogonType>",
		"<RunLevel>LeastPrivilege</RunLevel>",
		"<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>",
		"<StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>",
		"<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>",
		"<RestartOnFailure><Interval>PT1M</Interval><Count>999</Count></RestartOnFailure>",
		"<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>",
		`<Command>C:\Program Files\OwnGit\owngit.exe</Command>`,
		`<Arguments>serve --state-dir "C:\Users\you\My Files\AppData\Roaming\owngit" --no-open --log-file "C:\Users\you\My Files\AppData\Roaming\owngit\logs\service.log" --service --headless=false</Arguments>`,
	} {
		if !strings.Contains(definition, want) {
			t.Errorf("boot task lacks %s:\n%s", want, definition)
		}
	}
	for _, unwanted := range []string{"--listen", "--base-url", "LogonTrigger", "Password", "HighestAvailable", "conhost"} {
		if strings.Contains(definition, unwanted) {
			t.Errorf("boot task contains %s", unwanted)
		}
	}
}

func TestRenderLogonTaskStartsThroughHeadlessConhost(t *testing.T) {
	plan := bootPlan()
	plan.Mode, plan.Headless = ModeLogonTask, true
	definition, err := RenderTask(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<LogonTrigger><Enabled>true</Enabled><UserId>" + testSID + "</UserId></LogonTrigger>",
		"<LogonType>InteractiveToken</LogonType>",
		`<Command>C:\Windows\System32\conhost.exe</Command>`,
		`<Arguments>--headless "C:\Program Files\OwnGit\owngit.exe" serve --state-dir`,
		"--service --headless=true</Arguments>",
	} {
		if !strings.Contains(definition, want) {
			t.Errorf("logon task lacks %s:\n%s", want, definition)
		}
	}
	if strings.Contains(definition, "BootTrigger") || strings.Contains(definition, "S4U") {
		t.Errorf("logon task has a boot trigger or S4U:\n%s", definition)
	}
}

func TestRenderTaskRefusesWhatATaskCannotHold(t *testing.T) {
	for name, change := range map[string]func(*TaskPlan){
		"relative executable":   func(plan *TaskPlan) { plan.Executable = `owngit.exe` },
		"POSIX state directory": func(plan *TaskPlan) { plan.StateDir = `/home/you/owngit` },
		"percent sign":          func(plan *TaskPlan) { plan.StateDir = `C:\Users\%USERNAME%\owngit` },
		"quotation mark":        func(plan *TaskPlan) { plan.StateDir = `C:\a" --listen 0.0.0.0:1 "b` },
		"control character":     func(plan *TaskPlan) { plan.StateDir = "C:\\a\nb" },
		"account name":          func(plan *TaskPlan) { plan.UserSID = `OWNGIT\ann` },
		"systemd mode":          func(plan *TaskPlan) { plan.Mode = ModeUser },
		"logon without conhost": func(plan *TaskPlan) { plan.Mode, plan.Conhost = ModeLogonTask, "" },
	} {
		plan := bootPlan()
		change(&plan)
		if definition, err := RenderTask(plan); err == nil {
			t.Errorf("%s: rendered\n%s", name, definition)
		}
	}
}

// A path is one argument whatever it contains, and nothing in it becomes
// XML markup or another argument.
func TestTaskArgumentsKeepEveryPathWhole(t *testing.T) {
	for _, stateDir := range []string{
		`C:\Users\you\My Files\owngit`,
		`C:\state & more\x`,
		`C:\it's <here>\`,
		`D:\with trailing backslash\`,
		`\\nas\share\owngit`,
		`C:\이름\owngit`,
	} {
		plan := bootPlan()
		plan.StateDir, plan.Headless = stateDir, true
		definition, err := RenderTask(plan)
		if err != nil {
			t.Fatalf("%q: %v", stateDir, err)
		}
		installed, err := ParseTask(EncodeUTF16(definition))
		if err != nil {
			t.Fatalf("%q: %v", stateDir, err)
		}
		if installed.StateDir != stateDir || installed.Executable != plan.Executable || !installed.Headless || installed.User != testSID || installed.Mode != ModeBootTask {
			t.Errorf("%q read back as %+v", stateDir, installed)
		}
		start := strings.Index(definition, "<Arguments>") + len("<Arguments>")
		end := strings.Index(definition, "</Arguments>")
		arguments := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&#39;", "'", "&#34;", `"`).Replace(definition[start:end])
		if got := SplitWindowsCommandLine(arguments); !reflect.DeepEqual(got, plan.ServeArguments()) {
			t.Errorf("%q: arguments split into %q, want %q", stateDir, got, plan.ServeArguments())
		}
	}
}

func TestParseTask(t *testing.T) {
	plan := bootPlan()
	plan.Mode = ModeLogonTask
	definition, err := RenderTask(plan)
	if err != nil {
		t.Fatal(err)
	}
	// As Task Scheduler returns it: UTF-8 text that still declares UTF-16.
	installed, err := ParseTask([]byte(definition))
	if err != nil {
		t.Fatal(err)
	}
	want := Installed{Mode: ModeLogonTask, UnitPath: `\OwnGit`, User: testSID, Executable: plan.Executable, StateDir: plan.StateDir}
	if installed != want {
		t.Errorf("ParseTask = %+v, want %+v", installed, want)
	}
	// A task of an earlier build passed a bare --headless.
	for arguments, headless := range map[string]bool{"--service --headless=false": false, "--service --headless=true": true, "--service --headless": true, "--service": false} {
		changed := strings.Replace(definition, "--service --headless=false", arguments, 1)
		if installed, err := ParseTask([]byte(changed)); err != nil || installed.Headless != headless {
			t.Errorf("%s: headless %v, %v; want %v", arguments, installed.Headless, err, headless)
		}
	}
	foreign := strings.Replace(definition, "<Source>owngit service install</Source>", "<Source>someone else</Source>", 1)
	if _, err := ParseTask([]byte(foreign)); !errors.Is(err, ErrForeignTask) {
		t.Errorf("a foreign task: %v", err)
	}
	if _, err := ParseTask([]byte("<Task")); err == nil {
		t.Error("broken XML was accepted")
	}
}

func TestWindowsCommandLineRoundTrip(t *testing.T) {
	for _, arguments := range [][]string{
		{"serve", "--state-dir", `C:\a b\c`},
		{"", "x"},
		{`a"b`, `c\"d`, `e\\`, `f\\"g`, "tab\there"},
		{`C:\trailing\`, `C:\trailing space\ `},
	} {
		line := WindowsCommandLine(arguments)
		if got := SplitWindowsCommandLine(line); !reflect.DeepEqual(got, arguments) {
			t.Errorf("%q -> %s -> %q", arguments, line, got)
		}
	}
	// The rules of CommandLineToArgvW, as documented.
	for line, want := range map[string][]string{
		`"abc" d e`:        {"abc", "d", "e"},
		`a\\b d"e f"g h`:   {`a\\b`, "de fg", "h"},
		`a\\\"b c d`:       {`a\"b`, "c", "d"},
		`a\\\\"b c" d e`:   {`a\\b c`, "d", "e"},
		`a"b"" c d`:        {`ab" c d`},
		`  spaced   out  `: {"spaced", "out"},
	} {
		if got := SplitWindowsCommandLine(line); !reflect.DeepEqual(got, want) {
			t.Errorf("SplitWindowsCommandLine(%s) = %q, want %q", line, got, want)
		}
	}
}

func TestFirewallRule(t *testing.T) {
	program := `C:\Program Files\OwnGit\owngit.exe`
	rule, found := ParseFirewallRule("C:\\Program Files\\OwnGit\\OWNGIT.EXE\r\n2\r\nTrue\r\n1\r\n1\r\n")
	if !found || !rule.Allows(program) {
		t.Errorf("private inbound allow rule %+v found=%v does not allow %s", rule, found, program)
	}
	for output, why := range map[string]string{
		"":                                                  "no rule",
		program + "\r\n4\r\nTrue\r\n1\r\n1\r\n":             "Public profile only",
		program + "\r\n6\r\nTrue\r\n1\r\n1\r\n":             "Private and Public profiles",
		program + "\r\n2\r\nFalse\r\n1\r\n1\r\n":            "disabled",
		program + "\r\n2\r\nTrue\r\n2\r\n1\r\n":             "outbound",
		program + "\r\n2\r\nTrue\r\n1\r\n0\r\n":             "block",
		`C:\old\owngit.exe` + "\r\n2\r\nTrue\r\n1\r\n1\r\n": "another path",
	} {
		if rule, _ := ParseFirewallRule(output); rule.Allows(program) {
			t.Errorf("%s: %+v allows %s", why, rule, program)
		}
	}
}

// The scripts that run with administrator rights never hold a path; the
// program arrives in an environment variable, and the Windows Firewall COM
// API avoids loading a module through PSModulePath.
func TestFirewallScriptsTakeTheProgramFromTheEnvironment(t *testing.T) {
	if !strings.Contains(FirewallAllowScript, "$rule.ApplicationName = $env:"+FirewallProgramVariable) ||
		!strings.Contains(FirewallAllowScript, "$rule.Profiles = 2") ||
		!strings.Contains(FirewallAllowScript, "$rule.Direction = 1") ||
		!strings.Contains(FirewallAllowScript, "$rule.Action = 1") ||
		!strings.Contains(FirewallAllowScript, "HNetCfg.FWRule") ||
		strings.Contains(FirewallAllowScript, "Import-Module") || strings.Contains(FirewallAllowScript, "NetSecurity") {
		t.Errorf("unexpected allow script:\n%s", FirewallAllowScript)
	}
	if !strings.Contains(FirewallRemoveScript, "HNetCfg.FwPolicy2") || strings.Contains(FirewallRemoveScript, "NetSecurity") {
		t.Errorf("unexpected remove script:\n%s", FirewallRemoveScript)
	}
}

func TestPowerShellArgumentsEncodeTheScript(t *testing.T) {
	arguments := PowerShellArguments("'é' | Out-Host")
	if arguments[len(arguments)-2] != "-EncodedCommand" {
		t.Fatalf("arguments = %q", arguments)
	}
	data, err := base64.StdEncoding.DecodeString(arguments[len(arguments)-1])
	if err != nil {
		t.Fatal(err)
	}
	units := make([]uint16, len(data)/2)
	for index := range units {
		units[index] = binary.LittleEndian.Uint16(data[2*index:])
	}
	if script := string(utf16.Decode(units)); !strings.HasPrefix(script, powerShellPreamble) || !strings.HasSuffix(script, "'é' | Out-Host") {
		t.Errorf("decoded script %q", script)
	}
}

func TestStopEventNameIgnoresCase(t *testing.T) {
	if StopEventName(`C:\Users\you\owngit`) != StopEventName(`c:\users\you\OWNGIT`) || StopEventName(`C:\a`) == StopEventName(`C:\b`) {
		t.Error("stop event names do not follow the case-insensitive path")
	}
	if name := StopEventName(`C:\a`); strings.ContainsAny(name, `\/`) || !strings.HasPrefix(name, "OwnGit-") {
		t.Errorf("stop event name %q", name)
	}
}

func TestWindowsEnvironment(t *testing.T) {
	environment := func(values map[string]string, administrator bool) Environment {
		return Environment{Getenv: func(name string) string { return values[name] }, Windows: true, Administrator: administrator}
	}
	if environment(nil, true).Headless() {
		t.Error("a Windows desktop is headless")
	}
	if !environment(map[string]string{"SSH_CONNECTION": "10.0.0.2 50000 10.0.0.3 22"}, true).Headless() {
		t.Error("a Windows SSH session is not headless")
	}
	if mode := environment(nil, true).ChooseMode(false); mode != ModeBootTask {
		t.Errorf("administrator mode = %s", mode)
	}
	if mode := environment(nil, false).ChooseMode(true); mode != ModeLogonTask {
		t.Errorf("standard account mode = %s", mode)
	}
}

// Every firewall script decides ownership by the same test and changes only
// the rules that pass it, and none beside a same-name rule that fails it.
func TestFirewallScriptsChangeOnlyOwnGitsRule(t *testing.T) {
	for name, script := range map[string]string{"allow": FirewallAllowScript, "remove": FirewallRemoveScript, "show": FirewallShowScript} {
		if !strings.HasPrefix(script, firewallRules) || strings.Count(script, "Rules.Remove") > strings.Count(script, "$owned | ForEach-Object { $policy.Rules.Remove") {
			t.Errorf("%s script:\n%s", name, script)
		}
	}
	if !strings.Contains(firewallRules, "$_.Description -eq '"+FirewallRuleDescription+"'") || !strings.Contains(firewallRules, "-eq 'owngit.exe'") ||
		!strings.Contains(firewallRules, "if ($owned.Count -ne $same.Count) { '"+FirewallForeign+"'; exit }") ||
		!strings.Contains(FirewallAllowScript, "$rule.Description = '"+FirewallRuleDescription+"'") {
		t.Errorf("ownership test:\n%s", firewallRules)
	}
	if !FirewallCollision([]byte(FirewallForeign+"\r\n")) || FirewallCollision([]byte(`C:\Program Files\OwnGit\owngit.exe`+"\n2\nTrue\n1\n1\n")) {
		t.Error("collision output misread")
	}
}

// Only enabled inbound rules for TCP on the listen port count, whether
// they name the program or no program; a block rule wins.
func TestParseFirewallAccess(t *testing.T) {
	output := "2 7 0\r\n" +
		"1\t2\t256\t*\t*\r\n" + // OwnGit's rule: any protocol, any port, private
		"1\t4\t6\t80,443\t*\r\n" + // a public web rule
		"1\t4\t6\t7000-8000\tLocalSubnet\r\n" + // a public range with the port, nearby devices
		"1\t1\t6\t7654\t10.1.2.3\r\n" + // one device only
		"0\t1\t17\t7654\t*\r\n" + // UDP does not matter
		"0\t1\t6\tRPC\t*\r\n" // a keyword port does not match
	access, err := ParseFirewallAccess(output, "7654")
	if err != nil {
		t.Fatal(err)
	}
	if want := (FirewallAccess{Active: 2, On: 7, Allowed: 6}); access != want {
		t.Fatalf("access %+v, want %+v", access, want)
	}
	if closed := access.Closed(); closed != 0 {
		t.Errorf("closed %d", closed)
	}
	access.Blocked = 2
	if closed := access.Closed(); closed != 2 {
		t.Errorf("a block rule: closed %d", closed)
	}
	if closed := (FirewallAccess{Active: 5, On: 7, Allowed: 2}).Closed(); closed != 5 {
		t.Errorf("domain and public without a rule: closed %d", closed)
	}
	if closed := (FirewallAccess{Active: 2, On: 7, BlockAll: 2, Allowed: 7}).Closed(); closed != 2 {
		t.Errorf("block all incoming connections: closed %d", closed)
	}
	// As Windows 11 printed it: an allow rule for any protocol has no
	// local ports, a rule for every profile has the mask 0x7FFFFFFF, and a
	// block rule for the local subnet blocks the devices nearby.
	access, err = ParseFirewallAccess("2 7 4\r\n1\t2147483647\t6\t80\t*\r\n1\t3\t256\t\t*\r\n0\t4\t6\t7654\tLocalSubnet\r\n", "7654")
	if err != nil {
		t.Fatal(err)
	}
	if want := (FirewallAccess{Active: 2, On: 7, BlockAll: 4, Allowed: 3, Blocked: 4}); access != want {
		t.Fatalf("access %+v, want %+v", access, want)
	}
	for _, bad := range []string{"", "2 7\n", "2 7 0\n1\t2\t256\t\n", "2 7 0\n1\tx\t256\t\t*\n"} {
		if _, err := ParseFirewallAccess(bad, "7654"); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
