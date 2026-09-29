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
	// FirewallRuleDescription marks the rule as OwnGit's. Every OwnGit that
	// adds the rule writes this text.
	FirewallRuleDescription = "Lets devices on private networks reach OwnGit. Added by owngit service install; owngit service uninstall removes it."
	// FirewallForeign is what the firewall scripts print when a rule named
	// FirewallRuleName exists that is not OwnGit's; they then change
	// nothing.
	FirewallForeign = "foreign"
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
	// Headless is passed as --headless=true or --headless=false, as in a
	// systemd unit.
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
	return []string{"serve", "--state-dir", plan.StateDir, "--no-open", "--log-file", TaskLogFile(plan.StateDir), "--service", "--headless=" + strconv.FormatBool(plan.Headless)}
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
		case word == "--headless" || word == "--headless=true":
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
// They use the Windows Firewall COM API, not PowerShell modules found through
// a user-controlled module path.
//
// A rule is OwnGit's when it is named FirewallRuleName, carries
// FirewallRuleDescription and lets a program named owngit.exe through. The
// COM API removes rules by name only, so while a rule of that name exists
// that is not OwnGit's, the scripts add and remove nothing and print
// FirewallForeign; that rule and OwnGit's stay as they are.
const firewallRules = `$ErrorActionPreference = 'Stop'
$policy = New-Object -ComObject HNetCfg.FwPolicy2
$same = @($policy.Rules | Where-Object { $_.Name -eq '` + FirewallRuleName + `' })
$owned = @($same | Where-Object { $_.Description -eq '` + FirewallRuleDescription + `' -and [IO.Path]::GetFileName([string]$_.ApplicationName) -eq 'owngit.exe' })
if ($owned.Count -ne $same.Count) { '` + FirewallForeign + `'; exit }
`

// FirewallAllowScript replaces OwnGit's rule with one that lets
// connections from the Private network profile reach the program. Public
// networks stay closed.
const FirewallAllowScript = firewallRules + `$owned | ForEach-Object { $policy.Rules.Remove($_.Name) }
$rule = New-Object -ComObject HNetCfg.FWRule
$rule.Name = '` + FirewallRuleName + `'
$rule.Description = '` + FirewallRuleDescription + `'
$rule.Direction = 1
$rule.Action = 1
$rule.Enabled = $true
$rule.Profiles = 2
$rule.Protocol = 256
$rule.ApplicationName = $env:` + FirewallProgramVariable + `
$policy.Rules.Add($rule)
`

// FirewallRemoveScript removes OwnGit's rule and nothing else.
const FirewallRemoveScript = firewallRules + `$owned | ForEach-Object { $policy.Rules.Remove($_.Name) }
`

// FirewallShowScript prints the program, profiles, enabled state, direction
// and action of OwnGit's rule, one per line, nothing when there is no rule,
// or FirewallForeign. The COM interface works for any account, including an
// SSH session where administrator-only firewall writes are unavailable.
const FirewallShowScript = firewallRules + `$rule = $owned | Select-Object -First 1
if ($rule) { $rule.ApplicationName; $rule.Profiles; $rule.Enabled; $rule.Direction; $rule.Action }
`

// FirewallAccessScript prints what decides whether other devices reach
// the program: on the first line the network profiles in use and the
// profiles where Windows Firewall is on, as two NET_FW_PROFILE_TYPE2
// masks; then one line per enabled inbound rule that names the program or
// no program (action, profiles, protocol, local ports), tab-separated.
// Rules of a Windows service apply only to that service and are left out.
const FirewallAccessScript = `$ErrorActionPreference = 'Stop'
$policy = New-Object -ComObject HNetCfg.FwPolicy2
$on = 0; foreach ($kind in 1, 2, 4) { if ($policy.FirewallEnabled($kind)) { $on = $on -bor $kind } }
"$($policy.CurrentProfileTypes) $on"
$program = $env:` + FirewallProgramVariable + `
foreach ($rule in $policy.Rules) {
  if ($rule.Direction -ne 1 -or -not $rule.Enabled -or $rule.ServiceName) { continue }
  $app = [Environment]::ExpandEnvironmentVariables([string]$rule.ApplicationName)
  if ($app -and $app -ne $program) { continue }
  "$($rule.Action)` + "`t" + `$($rule.Profiles)` + "`t" + `$($rule.Protocol)` + "`t" + `$($rule.LocalPorts)"
}
`

// FirewallAccess is what FirewallAccessScript prints, for one TCP port.
// Each field is a NET_FW_PROFILE_TYPE2 mask.
type FirewallAccess struct {
	// Active are the profiles of the networks in use, and On those where
	// Windows Firewall is on.
	Active, On int
	// Allowed and Blocked are the profiles where a rule allows or blocks
	// the program on the port.
	Allowed, Blocked int
}

