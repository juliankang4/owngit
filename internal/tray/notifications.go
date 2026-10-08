package tray

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"owngit/internal/webui"
)

// ToastTarget stores a local state directory and a dashboard page, never a
// server address or credential. Activation obtains a fresh server proof.
type ToastTarget struct {
	StateDir string `json:"state_dir"`
	Page     string `json:"page"`
	Lang     string `json:"lang"`
}

func parseToastTarget(arguments string) (ToastTarget, error) {
	var target ToastTarget
	if err := json.Unmarshal([]byte(arguments), &target); err != nil {
		return target, err
	}
	_, languageOK := webui.ParseLang(target.Lang)
	if !filepath.IsAbs(target.StateDir) || strings.ContainsRune(target.StateDir, 0) || !dashboardPath(target.Page) || !languageOK {
		return ToastTarget{}, errors.New("the notification does not name a local dashboard page")
	}
	return target, nil
}
