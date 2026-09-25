package server

import (
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"owngit/internal/requestctx"
)

type HostPolicy struct {
	mu      sync.RWMutex
	allowed map[string]struct{}
}

func NewHostPolicy(hosts ...string) *HostPolicy {
	policy := &HostPolicy{allowed: make(map[string]struct{})}
	for _, host := range append([]string{"localhost", "127.0.0.1", "::1"}, hosts...) {
		_ = policy.Add(host)
	}
	return policy
}

func (policy *HostPolicy) Add(value string) error {
	host, err := normalizeHost(value)
	if err != nil {
		return err
	}
	policy.mu.Lock()
	policy.allowed[host] = struct{}{}
	policy.mu.Unlock()
	return nil
}

func (policy *HostPolicy) Allows(requestHost string) bool {
	host, err := normalizeHost(requestHost)
	if err != nil {
		return false
	}
	policy.mu.RLock()
	_, allowed := policy.allowed[host]
	policy.mu.RUnlock()
	return allowed
}

func (policy *HostPolicy) Middleware(next http.Handler) http.Handler {
	return policy.MiddlewareAdmitting(nil, next)
}

// MiddlewareAdmitting is Middleware with one exception: a request whose Host
// the policy refuses still passes when admit, if not nil, accepts it. The
// Origin check and the security headers apply either way.
func (policy *HostPolicy) MiddlewareAdmitting(admit func(*http.Request) bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !policy.Allows(requestctx.Of(request).Host) && (admit == nil || !admit(request)) {
			if strings.HasPrefix(request.URL.Path, "/api/") {
				writeAPIError(writer, http.StatusMisdirectedRequest, "unrecognized_host", "The request Host is not approved.", nil)
			} else {
				http.Error(writer, "unrecognized host", http.StatusMisdirectedRequest)
			}
			return
		}
		if origin := request.Header.Get("Origin"); origin != "" && !sameOrigin(request, origin) {
			if strings.HasPrefix(request.URL.Path, "/api/") {
				writeAPIError(writer, http.StatusForbidden, "origin_mismatch", "The supplied Origin does not match this server.", nil)
			} else {
				http.Error(writer, "origin does not match this server", http.StatusForbidden)
			}
			return
		}
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Referrer-Policy", "same-origin")
		writer.Header().Set("X-Frame-Options", "DENY")
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		next.ServeHTTP(writer, request)
	})
}

func requireFormOrigin(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	return origin != "" && sameOrigin(request, origin)
}

func sameOrigin(request *http.Request, value string) bool {
	origin, err := url.Parse(value)
	if err != nil || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return false
	}
	info := requestctx.Of(request)
	return origin.Scheme == info.Scheme && strings.EqualFold(origin.Host, info.Host)
}

func normalizeHost(value string) (string, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	} else if strings.Count(value, ":") == 1 {
		if host, _, splitErr := net.SplitHostPort(value); splitErr == nil {
			value = host
		}
	}
	value = strings.TrimSuffix(strings.Trim(value, "[]"), ".")
	if value == "" || strings.ContainsAny(value, "/\\@\x00\r\n \t") {
		return "", &net.AddrError{Err: "invalid host", Addr: value}
	}
	if ip := net.ParseIP(value); ip != nil {
		return ip.String(), nil
	}
	if len(value) > 253 {
		return "", &net.AddrError{Err: "host is too long", Addr: value}
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", &net.AddrError{Err: "invalid host", Addr: value}
		}
		for _, character := range label {
			if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' {
				continue
			}
			return "", &net.AddrError{Err: "invalid host", Addr: value}
		}
	}
	return value, nil
}
