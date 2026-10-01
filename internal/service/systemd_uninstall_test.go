package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type uninstallCase struct {
	name          string
	stopFails     bool
	activeState   string
	mainPID       string
	queryFails    bool
	disableFails  bool
	loadState     string
	reloadFails   bool
	absent        bool
	wantErr       bool
	wantUnit      bool
	wantEnabled   bool
	wantStopError bool
}

func uninstallCases() []uninstallCase {
	return []uninstallCase{
		{name: "running unit", activeState: "inactive", mainPID: "0"},
		{name: "already stopped", stopFails: true, activeState: "inactive", mainPID: "0"},
		{name: "absent unit", absent: true, stopFails: true, activeState: "inactive", mainPID: "0", disableFails: true, loadState: "not-found"},
		{name: "refused stop", stopFails: true, activeState: "active", wantErr: true, wantUnit: true, wantEnabled: true, wantStopError: true},
		{name: "unfinished stop", stopFails: true, activeState: "deactivating", wantErr: true, wantUnit: true, wantEnabled: true, wantStopError: true},
		{name: "failed stop", stopFails: true, activeState: "failed", wantErr: true, wantUnit: true, wantEnabled: true, wantStopError: true},
		{name: "unavailable manager", stopFails: true, queryFails: true, wantErr: true, wantUnit: true, wantEnabled: true, wantStopError: true},
		{name: "unknown state", stopFails: true, wantErr: true, wantUnit: true, wantEnabled: true, wantStopError: true},
		{name: "disable failure", activeState: "inactive", mainPID: "0", disableFails: true, loadState: "loaded", wantErr: true, wantUnit: true, wantEnabled: true},
		{name: "reload failure", activeState: "inactive", mainPID: "0", reloadFails: true, wantErr: true},
		{name: "zero exit failed stop with live process", activeState: "failed", mainPID: "4321", wantErr: true, wantUnit: true, wantEnabled: true, wantStopError: true},
		{name: "zero exit failed stop without main process", activeState: "failed", mainPID: "0", wantErr: true, wantUnit: true, wantEnabled: true, wantStopError: true},
		{name: "zero exit active stop", activeState: "active", mainPID: "4321", wantErr: true, wantUnit: true, wantEnabled: true, wantStopError: true},
		{name: "zero exit unfinished stop", activeState: "deactivating", mainPID: "0", wantErr: true, wantUnit: true, wantEnabled: true, wantStopError: true},
		{name: "zero exit inactive with live process", activeState: "inactive", mainPID: "4321", wantErr: true, wantUnit: true, wantEnabled: true, wantStopError: true},
		{name: "zero exit unavailable manager", queryFails: true, wantErr: true, wantUnit: true, wantEnabled: true, wantStopError: true},
		{name: "zero exit unknown state", wantErr: true, wantUnit: true, wantEnabled: true, wantStopError: true},
		{name: "zero exit missing main PID", activeState: "inactive", wantErr: true, wantUnit: true, wantEnabled: true, wantStopError: true},
	}
}

func TestUninstallUserUnitPreservesAnUnstoppedService(t *testing.T) {
	unixPathsOnly(t)
	for _, test := range uninstallCases() {
		t.Run(test.name, func(t *testing.T) {
			path := UserUnitPath(t.TempDir())
			unit := "installed unit"
			if !test.absent {
				noErr(t, os.MkdirAll(filepath.Dir(path), 0o755))
				noErr(t, os.WriteFile(path, []byte(unit), 0o644))
			}
			enabled := !test.absent
			stopErr := errors.New("synthetic stop refusal")
			var commands []string
			run := func(_ context.Context, name string, args ...string) ([]byte, error) {
				command := strings.Join(args, " ")
				commands = append(commands, name+" "+command)
				if name != "systemctl" || len(args) == 0 || args[0] != "--user" {
					t.Fatalf("unexpected command: %s %s", name, command)
				}
				switch strings.Join(args[1:], " ") {
				case "stop " + UnitName:
					if test.stopFails {
						return []byte("Unit refuses manual stop"), stopErr
					}
				case "show --property=ActiveState --property=MainPID " + UnitName:
					if test.queryFails {
						return nil, errors.New("manager unavailable")
					}
					return []byte("MainPID=" + test.mainPID + "\nActiveState=" + test.activeState + "\n"), nil
				case "show --property=LoadState --value " + UnitName:
					return []byte(test.loadState + "\n"), nil
				case "disable --quiet " + UnitName:
					if test.disableFails {
						return []byte("disable refused"), errors.New("disable failed")
					}
					enabled = false
				case "disable --now --quiet " + UnitName:
					// The combined command can disable the unit before stop fails.
					enabled = false
					if test.stopFails {
						return []byte("Unit refuses manual stop"), stopErr
					}
				case "daemon-reload":
					if test.reloadFails {
						return nil, errors.New("reload failed")
					}
				default:
					t.Fatalf("unexpected command: %s", command)
				}
				return nil, nil
			}
			err := UninstallUserUnit(context.Background(), run, path)
			if (err != nil) != test.wantErr {
				t.Errorf("uninstall error = %v, want error %v", err, test.wantErr)
			}
			if test.wantStopError && (err == nil || test.stopFails && !errors.Is(err, stopErr) || !strings.Contains(err.Error(), "Stop it manually") || !strings.Contains(err.Error(), "owngit service uninstall")) {
				t.Errorf("stop failure must retain its cause and explain how to retry: %v", err)
			}
			content, readErr := os.ReadFile(path)
			if test.wantUnit {
				if readErr != nil || string(content) != unit {
					t.Errorf("unit was not preserved: %q, %v", content, readErr)
				}
			} else if !errors.Is(readErr, os.ErrNotExist) {
				t.Errorf("unit remains: %v", readErr)
			}
			if enabled != test.wantEnabled {
				t.Errorf("enabled = %v, want %v; commands = %q", enabled, test.wantEnabled, commands)
			}
			if test.wantStopError {
				want := []string{"systemctl --user stop " + UnitName, "systemctl --user show --property=ActiveState --property=MainPID " + UnitName}
				if !reflect.DeepEqual(commands, want) {
					t.Errorf("stop failure ran mutating follow-up commands: %q", commands)
				}
			}
		})
	}
}

