package tray

import (
	"fmt"
	"net/url"
	"time"

	"owngit/internal/webui"
)

// View is what the panel shows for one report, in one language. Every
// string is plain text.
type View struct {
	Condition Condition
	// State names the condition; Tooltip is the icon's name with it.
	State, Tooltip string
	// Subtitle is the server's version and address, or "" when it did not
	// answer.
	Subtitle string
	// Notice holds the sentences about what needs doing or why the status
	// is missing. CommandIntro and Command are the one command that does
	// it, or "".
	Notice                []string
	CommandIntro, Command string
	// CloneAddress is "" when the server did not answer.
	CloneAddress string
	// Pushes are the latest successful pushes, newest first; NoPushes is
	// the sentence shown instead when there are none.
	Pushes   []PushRow
	NoPushes string
	// DashboardURL is the dashboard's address on this computer, or "" when
	// the server did not answer. It is the only address the icon opens.
	DashboardURL string
}

// PushRow is one recent push.
type PushRow struct {
	// Repository is its name; Branch is the branch, or the ref when the
	// push did not update a branch; When is the time as dashboard lists
	// show it.
	Repository, Branch, When string
}

// shownFindings is how many checkup findings the panel lists; the
// dashboard lists the rest.
const shownFindings = 2

// NewView turns report into what the panel shows in lang at time now.
func NewView(report Report, lang webui.Lang, now time.Time) View {
	text := func(code webui.MessageCode, args ...any) string {
		if len(args) == 0 {
			return webui.Text(lang, code)
		}
		return fmt.Sprintf(webui.Text(lang, code), args...)
	}
	view := View{Condition: report.Condition, NoPushes: text(webui.MsgTrayPushesLater)}
	view.State = text(map[Condition]webui.MessageCode{
		Unavailable: webui.MsgTrayUnavailable, Running: webui.MsgTrayRunning,
		Attention: webui.MsgTrayAttention, Stopped: webui.MsgTrayStopped,
	}[report.Condition])
	view.Tooltip = "OwnGit: " + view.State
	command := func(intro webui.MessageCode, value string) {
		if value != "" && view.Command == "" {
			view.CommandIntro, view.Command = text(intro), value
		}
	}
	status := report.Status
	if status == nil {
		switch {
		case report.Message != "":
			view.Notice = []string{report.Message}
		case report.Condition == Unavailable:
			view.Notice = []string{text(webui.MsgTrayNoStatus)}
		}
		command(webui.MsgTrayRepairRun, report.Repair)
		return view
	}
	view.Subtitle = text(webui.MsgTrayVersion, status.Version)
	if parsed, err := url.Parse(status.DashboardURL); err == nil && parsed.Host != "" {
		view.Subtitle += ", " + parsed.Host
	}
	view.CloneAddress, view.DashboardURL = status.CloneAddress, report.Dashboard
	if status.SetupRequired {
		view.Notice = append(view.Notice, text(webui.MsgTraySetup))
	}
	if update := status.Update; update != nil {
		view.Notice = append(view.Notice, text(webui.MsgTrayUpdate, update.Version, status.Version))
		command(webui.MsgTrayUpdateRun, update.Command)
		if update.Command == "" {
			view.Notice = append(view.Notice, text(webui.MsgTrayUpdateGuide))
		}
		if update.Start != "" {
			view.Notice = append(view.Notice, text(webui.MsgTrayUpdateStart, update.Start))
		}
		if update.Restart {
			view.Notice = append(view.Notice, text(webui.MsgTrayUpdateRestart))
		}
	}
	shown, more := 0, 0
	for _, finding := range status.Findings {
		switch {
		case finding.Unchecked:
		case shown < shownFindings:
			view.Notice = append(view.Notice, finding.Message)
			command(webui.MsgTrayRepairRun, finding.Repair)
			shown++
		default:
			more++
		}
	}
	if more > 0 {
		view.Notice = append(view.Notice, text(webui.MsgTrayMoreFindings, more))
	}
	view.NoPushes = text(webui.MsgTrayNoPushes)
	for _, push := range status.Pushes {
		row := PushRow{Repository: push.Repository, Branch: push.Branch, When: webui.ListTime(lang, now, push.PushedAt.In(now.Location()))}
		if row.Repository == "" {
			row.Repository = push.RepositoryID
		}
		if row.Branch == "" {
			row.Branch = push.Ref
		}
		view.Pushes = append(view.Pushes, row)
	}
	return view
}
