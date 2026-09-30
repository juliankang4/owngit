package tailscale

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"slices"
	"strconv"
)

// ServeConfig is the part of Tailscale's Serve configuration that tells what
// answers on a port.
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

	// version and content are the version and the whole configuration as
	// Command.ServeConfig read them, fields OwnGit does not know included.
	// A change is made from content and binds to version.
	version string
	content []byte
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

// ParseServeConfig reads a Serve configuration in Tailscale's JSON.
func ParseServeConfig(output []byte) (ServeConfig, error) {
	var config ServeConfig
	if nothingConfigured(output) {
		return config, nil
	}
	if err := json.Unmarshal(output, &config); err != nil {
		return ServeConfig{}, &Error{Kind: KindUnreadable}
	}
	return config, nil
}

// nothingConfigured reports whether a Serve configuration in Tailscale's
// JSON is empty: "null", as Tailscale writes it, or no answer at all.
func nothingConfigured(content []byte) bool {
	trimmed := bytes.TrimSpace(content)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

// withEndpoint returns the configuration as read with the HTTPS endpoint for
// name on port set to a single handler at "/" that proxies to target, or
// removed when target is empty. Only the entries of that name and port
// change: every other field, handler and Funnel setting stays exactly as
// Tailscale gave it, also fields OwnGit does not know. The port's TCP entry
// goes with its last web server, as "tailscale serve" does.
func (config ServeConfig) withEndpoint(name string, port int, target string) ([]byte, error) {
	top := map[string]json.RawMessage{}
	if !nothingConfigured(config.content) {
		if err := json.Unmarshal(config.content, &top); err != nil || top == nil {
			return nil, &Error{Kind: KindUnreadable}
		}
	}
	var tcp, web map[string]json.RawMessage
	if err := decodeEntries(top, "TCP", &tcp); err != nil {
		return nil, err
	}
	if err := decodeEntries(top, "Web", &web); err != nil {
		return nil, err
	}
	portText := strconv.Itoa(port)
	key := net.JoinHostPort(name, portText)
	if target != "" {
		server, err := json.Marshal(WebServer{Handlers: map[string]Handler{"/": {Proxy: target}}})
		if err != nil {
			return nil, err
		}
		tcp[portText], web[key] = json.RawMessage(`{"HTTPS":true}`), server
	} else {
		delete(web, key)
		last := true
		for other := range web {
			if _, p, err := net.SplitHostPort(other); err == nil && p == portText {
				last = false
			}
		}
		if last {
			delete(tcp, portText)
		}
	}
	for field, entries := range map[string]map[string]json.RawMessage{"TCP": tcp, "Web": web} {
		if len(entries) == 0 {
			delete(top, field)
			continue
		}
		encoded, err := json.Marshal(entries)
		if err != nil {
			return nil, err
		}
		top[field] = encoded
	}
	return json.Marshal(top)
}

// ReviewDigest identifies the whole configuration as read, with port: the
// owner reviews what is on a port before OwnGit replaces it, and the
// replacement goes ahead only while the configuration is still exactly the
// one reviewed. Key order and spacing do not count.
func (config ServeConfig) ReviewDigest(port int) (string, error) {
	canonical := []byte("null")
	if !nothingConfigured(config.content) {
		decoder := json.NewDecoder(bytes.NewReader(config.content))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return "", &Error{Kind: KindUnreadable}
		}
		var err error
		// Marshal sorts the keys of every object and compacts the rest.
		if canonical, err = json.Marshal(value); err != nil {
			return "", err
		}
	}
	sum := sha256.Sum256(append([]byte(strconv.Itoa(port)+"\n"), canonical...))
	return hex.EncodeToString(sum[:16]), nil
}

// ReviewDigestWithout is ReviewDigest of the configuration as read with the
// endpoint for name on removed taken away, as RemoveHTTPS writes it: what
// the owner reviewed, once OwnGit removed its own endpoint to move there.
func (config ServeConfig) ReviewDigestWithout(name string, removed, port int) (string, error) {
	content, err := config.withEndpoint(name, removed, "")
	if err != nil {
		return "", err
	}
	return ServeConfig{content: content}.ReviewDigest(port)
}

// Replaceable reports whether OwnGit may replace what uses lists on a port
// with its own endpoint when the owner asks: never a port open to Funnel,
// which would make OwnGit public, and never one a foreground "tailscale
// serve" session holds, which only that session can change.
func Replaceable(uses []Use) bool {
	return len(uses) > 0 && !slices.ContainsFunc(uses, func(use Use) bool { return use.Kind == UseFunnel || use.Kind == UseForeground })
}

// decodeEntries decodes the map in field of top into entries, leaving each
// entry as Tailscale wrote it; a missing or null field is an empty map.
func decodeEntries(top map[string]json.RawMessage, field string, entries *map[string]json.RawMessage) error {
	if raw, ok := top[field]; ok {
		if err := json.Unmarshal(raw, entries); err != nil {
			return &Error{Kind: KindUnreadable}
		}
	}
	if *entries == nil {
		*entries = map[string]json.RawMessage{}
	}
	return nil
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
	// Found lists everything on the port that answers for the node name or
	// could, for the owner.
	Found []Use
	// Stale lists the web handlers on the port for another name: names this
	// computer had before it was renamed. Tailscale answers only for the
	// current name, so they serve nothing and do not keep the port from
	// being used, but "tailscale serve" can remove them only under their
	// own name.
	Stale []Use
}

// Endpoint describes port for the node name and the expected proxy target.
func (config ServeConfig) Endpoint(name string, port int, target string) Endpoint {
	portText := strconv.Itoa(port)
	var found, stale []Use
	add := func(use Use) {
		if !slices.Contains(found, use) {
			found = append(found, use)
		}
	}
	webKeys, staleKeys := 0, 0
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
			if !foreground && host != name {
				staleKeys++
				for path, handler := range server.Handlers {
					stale = append(stale, handler.use("https://"+net.JoinHostPort(host, p)+path))
				}
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
	// The HTTPS setting of the port alone, without a handler for any name,
	// is incomplete.
	tcp, tcpOK := config.TCP[portText]
	if len(found) == 0 && (webKeys > 0 || tcpOK && staleKeys == 0) {
		add(Use{Kind: UseIncomplete, Address: portText})
	}
	byPlace := func(a, b Use) int {
		return cmp.Or(cmp.Compare(a.Address, b.Address), cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Target, b.Target))
	}
	slices.SortFunc(found, byPlace)
	slices.SortFunc(stale, byPlace)
	endpoint := Endpoint{Found: found, Stale: stale}
	endpoint.Free = len(found) == 0 && webKeys == 0
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
