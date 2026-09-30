package importgit

import (
	"errors"
	"strings"
	"testing"
)

// v2Capabilities is a protocol v2 capability advertisement as GitHub sends
// it, with the service announcement, and as git-http-backend sends it,
// without.
func v2Capabilities(announced bool, lines ...string) string {
	body := ""
	if announced {
		body = pkt("# service=git-upload-pack") + flush
	}
	body += pkt("version 2")
	for _, line := range lines {
		body += pkt(line)
	}
	return body + flush
}

var githubCapabilities = []string{"agent=git/github-24808ea0b646-Linux", "ls-refs=unborn", "fetch=shallow wait-for-done filter", "server-option", "object-format=sha1"}

func parseV2(t *testing.T, body string) *Advertisement {
	t.Helper()
	result, err := parseString(t, body, Options{ProtocolV2: true})
	if err != nil {
		t.Fatalf("parse v2 capabilities: %v", err)
	}
	return result
}

// Both forms of a v2 capability advertisement are read, capability values
// may hold spaces, and no refs come with it.
func TestParseV2CapabilityAdvertisement(t *testing.T) {
	for _, announced := range []bool{true, false} {
		result := parseV2(t, v2Capabilities(announced, githubCapabilities...))
		if result.ProtocolVersion != 2 || result.ObjectFormat != FormatSHA1 || !result.ObjectFormatAdvertised ||
			len(result.Refs) != 0 || result.Empty || strings.Join(result.Capabilities, "|") != strings.Join(githubCapabilities, "|") {
			t.Fatalf("announced=%v: %+v", announced, result)
		}
	}
	sha256 := parseV2(t, v2Capabilities(false, "ls-refs", "fetch", "object-format=sha256"))
	if sha256.ObjectFormat != FormatSHA256 {
		t.Fatalf("object format %q", sha256.ObjectFormat)
	}
	// Without the option a v2 answer stays unsupported, as before.
	wantError(t, v2Capabilities(true, githubCapabilities...), Options{}, ErrUnsupportedVersion)
	wantError(t, v2Capabilities(false, githubCapabilities...), Options{}, ErrUnsupportedVersion)
	// A v0 answer to a v2 request is read as v0.
	v0 := advertisement("", pkt(sha1A+" refs/heads/main\x00ofs-delta"))
	if result, err := parseString(t, v0, Options{ProtocolV2: true}); err != nil || result.ProtocolVersion != 0 || len(result.Refs) != 1 {
		t.Fatalf("v0 answer: %+v %v", result, err)
	}
}

func TestParseV2CapabilityAdvertisementRefusals(t *testing.T) {
	options := Options{ProtocolV2: true}
	for _, test := range []struct {
		name   string
		body   string
		target error
	}{
		{"no ls-refs", v2Capabilities(false, "fetch"), ErrInvalidCapability},
		{"no fetch", v2Capabilities(false, "ls-refs"), ErrInvalidCapability},
		{"repeated", v2Capabilities(false, "ls-refs", "fetch", "fetch"), ErrInvalidCapability},
		{"bad key", v2Capabilities(false, "ls-refs", "fetch", "Bad"), ErrInvalidCapability},
		{"empty value", v2Capabilities(false, "ls-refs", "fetch", "agent="), ErrInvalidCapability},
		{"control byte", v2Capabilities(false, "ls-refs", "fetch", "agent=a\x01b"), ErrInvalidCapability},
		{"object format", v2Capabilities(false, "ls-refs", "fetch", "object-format=md5"), ErrObjectFormat},
		{"no flush", pkt("version 2") + pkt("ls-refs") + pkt("fetch"), ErrTruncated},
		{"delimiter", pkt("version 2") + pkt("ls-refs") + "0001" + flush, ErrMalformedPacket},
		{"trailing", v2Capabilities(false, "ls-refs", "fetch") + pkt("extra"), ErrTrailingContent},
		{"remote error", pkt("version 2") + pkt("ERR no"), ErrRemoteError},
	} {
		t.Run(test.name, func(t *testing.T) { wantError(t, test.body, options, test.target) })
	}
	many := []string{"ls-refs", "fetch"}
	for i := 0; i < 3; i++ {
		many = append(many, "x"+string(rune('a'+i)))
	}
	wantError(t, v2Capabilities(false, many...), Options{ProtocolV2: true, Limits: Limits{MaxCapabilities: 4}}, ErrLimitExceeded)
	wantError(t, v2Capabilities(false, "ls-refs", "fetch", "agent="+strings.Repeat("a", 40)), Options{ProtocolV2: true, Limits: Limits{MaxCapabilityBytes: 32}}, ErrLimitExceeded)
}

