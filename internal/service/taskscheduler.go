package service

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"
)

// On Windows "owngit service install" registers one Task Scheduler task and,
// for an administrator account, one Windows Firewall rule. Both have fixed
// names so that later commands find and replace them. This file renders and
// reads them; it runs on every platform so that tests cover it everywhere.

const (
	// TaskName is the name of the scheduled task, in the root folder.
	TaskName = "OwnGit"
	// FirewallRuleName is the name (the rule ID) and display name of the
	// inbound Windows Firewall rule for owngit.exe.
	FirewallRuleName = "OwnGit"
	// taskSource marks a task that "owngit service install" registered.
	taskSource = "owngit service install"
	// FirewallProgramVariable passes the program path to the firewall
	// scripts, so that no path is ever part of script text.
	FirewallProgramVariable = "OWNGIT_FIREWALL_PROGRAM"
)

// ErrForeignTask means a task named OwnGit exists that "owngit service
// install" did not register.
var ErrForeignTask = errors.New(`a scheduled task named "` + TaskName + `" that owngit service install did not register already exists`)

// TaskPlan is one scheduled task installation.
type TaskPlan struct {
	// Mode is ModeBootTask or ModeLogonTask.
	Mode Mode
	// Executable is the absolute path of owngit.exe.
	Executable string
	// StateDir is the absolute state directory the task passes.
	StateDir string
	// UserSID is the security identifier of the installing account.
	UserSID string
	// Headless passes --headless, as for a systemd unit.
	Headless bool
	// Conhost is the absolute path of conhost.exe. ModeLogonTask starts
	// OwnGit through "conhost.exe --headless", so no console window opens
	// on the desktop of the signed-in account.
	Conhost string
}

