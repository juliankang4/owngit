package githttp

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"owngit/internal/repository"
)

// The Git backend learns the scheme, server name and client address from the
// request's own connection. Forwarded headers from any peer change nothing.
func TestCGIEnvironmentUsesTheConnectionOnly(t *testing.T) {
	handler := &Handler{Repositories: &repository.Manager{Root: t.TempDir()}}
	route := route{repositoryID: "demo", pathInfo: "/demo.git/info/refs", service: "git-upload-pack", query: "service=git-upload-pack"}
	cases := []struct {
		name, host, remote string
		tls                bool
		want, absent       []string
	}{
		{"plain IPv4", "127.0.0.1:7654", "192.0.2.4:5000", false,
			[]string{"REMOTE_ADDR=192.0.2.4", "SERVER_NAME=127.0.0.1", "SERVER_PORT=7654"}, []string{"HTTPS=on"}},
		{"TLS Host without port", "git.example", "[2001:db8::5]:5000", true,
			[]string{"REMOTE_ADDR=2001:db8::5", "SERVER_NAME=git.example", "SERVER_PORT=443", "HTTPS=on"}, nil},
		{"plain Host without port", "git.example", "192.0.2.4:5000", false,
			[]string{"REMOTE_ADDR=192.0.2.4", "SERVER_NAME=git.example", "SERVER_PORT=80"}, []string{"HTTPS=on"}},
		{"malformed peer", "git.example:8080", "unix-socket", false,
			[]string{"REMOTE_ADDR=unix-socket", "SERVER_NAME=git.example", "SERVER_PORT=8080"}, []string{"HTTPS=on"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/git/demo.git/info/refs?service=git-upload-pack", nil)
			request.Host = test.host
			request.RemoteAddr = test.remote
			request.TLS = nil
			if test.tls {
				request.TLS = &tls.ConnectionState{}
			}
			request.Header.Set("X-Forwarded-For", "203.0.113.9")
			request.Header.Set("X-Forwarded-Proto", "https")
			request.Header.Set("X-Forwarded-Host", "attacker.example")
			request.Header.Set("Forwarded", "for=203.0.113.9;proto=https;host=attacker.example")
			environment, err := handler.cgiEnvironment(request, route, -1)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range test.want {
				if !slices.Contains(environment, entry) {
					t.Fatalf("environment lacks %q: %q", entry, environment)
				}
			}
			for _, entry := range test.absent {
				if slices.Contains(environment, entry) {
					t.Fatalf("environment has %q: %q", entry, environment)
				}
			}
		})
	}
}
