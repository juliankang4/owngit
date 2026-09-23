package importgit

import (
	"errors"
	"io"
	"strings"
	"testing"
)

const (
	sha1A = "1111111111111111111111111111111111111111"
	sha1B = "2222222222222222222222222222222222222222"
	sha1C = "3333333333333333333333333333333333333333"
	zero1 = "0000000000000000000000000000000000000000"
	sha2A = "1111111111111111111111111111111111111111111111111111111111111111"
	sha2B = "2222222222222222222222222222222222222222222222222222222222222222"
	zero2 = "0000000000000000000000000000000000000000000000000000000000000000"

	// Uppercase and mixed-case spellings of the identifiers above. They name
	// the same objects, because the protocol requires receivers to compare
	// object IDs case-insensitively.
	lower1A = "abcdef0123456789abcdef0123456789abcdef01"
	upper1A = "ABCDEF0123456789ABCDEF0123456789ABCDEF01"
	mixed1A = "AbCdEf0123456789abcdef0123456789ABCDEF01"
	lower1B = "fedcba9876543210fedcba9876543210fedcba98"
	upper1B = "FEDCBA9876543210FEDCBA9876543210FEDCBA98"
	lower2A = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	upper2A = "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789"
)

// pkt frames one data packet the way a server would, with the trailing LF that
// gitprotocol-common(5) says a non-binary line should carry.
func pkt(payload string) string {
	return pktRaw(payload + "\n")
}

// pktRaw frames exactly the supplied bytes, so a test can omit the LF or send
// a deliberately odd payload.
func pktRaw(payload string) string {
	const digits = "0123456789abcdef"
	length := len(payload) + packetLengthBytes
	return string([]byte{
		digits[(length>>12)&0xf], digits[(length>>8)&0xf],
		digits[(length>>4)&0xf], digits[length&0xf],
	}) + payload
}

const flush = "0000"

// advertisement assembles a complete v1 smart reply around the supplied ref
// list packets.
func advertisement(version string, records ...string) string {
	body := pkt("# service=git-upload-pack") + flush
	if version != "" {
		body += pkt(version)
	}
	return body + strings.Join(records, "") + flush
}

func parseString(t *testing.T, body string, options Options) (*Advertisement, error) {
	t.Helper()
	return Parse(strings.NewReader(body), options)
}

func mustParse(t *testing.T, body string) *Advertisement {
	t.Helper()
	result, err := parseString(t, body, Options{})
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	return result
}

// wantError asserts the typed failure a caller would branch on.
func wantError(t *testing.T, body string, options Options, target error) *ParseError {
	t.Helper()
	result, err := parseString(t, body, options)
	if err == nil {
		t.Fatalf("expected %v, got a successful parse of %d refs", target, len(result.Refs))
	}
	if !errors.Is(err, target) {
		t.Fatalf("expected %v, got %v", target, err)
	}
	var parseError *ParseError
	if errors.As(err, &parseError) {
		return parseError
	}
	return nil
}

func TestParseNormalV1Advertisement(t *testing.T) {
	body := advertisement("version 1",
		pkt(sha1A+" HEAD\x00multi_ack thin-pack side-band-64k ofs-delta symref=HEAD:refs/heads/main agent=git/2.54.0"),
		pkt(sha1A+" refs/heads/main"),
		pkt(sha1B+" refs/tags/v1.0"),
		pkt(sha1C+" refs/tags/v1.0^{}"),
	)
	result := mustParse(t, body)
	if result.ProtocolVersion != 1 {
		t.Fatalf("protocol version = %d, want 1", result.ProtocolVersion)
	}
	if result.Empty {
		t.Fatal("a populated advertisement must not be reported as empty")
	}
	if result.ObjectFormat != FormatSHA1 || result.ObjectFormatAdvertised {
		t.Fatalf("object format = %q advertised=%v, want the documented sha1 default",
			result.ObjectFormat, result.ObjectFormatAdvertised)
	}
	if len(result.Refs) != 3 {
		t.Fatalf("refs = %d, want 3 (the peeled record folds into its tag)", len(result.Refs))
	}
	if result.Refs[0].Name != "HEAD" || result.Refs[0].OID != sha1A {
		t.Fatalf("first ref = %+v", result.Refs[0])
	}
	if result.Refs[0].SymrefTarget != "refs/heads/main" {
		t.Fatalf("HEAD symref target = %q", result.Refs[0].SymrefTarget)
	}
	tag := result.Refs[2]
	if tag.Name != "refs/tags/v1.0" || tag.OID != sha1B || tag.PeeledOID != sha1C || !tag.Peeled() {
		t.Fatalf("annotated tag = %+v", tag)
	}
	if result.Refs[1].Peeled() {
		t.Fatal("an unpeeled branch must not report a peeled value")
	}
	if !result.Head.Advertised || result.Head.OID != sha1A || result.Head.SymrefTarget != "refs/heads/main" {
		t.Fatalf("head = %+v", result.Head)
	}
	if len(result.Symrefs) != 1 || result.Symrefs[0] != (Symref{Name: "HEAD", Target: "refs/heads/main"}) {
		t.Fatalf("symrefs = %+v", result.Symrefs)
	}
	if got := strings.Join(result.Capabilities, " "); got !=
		"multi_ack thin-pack side-band-64k ofs-delta symref=HEAD:refs/heads/main agent=git/2.54.0" {
		t.Fatalf("capabilities = %q", got)
	}
	if result.TotalBytes != int64(len(body)) {
		t.Fatalf("total bytes = %d, want %d", result.TotalBytes, len(body))
	}
}

