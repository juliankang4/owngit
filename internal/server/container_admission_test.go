package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestContainerHostAdmissionStaysBoundToPassword(t *testing.T) {
	for _, path := range []string{"/", "/api/v1/repositories", "/git/unknown.git/info/refs?service=git-upload-pack"} {
		t.Run(path, func(t *testing.T) {
			app, store, root := newTestApp(t)
			if err := os.MkdirAll(root, 0o700); err != nil {
				t.Fatal(err)
			}
			canonical, err := filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.CompleteSetup(context.Background(), canonical, "password", fixturePasswordHash(t, "shared-password"), fixturePasswordHash(t, "admin-password"), true); err != nil {
				t.Fatal(err)
			}
			app.Repositories.SetRoot(canonical)
			app.Hosts = NewHostPolicy("gitbox.lan")
			changed := false
			app.Hosts.InContainer(func() (bool, int64, error) {
				settings, err := store.Settings(context.Background())
				if err != nil {
					return false, 0, err
				}
				if !changed {
					changed = true
					if err := store.DisableAccessPassword(context.Background()); err != nil {
						return false, 0, err
					}
				}
				return settings.AccessMode == "password", settings.AccessSessionVersion, nil
			})
			handler := app.Handler()
			send := func(host string) int {
				request := httptest.NewRequest(http.MethodGet, "http://"+host+path, nil)
				request.RemoteAddr = "172.18.0.1:40000"
				request.SetBasicAuth("owngit", "shared-password")
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				return response.Code
			}
			if status := send("localhost:7654"); status != http.StatusMisdirectedRequest && status != http.StatusUnauthorized {
				t.Fatalf("remote loopback Host continued as open access: status=%d", status)
			}
			if status := send("gitbox.lan:7654"); status == http.StatusMisdirectedRequest || status == http.StatusUnauthorized {
				t.Fatalf("approved network Host did not retain open access: status=%d", status)
			}
		})
	}
}

func TestContainerHostAdmissionKeepsPasswordAndApprovedHostsWorking(t *testing.T) {
	app, store, root := newTestApp(t)
	noErr(t, os.MkdirAll(root, 0o700))
	canonical, err := filepath.EvalSymlinks(root)
	noErr(t, err)
	noErr(t, store.CompleteSetup(context.Background(), canonical, "password", fixturePasswordHash(t, "shared-password"), fixturePasswordHash(t, "admin-password"), true))
	app.Repositories.SetRoot(canonical)
	app.Hosts = NewHostPolicy("gitbox.lan")
	app.Hosts.InContainer(func() (bool, int64, error) {
		settings, err := store.Settings(context.Background())
		return settings.AccessMode == "password", settings.AccessSessionVersion, err
	})
	handler := app.Handler()
	for _, host := range []string{"localhost:7654", "gitbox.lan:7654"} {
		for _, authenticated := range []bool{false, true} {
			request := httptest.NewRequest(http.MethodGet, "http://"+host+"/git/unknown.git/info/refs?service=git-upload-pack", nil)
			request.RemoteAddr = "172.18.0.1:40000"
			if authenticated {
				request.SetBasicAuth("owngit", "shared-password")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			want := http.StatusUnauthorized
			if authenticated {
				want = http.StatusNotFound
			}
			if response.Code != want {
				t.Fatalf("Host %s authenticated=%t: status=%d, want %d", host, authenticated, response.Code, want)
			}
		}
	}
}

func TestContainerHostAdmissionRequiresCurrentPasswordVersion(t *testing.T) {
	app, store, root := newTestApp(t)
	noErr(t, os.MkdirAll(root, 0o700))
	canonical, err := filepath.EvalSymlinks(root)
	noErr(t, err)
	noErr(t, store.CompleteSetup(context.Background(), canonical, "password", fixturePasswordHash(t, "shared-password"), fixturePasswordHash(t, "admin-password"), true))
	app.Repositories.SetRoot(canonical)
	app.Hosts = NewHostPolicy()
	app.Hosts.InContainer(func() (bool, int64, error) {
		settings, err := store.Settings(context.Background())
		return settings.AccessMode == "password", settings.AccessSessionVersion - 1, err
	})
	request := httptest.NewRequest(http.MethodGet, "http://localhost:7654/", nil)
	request.RemoteAddr = "172.18.0.1:40000"
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusMisdirectedRequest {
		t.Fatalf("changed password version: status=%d, want 421", response.Code)
	}
}
