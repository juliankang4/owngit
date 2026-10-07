package server

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

var orderedRow = regexp.MustCompile(`(?s)href="/repositories/([^"/?]+)"[^>]*?data-order-item`)

// listedOrder returns the repository IDs of the dashboard list and of the
// sidebar, in page order.
func listedOrder(t *testing.T, body string) (dashboard, sidebar []string) {
	t.Helper()
	start := strings.Index(body, `<nav class="sidebar"`)
	main := strings.Index(body, `<main`)
	if start < 0 || main < start {
		t.Fatalf("page has no sidebar and main:\n%s", body)
	}
	for _, match := range orderedRow.FindAllStringSubmatch(body[start:main], -1) {
		sidebar = append(sidebar, match[1])
	}
	for _, match := range orderedRow.FindAllStringSubmatch(body[main:], -1) {
		dashboard = append(dashboard, match[1])
	}
	return dashboard, sidebar
}

// The dashboard list and the sidebar follow the same chosen order, from the
// first view of a freshly started server on. A repository without commits
// stays listed, last in both time orders, and equal times follow name order
// with numbers compared by value.
func TestDashboardAndSidebarShareTheChosenOrder(t *testing.T) {
	app := newConfiguredApp(t)
	same := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	seedRepository(t, app, "project-10", map[string]string{"a.txt": "a\n"}, same)
	seedRepository(t, app, "project-2", map[string]string{"a.txt": "a\n"}, same)
	seedRepository(t, app, "alpha", map[string]string{"a.txt": "a\n"}, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	if _, err := app.Repositories.Create(context.Background(), "empty", ""); err != nil {
		t.Fatal(err)
	}
	server := serve(t, app.Handler())

	for _, test := range []struct {
		sort string
		want []string
	}{
		{"updated-desc", []string{"project-2", "project-10", "alpha", "empty"}},
		{"updated-asc", []string{"alpha", "project-2", "project-10", "empty"}},
		{"name-asc", []string{"alpha", "empty", "project-2", "project-10"}},
		{"name-desc", []string{"project-10", "project-2", "empty", "alpha"}},
	} {
		body, status := dashboardGET(t, &http.Client{}, server.URL+"/?order="+test.sort)
		dashboard, sidebar := listedOrder(t, body)
		if status != http.StatusOK || !slices.Equal(dashboard, test.want) || !slices.Equal(sidebar, test.want) {
			t.Errorf("%s: status %d, dashboard %v, sidebar %v, want %v", test.sort, status, dashboard, sidebar, test.want)
		}
		if !strings.Contains(body, `<option value="`+test.sort+`"`) || !strings.Contains(body, `data-order="`+test.sort+`"`) {
			t.Errorf("%s: the page does not show the chosen order", test.sort)
		}
	}
}

// Without the script, the order form is an ordinary GET: the server sorts
// and remembers the choice in this browser, so later pages, including the
// sidebar elsewhere, keep it. An unknown value changes nothing.
func TestListOrderIsRememberedPerBrowserWithoutScript(t *testing.T) {
	app := newConfiguredApp(t)
	seedRepository(t, app, "zulu", map[string]string{"a.txt": "a\n"}, time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	seedRepository(t, app, "alpha", map[string]string{"a.txt": "a\n"}, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	server := serve(t, app.Handler())
	jar, err := cookiejar.New(nil)
	noErr(t, err)
	client := &http.Client{Jar: jar}

	first := browserGET(t, client, server.URL+"/")
	if dashboard, _ := listedOrder(t, first.body); !slices.Equal(dashboard, []string{"zulu", "alpha"}) {
		t.Fatalf("the default order is not recently updated first: %v", dashboard)
	}

	chosen := browserGET(t, client, server.URL+"/?order=name-asc&lang=en")
	var saved *http.Cookie
	for _, cookie := range chosen.header.Values("Set-Cookie") {
		if parsed, err := http.ParseSetCookie(cookie); err == nil && parsed.Name == orderCookie+"_http" {
			saved = parsed
		}
	}
	if saved == nil || saved.Value != "name-asc" || saved.HttpOnly {
		t.Fatalf("the order was not saved as a script-readable preference: %+v", saved)
	}
	// Links built from this screen do not carry the parameter on.
	if strings.Contains(chosen.body, "order=name-asc") {
		t.Error("a link on the page repeats the sort parameter")
	}

	for _, target := range []string{"/", "/?order=bogus", "/activity"} {
		body, _ := dashboardGET(t, client, server.URL+target)
		dashboard, sidebar := listedOrder(t, body)
		if !slices.Equal(sidebar, []string{"alpha", "zulu"}) || (target != "/activity" && !slices.Equal(dashboard, []string{"alpha", "zulu"})) {
			t.Errorf("%s: the saved order is not kept: dashboard %v, sidebar %v", target, dashboard, sidebar)
		}
	}
	if cookies := jar.Cookies(&url.URL{Scheme: "http", Host: strings.TrimPrefix(server.URL, "http://")}); !slices.ContainsFunc(cookies, func(c *http.Cookie) bool {
		return c.Name == orderCookie+"_http" && c.Value == "name-asc"
	}) {
		t.Error("an unknown sort value replaced the saved order")
	}
}