func TestParseProtocolV0WithoutVersionPacket(t *testing.T) {
	result := mustParse(t, advertisement("",
		pkt(sha1A+" HEAD\x00ofs-delta"),
		pkt(sha1A+" refs/heads/main"),
	))
	if result.ProtocolVersion != 0 {
		t.Fatalf("protocol version = %d, want 0 when no version packet is sent", result.ProtocolVersion)
	}
	if len(result.Refs) != 2 {
		t.Fatalf("refs = %d, want 2", len(result.Refs))
	}
}

func TestParseAcceptsMissingTrailingLF(t *testing.T) {
	// gitprotocol-common(5) requires receivers not to complain about a missing LF.
	result := mustParse(t, pktRaw("# service=git-upload-pack")+flush+
		pktRaw("version 1")+
		pktRaw(sha1A+" refs/heads/main\x00ofs-delta")+flush)
	if len(result.Refs) != 1 || result.Refs[0].Name != "refs/heads/main" {
		t.Fatalf("refs = %+v", result.Refs)
	}
}

func TestParseEmptyRepositorySentinel(t *testing.T) {
	result := mustParse(t, advertisement("version 1",
		pkt(zero1+" capabilities^{}\x00multi_ack_detailed symref=HEAD:refs/heads/main agent=git/2.54.0"),
	))
	if !result.Empty {
		t.Fatal("the zero-id capabilities record is a genuine empty advertisement")
	}
	if len(result.Refs) != 0 {
		t.Fatalf("an empty advertisement has no refs, got %+v", result.Refs)
	}
	if result.Head.Advertised || result.Head.OID != "" {
		t.Fatalf("an empty advertisement must not invent HEAD: %+v", result.Head)
	}
	// An unborn HEAD is a statement of intent, not proof the branch exists.
	if result.Head.SymrefTarget != "refs/heads/main" {
		t.Fatalf("unborn symref target = %q", result.Head.SymrefTarget)
	}
}

func TestParseEmptyRepositoryWithoutSymref(t *testing.T) {
	result := mustParse(t, advertisement("version 1", pkt(zero1+" capabilities^{}\x00ofs-delta")))
	if !result.Empty {
		t.Fatal("expected an empty advertisement")
	}
	if result.Head.SymrefTarget != "" || result.Head.Advertised {
		t.Fatalf("no HEAD facts may be invented: %+v", result.Head)
	}
}

func TestParseSHA256Advertisement(t *testing.T) {
	result := mustParse(t, advertisement("version 1",
		pkt(sha2A+" HEAD\x00object-format=sha256 symref=HEAD:refs/heads/main"),
		pkt(sha2A+" refs/heads/main"),
		pkt(sha2B+" refs/tags/v2"),
	))
	if result.ObjectFormat != FormatSHA256 || !result.ObjectFormatAdvertised {
		t.Fatalf("object format = %q advertised=%v", result.ObjectFormat, result.ObjectFormatAdvertised)
	}
	if len(result.Refs) != 3 || result.Refs[2].OID != sha2B {
		t.Fatalf("refs = %+v", result.Refs)
	}
}

func TestParseSHA256EmptySentinel(t *testing.T) {
	result := mustParse(t, advertisement("version 1",
		pkt(zero2+" capabilities^{}\x00object-format=sha256"),
	))
	if !result.Empty || result.ObjectFormat != FormatSHA256 {
		t.Fatalf("empty=%v format=%q", result.Empty, result.ObjectFormat)
	}
}

func TestParseUsesFirstObjectFormatValue(t *testing.T) {
	// gitprotocol-capabilities(5): when repeated, the first value is the one
	// used in the ref advertisement.
	result := mustParse(t, advertisement("version 1",
		pkt(sha1A+" HEAD\x00object-format=sha1 object-format=sha256"),
	))
	if result.ObjectFormat != FormatSHA1 {
		t.Fatalf("object format = %q, want the first advertised value", result.ObjectFormat)
	}
}

func TestParseDetachedAndAbsentHead(t *testing.T) {
	detached := mustParse(t, advertisement("version 1",
		pkt(sha1A+" HEAD\x00ofs-delta"),
		pkt(sha1B+" refs/heads/main"),
	))
	if !detached.Head.Advertised || detached.Head.OID != sha1A {
		t.Fatalf("detached head = %+v", detached.Head)
	}
	if detached.Head.SymrefTarget != "" {
		t.Fatal("a detached HEAD must not be given a symref target")
	}

	absent := mustParse(t, advertisement("version 1",
		pkt(sha1B+" refs/heads/main\x00ofs-delta"),
	))
	if absent.Head.Advertised || absent.Head.OID != "" || absent.Head.SymrefTarget != "" {
		t.Fatalf("an absent HEAD must stay absent: %+v", absent.Head)
	}
}

func TestParsePeeledHead(t *testing.T) {
	result := mustParse(t, advertisement("version 1",
		pkt(sha1A+" HEAD\x00ofs-delta"),
		pkt(sha1B+" HEAD^{}"),
	))
	if result.Head.PeeledOID != sha1B || result.Refs[0].PeeledOID != sha1B {
		t.Fatalf("peeled head = %+v", result.Head)
	}
}

func TestParsePreservesOtherNamespaces(t *testing.T) {
	// Any refs/ namespace is preserved as a fact. Accepting it here is not
	// permission to publish it.
	result := mustParse(t, advertisement("version 1",
		pkt(sha1A+" HEAD\x00ofs-delta"),
		pkt(sha1A+" refs/pull/7/head"),
		pkt(sha1B+" refs/notes/commits"),
		pkt(sha1C+" refs/owngit/retained/heads/main"),
	))
	names := make([]string, 0, len(result.Refs))
	for _, ref := range result.Refs {
		names = append(names, ref.Name)
	}
	want := "HEAD refs/pull/7/head refs/notes/commits refs/owngit/retained/heads/main"
	if got := strings.Join(names, " "); got != want {
		t.Fatalf("names = %q, want %q", got, want)
	}
}

