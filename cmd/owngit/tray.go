package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"owngit/internal/state"
)

// trayCommand shows, hides or reports the OwnGit icon of this computer: its
// menu bar, notification area or panel icon. Hiding it never stops the
// server. The choice belongs to this computer, like the dashboard switch
// that does the same.
func trayCommand(arguments []string) error {
	flags := flag.NewFlagSet("tray", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateDir := flags.String("state-dir", defaultStateDir(), "host-local state directory")
	asJSON := flags.Bool("json", false, "print JSON")
	operands, err := parseFlagsAndOperands(flags, arguments)
	if err != nil {
		if errors.Is(err, errUsageShown) {
			return err
		}
		return jsonFailure(jsonRequested(arguments), "invalid_arguments", err)
	}
	operation := "status"
	if len(operands) == 1 {
		operation = operands[0]
	}
	if len(operands) > 1 || (operation != "on" && operation != "off" && operation != "status") {
		return jsonFailure(*asJSON, "invalid_arguments", errors.New("tray takes on, off, status or nothing"))
	}
	if err := state.RequireExisting(*stateDir); err != nil {
		return jsonFailure(*asJSON, "state_unavailable", err)
	}
	held, err := state.OpenStateDirectory(*stateDir)
	if err != nil {
		return jsonFailure(*asJSON, "state_unavailable", err)
	}
	defer held.Close()
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
	if *asJSON {
		return writeJSONValue(struct {
			Shown      bool   `json:"shown"`
			Desktop    bool   `json:"desktop"`
			StateDir   string `json:"state_dir"`
			AccessFile string `json:"access_file"`
		}{!hidden, desktop, held.Name(), filepath.Join(held.Name(), state.TrayAccessFile)})
	}
	switch {
	case hidden:
		fmt.Println("The OwnGit icon is hidden on this computer. OwnGit keeps running. \"owngit tray on\" shows the icon again.")
	case !desktop:
		fmt.Println("The OwnGit icon is not hidden, but this computer has no desktop session now, so it does not show. OwnGit keeps running.")
	default:
		fmt.Println("The OwnGit icon shows on this computer. \"owngit tray off\" hides it; OwnGit keeps running.")
	}
	return nil
}
