package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestPreferencesKeepSeparateSchemeChoicesAndUpgradeValues(t *testing.T) {
	app := newConfiguredApp(t)
	secure := httptest.NewTLSServer(app.Handler())
	t.Cleanup(secure.Close)
	plain := serve(t, app.Handler())
	for _, pref := range []struct {
		base, parameter, old, chosen, marker string
	}{
		{languageCookie, "lang", "ko", "en", `data-lang="%s"`},
		{appearanceCookie, "appearance", "dark", "light", `data-appearance="%s"`},
		{orderCookie, "order", "name-desc", "name-asc", `data-order="%s"`},
	} {
		t.Run(pref.parameter, func(t *testing.T) {
			jar := newBrowserJar()
			client := &http.Client{Jar: jar, Transport: secure.Client().Transport}
			address, _ := url.Parse(secure.URL)
			jar.SetCookies(address, []*http.Cookie{{Name: pref.base, Value: pref.old, Path: "/", Secure: true}})
			for _, scheme := range []struct {
				address, name, choice string
				secure                bool
			}{
				{secure.URL, "__Host-" + pref.base, pref.old, true},
				{plain.URL, pref.base + "_http", pref.chosen, false},
			} {
				if scheme.secure {
					page := browserGET(t, client, scheme.address+"/")
					migrated := setCookie(t, page, scheme.name)
					if migrated.Value != pref.old || !strings.Contains(page.body, strings.Replace(pref.marker, "%s", pref.old, 1)) {
						t.Fatal("the earlier preference was not rendered and migrated")
					}
				}
				page := browserGET(t, client, scheme.address+"/?"+pref.parameter+"="+scheme.choice)
				cookie := setCookie(t, page, scheme.name)
				if cookie.Value != scheme.choice || cookie.Secure != scheme.secure || cookie.HttpOnly || cookie.Path != "/" || cookie.Domain != "" {
					t.Fatalf("preference cookie attributes: %+v", cookie)
				}
				if page := browserGET(t, client, scheme.address+"/"); !strings.Contains(page.body, strings.Replace(pref.marker, "%s", scheme.choice, 1)) {
					t.Fatal("the new preference was not kept on reload")
				}
			}
			// An old value that changes later must not replace either scheme's choice.
			for _, scheme := range []struct {
				address, legacy, choice string
				secure                  bool
			}{
				{secure.URL, pref.chosen, pref.old, true},
				{plain.URL, pref.old, pref.chosen, false},
			} {
				jar.SetCookies(address, []*http.Cookie{{Name: pref.base, Value: scheme.legacy, Path: "/", Secure: scheme.secure}})
				page := browserGET(t, client, scheme.address+"/")
				if !strings.Contains(page.body, strings.Replace(pref.marker, "%s", scheme.choice, 1)) {
					t.Fatal("the earlier cookie replaced a saved scheme preference")
				}
			}
			// Plain legacy values migrate too, before any in-place choice.
			jar.held = map[string]*http.Cookie{pref.base: {Name: pref.base, Value: pref.old, Path: "/"}}
			if cookie := setCookie(t, browserGET(t, client, plain.URL+"/"), pref.base+"_http"); cookie.Value != pref.old || cookie.Secure {
				t.Fatal("the earlier plain preference was not migrated")
			}
		})
	}
}