func TestParseKeepsSymrefTargetsAbsentFromTheRefList(t *testing.T) {
	// A hidden or unborn target is a legitimate statement about a ref this
	// response does not carry, so it must survive the consistency check.
	unborn := mustParse(t, advertisement("version 1",
		pkt(zero1+" capabilities^{}\x00symref=HEAD:refs/heads/main"),
	))
	if !unborn.Empty || unborn.Head.SymrefTarget != "refs/heads/main" {
		t.Fatalf("unborn = empty:%v head:%+v", unborn.Empty, unborn.Head)
	}

	hidden := mustParse(t, advertisement("version 1",
		pkt(sha1A+" HEAD\x00symref=HEAD:refs/heads/hidden"),
		pkt(sha1B+" refs/heads/other"),
	))
	if hidden.Head.SymrefTarget != "refs/heads/hidden" {
		t.Fatalf("hidden target was dropped: %+v", hidden.Head)
	}
	for _, ref := range hidden.Refs {
		if ref.Name == "refs/heads/hidden" {
			t.Fatal("a hidden symref target must not become an advertised ref")
		}
	}
}

func TestParseSymrefForNonHeadRef(t *testing.T) {
	result := mustParse(t, advertisement("version 1",
		pkt(sha1A+" HEAD\x00symref=HEAD:refs/heads/main symref=refs/remotes/origin/HEAD:refs/remotes/origin/main"),
		pkt(sha1A+" refs/heads/main"),
		pkt(sha1B+" refs/remotes/origin/HEAD"),
	))
	if len(result.Symrefs) != 2 {
		t.Fatalf("symrefs = %+v", result.Symrefs)
	}
	if result.Refs[2].SymrefTarget != "refs/remotes/origin/main" {
		t.Fatalf("non-HEAD symref not applied: %+v", result.Refs[2])
	}
	// The symref target itself was not advertised, and must not be invented.
	for _, ref := range result.Refs {
		if ref.Name == "refs/remotes/origin/main" {
			t.Fatal("a symref target must not become an advertised ref")
		}
	}
}

func TestParseAcceptsUppercaseObjectIDs(t *testing.T) {
	// gitprotocol-pack(5): both peers "MUST treat obj-id as case-insensitive".
	// Git's own receiver does, so refusing an uppercase advertisement would
	// reject a source real Git reads.
	result := mustParse(t, advertisement("version 1",
		pkt(upper1A+" HEAD\x00ofs-delta"),
		pkt(mixed1A+" refs/heads/main"),
		pkt(upper1B+" refs/tags/v1"),
		pkt(mixed1A+" refs/tags/v1^{}"),
	))
	if result.Head.OID != lower1A {
		t.Fatalf("HEAD OID = %q, want the canonical %q", result.Head.OID, lower1A)
	}
	if result.Refs[1].OID != lower1A {
		t.Fatalf("mixed-case ref OID = %q, want %q", result.Refs[1].OID, lower1A)
	}
	tag := result.Refs[2]
	if tag.OID != lower1B || tag.PeeledOID != lower1A {
		t.Fatalf("tag = %+v, want canonical %q/%q", tag, lower1B, lower1A)
	}
	// Ref names keep the case the server advertised; only object IDs fold.
	if result.Refs[2].Name != "refs/tags/v1" {
		t.Fatalf("name = %q", result.Refs[2].Name)
	}
}

func TestParsePreservesRefNameCase(t *testing.T) {
	// The case rule applies to object IDs only. A ref name differing only by
	// case is a different ref and must survive verbatim.
	result := mustParse(t, advertisement("version 1",
		pkt(upper1A+" refs/heads/Main\x00ofs-delta"),
		pkt(upper1B+" refs/heads/main"),
		pkt(sha1C+" refs/tags/RELEASE-1.0"),
	))
	want := []string{"refs/heads/Main", "refs/heads/main", "refs/tags/RELEASE-1.0"}
	for index, name := range want {
		if result.Refs[index].Name != name {
			t.Fatalf("ref %d name = %q, want %q", index, result.Refs[index].Name, name)
		}
	}
}

func TestParseAcceptsUppercaseSHA256ObjectIDs(t *testing.T) {
	result := mustParse(t, advertisement("version 1",
		pkt(upper2A+" HEAD\x00object-format=sha256"),
		pkt(upper2A+" refs/heads/main"),
	))
	if result.ObjectFormat != FormatSHA256 {
		t.Fatalf("object format = %q", result.ObjectFormat)
	}
	for _, ref := range result.Refs {
		if ref.OID != lower2A {
			t.Fatalf("ref %q OID = %q, want the canonical %q", ref.Name, ref.OID, lower2A)
		}
	}
}

func TestParseComparesSymrefObjectIDsCaseInsensitively(t *testing.T) {
	// HEAD and its target name the same object in different cases, so the
	// consistency check must agree rather than report a contradiction.
	agreeing := mustParse(t, advertisement("version 1",
		pkt(upper1A+" HEAD\x00symref=HEAD:refs/heads/main"),
		pkt(lower1A+" refs/heads/main"),
	))
	if agreeing.Head.SymrefTarget != "refs/heads/main" || agreeing.Head.OID != lower1A {
		t.Fatalf("head = %+v", agreeing.Head)
	}

	mixedBoth := mustParse(t, advertisement("version 1",
		pkt(mixed1A+" HEAD\x00symref=HEAD:refs/heads/main"),
		pkt(upper1A+" refs/heads/main"),
	))
	if mixedBoth.Refs[1].OID != lower1A {
		t.Fatalf("target OID = %q", mixedBoth.Refs[1].OID)
	}
}