func lsRefs(t *testing.T, capabilities *Advertisement, options Options, records ...string) (*Advertisement, error) {
	t.Helper()
	return lsRefsWith(t, capabilities, nil, options, records...)
}

func lsRefsWith(t *testing.T, capabilities *Advertisement, extra []string, options Options, records ...string) (*Advertisement, error) {
	t.Helper()
	prefixes, err := LsRefsPrefixes(extra)
	if err != nil {
		t.Fatal(err)
	}
	body := ""
	for _, record := range records {
		body += pkt(record)
	}
	return ParseLsRefs(strings.NewReader(body+flush), capabilities, prefixes, options)
}

// An ls-refs answer describes the source as a v0 advertisement does: refs,
// HEAD and its symref, and peeled tags.
func TestParseLsRefs(t *testing.T) {
	capabilities := parseV2(t, v2Capabilities(true, githubCapabilities...))
	result, err := lsRefs(t, capabilities, Options{},
		upper1A+" HEAD symref-target:refs/heads/main",
		upper1A+" refs/heads/main",
		sha1B+" refs/tags/v1.0 peeled:"+sha1C,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.ProtocolVersion != 2 || result.Empty || len(result.Refs) != 3 ||
		result.Refs[0] != (Ref{Name: "HEAD", OID: lower1A, SymrefTarget: "refs/heads/main"}) ||
		result.Refs[1] != (Ref{Name: "refs/heads/main", OID: lower1A}) ||
		result.Refs[2] != (Ref{Name: "refs/tags/v1.0", OID: sha1B, PeeledOID: sha1C}) ||
		result.Head != (Head{Advertised: true, OID: lower1A, SymrefTarget: "refs/heads/main"}) ||
		len(result.Symrefs) != 1 || result.Symrefs[0] != (Symref{Name: "HEAD", Target: "refs/heads/main"}) {
		t.Fatalf("ls-refs: %+v", result)
	}
	empty, err := lsRefs(t, capabilities, Options{})
	if err != nil || !empty.Empty || len(empty.Refs) != 0 {
		t.Fatalf("empty ls-refs: %+v %v", empty, err)
	}
	sha256 := parseV2(t, v2Capabilities(false, "ls-refs", "fetch", "object-format=sha256"))
	if result, err := lsRefs(t, sha256, Options{}, sha2A+" refs/heads/main"); err != nil || result.ObjectFormat != FormatSHA256 {
		t.Fatalf("sha256 ls-refs: %+v %v", result, err)
	}
}

func TestParseLsRefsRefusals(t *testing.T) {
	capabilities := parseV2(t, v2Capabilities(true, githubCapabilities...))
	for _, test := range []struct {
		name    string
		records []string
		target  error
	}{
		{"duplicate", []string{sha1A + " refs/heads/main", sha1B + " refs/heads/main"}, ErrConflictingRefs},
		{"zero", []string{zero1 + " refs/heads/main"}, ErrInvalidObjectID},
		{"sha256 width", []string{sha2A + " refs/heads/main"}, ErrObjectFormat},
		{"unborn", []string{"unborn HEAD symref-target:refs/heads/main"}, ErrInvalidRecord},
		{"unrequested ref with the zero ID", []string{zero1 + " refs/pull/1/head"}, ErrInvalidObjectID},
		{"unrequested ref with a bad name", []string{sha1A + " refs/pull/1..2/head"}, ErrInvalidName},
		{"unrequested ref with a bad attribute", []string{sha1A + " refs/pull/1/head peeled:1234"}, ErrInvalidObjectID},
		{"unrequested ref with an unknown attribute", []string{sha1A + " refs/pull/1/head other:x"}, ErrInvalidRecord},
		{"unrequested ref repeated", []string{sha1A + " refs/pull/1/head", sha1B + " refs/pull/1/head"}, ErrConflictingRefs},
		{"no name", []string{sha1A}, ErrInvalidRecord},
		{"bad name", []string{sha1A + " refs/heads/a..b"}, ErrInvalidName},
		{"unrequested attribute", []string{sha1A + " refs/heads/main other:x"}, ErrInvalidRecord},
		{"two peeled", []string{sha1A + " refs/tags/v peeled:" + sha1B + " peeled:" + sha1C}, ErrConflictingRefs},
		{"zero peeled", []string{sha1A + " refs/tags/v peeled:" + zero1}, ErrInvalidObjectID},
		{"short peeled", []string{sha1A + " refs/tags/v peeled:1234"}, ErrInvalidObjectID},
		{"bad symref", []string{sha1A + " HEAD symref-target:main"}, ErrInvalidName},
		{"two symrefs", []string{sha1A + " HEAD symref-target:refs/heads/a symref-target:refs/heads/b"}, ErrConflictingRefs},
		{"symref disagrees", []string{sha1A + " HEAD symref-target:refs/heads/main", sha1B + " refs/heads/main"}, ErrConflictingRefs},
		{"remote error", []string{"ERR denied"}, ErrRemoteError},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := lsRefs(t, capabilities, Options{}, test.records...); !errors.Is(err, test.target) {
				t.Fatalf("got %v, want %v", err, test.target)
			}
		})
	}
	// Every record counts once toward the record limit.
	if _, err := lsRefs(t, capabilities, Options{Limits: Limits{MaxRefRecords: 2}},
		sha1A+" refs/heads/a", sha1A+" refs/heads/b", sha1A+" refs/heads/c"); !errors.Is(err, ErrTooManyRefs) {
		t.Fatalf("record limit: %v", err)
	}
	if _, err := lsRefs(t, capabilities, Options{Limits: Limits{MaxRefRecords: 3}},
		sha1A+" refs/heads/a", sha1A+" refs/heads/b", sha1A+" refs/tags/c peeled:"+sha1B); err != nil {
		t.Fatalf("at the record limit: %v", err)
	}
	if _, err := ParseLsRefs(strings.NewReader(pkt(sha1A+" refs/heads/main")), capabilities, []string{"HEAD"}, Options{}); !errors.Is(err, ErrTruncated) {
		t.Fatalf("no flush: %v", err)
	}
	if _, err := ParseLsRefs(strings.NewReader(pkt(sha1A+" refs/heads/main")+flush+"x"), capabilities, []string{"HEAD"}, Options{}); !errors.Is(err, ErrTrailingContent) {
		t.Fatalf("trailing: %v", err)
	}
	if _, err := ParseLsRefs(strings.NewReader(flush), &Advertisement{ProtocolVersion: 0}, []string{"HEAD"}, Options{}); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("v0 capabilities: %v", err)
	}
	long := Options{Limits: Limits{MaxTotalBytes: 64}}
	if _, err := lsRefs(t, capabilities, long, sha1A+" refs/heads/a", sha1A+" refs/heads/b"); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("total bytes: %v", err)
	}
}