func TestRootUninstallPreservesAnUnstoppedService(t *testing.T) {
	unixPathsOnly(t)
	for _, test := range uninstallCases() {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, UnitName)
			unit := "installed unit"
			if !test.absent {
				noErr(t, os.WriteFile(path, []byte(unit), 0o644))
			}
			enabledPath := filepath.Join(root, "enabled")
			if !test.absent {
				noErr(t, os.WriteFile(enabledPath, []byte("enabled"), 0o644))
			}
			noErr(t, os.WriteFile(filepath.Join(root, "systemctl"), []byte(`#!/bin/sh
printf '%s\n' "$*" >>"$COMMANDS"
case "$*" in
  'stop owngit.service')
    if [ "$STOP_FAILS" = true ]; then echo 'Unit refuses manual stop' >&2; exit 4; fi ;;
  'show --property=ActiveState --property=MainPID owngit.service')
    [ "$QUERY_FAILS" = false ] || exit 1
    printf 'ActiveState=%s\nMainPID=%s\n' "$ACTIVE_STATE" "$MAIN_PID" ;;
  'show --property=LoadState --value owngit.service') printf '%s\n' "$LOAD_STATE" ;;
  'disable --quiet owngit.service')
    if [ "$DISABLE_FAILS" = true ]; then echo 'disable refused' >&2; exit 1; fi
    [ ! -f "$ENABLED" ] || mv "$ENABLED" "$ENABLED.off" ;;
  'disable --now --quiet owngit.service')
    [ ! -f "$ENABLED" ] || mv "$ENABLED" "$ENABLED.off"
    if [ "$STOP_FAILS" = true ]; then echo 'Unit refuses manual stop' >&2; exit 4; fi ;;
  'daemon-reload') [ "$RELOAD_FAILS" = false ] || exit 1 ;;
  *) echo "unexpected command: $*" >&2; exit 2 ;;
esac
`), 0o755))
			script := strings.ReplaceAll(RootUninstallScript(), SystemUnitPath, path)
			// Preserve disposable files instead of permanently deleting them.
			script = strings.ReplaceAll(script, "rm -f "+shellQuote(path), "if [ -e "+shellQuote(path)+" ]; then mv "+shellQuote(path)+" "+shellQuote(path+".removed")+"; fi")
			boolString := func(value bool) string {
				if value {
					return "true"
				}
				return "false"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "/bin/sh", "-s")
			command.Stdin = strings.NewReader(script)
			command.Env = append(os.Environ(), "PATH="+root+":/usr/bin:/bin", "COMMANDS="+filepath.Join(root, "commands"), "ENABLED="+enabledPath,
				"STOP_FAILS="+boolString(test.stopFails), "ACTIVE_STATE="+test.activeState, "MAIN_PID="+test.mainPID, "QUERY_FAILS="+boolString(test.queryFails),
				"DISABLE_FAILS="+boolString(test.disableFails), "LOAD_STATE="+test.loadState, "RELOAD_FAILS="+boolString(test.reloadFails))
			output, err := command.CombinedOutput()
			if (err != nil) != test.wantErr {
				t.Errorf("uninstall error = %v, want error %v; output = %s", err, test.wantErr, output)
			}
			if test.wantStopError && (!strings.Contains(string(output), "Stop it manually") || !strings.Contains(string(output), "owngit service uninstall")) {
				t.Errorf("stop failure must explain how to retry: %s", output)
			}
			content, readErr := os.ReadFile(path)
			if test.wantUnit {
				if readErr != nil || string(content) != unit {
					t.Errorf("unit was not preserved: %q, %v", content, readErr)
				}
			} else if !errors.Is(readErr, os.ErrNotExist) {
				t.Errorf("unit remains: %v", readErr)
			}
			_, enabledErr := os.Stat(enabledPath)
			if enabled := enabledErr == nil; enabled != test.wantEnabled {
				t.Errorf("enabled = %v, want %v", enabled, test.wantEnabled)
			}
			if test.wantStopError {
				commands, err := os.ReadFile(filepath.Join(root, "commands"))
				noErr(t, err)
				if string(commands) != "stop owngit.service\nshow --property=ActiveState --property=MainPID owngit.service\n" {
					t.Errorf("stop failure ran mutating follow-up commands: %s", commands)
				}
			}
		})
	}
}