func TestParseRejectsInvalidNames(t *testing.T) {
	cases := map[string]string{
		"not rooted at refs":    "heads/main",
		"bare refs prefix":      "refs/",
		"trailing slash":        "refs/heads/main/",
		"empty component":       "refs/heads//main",
		"leading dot component": "refs/heads/.hidden",
		"double dot":            "refs/heads/a..b",
		"lock suffix":           "refs/heads/main.lock",
		"trailing dot":          "refs/heads/main.",
		"reflog sequence":       "refs/heads/main@{0}",
		"caret":                 "refs/heads/ma^in",
		"colon":                 "refs/heads/ma:in",
		"tilde":                 "refs/heads/ma~in",
		"question mark":         "refs/heads/ma?in",
		"asterisk":              "refs/heads/ma*in",
		"open bracket":          "refs/heads/ma[in",
		"backslash":             "refs/heads/ma\\in",
		"control character":     "refs/heads/ma\x01in",
		"delete character":      "refs/heads/ma\x7fin",
		"invalid utf-8":         "refs/heads/ma\xffin",
		"lowercase head":        "head",
	}
	for name, refName := range cases {
		t.Run(name, func(t *testing.T) {
			// A space would split the record instead, so it is covered by the
			// record-structure rows of TestParseRejections.
			wantError(t, advertisement("version 1", pkt(sha1A+" "+refName+"\x00ofs-delta")), Options{}, ErrInvalidName)
		})
	}
}

func TestParseAcceptsUnusualButValidNames(t *testing.T) {
	for _, refName := range []string{
		"refs/heads/main",
		"refs/heads/feature/a.b",
		"refs/heads/ünïcode",
		"refs/heads/-dash",
		"refs/heads/main.locked",
		"refs/heads/@",
		"refs/x",
	} {
		t.Run(refName, func(t *testing.T) {
			result := mustParse(t, advertisement("version 1", pkt(sha1A+" "+refName+"\x00ofs-delta")))
			if len(result.Refs) != 1 || result.Refs[0].Name != refName {
				t.Fatalf("refs = %+v, want the exact advertised name", result.Refs)
			}
		})
	}
}

func TestParseAcceptsWellFormedCapabilities(t *testing.T) {
	// An unknown but well-formed capability is not evidence of protocol v2.
	result := mustParse(t, advertisement("version 1",
		pkt(sha1A+" HEAD\x00ofs-delta future-thing future-thing-2=value ls-refs fetch"),
	))
	if len(result.Capabilities) != 5 {
		t.Fatalf("capabilities = %+v", result.Capabilities)
	}
	if result.ProtocolVersion != 1 {
		t.Fatalf("protocol version = %d", result.ProtocolVersion)
	}
	// cap-list needs one capability, and one is enough.
	if result := mustParse(t, advertisement("version 1", pkt(zero1+" capabilities^{}\x00a"))); !result.Empty {
		t.Fatal("a single capability must satisfy the list requirement")
	}
	// The shallow capability only offers the feature; it is not a truncated
	// history and must stay parseable.
	capabilityOnly := advertisement("version 1",
		pkt(sha1A+" HEAD\x00shallow deepen-since ofs-delta"),
		pkt(sha1A+" refs/heads/main"),
	)
	if result := mustParse(t, capabilityOnly); len(result.Refs) != 2 {
		t.Fatalf("refs = %+v", result.Refs)
	}
}

func TestParseReportsServerErrorPacket(t *testing.T) {
	inList := pkt("# service=git-upload-pack") + flush + pkt("ERR access denied\x07") + flush
	failure := wantError(t, inList, Options{}, ErrRemoteError)
	if failure == nil || !strings.Contains(failure.Detail, "access denied?") {
		t.Fatalf("remote text was not sanitized: %+v", failure)
	}

	first := pkt("ERR repository not found") + flush
	wantError(t, first, Options{}, ErrRemoteError)

	long := pkt("ERR "+strings.Repeat("a", 500)) + flush
	failure = wantError(t, long, Options{}, ErrRemoteError)
	if failure == nil || !strings.HasSuffix(failure.Detail, "...") {
		t.Fatalf("long remote text was not bounded: %+v", failure)
	}
}

