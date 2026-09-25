package tailscale

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strconv"
)

// ServeConfig is the part of Tailscale's Serve configuration ("tailscale
// serve status --json") that tells what answers on a port.
type ServeConfig struct {
	// TCP maps a port to how Tailscale handles it.
	TCP map[string]TCPHandler `json:"TCP,omitempty"`
	// Web maps "name:port" to the web handlers there, by path.
	Web map[string]WebServer `json:"Web,omitempty"`
	// AllowFunnel lists the "name:port" values open to the public Internet.
	AllowFunnel map[string]bool `json:"AllowFunnel,omitempty"`
	// Foreground holds the configuration of foreground "tailscale serve"
	// sessions, by session.
	Foreground map[string]ServeConfig `json:"Foreground,omitempty"`
}

// TCPHandler is how Tailscale handles connections to one port.
type TCPHandler struct {
	HTTPS         bool   `json:"HTTPS,omitempty"`
	HTTP          bool   `json:"HTTP,omitempty"`
	TCPForward    string `json:"TCPForward,omitempty"`
	TerminateTLS  string `json:"TerminateTLS,omitempty"`
	ProxyProtocol int    `json:"ProxyProtocol,omitempty"`
}

// WebServer holds the web handlers of one name and port.
type WebServer struct {
	Handlers map[string]Handler `json:"Handlers,omitempty"`
}

// Handler is one web handler: a proxy, files, text or a redirect.
type Handler struct {
	Path          string   `json:"Path,omitempty"`
	Proxy         string   `json:"Proxy,omitempty"`
	Text          string   `json:"Text,omitempty"`
	Redirect      string   `json:"Redirect,omitempty"`
	AcceptAppCaps []string `json:"AcceptAppCaps,omitempty"`
}

// ParseServeConfig reads "tailscale serve status --json"; "null" and an
// empty answer mean nothing is configured.
func ParseServeConfig(output []byte) (ServeConfig, error) {
	var config ServeConfig
	if trimmed := bytes.TrimSpace(output); len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return config, nil
	}
	if err := json.Unmarshal(output, &config); err != nil {
		return ServeConfig{}, &Error{Kind: KindUnreadable}
	}
	return config, nil
}

// Endpoint is what the Serve configuration has on one HTTPS port.
type Endpoint struct {
	// Free is true when nothing uses the port.
	Free bool
	// Exact is true when the port serves HTTPS for exactly one name with
	// exactly one handler, at "/", that proxies to the expected target, and
	// the port is not open to Funnel.
	Exact bool
	// Found describes everything on the port, for the owner.
	Found []string
}

// Endpoint describes port for the node name and the expected proxy target.
func (config ServeConfig) Endpoint(name string, port int, target string) Endpoint {
	portText := strconv.Itoa(port)
	var found []string
	add := func(text string) {
		if !slices.Contains(found, text) {
			found = append(found, text)
		}
	}
	webKeys := 0
	exactHandler := false
	var visit func(ServeConfig, bool)
	visit = func(current ServeConfig, foreground bool) {
		if handler, ok := current.TCP[portText]; ok {
			if handler.TCPForward != "" {
				add(fmt.Sprintf("TCP forwarding of port %d to %s", port, handler.TCPForward))
			} else if !handler.HTTPS {
				add(fmt.Sprintf("plain HTTP on port %d", port))
			}
		}
		for hostPort, server := range current.Web {
			host, p, err := net.SplitHostPort(hostPort)
			if err != nil || p != portText {
				continue
			}
			webKeys++
			for path, handler := range server.Handlers {
				add(fmt.Sprintf("https://%s%s to %s", net.JoinHostPort(host, p), path, handler.describe()))
				if !foreground && host == name && path == "/" && len(server.Handlers) == 1 &&
					handler.Proxy == target && handler.Path == "" && handler.Text == "" && handler.Redirect == "" && len(handler.AcceptAppCaps) == 0 {
					exactHandler = true
				}
			}
		}
		for hostPort, open := range current.AllowFunnel {
			if _, p, err := net.SplitHostPort(hostPort); err == nil && p == portText && open {
				add(fmt.Sprintf("Funnel, open to the public Internet, on %s", hostPort))
			}
		}
		if foreground {
			if _, ok := current.TCP[portText]; ok {
				add(fmt.Sprintf("a foreground \"tailscale serve\" session on port %d", port))
			}
		}
	}
	visit(config, false)
	for _, session := range config.Foreground {
		visit(session, true)
	}
	tcp, tcpOK := config.TCP[portText]
	if len(found) == 0 && (tcpOK || webKeys > 0) {
		add(fmt.Sprintf("an incomplete Serve setting on port %d", port))
	}
	slices.Sort(found)
	endpoint := Endpoint{Found: found}
	endpoint.Free = len(found) == 0 && !tcpOK && webKeys == 0
	endpoint.Exact = exactHandler && webKeys == 1 && len(found) == 1 &&
		tcpOK && tcp == (TCPHandler{HTTPS: true})
	return endpoint
}

// describe names what a handler serves.
func (handler Handler) describe() string {
	switch {
	case handler.Proxy != "":
		return handler.Proxy
	case handler.Path != "":
		return "files at " + handler.Path
	case handler.Redirect != "":
		return "a redirect to " + handler.Redirect
	case handler.Text != "":
		return "fixed text"
	}
	return "nothing"
}
