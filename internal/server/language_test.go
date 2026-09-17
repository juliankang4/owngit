package server

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/auth"
)

func TestValidatedLanguageQueryRendersWithoutRedirectAndPersistsPreference(t *testing.T) {
	app, store, repositoryRoot := newTestApp(t)
	if err := os.MkdirAll(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	canonical, _ := filepath.EvalSymlinks(repositoryRoot)
	adminHash, _ := auth.HashPassword("admin-password")
	if err := store.CompleteSetup(context.Background(), canonical, "open", "", adminHash, true); err != nil {
		t.Fatal(err)
	}
	app.Repositories.SetRoot(canonical)
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
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
