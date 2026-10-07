package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"syscall"

	"owngit/internal/doctor"
	"owngit/internal/service"
	"owngit/internal/state"
	"owngit/internal/tray"
	"owngit/internal/webui"
)

// errTrayUnavailable is the answer of an install that offers no icon.
var errTrayUnavailable = errors.New("this kind of install has no OwnGit icon, because OwnGit runs as its own service account, not as the account that signs in at the desktop")

// trayAvailable reports whether the OwnGit icon is offered for the state in
// stateDir: always, except for the Linux service that runs as the dedicated
// owngit account, which no one signs in as at a desktop. Its state belongs
// to that account, so a desktop account could not read the icon's files.
func trayAvailable(stateDir string) bool {
	if runtime.GOOS != "linux" {
		return true
	}
	dir := filepath.Clean(mustAbs(stateDir))
	return dir != service.AccountStateDir && dir != pointerStateDir()
}

// trayCommand shows, hides or reports the OwnGit icon of this computer: its
// menu bar, notification area or panel icon. Hiding it never stops the
// server. The choice belongs to this computer, like the dashboard switch
// that does the same. "icon" runs the icon itself on Windows and Linux;
// "read" prints what its panel shows and "open" opens the dashboard after
// the server proves it answers, for panels drawn by other programs.
// "notifications" shows or changes which desktop notifications the icon
// shows.
func trayCommand(arguments []string) error {
	flags := flag.NewFlagSet("tray", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateDir := flags.String("state-dir", defaultStateDir(), "host-local state directory")
	asJSON := flags.Bool("json", false, "print JSON")
	lang := flags.String("lang", string(tray.DesktopLanguage()), "language of \"read\": en or ko")
	operands, err := parseFlagsAndOperands(flags, arguments)
	if err != nil {
		if errors.Is(err, errUsageShown) {
			return err
		}
		return jsonFailure(jsonRequested(arguments), "invalid_arguments", err)
	}
	operation := "status"
	if len(operands) > 0 {
		operation = operands[0]
	}
	switch {
	case operation == "notifications" && len(operands) != 1 && len(operands) != 3:
		return jsonFailure(*asJSON, "invalid_arguments", errors.New("tray notifications takes nothing, or a setting and on or off"))
	case operation != "notifications" && len(operands) > 1,
		!slices.Contains([]string{"on", "off", "status", "icon", "read", "open", "notifications"}, operation):
		return jsonFailure(*asJSON, "invalid_arguments", errors.New("tray takes on, off, status, icon, read, open, notifications or nothing"))
	}
	switch operation {
	case "icon":
		if *asJSON {
			return jsonFailure(true, "invalid_arguments", errors.New("tray icon prints no JSON"))
		}
		return runTrayIcon(filepath.Clean(mustAbs(*stateDir)))
	case "read", "open":
		language, ok := webui.ParseLang(*lang)
		if !ok {
			return jsonFailure(*asJSON, "invalid_arguments", errors.New("--lang is en or ko"))
		}
		return trayPanelCommand(operation, filepath.Clean(mustAbs(*stateDir)), language, *asJSON)
	}
	type report struct {
		Available  bool   `json:"available"`
		Shown      bool   `json:"shown"`
		Desktop    bool   `json:"desktop"`
		StateDir   string `json:"state_dir"`
		AccessFile string `json:"access_file"`
		// Problem says why the icon cannot show on this computer, or "".
		Problem string `json:"problem"`
	}
	dir := filepath.Clean(mustAbs(*stateDir))
	if !trayAvailable(dir) {
		if operation != "status" {
			return jsonFailure(*asJSON, "tray_unavailable", errTrayUnavailable)
		}
		if *asJSON {
			return writeJSONValue(report{StateDir: dir})
		}
		fmt.Println("The OwnGit icon is not available: " + errTrayUnavailable.Error() + ".")
		return nil
	}
	if err := state.RequireExisting(dir); err != nil {
		return jsonFailure(*asJSON, "state_unavailable", err)
	}
	held, err := state.OpenStateDirectory(dir)
	if err != nil {
		return jsonFailure(*asJSON, "state_unavailable", err)
	}
	defer held.Close()
	if operation == "notifications" {
		return trayNotificationsCommand(held, dir, operands[1:], *asJSON)
	}
	if operation != "status" {
		if err := state.SetTrayHidden(held, operation == "off"); err != nil {
			return jsonFailure(*asJSON, "state_unavailable", err)
		}
	}
	hidden, err := state.TrayHidden(held)
	if err != nil {
		return jsonFailure(*asJSON, "state_unavailable", err)
	}
	desktop := probeEnvironment().Desktop()
	problem := tray.IconProblem()
	if *asJSON {
		return writeJSONValue(report{true, !hidden, desktop, dir, filepath.Join(dir, state.TrayAccessFile), problem})
	}
	switch {
	case hidden:
		fmt.Println("The OwnGit icon is hidden on this computer. OwnGit keeps running. \"owngit tray on\" shows the icon again.")
	case !desktop:
		fmt.Println("The OwnGit icon is not hidden, but this computer has no desktop session now, so it does not show. OwnGit keeps running.")
	case problem != "":
		fmt.Println("The OwnGit icon is not hidden, but it cannot show on this computer: " + problem + ".")
	default:
		fmt.Println("The OwnGit icon shows on this computer. \"owngit tray off\" hides it; OwnGit keeps running.")
	}
	return nil
}

// trayNotificationsCommand prints the notification settings of this
// computer, after changing one when change names a setting and on or off:
// "all", "only_others" or one of state.NotifyKinds.
func trayNotificationsCommand(held *os.File, dir string, change []string, asJSON bool) error {
	choice, err := state.ReadTrayNotifications(held)
	if err != nil {
		return jsonFailure(asJSON, "state_unavailable", err)
	}
	if len(change) == 2 {
		setting, value := change[0], change[1]
		if value != "on" && value != "off" {
			return jsonFailure(asJSON, "invalid_arguments", errors.New("a notification setting is on or off"))
		}
		if choice, err = choice.With(setting, value == "on"); err != nil {
			return jsonFailure(asJSON, "invalid_arguments", err)
		}
		if err := state.WriteTrayNotifications(held, choice); err != nil {
			return jsonFailure(asJSON, "state_unavailable", err)
		}
	}
	kinds := map[string]bool{}
	for _, kind := range state.NotifyKinds {
		kinds[kind] = !slices.Contains(choice.KindsOff, kind)
	}
	if asJSON {
		return writeJSONValue(struct {
			All        bool            `json:"all"`
			OnlyOthers bool            `json:"only_others"`
			Kinds      map[string]bool `json:"kinds"`
			StateDir   string          `json:"state_dir"`
		}{!choice.Off, choice.OnlyOthers, kinds, dir})
	}
	onOff := map[bool]string{true: "on", false: "off"}
	fmt.Printf("all: %s\n", onOff[!choice.Off])
	fmt.Printf("only_others: %s\n", onOff[choice.OnlyOthers])
	for _, kind := range state.NotifyKinds {
		fmt.Printf("%s: %s\n", kind, onOff[kinds[kind]])
	}
	if choice.Off {
		fmt.Println("All notifications are off; the kinds keep their own choice for when they are on again.")
	}
	return nil
}

// runTrayIcon runs the notification area icon of the server of stateDir
// until it is quit, this program is interrupted, or the program that
// started it ends: the sign-in task ends its console host that way.
func runTrayIcon(stateDir string) error {
	if !trayAvailable(stateDir) {
		return errTrayUnavailable
	}
	stop := make(chan struct{})
	var once sync.Once
	end := func() { once.Do(func() { close(stop) }) }
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(interrupts)
	go func() {
		<-interrupts
		end()
	}()
	watchParentExit(end)
	err := tray.Run(tray.Options{StateDir: stateDir, Diagnose: trayDiagnosis(stateDir), Stop: stop})
	if errors.Is(err, tray.ErrAlreadyRunning) {
		fmt.Println("The OwnGit icon already runs for " + printable(stateDir) + ".")
		return nil
	}
	return err
}

// trayDiagnosis is the checkup the icon runs when the server does not
// answer, as "owngit doctor" runs it: only the finding that the server does
// not run makes the icon say that OwnGit stopped.
func trayDiagnosis(stateDir string) func(context.Context, string) (tray.Diagnosis, error) {
	return func(ctx context.Context, lang string) (tray.Diagnosis, error) {
		subject, err := commandSubject(stateDir)
		if err != nil {
			return tray.Diagnosis{}, err
		}
		if subject.server == doctor.ServerRunning {
			return tray.Diagnosis{}, errors.New("OwnGit answers again")
		}
		language, _ := webui.ParseLang(lang)
		for _, finding := range diagnose(ctx, subject) {
			switch finding.Code {
			case webui.MsgDoctorNotRunning, webui.MsgDoctorSilent, webui.MsgDoctorAddressTaken, webui.MsgDoctorUncheckedServer,
				webui.MsgTrayUnproven, webui.MsgTrayHealthKey, webui.MsgTrayStateUnsafe:
				message := finding.Sentence(language)
				if finding.Code == webui.MsgDoctorUncheckedServer {
					message = webui.Text(language, webui.MsgTrayNoStatus)
				}
				return tray.Diagnosis{
					Stopped: finding.Code == webui.MsgDoctorNotRunning,
					Message: message, Repair: finding.Repair,
				}, nil
			}
		}
		return tray.Diagnosis{}, errors.New("the checkup did not say why OwnGit does not answer")
	}
}
