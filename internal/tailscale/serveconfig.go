package tailscale

import (
	"bytes"
	"cmp"
	"encoding/json"
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

// Use is one thing the Serve configuration has on a port. The words that
// describe it for the owner come from the message catalog, so only the
// kind and the values are here.
type Use struct {
	// Kind is one of the Use values below.
	Kind string `json:"kind"`
	// Address is where: an HTTPS address with its path, "name:port", or a
	// port number.
	Address string `json:"address"`
	// Target is where it leads, when it leads anywhere: a proxy target, a
	// file path or a redirect address.
	Target string `json:"target,omitempty"`
}

// Kinds of Use.
const (
	UseProxy      = "proxy"       // Address passes requests to Target
	UseFiles      = "files"       // Address serves the files at Target
	UseRedirect   = "redirect"    // Address redirects to Target
	UseText       = "text"        // Address answers with fixed text
	UseEmpty      = "empty"       // Address has a handler that serves nothing
	UseTCPForward = "tcp_forward" // port Address is forwarded to Target over TCP
	UsePlainHTTP  = "plain_http"  // port Address answers plain HTTP
	UseFunnel     = "funnel"      // Address is open to the public Internet
	UseForeground = "foreground"  // a foreground "tailscale serve" session uses port Address
	UseIncomplete = "incomplete"  // port Address has an incomplete setting
)

// Endpoint is what the Serve configuration has on one HTTPS port.
type Endpoint struct {
	// Free is true when nothing uses the port.
	Free bool
	// Exact is true when the port serves HTTPS for exactly one name with
	// exactly one handler, at "/", that proxies to the expected target, and
	// the port is not open to Funnel.
	Exact bool
	// Found lists everything on the port, for the owner.
	Found []Use
}

// Endpoint describes port for the node name and the expected proxy target.
func (config ServeConfig) Endpoint(name string, port int, target string) Endpoint {
	portText := strconv.Itoa(port)
	var found []Use
	add := func(use Use) {
		if !slices.Contains(found, use) {
			found = append(found, use)
		}
	}
	webKeys := 0
	exactHandler := false
	var visit func(ServeConfig, bool)
	visit = func(current ServeConfig, foreground bool) {
		if handler, ok := current.TCP[portText]; ok {
			if handler.TCPForward != "" {
				add(Use{Kind: UseTCPForward, Address: portText, Target: handler.TCPForward})
			} else if !handler.HTTPS {
				add(Use{Kind: UsePlainHTTP, Address: portText})
			}
		}
		for hostPort, server := range current.Web {
			host, p, err := net.SplitHostPort(hostPort)
			if err != nil || p != portText {
				continue
			}
			webKeys++
			for path, handler := range server.Handlers {
				add(handler.use("https://" + net.JoinHostPort(host, p) + path))
				if !foreground && host == name && path == "/" && len(server.Handlers) == 1 &&
					handler.Proxy == target && handler.Path == "" && handler.Text == "" && handler.Redirect == "" && len(handler.AcceptAppCaps) == 0 {
					exactHandler = true
				}
			}
		}
		for hostPort, open := range current.AllowFunnel {
			if _, p, err := net.SplitHostPort(hostPort); err == nil && p == portText && open {
				add(Use{Kind: UseFunnel, Address: hostPort})
			}
		}
		if foreground {
			if _, ok := current.TCP[portText]; ok {
				add(Use{Kind: UseForeground, Address: portText})
			}
		}
	}
	visit(config, false)
	for _, session := range config.Foreground {
		visit(session, true)
	}
	tcp, tcpOK := config.TCP[portText]
	if len(found) == 0 && (tcpOK || webKeys > 0) {
		add(Use{Kind: UseIncomplete, Address: portText})
	}
	slices.SortFunc(found, func(a, b Use) int {
		return cmp.Or(cmp.Compare(a.Address, b.Address), cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Target, b.Target))
	})
	endpoint := Endpoint{Found: found}
	endpoint.Free = len(found) == 0 && !tcpOK && webKeys == 0
	endpoint.Exact = exactHandler && webKeys == 1 && len(found) == 1 &&
		tcpOK && tcp == (TCPHandler{HTTPS: true})
	return endpoint
}

// use describes what a handler at address serves.
func (handler Handler) use(address string) Use {
	switch {
	case handler.Proxy != "":
		return Use{Kind: UseProxy, Address: address, Target: handler.Proxy}
	case handler.Path != "":
		return Use{Kind: UseFiles, Address: address, Target: handler.Path}
	case handler.Redirect != "":
		return Use{Kind: UseRedirect, Address: address, Target: handler.Redirect}
	case handler.Text != "":
		return Use{Kind: UseText, Address: address}
	}
	return Use{Kind: UseEmpty, Address: address}
}
