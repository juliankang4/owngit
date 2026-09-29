package tray

import (
	"os"
	"strings"
	"time"

	"owngit/internal/webui"
)

// Panel is a View with the words of the panel around it, as JSON. The
// Linux panel and the Omarchy bar widget draw it: they receive it from
// "owngit tray icon" and "owngit tray read --json" and show every string as
// plain text. It holds no address to open; opening the dashboard goes back
// to owngit, which asks the server for a fresh proof first.
type Panel struct {
	// Condition is "running", "attention", "stopped" or "unavailable".
	Condition    string      `json:"condition"`
	State        string      `json:"state"`
	Tooltip      string      `json:"tooltip"`
	Subtitle     string      `json:"subtitle"`
	Notice       []string    `json:"notice"`
	CommandIntro string      `json:"command_intro"`
	Command      string      `json:"command"`
	CloneAddress string      `json:"clone_address"`
	Pushes       []PanelPush `json:"pushes"`
	NoPushes     string      `json:"no_pushes"`
	// CanOpen is true when the server answered, so the dashboard can be
	// opened.
	CanOpen bool        `json:"can_open"`
	Labels  PanelLabels `json:"labels"`
}

// PanelPush is one recent push.
type PanelPush struct {
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	When       string `json:"when"`
}

// PanelLabels are the panel's headings and button names.
type PanelLabels struct {
	CloneAddress string `json:"clone_address"`
	CloneHelp    string `json:"clone_help"`
	Copy         string `json:"copy"`
	CopyCommand  string `json:"copy_command"`
	CopyClone    string `json:"copy_clone"`
	Copied       string `json:"copied"`
	Recent       string `json:"recent"`
	Open         string `json:"open"`
	ShowPanel    string `json:"show_panel"`
	ThisComputer string `json:"this_computer"`
	Hide         string `json:"hide"`
	Quit         string `json:"quit"`
	KeepsRunning string `json:"keeps_running"`
}

var conditionNames = map[Condition]string{
	Unavailable: "unavailable", Running: "running", Attention: "attention", Stopped: "stopped",
}

// NewPanel is the panel of report in lang at time now.
func NewPanel(report Report, lang webui.Lang, now time.Time) Panel {
	view := NewView(report, lang, now)
	text := func(code webui.MessageCode) string { return webui.Text(lang, code) }
	panel := Panel{
		Condition: conditionNames[view.Condition], State: view.State, Tooltip: view.Tooltip,
		Subtitle: view.Subtitle, Notice: append([]string{}, view.Notice...),
		CommandIntro: view.CommandIntro, Command: view.Command, CloneAddress: view.CloneAddress,
		Pushes: []PanelPush{}, NoPushes: view.NoPushes, CanOpen: view.DashboardURL != "",
		Labels: PanelLabels{
			CloneAddress: text(webui.MsgTrayCloneAddress), CloneHelp: text(webui.MsgTrayCloneHelp),
			Copy: text(webui.MsgTrayCopy), CopyCommand: text(webui.MsgTrayCopyCommand),
			CopyClone: text(webui.MsgTrayCopyClone), Copied: text(webui.MsgTrayCopied),
			Recent: text(webui.MsgTrayRecent), Open: text(webui.MsgTrayOpen),
			ShowPanel: text(webui.MsgTrayShowPanel), ThisComputer: text(webui.MsgTrayThisComputer),
			Hide: text(webui.MsgTrayHidePanel), Quit: text(webui.MsgTrayQuit),
			KeepsRunning: text(webui.MsgTrayKeepsRunning),
		},
	}
	for _, push := range view.Pushes {
		panel.Pushes = append(panel.Pushes, PanelPush(push))
	}
	return panel
}

// DesktopLanguage is Korean when the session's language is Korean, and
// English otherwise, read from the locale variables in their usual order.
func DesktopLanguage() webui.Lang {
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if value := os.Getenv(name); value != "" {
			if strings.HasPrefix(value, "ko") {
				return webui.LangKO
			}
			return webui.LangEN
		}
	}
	return webui.LangEN
}