// TestParseRejections lists every refusal with the typed error a caller
// branches on. Each refusal must be a *ParseError, and only a valid shallow
// boundary may assert ErrIncompleteHistory, because that error claims the
// source really is shallow.
func TestParseRejections(t *testing.T) {
	v1 := func(records ...string) string { return advertisement("version 1", records...) }
	service := pkt("# service=git-upload-pack") + flush
	head := pkt(sha1A + " HEAD\x00ofs-delta")
	main := pkt(sha1A + " refs/heads/main")
	sentinel := pkt(zero1 + " capabilities^{}\x00ofs-delta")
	tag := pkt(sha1A + " refs/tags/v1\x00ofs-delta")
	// A dumb HTTP server answers with plain text whose leading hex digits read
	// as a pkt-len of 0x1111.
	dumb := sha1A + "\trefs/heads/main\n" + sha1B + "\trefs/tags/v1.0\n"

	cases := []struct {
		name, body string
		want       error
		detail     string // required substring of ParseError.Detail
	}{
		// Emptiness is only the zero-ID sentinel. A v2 reply or a cut-off list
		// yields zero refs from the Git CLI too, but must stay distinct here.
		{"v2 first packet is not empty", pkt("version 2") + pkt("agent=git/2.54.0") + pkt("ls-refs=unborn") + pkt("object-format=sha1") + flush, ErrUnsupportedVersion, ""},
		{"v2 after service is not empty", service + pkt("version 2") + pkt("ls-refs") + flush, ErrUnsupportedVersion, ""},
		{"list without flush is not empty", service + pkt("version 1"), ErrTruncated, ""},
		// An empty repository still sends the capabilities record.
		{"v1 list without records", v1(), ErrInvalidRecord, ""},
		{"v0 list without records", advertisement(""), ErrInvalidRecord, ""},

		{"sha1 ID in sha256 advertisement", v1(pkt(sha1A + " HEAD\x00object-format=sha256")), ErrObjectFormat, ""},
		{"sha256 ID in sha1 advertisement", v1(head, pkt(sha2B+" refs/heads/main")), ErrObjectFormat, ""},
		{"sha256 sentinel in sha1 advertisement", v1(pkt(zero2 + " capabilities^{}\x00ofs-delta")), ErrObjectFormat, ""},
		{"unknown object format", v1(pkt(sha1A + " HEAD\x00object-format=sha3-256")), ErrObjectFormat, ""},
		{"object format without value", v1(pkt(sha1A + " HEAD\x00object-format")), ErrObjectFormat, ""},

		// An advertised symref and its advertised target name the same object;
		// the caller must not guess which differing ID is current.
		{"HEAD symref names a different object", v1(pkt(sha1A+" HEAD\x00symref=HEAD:refs/heads/main"), pkt(sha1B+" refs/heads/main")), ErrConflictingRefs, ""},
		{"non-HEAD symref names a different object", v1(pkt(sha1A+" HEAD\x00symref=refs/remotes/origin/HEAD:refs/remotes/origin/main"), pkt(sha1B+" refs/remotes/origin/HEAD"), pkt(sha1C+" refs/remotes/origin/main")), ErrConflictingRefs, ""},
		{"symref disagreement in uppercase", v1(pkt(upper1A+" HEAD\x00symref=HEAD:refs/heads/main"), pkt(upper1B+" refs/heads/main")), ErrConflictingRefs, ""},
		{"duplicate ref", v1(head, main, pkt(sha1B+" refs/heads/main")), ErrConflictingRefs, ""},
		// The duplicate is the repeated name; OID case must not hide it.
		{"duplicate ref with differing OID case", v1(pkt(upper1A+" refs/heads/main\x00ofs-delta"), pkt(lower1A+" refs/heads/main")), ErrConflictingRefs, ""},
		{"tag peeled twice", v1(tag, pkt(sha1B+" refs/tags/v1^{}"), pkt(sha1C+" refs/tags/v1^{}")), ErrConflictingRefs, ""},
		{"conflicting symrefs", v1(pkt(sha1A + " HEAD\x00symref=HEAD:refs/heads/main symref=HEAD:refs/heads/other")), ErrConflictingRefs, ""},
		{"repeated symref", v1(pkt(sha1A + " HEAD\x00symref=HEAD:refs/heads/main symref=HEAD:refs/heads/main")), ErrConflictingRefs, ""},
		// Only shallow records may follow the empty sentinel.
		{"ref after empty sentinel", v1(sentinel, main), ErrConflictingRefs, ""},

		{"peeled record not following its tag", v1(tag, pkt(sha1B+" refs/heads/main"), pkt(sha1C+" refs/tags/v1^{}")), ErrInvalidRecord, ""},
		{"leading peeled record", v1(pkt(sha1B + " refs/tags/v1^{}\x00ofs-delta")), ErrInvalidRecord, ""},
		{"zero ID on first ref", v1(pkt(zero1 + " refs/heads/main\x00ofs-delta")), ErrInvalidObjectID, ""},
		{"zero ID on later ref", v1(head, pkt(zero1+" refs/heads/main")), ErrInvalidObjectID, ""},
		{"zero peeled ID", v1(tag, pkt(zero1+" refs/tags/v1^{}")), ErrInvalidObjectID, ""},
		{"sentinel with a real ID", v1(pkt(sha1A + " capabilities^{}\x00ofs-delta")), ErrInvalidRecord, ""},
		{"sentinel after a ref", v1(head, pkt(zero1+" capabilities^{}")), ErrInvalidRecord, ""},

		// Case is accepted for hexadecimal digits only, and width is checked
		// before case.
		{"non-hex ID", v1(pkt(strings.Repeat("g", 40) + " refs/heads/main\x00ofs-delta")), ErrInvalidObjectID, ""},
		{"non-hex uppercase ID", v1(pkt(strings.Repeat("G", 40) + " refs/heads/main\x00ofs-delta")), ErrInvalidObjectID, ""},
		{"short ID", v1(pkt(sha1A[:39] + " refs/heads/main\x00ofs-delta")), ErrObjectFormat, ""},
		{"short uppercase ID", v1(pkt(upper1A[:39] + " refs/heads/main\x00ofs-delta")), ErrObjectFormat, ""},

		{"symref target outside refs", v1(pkt(sha1A + " HEAD\x00symref=HEAD:heads/main")), ErrInvalidName, ""},
		{"invalid symref name", v1(pkt(sha1A + " HEAD\x00symref=refs/heads/..:refs/heads/main")), ErrInvalidName, ""},
		{"symref without target", v1(pkt(sha1A + " HEAD\x00symref=HEAD")), ErrInvalidCapability, ""},

		{"record without space", v1(pkt(sha1A + "refs/heads/main\x00ofs-delta")), ErrInvalidRecord, ""},
		{"first record without capabilities", v1(pkt(sha1A + " refs/heads/main")), ErrInvalidRecord, ""},
		{"capabilities on a later record", v1(head, pkt(sha1B+" refs/heads/main\x00ofs-delta")), ErrInvalidRecord, ""},
		{"empty ref name", v1(pkt(sha1A + " \x00ofs-delta")), ErrInvalidName, ""},

		// cap-list requires at least one capability, in v1 and v0 alike, so an
		// empty sentinel is not a successful empty repository.
		{"empty capabilities on v1 sentinel", v1(pkt(zero1 + " capabilities^{}\x00")), ErrInvalidCapability, ""},
		{"empty capabilities on v1 ref", v1(pkt(sha1A + " HEAD\x00")), ErrInvalidCapability, ""},
		{"empty capabilities on v0 sentinel", advertisement("", pkt(zero1+" capabilities^{}\x00")), ErrInvalidCapability, ""},
		{"empty capabilities on v0 ref", advertisement("", pkt(sha1A+" refs/heads/main\x00")), ErrInvalidCapability, ""},
		{"uppercase capability", v1(pkt(sha1A + " HEAD\x00Multi-Ack")), ErrInvalidCapability, ""},
		{"double space between capabilities", v1(pkt(sha1A + " HEAD\x00ofs-delta  thin-pack")), ErrInvalidCapability, ""},
		{"control byte in capability", v1(pkt(sha1A + " HEAD\x00agent=git/2.54\x01")), ErrInvalidCapability, ""},

		{"shallow boundary", v1(pkt(sha1A+" HEAD\x00shallow ofs-delta"), main, pkt("shallow "+sha1B)), ErrIncompleteHistory, ""},
		{"sha256 shallow boundary", v1(pkt(sha2A+" HEAD\x00object-format=sha256"), pkt(sha2A+" refs/heads/main"), pkt("shallow "+sha2B)), ErrIncompleteHistory, ""},
		{"uppercase shallow boundary", v1(head, main, pkt("shallow "+upper1B)), ErrIncompleteHistory, ""},
		{"mixed-case shallow boundary", v1(head, main, pkt("shallow "+mixed1A)), ErrIncompleteHistory, ""},
		// advertised-refs is (no-refs / list-of-refs) *shallow.
		{"shallow boundary after empty sentinel", v1(sentinel, pkt("shallow "+sha1B)), ErrIncompleteHistory, ""},
		// A record that only looks shallow must not claim shallow history.
		{"shallow without ID", v1(head, main, pkt("shallow")), ErrInvalidRecord, ""},
		{"shallow with empty ID", v1(head, main, pkt("shallow ")), ErrObjectFormat, ""},
		{"shallow with short ID", v1(head, main, pkt("shallow "+sha1B[:39])), ErrObjectFormat, ""},
		{"short uppercase shallow ID", v1(head, main, pkt("shallow "+upper1B[:39])), ErrObjectFormat, ""},
		{"shallow with sha256 width", v1(head, main, pkt("shallow "+sha2B)), ErrObjectFormat, ""},
		{"shallow with non-hex ID", v1(head, main, pkt("shallow "+strings.Repeat("z", 40))), ErrInvalidObjectID, ""},
		{"shallow with non-hex uppercase ID", v1(head, main, pkt("shallow "+strings.Repeat("Z", 40))), ErrInvalidObjectID, ""},
		{"shallow with zero ID", v1(head, main, pkt("shallow "+zero1)), ErrInvalidObjectID, ""},
		{"shallow with trailing garbage", v1(head, main, pkt("shallow "+sha1B+" extra")), ErrObjectFormat, ""},
		{"shallow with zero ID after sentinel", v1(sentinel, pkt("shallow "+zero1)), ErrInvalidObjectID, ""},
		// *shallow follows the ref list, after the capabilities.
		{"leading shallow record", v1(pkt("shallow "+sha1B), head), ErrInvalidRecord, ""},

		{"receive-pack service", pkt("# service=git-receive-pack") + flush + head + flush, ErrMalformedService, ""},
		{"no service announcement", head + flush, ErrMalformedService, ""},
		{"service without flush", pkt("# service=git-upload-pack") + head + flush, ErrMalformedService, ""},
		{"service followed by two flushes", service + flush, ErrInvalidRecord, ""},
		{"empty body", "", ErrTruncated, ""},
		// A dumb reply must never read as an advertisement: short, it is cut
		// off; padded to its declared length, it fails as a service line.
		{"dumb HTTP reply", dumb, ErrTruncated, ""},
		{"dumb HTTP reply padded to its pkt-len", dumb + strings.Repeat("x", 0x1111-len(dumb)), ErrMalformedService, ""},
		{"dumb HTTP reply without hex prefix", "not a git response\n", ErrMalformedPacket, ""},

		{"v2 delimiter packet", service + "0001" + flush, ErrUnsupportedVersion, ""},
		{"v2 response-end packet", service + head + "0002" + flush, ErrUnsupportedVersion, ""},
		{"v2 delimiter before service", "0001" + pkt("# service=git-upload-pack"), ErrUnsupportedVersion, ""},
		// A supported version in the wrong place is broken framing; version 0
		// is exactly the no-version-packet case, so it is not unsupported.
		{"version packet inside ref list", v1(head, pkt("version 1")), ErrInvalidRecord, ""},
		{"repeated version packet", v1(pkt("version 1"), head), ErrInvalidRecord, ""},
		{"explicit version 0", advertisement("version 0", head), ErrInvalidRecord, ""},
		{"version 99", advertisement("version 99", head), ErrUnsupportedVersion, ""},
		{"version 2 in body", advertisement("version 2", head), ErrUnsupportedVersion, ""},
		{"version 2 after refs", v1(head, pkt("version 2")), ErrUnsupportedVersion, ""},
		// The service line comes first, so a leading supported version is a
		// broken reply; calling it unsupported would abandon a v1 source.
		{"leading version 1", pkt("version 1") + head + flush, ErrMalformedService, ""},
		{"leading version 0", pkt("version 0") + flush, ErrMalformedService, ""},
		{"leading version 2", pkt("version 2") + pkt("ls-refs") + flush, ErrUnsupportedVersion, ""},
		{"leading version 3", pkt("version 3") + flush, ErrUnsupportedVersion, ""},

		{"non-hex packet length", service + "zzzz" + flush, ErrMalformedPacket, ""},
		// gitprotocol-http(5) fixes the length as "^[0-9a-f]{4}#"; the
		// case rule covers object IDs only.
		{"uppercase packet length", "001E# service=git-upload-pack\n" + flush, ErrMalformedPacket, ""},
		{"empty data packet", service + "0004" + flush, ErrMalformedPacket, ""},
		{"packet length three", service + "0003" + flush, ErrMalformedPacket, ""},
		{"short length header", "001", ErrTruncated, ""},
		// Beyond the protocol maximum is a violation, not a local limit.
		{"packet above protocol maximum", service + "fff1" + strings.Repeat("x", 4), ErrMalformedPacket, "protocol maximum"},
		{"truncated payload", service + pktRaw(sha1A + " refs/heads/main\x00ofs")[:20], ErrTruncated, ""},
		{"no terminating flush", service + pkt("version 1") + head, ErrTruncated, ""},
		{"service only", pkt("# service=git-upload-pack"), ErrTruncated, ""},
		{"trailing packet", v1(head) + pkt("extra"), ErrTrailingContent, ""},
		{"trailing byte", v1(head) + "\n", ErrTrailingContent, ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			failure := wantError(t, testCase.body, Options{}, testCase.want)
			if failure == nil {
				t.Fatal("expected a *ParseError")
			}
			if testCase.want != ErrIncompleteHistory && errors.Is(failure, ErrIncompleteHistory) {
				t.Fatal("only a valid shallow boundary may assert incomplete history")
			}
			if !strings.Contains(failure.Detail, testCase.detail) {
				t.Fatalf("detail = %q, want %q", failure.Detail, testCase.detail)
			}
		})
	}
}

