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

// The review digest without OwnGit's own endpoint is that of the
// configuration Tailscale has once the endpoint is removed, and of nothing
// else.
func TestReviewDigestWithoutOwnGitsEndpoint(t *testing.T) {
	digest := func(content string) string {
		t.Helper()
		value, err := ServeConfig{content: []byte(content)}.ReviewDigest(443)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	read := ServeConfig{content: []byte(`{"TCP": {"443": {"HTTPS": true}, "4443": {"HTTPS": true}},
		"Web": {"box.tail0000.ts.net:443": {"Handlers": {"/": {"Proxy": "http://127.0.0.1:3000"}}},
		        "box.tail0000.ts.net:4443": {"Handlers": {"/": {"Proxy": "http://127.0.0.1:7654"}}}},
		"AllowFunnel": {"box.tail0000.ts.net:8443": true}}`)}
	got, err := read.ReviewDigestWithout("box.tail0000.ts.net", 4443, 443)
	if err != nil {
		t.Fatal(err)
	}
	after := `{"AllowFunnel":{"box.tail0000.ts.net:8443":true},"TCP":{"443":{"HTTPS":true}},"Web":{"box.tail0000.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:3000"}}}}}`
	if got != digest(after) {
		t.Error("the digest differs from the configuration without the endpoint")
	}
	for _, other := range []string{
		`{"AllowFunnel":{"box.tail0000.ts.net:8443":true,"box.tail0000.ts.net:5000":true},"TCP":{"443":{"HTTPS":true}},"Web":{"box.tail0000.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:3000"}}}}}`,
		`{"AllowFunnel":{"box.tail0000.ts.net:8443":true},"TCP":{"443":{"HTTPS":true}},"Web":{"box.tail0000.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:3001"}}}}}`,
	} {
		if got == digest(other) {
			t.Errorf("another change kept the digest: %s", other)
		}
	}
}

// The review digest changes with anything in the configuration, on any
// port, and with the port reviewed, but not with key order or spacing.
func TestReviewDigestCoversTheWholeConfiguration(t *testing.T) {
	digest := func(content string, port int) string {
		t.Helper()
		value, err := ServeConfig{content: []byte(content)}.ReviewDigest(port)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	base := `{"TCP": {"443": {"HTTPS": true}}, "Web": {"box.tail0000.ts.net:443": {"Handlers": {"/": {"Proxy": "http://127.0.0.1:3000"}}}}}`
	want := digest(base, 443)
	if got := digest(`{"Web":{"box.tail0000.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:3000"}}}},"TCP":{"443":{"HTTPS":true}}}`, 443); got != want {
		t.Error("key order or spacing changed the digest")
	}
	if digest(base, 8443) == want {
		t.Error("another port kept the digest")
	}
	for _, content := range []string{
		`{"TCP": {"443": {"HTTPS": true}, "5000": {"HTTPS": true}}, "Web": {"box.tail0000.ts.net:443": {"Handlers": {"/": {"Proxy": "http://127.0.0.1:3000"}}}}}`,
		`{"TCP": {"443": {"HTTPS": true}}, "Web": {"box.tail0000.ts.net:443": {"Handlers": {"/": {"Proxy": "http://127.0.0.1:3000"}}}}, "AllowFunnel": {"box.tail0000.ts.net:5000": true}}`,
		`{"TCP": {"443": {"HTTPS": true}}, "Web": {"box.tail0000.ts.net:443": {"Handlers": {"/": {"Proxy": "http://127.0.0.1:3000"}}}}, "ETag": 1}`,
		`null`,
	} {
		if digest(content, 443) == want {
			t.Errorf("a change kept the digest: %s", content)
		}
	}
}
