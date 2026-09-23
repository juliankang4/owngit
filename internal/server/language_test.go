package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestValidatedLanguageQueryRendersWithoutRedirectAndPersistsPreference(t *testing.T) {
	app := newConfiguredApp(t)
	server := serve(t, app.Handler())
	client, jar := newBrowserClient(t)
	body, status := dashboardGET(t, client, server.URL+"/?lang=ko")
	if status != http.StatusOK || !strings.Contains(body, "저장소") {
		t.Fatalf("Korean request status=%d did not render Korean", status)
	}
	parsed, _ := url.Parse(server.URL)
	found := false
	for _, cookie := range jar.Cookies(parsed) {
		if cookie.Name == languageCookie && cookie.Value == "ko" {
			found = true
		}
	}
	if !found {
		t.Fatal("validated language preference cookie was not saved")
	}
	body, status = dashboardGET(t, client, server.URL+"/")
	if status != http.StatusOK || !strings.Contains(body, "저장소") {
		t.Fatalf("stored Korean preference was not reused: status=%d", status)
	}
}
