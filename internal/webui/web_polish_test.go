package webui

import (
	"strings"
	"testing"
)

// New repository and Import are icon buttons beside both Repositories
// headings, the sidebar's and the dashboard list's. The icon is the visible
// label; the full name is the accessible name and the hover title, and
// Import carries the lock mark while the administrator password would be
// asked.
func TestRepositoryActionsAreNamedIconButtons(t *testing.T) {
	r := newRenderer(t)
	for _, lang := range Langs() {
		chrome := fullChrome(lang)
		chrome.Nav.NewImportURL = "/repositories/new-import"
		out := render(t, r, OverviewPage{Chrome: chrome, Activity: sampleGraph(), TotalCount: 1,
			Repositories: []RepositorySummary{{ID: "r1", Name: "forge-cli", URL: "/repositories/r1"}}})
		newButton := `<a class="iconbtn" href="/repositories/new" ` + string(biAttr(lang, "title", MsgRepoNewTitle)) + `>` + string(icon("plus")) +
			`<span class="visually-hidden">` + string(bi(lang, MsgRepoNewTitle)) + `</span></a>`
		importButton := `<a class="iconbtn" href="/repositories/new-import" ` + string(biAttr(lang, "title", MsgImportListLink)) + `>` + string(icon("import")) +
			`<span class="visually-hidden">` + string(bi(lang, MsgImportListLink)) + `</span><span class="adminlock"`
		menu := sidebarOfOutput(t, out)
		main := out[strings.Index(out, `<main`):]
		for where, part := range map[string]string{"sidebar": menu, "dashboard": main} {
			if strings.Count(part, newButton) != 1 || strings.Count(part, importButton) != 1 {
				t.Errorf("%s %s: the actions are not one named icon button each:\n%s", lang, where, part)
			}
			heading := strings.Index(part, wantText(lang, MsgNavRepos))
			if heading < 0 || strings.Index(part, newButton) < heading {
				t.Errorf("%s %s: the actions are not beside the Repositories heading", lang, where)
			}
		}
	}
}