// Closed returns the profiles in use, with the firewall on, where devices
// cannot reach the program: a rule blocks it, or none allows it. Windows
// Firewall lets a block rule win over an allow rule.
func (access FirewallAccess) Closed() int {
	guarded := access.Active & access.On
	return guarded&access.Blocked | guarded&^access.Allowed
}

// ParseFirewallAccess reads FirewallAccessScript's output for port.
func ParseFirewallAccess(output, port string) (FirewallAccess, error) {
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(output, "\r", "")), "\n")
	var access FirewallAccess
	if _, err := fmt.Sscanf(lines[0], "%d %d", &access.Active, &access.On); err != nil {
		return FirewallAccess{}, fmt.Errorf("unexpected firewall profiles %q", lines[0])
	}
	for _, line := range lines[1:] {
		fields := strings.Split(line, "\t")
		if len(fields) != 4 {
			return FirewallAccess{}, fmt.Errorf("unexpected firewall rule %q", line)
		}
		action, actionErr := strconv.Atoi(fields[0])
		profiles, profilesErr := strconv.Atoi(fields[1])
		protocol, protocolErr := strconv.Atoi(fields[2])
		if actionErr != nil || profilesErr != nil || protocolErr != nil {
			return FirewallAccess{}, fmt.Errorf("unexpected firewall rule %q", line)
		}
		if protocol != firewallProtocolTCP && protocol != firewallProtocolAny || !portListed(fields[3], port) {
			continue
		}
		if action == firewallActionAllow {
			access.Allowed |= profiles
		} else {
			access.Blocked |= profiles
		}
	}
	return access, nil
}

// portListed reports whether a rule's local ports, "*" or a comma list of
// ports and ranges, include port. A rule without ports has "*".
func portListed(ports, port string) bool {
	want, err := strconv.Atoi(port)
	if err != nil {
		return false
	}
	for _, entry := range strings.Split(ports, ",") {
		entry = strings.TrimSpace(entry)
		low, high, isRange := strings.Cut(entry, "-")
		first, firstErr := strconv.Atoi(low)
		last := first
		var lastErr error
		if isRange {
			last, lastErr = strconv.Atoi(high)
		}
		if entry == "*" || entry == "" || firstErr == nil && lastErr == nil && first <= want && want <= last {
			return true
		}
	}
	return false
}

// FirewallCollision reports whether a firewall script found a rule named
// FirewallRuleName that is not OwnGit's.
func FirewallCollision(output []byte) bool {
	return strings.TrimSpace(string(output)) == FirewallForeign
}

// FirewallRule is OwnGit's rule as FirewallShowScript prints it.
type FirewallRule struct {
	Program string
	// Profiles is the NET_FW_PROFILE_TYPE2 mask: 1 Domain, 2 Private,
	// 4 Public.
	Profiles  int
	Enabled   bool
	Direction int
	Action    int
}

const (
	firewallPrivateProfile = 2 // NET_FW_PROFILE2_PRIVATE
	firewallDirectionIn    = 1 // NET_FW_RULE_DIRECTION_IN
	firewallActionAllow    = 1 // NET_FW_ACTION_ALLOW
	firewallProtocolTCP    = 6
	firewallProtocolAny    = 256
)

// ParseFirewallRule reads FirewallShowScript's output. found is false when
// there is no rule.
func ParseFirewallRule(output string) (rule FirewallRule, found bool) {
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(output, "\r", "")), "\n")
	if len(lines) < 5 || strings.TrimSpace(lines[0]) == "" {
		return FirewallRule{}, false
	}
	profiles, profileErr := strconv.Atoi(strings.TrimSpace(lines[1]))
	direction, directionErr := strconv.Atoi(strings.TrimSpace(lines[3]))
	action, actionErr := strconv.Atoi(strings.TrimSpace(lines[4]))
	if profileErr != nil || directionErr != nil || actionErr != nil {
		return FirewallRule{}, false
	}
	return FirewallRule{
		Program: strings.TrimSpace(lines[0]), Profiles: profiles,
		Enabled:   strings.EqualFold(strings.TrimSpace(lines[2]), "True"),
		Direction: direction, Action: action,
	}, true
}

// Allows reports whether the rule is an enabled inbound allow rule for only
// the Private profile and program.
func (rule FirewallRule) Allows(program string) bool {
	return rule.Enabled && strings.EqualFold(rule.Program, program) &&
		rule.Profiles == firewallPrivateProfile && rule.Direction == firewallDirectionIn && rule.Action == firewallActionAllow
}

// The task scripts use the Task Scheduler COM interface, which works for
// the account in any session, also over SSH, where the ScheduledTasks
// cmdlets (through WMI) are refused without administrator rights.

// TaskDefinitionScript prints the definition of the task as XML, or
// nothing when there is none.
const TaskDefinitionScript = `$ErrorActionPreference = 'Stop'
$scheduler = New-Object -ComObject Schedule.Service; $scheduler.Connect()
$task = $scheduler.GetFolder('\').GetTasks(1) | Where-Object { $_.Name -eq '` + TaskName + `' }
if ($task) { $task.Xml }
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