func TestParseEnforcesLimits(t *testing.T) {
	body := advertisement("version 1",
		pkt(sha1A+" HEAD\x00ofs-delta"),
		pkt(sha1A+" refs/heads/main"),
		pkt(sha1B+" refs/heads/other"),
	)

	t.Run("ref records", func(t *testing.T) {
		wantError(t, body, Options{Limits: Limits{MaxRefRecords: 2}}, ErrLimitExceeded)
		if result, err := parseString(t, body, Options{Limits: Limits{MaxRefRecords: 3}}); err != nil {
			t.Fatalf("the exact record limit must pass: %v", err)
		} else if len(result.Refs) != 3 {
			t.Fatalf("refs = %d", len(result.Refs))
		}
	})

	t.Run("peeled records count", func(t *testing.T) {
		// Three records: the tag, its peeled value, and one more ref.
		peeled := advertisement("version 1",
			pkt(sha1A+" refs/tags/v1\x00ofs-delta"),
			pkt(sha1B+" refs/tags/v1^{}"),
			pkt(sha1C+" refs/heads/main"),
		)
		wantError(t, peeled, Options{Limits: Limits{MaxRefRecords: 2}}, ErrLimitExceeded)
		if _, err := parseString(t, peeled, Options{Limits: Limits{MaxRefRecords: 3}}); err != nil {
			t.Fatalf("the exact record limit must pass: %v", err)
		}
	})

	t.Run("shallow records count", func(t *testing.T) {
		// A shallow record is a ref-list record, so the documented bound covers
		// it. At the limit the record is refused for being over the bound; one
		// higher it is reached and refused as incomplete history.
		shallow := advertisement("version 1",
			pkt(sha1A+" HEAD\x00ofs-delta"),
			pkt(sha1A+" refs/heads/main"),
			pkt("shallow "+sha1B),
		)
		wantError(t, shallow, Options{Limits: Limits{MaxRefRecords: 2}}, ErrLimitExceeded)
		wantError(t, shallow, Options{Limits: Limits{MaxRefRecords: 3}}, ErrIncompleteHistory)

		// The same holds after the sentinel, where the shallow record is the
		// second ref-list record.
		sentinel := advertisement("version 1",
			pkt(zero1+" capabilities^{}\x00ofs-delta"),
			pkt("shallow "+sha1B),
		)
		wantError(t, sentinel, Options{Limits: Limits{MaxRefRecords: 1}}, ErrLimitExceeded)
		wantError(t, sentinel, Options{Limits: Limits{MaxRefRecords: 2}}, ErrIncompleteHistory)
	})

	t.Run("version packet is not counted", func(t *testing.T) {
		// The version packet precedes the ref list, so a two-record list must
		// pass a limit of two.
		two := advertisement("version 1",
			pkt(sha1A+" HEAD\x00ofs-delta"),
			pkt(sha1A+" refs/heads/main"),
		)
		if _, err := parseString(t, two, Options{Limits: Limits{MaxRefRecords: 2}}); err != nil {
			t.Fatalf("the version packet must not consume the record budget: %v", err)
		}
		wantError(t, two, Options{Limits: Limits{MaxRefRecords: 1}}, ErrLimitExceeded)
	})

	t.Run("total bytes", func(t *testing.T) {
		wantError(t, body, Options{Limits: Limits{MaxTotalBytes: int64(len(body)) - 1}}, ErrLimitExceeded)
		if _, err := parseString(t, body, Options{Limits: Limits{MaxTotalBytes: int64(len(body))}}); err != nil {
			t.Fatalf("the exact total limit must pass: %v", err)
		}
	})

	t.Run("packet bytes", func(t *testing.T) {
		single := advertisement("version 1", pkt(sha1A+" HEAD\x00ofs-delta"))
		longest := len(sha1A+" HEAD\x00ofs-delta\n") + packetLengthBytes
		wantError(t, single, Options{Limits: Limits{MaxPacketBytes: longest - 1}}, ErrLimitExceeded)
		if _, err := parseString(t, single, Options{Limits: Limits{MaxPacketBytes: longest}}); err != nil {
			t.Fatalf("the exact packet limit must pass: %v", err)
		}
	})

	t.Run("name bytes", func(t *testing.T) {
		name := "refs/heads/" + strings.Repeat("n", 40)
		long := advertisement("version 1", pkt(sha1A+" "+name+"\x00ofs-delta"))
		wantError(t, long, Options{Limits: Limits{MaxNameBytes: len(name) - 1}}, ErrLimitExceeded)
		if _, err := parseString(t, long, Options{Limits: Limits{MaxNameBytes: len(name)}}); err != nil {
			t.Fatalf("the exact name limit must pass: %v", err)
		}
	})

	t.Run("capability count", func(t *testing.T) {
		wantError(t, advertisement("version 1", pkt(sha1A+" HEAD\x00a b c")),
			Options{Limits: Limits{MaxCapabilities: 2}}, ErrLimitExceeded)
		if _, err := parseString(t, advertisement("version 1", pkt(sha1A+" HEAD\x00a b c")),
			Options{Limits: Limits{MaxCapabilities: 3}}); err != nil {
			t.Fatalf("the exact capability limit must pass: %v", err)
		}
		// The count is enforced while scanning, so a list far past the limit
		// still fails as a limit rather than being expanded first.
		many := strings.TrimSuffix(strings.Repeat("cap ", 4000), " ")
		wantError(t, advertisement("version 1", pkt(sha1A+" HEAD\x00"+many)),
			Options{Limits: Limits{MaxCapabilities: 4}}, ErrLimitExceeded)
	})

	t.Run("capability bytes", func(t *testing.T) {
		capability := "agent=" + strings.Repeat("v", 30)
		one := advertisement("version 1", pkt(sha1A+" HEAD\x00"+capability))
		wantError(t, one, Options{Limits: Limits{MaxCapabilityBytes: len(capability) - 1}}, ErrLimitExceeded)
		if _, err := parseString(t, one, Options{Limits: Limits{MaxCapabilityBytes: len(capability)}}); err != nil {
			t.Fatalf("the exact capability byte limit must pass: %v", err)
		}
	})
}

