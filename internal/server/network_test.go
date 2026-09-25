package server

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestValidateListenAddress(t *testing.T) {
	for _, value := range []string{"127.0.0.1:7654", "0.0.0.0:7654", ":7654", "[::]:7654", "[::1]:1", "gitbox.internal:65535", "localhost:7654"} {
		if err := ValidateListenAddress(value); err != nil {
			t.Errorf("%q refused: %v", value, err)
		}
	}
	for _, value := range []string{"", "7654", "127.0.0.1", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:http", "bad host:7654", "exa_mple:7654", "::1:7654"} {
		if err := ValidateListenAddress(value); err == nil {
			t.Errorf("%q accepted", value)
		}
	}
}

func TestValidateBaseURL(t *testing.T) {
	for value, want := range map[string]string{
		"http://gitbox.internal:7654":    "http://gitbox.internal:7654",
		"https://my-mac.tail0000.ts.net": "https://my-mac.tail0000.ts.net",
		"http://[fd00::7]:7654":          "http://[fd00::7]:7654",
		"HTTP://192.168.1.20:7654":       "http://192.168.1.20:7654",
		"http://gitbox.test:1":           "http://gitbox.test:1",
		"http://gitbox.test:65535":       "http://gitbox.test:65535",
	} {
		if got, err := ValidateBaseURL(value); err != nil || got != want {
			t.Errorf("%q = %q, %v; want %q", value, got, err, want)
		}
	}
	for _, value := range []string{"", "gitbox.internal:7654", "ftp://gitbox.internal", "http://", "http://user@gitbox.internal",
		"http://gitbox.internal/", "http://gitbox.internal/owngit", "http://gitbox.internal?x=1", "http://gitbox.internal?",
		"http://gitbox.internal#top", "http://exa_mple.internal", "http:gitbox.internal",
		"http://gitbox.test:99999", "http://gitbox.test:0", "http://gitbox.test:", "http://[fd00::7]:", "http://gitbox.test:65536"} {
		if got, err := ValidateBaseURL(value); err == nil {
			t.Errorf("%q accepted as %q", value, got)
		}
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{"localhost": true, "LOCALHOST.": true, "127.0.0.1": true, "127.1.2.3": true, "::1": true,
		"": false, "0.0.0.0": false, "::": false, "192.168.1.20": false, "gitbox.internal": false} {
		if got := IsLoopbackHost(host); got != want {
			t.Errorf("IsLoopbackHost(%q)=%v, want %v", host, got, want)
		}
	}
}

// Clone addresses use the configured base URL when there is one, whatever
// Host the browser used, and otherwise the browser's own address.
func TestCloneAddressesPreferTheConfiguredBaseURL(t *testing.T) {
	app := newConfiguredApp(t)
	_, err := app.Repositories.Create(context.Background(), "demo", "")
	noErr(t, err)
	server := serve(t, app.Handler())
	repositoryPage := func() string {
		t.Helper()
		response, err := http.Get(server.URL + "/repositories/demo")
		noErr(t, err)
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		noErr(t, err)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("repository page status=%d", response.StatusCode)
		}
		return string(body)
	}
	if body := repositoryPage(); !strings.Contains(body, server.URL+"/git/demo.git") {
		t.Fatalf("derived clone URL %s/git/demo.git missing", server.URL)
	}
	app.BaseURL = "http://gitbox.internal:7654"
	body := repositoryPage()
	if !strings.Contains(body, "http://gitbox.internal:7654/git/demo.git") || strings.Contains(body, server.URL+"/git/") {
		t.Fatal("clone URL does not use the configured base URL")
	}
	if got := app.cloneURL(&http.Request{Host: "127.0.0.1:1"}, "my project"); got != "http://gitbox.internal:7654/git/my%20project.git" {
		t.Fatalf("clone URL=%q", got)
	}
}
