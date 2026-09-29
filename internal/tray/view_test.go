package tray

import (
	"reflect"
	"testing"
	"time"

	"owngit/internal/server"
	"owngit/internal/webui"
)

func TestViewOfARunningServer(t *testing.T) {
	now := time.Date(2026, 9, 29, 15, 0, 0, 0, time.UTC)
	status := &server.TrayStatus{
		OK: true, State: "running", Version: "1.1.3", Shown: true,
		DashboardURL: "http://127.0.0.1:7654", CloneAddress: "http://127.0.0.1:7654/git/",
		Findings: []server.TrayFinding{{Code: "doctor.unchecked_firewall", Message: "Could not check the firewall.", Unchecked: true}},
		Pushes: []server.TrayPush{
			{RepositoryID: "notes", Repository: "notes", Ref: "refs/heads/main", Branch: "main", PushedAt: now.Add(-12 * time.Minute)},
			{RepositoryID: "site", Ref: "refs/tags/v1", PushedAt: now.Add(-26 * time.Hour)},
		},
	}
	view := NewView(Report{Condition: Running, Status: status}, webui.LangEN, now)
	want := View{
		Condition: Running, State: "Running", Tooltip: "OwnGit: Running",
		Subtitle: "Version 1.1.3, 127.0.0.1:7654", CloneAddress: status.CloneAddress, DashboardURL: status.DashboardURL,
		Pushes: []PushRow{
			{Repository: "notes", Branch: "main", When: "Today 14:48"},
			{Repository: "site", Branch: "refs/tags/v1", When: "Yesterday 13:00"},
		},
		NoPushes: "No pushes yet.",
	}
	if !reflect.DeepEqual(view, want) {
		t.Errorf("view\n%+v\nwant\n%+v", view, want)
	}
	korean := NewView(Report{Condition: Running, Status: status}, webui.LangKO, now)
	if korean.State != "실행 중" || korean.Subtitle != "버전 1.1.3, 127.0.0.1:7654" || korean.Pushes[0].When != "오늘 14:48" {
		t.Errorf("Korean view %+v", korean)
	}
}

// Attention lists what needs doing with one command to copy; checks that
// could not run do not ask for anything.
func TestViewOfAServerThatNeedsAttention(t *testing.T) {
	status := &server.TrayStatus{
		OK: true, State: "attention", Version: "1.1.3", SetupRequired: true,
		Update: &server.TrayUpdate{Version: "1.1.4", Command: "irm https://owngit.app/install.ps1 | iex", Start: `C:\owngit\owngit.exe`, Restart: true},
		Findings: []server.TrayFinding{
			{Message: "A", Repair: "repair-a"},
			{Message: "unchecked", Unchecked: true},
			{Message: "B"},
			{Message: "C"},
			{Message: "D"},
		},
	}
	view := NewView(Report{Condition: Attention, Status: status}, webui.LangEN, time.Now())
	notice := []string{
		"Finish setup in the dashboard.",
		"OwnGit 1.1.4 is available. You are running 1.1.3.",
		`Then start OwnGit again: C:\owngit\owngit.exe`,
		"Then restart OwnGit.",
		"A", "B",
		"The dashboard Settings list 2 more.",
	}
	if !reflect.DeepEqual(view.Notice, notice) || view.Command != status.Update.Command || view.CommandIntro != "To update, run this command:" || view.GuideURL != "" {
		t.Errorf("notice %q, command %q (%q), guide %q", view.Notice, view.Command, view.CommandIntro, view.GuideURL)
	}
	status.Update = &server.TrayUpdate{Version: "1.1.4", GuideURL: "https://github.com/juliankang4/owngit#install"}
	view = NewView(Report{Condition: Attention, Status: status}, webui.LangEN, time.Now())
	if view.Command != "repair-a" || view.CommandIntro != "To repair it, run this command:" || view.GuideURL != status.Update.GuideURL {
		t.Errorf("without an update command: command %q (%q), guide %q", view.Command, view.CommandIntro, view.GuideURL)
	}
}

func TestViewWithoutAnAnswer(t *testing.T) {
	stopped := NewView(Report{Condition: Stopped, Message: "OwnGit is not running.", Repair: "owngit service start"}, webui.LangEN, time.Now())
	if stopped.State != "Not running" || !reflect.DeepEqual(stopped.Notice, []string{"OwnGit is not running."}) || stopped.Command != "owngit service start" ||
		stopped.CloneAddress != "" || stopped.DashboardURL != "" || stopped.Subtitle != "" || stopped.NoPushes != "Recent pushes appear here while OwnGit runs." {
		t.Errorf("stopped %+v", stopped)
	}
	unavailable := NewView(Report{Condition: Unavailable}, webui.LangKO, time.Now())
	if unavailable.State != "상태를 알 수 없음" || len(unavailable.Notice) != 1 || unavailable.Notice[0] != webui.Text(webui.LangKO, webui.MsgTrayNoStatus) || unavailable.Command != "" {
		t.Errorf("unavailable %+v", unavailable)
	}
}

// The condition shows in the icon's shape, not only in color: every
// condition has its own glyph and symbol.
func TestEveryConditionHasItsOwnShape(t *testing.T) {
	conditions := []Condition{Running, Attention, Stopped, Unavailable}
	for _, draw := range []func(Condition, int) []uint8{Glyph, Symbol} {
		for index, first := range conditions {
			shape := draw(first, 32)
			if len(shape) != 32*32 || reflect.DeepEqual(shape, make([]uint8, 32*32)) {
				t.Fatalf("condition %d draws nothing", first)
			}
			for _, second := range conditions[index+1:] {
				if reflect.DeepEqual(shape, draw(second, 32)) {
					t.Errorf("conditions %d and %d look the same", first, second)
				}
			}
		}
	}
}
