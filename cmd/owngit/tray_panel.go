package main

import (
	"context"
	"fmt"
	"time"

	"owngit/internal/bootstrap"
	"owngit/internal/tray"
	"owngit/internal/webui"
)

// trayPanelCommand is "owngit tray read" and "owngit tray open", for panels
// that other programs draw, such as the Omarchy bar widget. Such a panel
// runs these commands instead of reading the tray status itself, so the
// access file, the proof of every answer and the fresh proof before the
// dashboard opens stay here, as in the icon.
func trayPanelCommand(operation, stateDir string, lang webui.Lang, asJSON bool) error {
	if !trayAvailable(stateDir) {
		return jsonFailure(asJSON, "tray_unavailable", errTrayUnavailable)
	}
	if operation == "open" {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		target, err := tray.NewClient(stateDir, nil).Dashboard(ctx, string(lang))
		if err != nil {
			return jsonFailure(asJSON, "status_unavailable", fmt.Errorf("OwnGit did not prove that it answers, so the dashboard was not opened: %w", err))
		}
		if err := bootstrap.Open(target); err != nil {
			return jsonFailure(asJSON, "open_failed", fmt.Errorf("the browser could not be started for %s: %w", target, err))
		}
		if asJSON {
			return writeJSONValue(map[string]any{"ok": true, "url": target})
		}
		fmt.Println("Opened the dashboard at " + target + ".")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	panel := tray.NewPanel(tray.NewClient(stateDir, trayDiagnosis(stateDir)).Read(ctx, string(lang)), lang, time.Now())
	if asJSON {
		return writeJSONValue(struct {
			Shown bool       `json:"shown"`
			Panel tray.Panel `json:"panel"`
		}{trayShown(stateDir), panel})
	}
	lines := append([]string{panel.Tooltip}, panel.Notice...)
	if panel.Command != "" {
		lines = append(lines, panel.CommandIntro+" "+panel.Command)
	}
	if panel.CloneAddress != "" {
		lines = append(lines, panel.Labels.CloneAddress+": "+panel.CloneAddress)
	}
	lines = append(lines, panel.Labels.Recent+":")
	for _, push := range panel.Pushes {
		lines = append(lines, "  "+push.Repository+"  "+push.Branch+"  "+push.When)
	}
	if len(panel.Pushes) == 0 {
		lines = append(lines, "  "+panel.NoPushes)
	}
	for _, line := range lines {
		fmt.Println(printable(line))
	}
	return nil
}

// trayShown reports whether the owner lets the icon show; a choice that
// cannot be read leaves the icon visible with its diagnostic.
func trayShown(stateDir string) bool {
	shown, _ := tray.Shown(stateDir)
	return shown
}
