package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// This opt-in fixture drives the real public-share and pre-login setup
// handlers in a browser. Its authority is synthetic and never exported.
func TestBrowserAssetBoundariesFixture(t *testing.T) {
	ready := os.Getenv("OWNGIT_ASSET_BROWSER_READY")
	if ready == "" {
		t.Skip("requires the isolated browser boundary fixture")
	}
	app := newConfiguredApp(t)
	seedRepository(t, app, "shared-pages", map[string]string{"lines.txt": strings.Repeat("a\n", 20000)}, time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	private := httptest.NewServer(app.Handler())
	t.Cleanup(private.Close)
	public := httptest.NewServer(app.PublicShareHandler())
	t.Cleanup(public.Close)
	created := createShare(t, private.URL, "shared-pages", map[string]any{"label": "Synthetic browse"})
	created.URL = public.URL + strings.TrimPrefix(created.URL, private.URL)
	client, home := openShare(t, created)
	address, _ := url.Parse(home)
	secret := ""
	for _, cookie := range client.Jar.Cookies(address) {
		if cookie.Name == shareCookie {
			secret = cookie.Value
		}
	}
	if secret == "" {
		t.Fatal("share authority cookie missing")
	}
	setupApp, store, _ := newTestApp(t)
	noErr(t, store.PutBootstrap(context.Background(), "synthetic-owner-token", time.Now().Add(time.Hour)))
	setup := httptest.NewServer(setupApp.Handler())
	t.Cleanup(setup.Close)
	setupAddress, _ := url.Parse(setup.URL)
	data, err := json.Marshal(map[string]string{
		"sharePage": home + "/code?path=lines.txt",
		"shareBase": public.URL,
		"sharePath": "/share/" + created.ShareLink.ID,
		"cookie":    secret,
		"setupPage": "http://setup.example.test:" + setupAddress.Port() + "/setup",
	})
	noErr(t, err)
	noErr(t, os.WriteFile(ready, data, 0o600))
	for until := time.Now().Add(5 * time.Minute); time.Now().Before(until); {
		if _, err := os.Stat(ready + ".stop"); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("browser boundary fixture did not finish")
}
