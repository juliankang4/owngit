package server

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"owngit/internal/repository"
	"owngit/internal/webui"
)

// The overview's Languages panel counts the default branch in both
// languages of the interface, and a repository with no listed language says
// nothing was detected. (An empty repository shows the first-push screen,
// which has no side column at all.)
func TestOverviewShowsLanguages(t *testing.T) {
	app := newConfiguredApp(t)
	seedRepository(t, app, "mixed", map[string]string{
		"main.go":         strings.Repeat("g", 750),
		"web/site.css":    strings.Repeat("c", 250),
		"README.md":       strings.Repeat("r", 5000),
		"vendor/dep/x.go": strings.Repeat("v", 5000),
		"web/lib.min.js":  strings.Repeat("m", 5000),
		"config/app.yaml": strings.Repeat("y", 5000),
	}, time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	seedRepository(t, app, "prose", map[string]string{"README.md": "# Notes\n", "data.json": "{}\n"}, time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	server := serve(t, app.Handler())
	client := &http.Client{}
	for _, lang := range []webui.Lang{webui.LangEN, webui.LangKO} {
		body, status := dashboardGET(t, client, server.URL+"/repositories/mixed?lang="+string(lang))
		start := strings.Index(body, `<section class="ov-panel langs"`)
		if status != http.StatusOK || start < 0 {
			t.Fatalf("%s overview status=%d has no Languages panel", lang, status)
		}
		panel := body[start:]
		panel = panel[:strings.Index(panel, "</section>")]
		for _, want := range []string{
			webui.Text(lang, webui.MsgRepoLanguagesTitle),
			`<span class="langs__name">Go</span> <span class="langs__pct">75.0%</span>`,
			`<span class="langs__name">CSS</span> <span class="langs__pct">25.0%</span>`,
			`--lang:#00add8`, `<div class="langs__bar" aria-hidden="true">`,
			`aria-label="` + webui.Text(lang, webui.MsgRepoLanguagesBarLabel) + `"`,
		} {
			if !strings.Contains(panel, want) {
				t.Errorf("%s Languages panel lacks %q:\n%s", lang, want, panel)
			}
		}
		if strings.Contains(panel, "YAML") || strings.Contains(panel, "Markdown") || strings.Contains(panel, "JavaScript") {
			t.Errorf("%s Languages panel counts data, prose, or generated files:\n%s", lang, panel)
		}
		body, _ = dashboardGET(t, client, server.URL+"/repositories/prose?lang="+string(lang))
		if !strings.Contains(body, webui.Text(lang, webui.MsgRepoLanguagesNone)) || strings.Contains(body, "langs__bar") {
			t.Errorf("%s repository without code does not say no languages were detected", lang)
		}
	}
}

// Up to six languages are named, largest first; the rest is folded into
// Other, and a share too small for one decimal says so.
func TestLanguageRowsFoldTheRestIntoOther(t *testing.T) {
	shares := []repository.LanguageShare{
		{Name: "Go", Bytes: 900_000}, {Name: "HTML", Bytes: 40_000}, {Name: "CSS", Bytes: 30_000},
		{Name: "Shell", Bytes: 20_000}, {Name: "Python", Bytes: 9_600}, {Name: "Makefile", Bytes: 300},
		{Name: "Dockerfile", Bytes: 70}, {Name: "Lua", Bytes: 30},
	}
	rows := languageRows(shares)
	var got []string
	for _, row := range rows {
		got = append(got, row.Name+" "+row.Percent)
	}
	want := []string{"Go 90.0%", "HTML 4.0%", "CSS 3.0%", "Shell 2.0%", "Python 1.0%", "Makefile <0.1%", "Other <0.1%"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	if last := rows[len(rows)-1]; !last.Other || last.Color != "" || rows[0].Color != "#00add8" {
		t.Fatalf("Other row %+v or Go color %q", last, rows[0].Color)
	}
	if languageRows(nil) != nil {
		t.Fatal("no shares made rows")
	}
}