// TaskLogFile is the log file the task passes to "owngit serve --log-file".
// Task Scheduler keeps no output, so the server writes its log there.
func TaskLogFile(stateDir string) string {
	return strings.TrimRight(stateDir, `\`) + `\logs\service.log`
}

// ServeArguments are the arguments of "owngit serve" in the task. Like a
// systemd unit, the task never passes --listen or --base-url, which would
// override the saved network settings. --service makes the server give up
// administrator rights and accept "owngit service stop".
func (plan TaskPlan) ServeArguments() []string {
	arguments := []string{"serve", "--state-dir", plan.StateDir, "--no-open", "--log-file", TaskLogFile(plan.StateDir), "--service"}
	if plan.Headless {
		arguments = append(arguments, "--headless")
	}
	return arguments
}

// RenderTask writes the Task Scheduler XML of a plan.
//
// ModeBootTask starts at boot without a sign-in, as the account (S4U: no
// stored password), and restarts after a failure. ModeLogonTask starts
// when the account signs in. Neither has a time limit or a battery rule.
func RenderTask(plan TaskPlan) (string, error) {
	if plan.Mode != ModeBootTask && plan.Mode != ModeLogonTask {
		return "", fmt.Errorf("no scheduled task for mode %q", plan.Mode)
	}
	if err := checkWindowsPath("executable", plan.Executable); err != nil {
		return "", err
	}
	if err := checkWindowsPath("state directory", plan.StateDir); err != nil {
		return "", err
	}
	if !validSID(plan.UserSID) {
		return "", fmt.Errorf("unsupported account SID %q", plan.UserSID)
	}
	command, words := plan.Executable, plan.ServeArguments()
	if plan.Mode == ModeLogonTask {
		if err := checkWindowsPath("conhost", plan.Conhost); err != nil {
			return "", err
		}
		command, words = plan.Conhost, append([]string{"--headless", plan.Executable}, words...)
	}
	trigger, logonType := "<BootTrigger><Enabled>true</Enabled></BootTrigger>", "S4U"
	if plan.Mode == ModeLogonTask {
		trigger, logonType = "<LogonTrigger><Enabled>true</Enabled><UserId>"+plan.UserSID+"</UserId></LogonTrigger>", "InteractiveToken"
	}
	var task strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&task, format+"\n", args...) }
	line(`<?xml version="1.0" encoding="UTF-16"?>`)
	line(`<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">`)
	line(`  <RegistrationInfo>`)
	line(`    <Source>%s</Source>`, taskSource)
	line(`    <Description>OwnGit private Git server. Run "owngit service install" again to update this task, or "owngit service uninstall" to remove it. Both keep the state directory and the repositories.</Description>`)
	line(`  </RegistrationInfo>`)
	line(`  <Triggers>%s</Triggers>`, trigger)
	line(`  <Principals>`)
	line(`    <Principal id="Author">`)
	line(`      <UserId>%s</UserId>`, plan.UserSID)
	line(`      <LogonType>%s</LogonType>`, logonType)
	line(`      <RunLevel>LeastPrivilege</RunLevel>`)
	line(`    </Principal>`)
	line(`  </Principals>`)
	line(`  <Settings>`)
	line(`    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>`)
	line(`    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>`)
	line(`    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>`)
	line(`    <AllowHardTerminate>true</AllowHardTerminate>`)
	line(`    <StartWhenAvailable>false</StartWhenAvailable>`)
	line(`    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>`)
	line(`    <IdleSettings><StopOnIdleEnd>false</StopOnIdleEnd><RestartOnIdle>false</RestartOnIdle></IdleSettings>`)
	line(`    <AllowStartOnDemand>true</AllowStartOnDemand>`)
	line(`    <Enabled>true</Enabled>`)
	line(`    <Hidden>false</Hidden>`)
	line(`    <RunOnlyIfIdle>false</RunOnlyIfIdle>`)
	line(`    <WakeToRun>false</WakeToRun>`)
	// No time limit: Task Scheduler stops a task after 72 hours by default.
	line(`    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>`)
	// 5 is normal process and thread priority; the default 7 is below
	// normal, with lower I/O and memory priority as well.
	line(`    <Priority>5</Priority>`)
	line(`    <RestartOnFailure><Interval>PT1M</Interval><Count>999</Count></RestartOnFailure>`)
	line(`  </Settings>`)
	line(`  <Actions Context="Author">`)
	line(`    <Exec>`)
	line(`      <Command>%s</Command>`, escapeXML(command))
	line(`      <Arguments>%s</Arguments>`, escapeXML(WindowsCommandLine(words)))
	line(`    </Exec>`)
	line(`  </Actions>`)
	line(`</Task>`)
	return task.String(), nil
}

// taskDocument is the part of a task definition that ParseTask reads.
type taskDocument struct {
	Source   string `xml:"RegistrationInfo>Source"`
	Triggers struct {
		Boot  *struct{} `xml:"BootTrigger"`
		Logon *struct{} `xml:"LogonTrigger"`
	} `xml:"Triggers"`
	UserID    string `xml:"Principals>Principal>UserId"`
	LogonType string `xml:"Principals>Principal>LogonType"`
	Command   string `xml:"Actions>Exec>Command"`
	Arguments string `xml:"Actions>Exec>Arguments"`
}

// ParseTask reads a task definition that RenderTask wrote, as Task
// Scheduler returns it. It returns ErrForeignTask for any other task.
func ParseTask(definition []byte) (Installed, error) {
	text, err := decodeXMLText(definition)
	if err != nil {
		return Installed{}, err
	}
	var document taskDocument
	decoder := xml.NewDecoder(strings.NewReader(text))
	// The text is already UTF-8; the declaration may still say UTF-16.
	decoder.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }
	if err := decoder.Decode(&document); err != nil {
		return Installed{}, fmt.Errorf("read the scheduled task: %w", err)
	}
	if document.Source != taskSource {
		return Installed{}, ErrForeignTask
	}
	installed := Installed{Mode: ModeBootTask, UnitPath: `\` + TaskName, User: document.UserID}
	if document.Triggers.Logon != nil {
		installed.Mode = ModeLogonTask
	}
	words := SplitWindowsCommandLine(document.Arguments)
	installed.Executable = document.Command
	if installed.Mode == ModeLogonTask && len(words) >= 2 && words[0] == "--headless" {
		installed.Executable, words = words[1], words[2:]
	}
	for index, word := range words {
		switch {
		case word == "--state-dir" && index+1 < len(words):
			installed.StateDir = words[index+1]
		case word == "--headless":
			installed.Headless = true
		}
	}
	return installed, nil
}

// decodeXMLText turns UTF-16 (with a byte order mark, as schtasks writes
// task XML) or UTF-8 into a string.
func decodeXMLText(data []byte) (string, error) {
	switch {
	case len(data) >= 2 && data[0] == 0xff && data[1] == 0xfe:
		return decodeUTF16(data[2:], binary.LittleEndian)
	case len(data) >= 2 && data[0] == 0xfe && data[1] == 0xff:
		return decodeUTF16(data[2:], binary.BigEndian)
	case len(data) >= 3 && data[0] == 0xef && data[1] == 0xbb && data[2] == 0xbf:
		return string(data[3:]), nil
	}
	return string(data), nil
}

func decodeUTF16(data []byte, order binary.ByteOrder) (string, error) {
	if len(data)%2 != 0 {
		return "", errors.New("task XML has an odd number of UTF-16 bytes")
	}
	units := make([]uint16, len(data)/2)
	for index := range units {
		units[index] = order.Uint16(data[2*index:])
	}
	return string(utf16.Decode(units)), nil
}

// EncodeUTF16 returns text as UTF-16LE with a byte order mark, the form
// schtasks /Create /XML reads.
func EncodeUTF16(text string) []byte {
	units := utf16.Encode([]rune(text))
	data := make([]byte, 2+2*len(units))
	data[0], data[1] = 0xff, 0xfe
	for index, unit := range units {
		binary.LittleEndian.PutUint16(data[2+2*index:], unit)
	}
	return data
}

// checkWindowsPath accepts an absolute Windows path (drive or UNC) that a
// task can hold: no control characters, no quotation mark (Windows names
// cannot contain one) and no percent sign, which Task Scheduler would
// expand as an environment variable.
func checkWindowsPath(label, path string) error {
	absolute := len(path) >= 3 && isASCIILetter(path[0]) && path[1] == ':' && path[2] == '\\' ||
		strings.HasPrefix(path, `\\`) && len(path) > 2
	if !absolute {
		return fmt.Errorf("%s must be an absolute Windows path: %q", label, path)
	}
	if strings.ContainsAny(path, `"%`) || strings.ContainsFunc(path, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return fmt.Errorf("%s %q contains a character a scheduled task cannot hold (quotation mark, percent sign or control character)", label, path)
	}
	return nil
}

