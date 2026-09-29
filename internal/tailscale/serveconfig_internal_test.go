package tailscale

import (
	"encoding/json"
	"reflect"
	"testing"
)

// A change rewrites only the entries of OwnGit's name and port. Everything
// else goes back to Tailscale as it came, also fields inside entries that
// OwnGit does not know, which decoding into ServeConfig would drop.
func TestWithEndpointKeepsFieldsOwnGitDoesNotKnow(t *testing.T) {
	read := ServeConfig{content: []byte(`{
		"TCP": {"8443": {"HTTPS": true, "FutureTCP": 7}},
		"Web": {"box.tail0000.ts.net:8443": {"Handlers": {"/": {"Proxy": "http://127.0.0.1:3000", "FutureHandler": {"a": [1, 2]}}}}},
		"AllowFunnel": {"box.tail0000.ts.net:8443": true},
		"FutureTop": "kept"
	}`)}
	added, err := read.withEndpoint("box.tail0000.ts.net", 443, "http://127.0.0.1:7654")
	if err != nil {
		t.Fatal(err)
	}
	want := `{
		"TCP": {"443": {"HTTPS": true}, "8443": {"HTTPS": true, "FutureTCP": 7}},
		"Web": {
			"box.tail0000.ts.net:443": {"Handlers": {"/": {"Proxy": "http://127.0.0.1:7654"}}},
			"box.tail0000.ts.net:8443": {"Handlers": {"/": {"Proxy": "http://127.0.0.1:3000", "FutureHandler": {"a": [1, 2]}}}}
		},
		"AllowFunnel": {"box.tail0000.ts.net:8443": true},
		"FutureTop": "kept"
	}`
	if !sameJSON(t, added, want) {
		t.Fatalf("added:\n%s\nwant:\n%s", added, want)
	}
	removed, err := ServeConfig{content: added}.withEndpoint("box.tail0000.ts.net", 443, "")
	if err != nil {
		t.Fatal(err)
	}
	if !sameJSON(t, removed, string(read.content)) {
		t.Fatalf("removed:\n%s\nwant:\n%s", removed, read.content)
	}
	// Nothing configured is "null"; removing the last endpoint leaves an
	// empty configuration.
	added, err = ServeConfig{content: []byte("null")}.withEndpoint("box.tail0000.ts.net", 443, "http://127.0.0.1:7654")
	if err != nil {
		t.Fatal(err)
	}
	removed, err = ServeConfig{content: added}.withEndpoint("box.tail0000.ts.net", 443, "")
	if err != nil || string(removed) != "{}" {
		t.Fatalf("removed=%s err=%v", removed, err)
	}
	// An empty answer is read as "null" is, by ParseServeConfig too.
	fromEmpty, err := ServeConfig{content: []byte(" \n")}.withEndpoint("box.tail0000.ts.net", 443, "http://127.0.0.1:7654")
	if err != nil || string(fromEmpty) != string(added) {
		t.Fatalf("from an empty answer: %s err=%v, want %s", fromEmpty, err, added)
	}
	if parsed, err := ParseServeConfig(nil); err != nil || len(parsed.Web) != 0 {
		t.Fatalf("ParseServeConfig(empty) = %+v, %v", parsed, err)
	}
}

func sameJSON(t *testing.T, got []byte, want string) bool {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(a, b)
}