func TestParseRejectsInvalidOptions(t *testing.T) {
	body := advertisement("version 1", pkt(sha1A+" HEAD\x00ofs-delta"))
	for name, limits := range map[string]Limits{
		"negative packets":       {MaxPacketBytes: -1},
		"negative total":         {MaxTotalBytes: -1},
		"negative records":       {MaxRefRecords: -1},
		"negative names":         {MaxNameBytes: -1},
		"negative capabilities":  {MaxCapabilities: -1},
		"negative capability":    {MaxCapabilityBytes: -1},
		"packets above protocol": {MaxPacketBytes: protocolMaxPacket + 1},
	} {
		t.Run(name, func(t *testing.T) {
			wantError(t, body, Options{Limits: limits}, ErrInvalidLimits)
		})
	}

	if _, err := Parse(nil, Options{}); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("a nil body must be refused: %v", err)
	}
	if _, err := parseString(t, body, Options{Service: "git-receive-pack"}); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("an unsupported service must be refused: %v", err)
	}
}

func TestParseReportsPacketOffset(t *testing.T) {
	prefix := pkt("# service=git-upload-pack") + flush + pkt("version 1") + pkt(sha1A+" HEAD\x00ofs-delta")
	body := prefix + pkt(zero1+" refs/heads/main") + flush
	failure := wantError(t, body, Options{}, ErrInvalidObjectID)
	if failure == nil || failure.Offset != int64(len(prefix)) {
		t.Fatalf("offset = %+v, want %d", failure, len(prefix))
	}
}