func isASCIILetter(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }

// validSID accepts the text form of a security identifier.
func validSID(sid string) bool {
	parts := strings.Split(sid, "-")
	if len(parts) < 3 || parts[0] != "S" || parts[1] != "1" {
		return false
	}
	for _, part := range parts[2:] {
		if part == "" || strings.Trim(part, "0123456789") != "" {
			return false
		}
	}
	return true
}

// escapeXML escapes element text. Quotation marks may stay as they are.
var escapeXML = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace

// WindowsCommandLine joins arguments into a command line that Windows
// programs (CommandLineToArgvW and the C runtime) split back into the same
// arguments: an argument with a space, a tab or a quotation mark is quoted,
// quotation marks are escaped, and backslashes before them are doubled.
func WindowsCommandLine(arguments []string) string {
	quoted := make([]string, len(arguments))
	for index, argument := range arguments {
		quoted[index] = windowsArgument(argument)
	}
	return strings.Join(quoted, " ")
}

func windowsArgument(argument string) string {
	if argument != "" && !strings.ContainsAny(argument, " \t\n\v\"") {
		return argument
	}
	var quoted strings.Builder
	quoted.WriteByte('"')
	backslashes := 0
	for _, r := range argument {
		switch r {
		case '\\':
			backslashes++
			continue
		case '"':
			quoted.WriteString(strings.Repeat(`\`, 2*backslashes+1))
		default:
			quoted.WriteString(strings.Repeat(`\`, backslashes))
		}
		backslashes = 0
		quoted.WriteRune(r)
	}
	quoted.WriteString(strings.Repeat(`\`, 2*backslashes))
	quoted.WriteByte('"')
	return quoted.String()
}

// SplitWindowsCommandLine splits a command line (not including the program
// name) by the rules of CommandLineToArgvW.
func SplitWindowsCommandLine(line string) []string {
	var arguments []string
	var current strings.Builder
	inArgument, quoted := false, false
	runes := []rune(line)
	for index := 0; index < len(runes); index++ {
		r := runes[index]
		switch {
		case r == '\\':
			count := 0
			for index < len(runes) && runes[index] == '\\' {
				count++
				index++
			}
			if index < len(runes) && runes[index] == '"' {
				current.WriteString(strings.Repeat(`\`, count/2))
				if count%2 == 1 {
					current.WriteByte('"')
				} else {
					quoted = !quoted
				}
			} else {
				current.WriteString(strings.Repeat(`\`, count))
				index--
			}
			inArgument = true
		case r == '"':
			if quoted && index+1 < len(runes) && runes[index+1] == '"' {
				current.WriteByte('"')
				index++
			} else {
				quoted = !quoted
			}
			inArgument = true
		case !quoted && (r == ' ' || r == '\t'):
			if inArgument {
				arguments = append(arguments, current.String())
				current.Reset()
				inArgument = false
			}
		default:
			current.WriteRune(r)
			inArgument = true
		}
	}
	if inArgument {
		arguments = append(arguments, current.String())
	}
	return arguments
}

// Firewall scripts for Windows PowerShell. The program path is read from
// the environment variable FirewallProgramVariable, never from the text.

// FirewallAllowScript replaces OwnGit's rule with one that lets
// connections from the Private network profile reach the program. Public
// networks stay closed.
const FirewallAllowScript = `$ErrorActionPreference = 'Stop'
Remove-NetFirewallRule -Name '` + FirewallRuleName + `' -ErrorAction SilentlyContinue
New-NetFirewallRule -Name '` + FirewallRuleName + `' -DisplayName '` + FirewallRuleName + `' -Description 'Lets devices on private networks reach OwnGit. Added by owngit service install; owngit service uninstall removes it.' -Direction Inbound -Action Allow -Profile Private -Program $env:` + FirewallProgramVariable + ` -Enabled True | Out-Null
`

// FirewallRemoveScript removes OwnGit's rule and nothing else.
const FirewallRemoveScript = `$ErrorActionPreference = 'Stop'
Remove-NetFirewallRule -Name '` + FirewallRuleName + `' -ErrorAction SilentlyContinue
`

// FirewallShowScript prints the program, profiles and enabled state of
// OwnGit's rule, one per line, or nothing when there is no rule. It uses
// the firewall's COM interface, which any account may read, also from an
// SSH session where the NetSecurity cmdlets are refused.
const FirewallShowScript = `$ErrorActionPreference = 'Stop'
$rule = (New-Object -ComObject HNetCfg.FwPolicy2).Rules | Where-Object { $_.Name -eq '` + FirewallRuleName + `' } | Select-Object -First 1
if ($rule) { $rule.ApplicationName; $rule.Profiles; $rule.Enabled }
`

// FirewallRule is OwnGit's rule as FirewallShowScript prints it.
type FirewallRule struct {
	Program string
	// Profiles is the NET_FW_PROFILE_TYPE2 mask: 1 Domain, 2 Private,
	// 4 Public.
	Profiles int
	Enabled  bool
}

// firewallPrivateProfile is NET_FW_PROFILE2_PRIVATE.
const firewallPrivateProfile = 2

// ParseFirewallRule reads FirewallShowScript's output. found is false when
// there is no rule.
func ParseFirewallRule(output string) (rule FirewallRule, found bool) {
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(output, "\r", "")), "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) == "" {
		return FirewallRule{}, false
	}
	profiles, err := strconv.Atoi(strings.TrimSpace(lines[1]))
	if err != nil {
		return FirewallRule{}, false
	}
	return FirewallRule{
		Program: strings.TrimSpace(lines[0]), Profiles: profiles,
		Enabled: strings.EqualFold(strings.TrimSpace(lines[2]), "True"),
	}, true
}

// Allows reports whether the rule lets the Private profile reach program.
func (rule FirewallRule) Allows(program string) bool {
	return rule.Enabled && strings.EqualFold(rule.Program, program) && rule.Profiles&firewallPrivateProfile != 0
}

// The task scripts use the Task Scheduler COM interface, which works for
// the account in any session, also over SSH, where the ScheduledTasks
// cmdlets (through WMI) are refused without administrator rights.

// TaskDefinitionScript prints the definition of the task as XML, or
// nothing when there is none.
const TaskDefinitionScript = `$ErrorActionPreference = 'Stop'
$scheduler = New-Object -ComObject Schedule.Service; $scheduler.Connect()
try { $task = $scheduler.GetFolder('\').GetTask('` + TaskName + `') } catch { if ($_.Exception.HResult -eq -2147024894) { return }; throw }
$task.Xml
`

// TaskStateScript prints the state of the task (TASK_STATE: 1 Disabled,
// 2 Queued, 3 Ready, 4 Running) and its last result as a decimal number,
// one per line.
const TaskStateScript = `$ErrorActionPreference = 'Stop'
$scheduler = New-Object -ComObject Schedule.Service; $scheduler.Connect()
$task = $scheduler.GetFolder('\').GetTask('` + TaskName + `')
$task.State
$task.LastTaskResult
`

// Task states that TaskStateScript prints.
const (
	TaskDisabled = 1
	TaskQueued   = 2
	TaskReady    = 3
	TaskRunning  = 4
)

const powerShellPreamble = "[Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)\n$ProgressPreference = 'SilentlyContinue'\n"

// PowerShellArguments are the arguments that run script in Windows
// PowerShell without a profile, as an encoded command so that no quoting
// is involved.
// Output is UTF-8, so that paths in any language arrive intact.
func PowerShellArguments(script string) []string {
	units := utf16.Encode([]rune(powerShellPreamble + script))
	data := make([]byte, 2*len(units))
	for index, unit := range units {
		binary.LittleEndian.PutUint16(data[2*index:], unit)
	}
	return []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-EncodedCommand", base64.StdEncoding.EncodeToString(data)}
}

// StopEventName names the event that "owngit service stop" sets to stop the
// server of stateDir in order. Windows paths are not case sensitive, so the
// name uses the lower-case path.
func StopEventName(stateDir string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(stateDir)))
	return "OwnGit-service-stop-" + hex.EncodeToString(sum[:16])
}