// ref-prefix only narrows the answer, so a server may list other refs. They
// are validated and counted like any other record, then left out.
func TestParseLsRefsLeavesOutUnrequestedRefs(t *testing.T) {
	capabilities := parseV2(t, v2Capabilities(true, githubCapabilities...))
	records := []string{
		sha1A + " HEAD symref-target:refs/heads/main",
		sha1A + " refs/heads/main",
		sha1B + " refs/pull/1/head",
		sha1C + " refs/notes/commits symref-target:refs/heads/main",
		sha1B + " refs/headsx",
	}
	result, err := lsRefs(t, capabilities, Options{}, records...)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Refs) != 2 || result.Refs[0].Name != "HEAD" || result.Refs[1].Name != "refs/heads/main" ||
		len(result.Symrefs) != 1 || result.Empty {
		t.Fatalf("ls-refs = %+v", result)
	}
	if _, err := lsRefs(t, capabilities, Options{Limits: Limits{MaxRefRecords: 4}}, records...); !errors.Is(err, ErrTooManyRefs) {
		t.Fatalf("unrequested refs are not counted: %v", err)
	}
	if only, err := lsRefs(t, capabilities, Options{}, sha1B+" refs/pull/1/head"); err != nil || !only.Empty {
		t.Fatalf("only unrequested refs: %+v, %v", only, err)
	}
}

// An extra namespace is listed with branches and tags; a name that only
// starts with the same letters is not in it.
func TestParseLsRefsKeepsExtraNamespaces(t *testing.T) {
	capabilities := parseV2(t, v2Capabilities(true, githubCapabilities...))
	result, err := lsRefsWith(t, capabilities, []string{"refs/notes/"}, Options{},
		sha1A+" refs/heads/main", sha1B+" refs/notes/commits", sha1C+" refs/notesx/commits", sha1B+" refs/pull/1/head")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Refs) != 2 || result.Refs[0].Name != "refs/heads/main" || result.Refs[1].Name != "refs/notes/commits" {
		t.Fatalf("ls-refs = %+v", result.Refs)
	}
	for _, prefix := range []string{"refs/notes", "notes/", "refs/no tes/", "HEAD/", "refs//"} {
		if _, err := LsRefsPrefixes([]string{prefix}); !errors.Is(err, ErrInvalidOptions) {
			t.Fatalf("extra namespace %q: %v", prefix, err)
		}
	}
}