// chunkedReader returns one byte at a time, and an occasional empty read, the
// way a network body can. Parsing must not depend on read sizes.
type chunkedReader struct {
	data  string
	index int
	stall bool
}

func (r *chunkedReader) Read(buffer []byte) (int, error) {
	if r.index >= len(r.data) {
		return 0, io.EOF
	}
	r.stall = !r.stall
	if r.stall {
		return 0, nil
	}
	buffer[0] = r.data[r.index]
	r.index++
	return 1, nil
}

func TestParseHandlesFragmentedReads(t *testing.T) {
	body := advertisement("version 1",
		pkt(sha1A+" HEAD\x00ofs-delta symref=HEAD:refs/heads/main"),
		pkt(sha1A+" refs/heads/main"),
	)
	result, err := Parse(&chunkedReader{data: body}, Options{})
	if err != nil {
		t.Fatalf("fragmented parse failed: %v", err)
	}
	if len(result.Refs) != 2 || result.Head.SymrefTarget != "refs/heads/main" {
		t.Fatalf("result = %+v", result)
	}

	trailing, err := Parse(&chunkedReader{data: body + "x"}, Options{})
	if err == nil {
		t.Fatalf("a stalling reader must not hide trailing content: %+v", trailing)
	}
	if !errors.Is(err, ErrTrailingContent) {
		t.Fatalf("expected trailing content, got %v", err)
	}
}

// failingReader reports a transport failure mid-body.
type failingReader struct{ err error }

func (r *failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestParsePropagatesReaderErrors(t *testing.T) {
	sentinel := errors.New("connection reset")
	if _, err := Parse(&failingReader{err: sentinel}, Options{}); !errors.Is(err, sentinel) {
		t.Fatalf("reader errors must reach the caller unchanged: %v", err)
	}
}
