package main

import (
	"context"
	"fmt"
	"time"

	"owngit/internal/bootstrap"
	"owngit/internal/tray"
)

func openToastNotification(asJSON bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	target, err := tray.AwaitToastActivation(ctx)
	if err != nil {
		return jsonFailure(asJSON, "notification_unavailable", err)
	}
	address, err := tray.NewClient(target.StateDir, nil).DashboardPage(ctx, target.Lang, target.Page)
	if err != nil {
		return jsonFailure(asJSON, "status_unavailable", fmt.Errorf("OwnGit did not prove that it answers, so the notification page was not opened: %w", err))
	}
	if err := bootstrap.Open(address); err != nil {
		return jsonFailure(asJSON, "open_failed", err)
	}
	if asJSON {
		return writeJSONValue(map[string]any{"ok": true, "url": address})
	}
	return nil
}
