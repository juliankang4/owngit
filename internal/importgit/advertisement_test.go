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

func TestParseEmptyIsDistinctFromUnsupportedAndMalformed(t *testing.T) {
	// The v2 mismatch that Main's matrix showed produces the same exit0 and
	// zero refs from the Git CLI must be a distinct outcome here.
	v2First := pkt("version 2") + pkt("agent=git/2.54.0") + pkt("ls-refs=unborn") + pkt("object-format=sha1") + flush
	wantError(t, v2First, Options{}, ErrUnsupportedVersion)

	v2Body := pkt("# service=git-upload-pack") + flush + pkt("version 2") + pkt("ls-refs") + flush
	wantError(t, v2Body, Options{}, ErrUnsupportedVersion)

	// A ref list truncated before its flush packet is also not emptiness.
	wantError(t, pkt("# service=git-upload-pack")+flush+pkt("version 1"), Options{}, ErrTruncated)
}

func TestParseRejectsRefListWithoutAnyRecord(t *testing.T) {
	// The grammar has no advertisement without a ref list; an empty repository
	// still sends the capabilities record.
	wantError(t, advertisement("version 1"), Options{}, ErrInvalidRecord)
	wantError(t, advertisement(""), Options{}, ErrInvalidRecord)
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

func TestParseRejectsIncompatibleOIDWidths(t *testing.T) {
	// A sha256 advertisement carrying a 40-character ID, and the reverse.
	wantError(t, advertisement("version 1",
		pkt(sha1A+" HEAD\x00object-format=sha256"),
	), Options{}, ErrObjectFormat)
	wantError(t, advertisement("version 1",
		pkt(sha1A+" HEAD\x00ofs-delta"),
		pkt(sha2B+" refs/heads/main"),
	), Options{}, ErrObjectFormat)
	wantError(t, advertisement("version 1",
		pkt(zero2+" capabilities^{}\x00ofs-delta"),
	), Options{}, ErrObjectFormat)
}

func TestParseRejectsUnknownObjectFormat(t *testing.T) {
	wantError(t, advertisement("version 1",
		pkt(sha1A+" HEAD\x00object-format=sha3-256"),
	), Options{}, ErrObjectFormat)
	wantError(t, advertisement("version 1",
		pkt(sha1A+" HEAD\x00object-format"),
	), Options{}, ErrObjectFormat)
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

func TestParseRejectsSymrefPointingAtADifferentObject(t *testing.T) {
	// When both the symbolic ref and its target were advertised, they name the
	// same object by definition. Differing IDs are a contradiction the caller
	// must not resolve by guessing which one is current.
	head := advertisement("version 1",
		pkt(sha1A+" HEAD\x00symref=HEAD:refs/heads/main"),
		pkt(sha1B+" refs/heads/main"),
	)
	wantError(t, head, Options{}, ErrConflictingRefs)

	nonHead := advertisement("version 1",
		pkt(sha1A+" HEAD\x00symref=refs/remotes/origin/HEAD:refs/remotes/origin/main"),
		pkt(sha1B+" refs/remotes/origin/HEAD"),
		pkt(sha1C+" refs/remotes/origin/main"),
	)
	wantError(t, nonHead, Options{}, ErrConflictingRefs)

	// Matching object IDs are the ordinary case and must still pass.
	agreeing := mustParse(t, advertisement("version 1",
		pkt(sha1A+" HEAD\x00symref=HEAD:refs/heads/main"),
		pkt(sha1A+" refs/heads/main"),
	))
	if agreeing.Head.SymrefTarget != "refs/heads/main" {
		t.Fatalf("head = %+v", agreeing.Head)
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

func TestParseRejectsConflictingRecords(t *testing.T) {
	duplicate := advertisement("version 1",
		pkt(sha1A+" HEAD\x00ofs-delta"),
		pkt(sha1A+" refs/heads/main"),
		pkt(sha1B+" refs/heads/main"),
	)
	wantError(t, duplicate, Options{}, ErrConflictingRefs)

	doublePeel := advertisement("version 1",
		pkt(sha1A+" refs/tags/v1\x00ofs-delta"),
		pkt(sha1B+" refs/tags/v1^{}"),
		pkt(sha1C+" refs/tags/v1^{}"),
	)
	wantError(t, doublePeel, Options{}, ErrConflictingRefs)

	conflictingSymrefs := advertisement("version 1",
		pkt(sha1A+" HEAD\x00symref=HEAD:refs/heads/main symref=HEAD:refs/heads/other"),
	)
	wantError(t, conflictingSymrefs, Options{}, ErrConflictingRefs)

	repeatedSymref := advertisement("version 1",
		pkt(sha1A+" HEAD\x00symref=HEAD:refs/heads/main symref=HEAD:refs/heads/main"),
	)
	wantError(t, repeatedSymref, Options{}, ErrConflictingRefs)

	sentinelThenRef := advertisement("version 1",
		pkt(zero1+" capabilities^{}\x00ofs-delta"),
		pkt(sha1A+" refs/heads/main"),
	)
	wantError(t, sentinelThenRef, Options{}, ErrConflictingRefs)
}

func TestParseRejectsMisplacedPeeledRecord(t *testing.T) {
	notFollowing := advertisement("version 1",
		pkt(sha1A+" refs/tags/v1\x00ofs-delta"),
		pkt(sha1B+" refs/heads/main"),
		pkt(sha1C+" refs/tags/v1^{}"),
	)
	wantError(t, notFollowing, Options{}, ErrInvalidRecord)

	leading := advertisement("version 1", pkt(sha1B+" refs/tags/v1^{}\x00ofs-delta"))
	wantError(t, leading, Options{}, ErrInvalidRecord)
}

func TestParseRejectsZeroObjectIDs(t *testing.T) {
	ordinary := advertisement("version 1", pkt(zero1+" refs/heads/main\x00ofs-delta"))
	wantError(t, ordinary, Options{}, ErrInvalidObjectID)

	later := advertisement("version 1",
		pkt(sha1A+" HEAD\x00ofs-delta"),
		pkt(zero1+" refs/heads/main"),
	)
	wantError(t, later, Options{}, ErrInvalidObjectID)

	peeled := advertisement("version 1",
		pkt(sha1A+" refs/tags/v1\x00ofs-delta"),
		pkt(zero1+" refs/tags/v1^{}"),
	)
	wantError(t, peeled, Options{}, ErrInvalidObjectID)

	sentinelWithRealOID := advertisement("version 1", pkt(sha1A+" capabilities^{}\x00ofs-delta"))
	wantError(t, sentinelWithRealOID, Options{}, ErrInvalidRecord)

	sentinelLater := advertisement("version 1",
		pkt(sha1A+" HEAD\x00ofs-delta"),
		pkt(zero1+" capabilities^{}"),
	)
	wantError(t, sentinelLater, Options{}, ErrInvalidRecord)
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

	// A genuine disagreement is still a conflict, whatever the case.
	wantError(t, advertisement("version 1",
		pkt(upper1A+" HEAD\x00symref=HEAD:refs/heads/main"),
		pkt(upper1B+" refs/heads/main"),
	), Options{}, ErrConflictingRefs)
}

func TestParseDetectsDuplicateRefsRegardlessOfObjectIDCase(t *testing.T) {
	// The duplicate is the repeated name, so the differing OID case must not
	// hide it.
	wantError(t, advertisement("version 1",
		pkt(upper1A+" refs/heads/main\x00ofs-delta"),
		pkt(lower1A+" refs/heads/main"),
	), Options{}, ErrConflictingRefs)
}

func TestParseAcceptsUppercaseShallowObjectID(t *testing.T) {
	// A valid boundary in uppercase is still a valid boundary.
	shallow := advertisement("version 1",
		pkt(sha1A+" HEAD\x00ofs-delta"),
		pkt(sha1A+" refs/heads/main"),
		pkt("shallow "+upper1B),
	)
	wantError(t, shallow, Options{}, ErrIncompleteHistory)

	mixedShallow := advertisement("version 1",
		pkt(sha1A+" HEAD\x00ofs-delta"),
		pkt(sha1A+" refs/heads/main"),
		pkt("shallow "+mixed1A),
	)
	wantError(t, mixedShallow, Options{}, ErrIncompleteHistory)

	// Width and non-hex checks still apply to an uppercase spelling.
	wantError(t, advertisement("version 1",
		pkt(sha1A+" HEAD\x00ofs-delta"),
		pkt(sha1A+" refs/heads/main"),
		pkt("shallow "+upper1B[:39]),
	), Options{}, ErrObjectFormat)
	// Non-hex uppercase is covered by the malformed-shallow table.
}

func TestParseRejectsInvalidObjectIDs(t *testing.T) {
	// Case is accepted for hexadecimal digits only. A letter outside [a-fA-F]
	// is still not an object ID in either case.
	nonHex := advertisement("version 1", pkt(strings.Repeat("g", 40)+" refs/heads/main\x00ofs-delta"))
	wantError(t, nonHex, Options{}, ErrInvalidObjectID)

	nonHexUppercase := advertisement("version 1", pkt(strings.Repeat("G", 40)+" refs/heads/main\x00ofs-delta"))
	wantError(t, nonHexUppercase, Options{}, ErrInvalidObjectID)

	short := advertisement("version 1", pkt(sha1A[:39]+" refs/heads/main\x00ofs-delta"))
	wantError(t, short, Options{}, ErrObjectFormat)

	// A valid uppercase ID of the wrong width is a width failure, not a case
	// failure.
	shortUppercase := advertisement("version 1", pkt(upper1A[:39]+" refs/heads/main\x00ofs-delta"))
	wantError(t, shortUppercase, Options{}, ErrObjectFormat)
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
			// record-structure tests.
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

func TestParseRejectsInvalidSymrefNames(t *testing.T) {
	wantError(t, advertisement("version 1",
		pkt(sha1A+" HEAD\x00symref=HEAD:heads/main"),
	), Options{}, ErrInvalidName)
	wantError(t, advertisement("version 1",
		pkt(sha1A+" HEAD\x00symref=refs/heads/..:refs/heads/main"),
	), Options{}, ErrInvalidName)
	wantError(t, advertisement("version 1",
		pkt(sha1A+" HEAD\x00symref=HEAD"),
	), Options{}, ErrInvalidCapability)
}

func TestParseRejectsInvalidRecordStructure(t *testing.T) {
	noSpace := advertisement("version 1", pkt(sha1A+"refs/heads/main\x00ofs-delta"))
	wantError(t, noSpace, Options{}, ErrInvalidRecord)

	noCapabilitySeparator := advertisement("version 1", pkt(sha1A+" refs/heads/main"))
	wantError(t, noCapabilitySeparator, Options{}, ErrInvalidRecord)

	lateCapabilities := advertisement("version 1",
		pkt(sha1A+" HEAD\x00ofs-delta"),
		pkt(sha1B+" refs/heads/main\x00ofs-delta"),
	)
	wantError(t, lateCapabilities, Options{}, ErrInvalidRecord)

	emptyName := advertisement("version 1", pkt(sha1A+" \x00ofs-delta"))
	wantError(t, emptyName, Options{}, ErrInvalidName)
}

func TestParseRejectsEmptyCapabilityList(t *testing.T) {
	// cap-list is "capability *(SP capability)", so at least one capability is
	// required. A zero-id sentinel with no capabilities must not be reported as
	// a successful empty repository.
	emptySentinel := advertisement("version 1", pkt(zero1+" capabilities^{}\x00"))
	failure := wantError(t, emptySentinel, Options{}, ErrInvalidCapability)
	if failure == nil {
		t.Fatal("expected a *ParseError")
	}

	emptyOnRef := advertisement("version 1", pkt(sha1A+" HEAD\x00"))
	wantError(t, emptyOnRef, Options{}, ErrInvalidCapability)

	// The same applies without a version packet, so v0 is covered too.
	wantError(t, advertisement("", pkt(zero1+" capabilities^{}\x00")), Options{}, ErrInvalidCapability)
	wantError(t, advertisement("", pkt(sha1A+" refs/heads/main\x00")), Options{}, ErrInvalidCapability)

	// One capability is enough.
	if result := mustParse(t, advertisement("version 1", pkt(zero1+" capabilities^{}\x00a"))); !result.Empty {
		t.Fatal("a single capability must satisfy the list requirement")
	}
}

func TestParseRejectsInvalidCapabilities(t *testing.T) {
	uppercase := advertisement("version 1", pkt(sha1A+" HEAD\x00Multi-Ack"))
	wantError(t, uppercase, Options{}, ErrInvalidCapability)

	doubleSpace := advertisement("version 1", pkt(sha1A+" HEAD\x00ofs-delta  thin-pack"))
	wantError(t, doubleSpace, Options{}, ErrInvalidCapability)

	controlByte := advertisement("version 1", pkt(sha1A+" HEAD\x00agent=git/2.54\x01"))
	wantError(t, controlByte, Options{}, ErrInvalidCapability)
}

func TestParseAcceptsUnknownCapability(t *testing.T) {
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
}

func TestParseRejectsShallowAdvertisement(t *testing.T) {
	withBoundary := advertisement("version 1",
		pkt(sha1A+" HEAD\x00shallow ofs-delta"),
		pkt(sha1A+" refs/heads/main"),
		pkt("shallow "+sha1B),
	)
	wantError(t, withBoundary, Options{}, ErrIncompleteHistory)

	sha256Boundary := advertisement("version 1",
		pkt(sha2A+" HEAD\x00object-format=sha256"),
		pkt(sha2A+" refs/heads/main"),
		pkt("shallow "+sha2B),
	)
	wantError(t, sha256Boundary, Options{}, ErrIncompleteHistory)

	// The shallow capability alone only offers the feature; it is not a
	// truncated history and must stay parseable.
	capabilityOnly := advertisement("version 1",
		pkt(sha1A+" HEAD\x00shallow deepen-since ofs-delta"),
		pkt(sha1A+" refs/heads/main"),
	)
	if result := mustParse(t, capabilityOnly); len(result.Refs) != 2 {
		t.Fatalf("refs = %+v", result.Refs)
	}
}

func TestParseRejectsMalformedShallowWithoutAssertingShallowHistory(t *testing.T) {
	// ErrIncompleteHistory asserts that the source really is shallow. A record
	// that only looks shallow must not carry that assertion.
	//
	// Object-ID case is not a malformation. An uppercase or mixed-case shallow
	// boundary is valid and is covered by
	// TestParseAcceptsUppercaseShallowObjectID.
	prefix := []string{
		pkt(sha1A + " HEAD\x00ofs-delta"),
		pkt(sha1A + " refs/heads/main"),
	}
	cases := map[string]struct {
		record string
		want   error
	}{
		"no object ID":      {pkt("shallow"), ErrInvalidRecord},
		"empty object ID":   {pkt("shallow "), ErrObjectFormat},
		"short object ID":   {pkt("shallow " + sha1B[:39]), ErrObjectFormat},
		"sha256 width":      {pkt("shallow " + sha2B), ErrObjectFormat},
		"non-hex":           {pkt("shallow " + strings.Repeat("z", 40)), ErrInvalidObjectID},
		"non-hex uppercase": {pkt("shallow " + strings.Repeat("Z", 40)), ErrInvalidObjectID},
		"zero object ID":    {pkt("shallow " + zero1), ErrInvalidObjectID},
		"trailing garbage":  {pkt("shallow " + sha1B + " extra"), ErrObjectFormat},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			body := advertisement("version 1", append(append([]string{}, prefix...), testCase.record)...)
			failure := wantError(t, body, Options{}, testCase.want)
			if failure == nil {
				t.Fatal("expected a *ParseError")
			}
			if errors.Is(failure, ErrIncompleteHistory) {
				t.Fatal("a malformed shallow record must not assert incomplete history")
			}
		})
	}
}

func TestParseRejectsMisplacedShallowRecord(t *testing.T) {
	// The grammar places *shallow after the ref list, where the capability
	// list has already been advertised.
	leading := advertisement("version 1", pkt("shallow "+sha1B), pkt(sha1A+" HEAD\x00ofs-delta"))
	failure := wantError(t, leading, Options{}, ErrInvalidRecord)
	if failure != nil && errors.Is(failure, ErrIncompleteHistory) {
		t.Fatal("a leading shallow record must not assert incomplete history")
	}
}

func TestParseAcceptsShallowAfterTheEmptySentinel(t *testing.T) {
	// advertised-refs is (no-refs / list-of-refs) *shallow, so a shallow
	// record may follow the zero-OID sentinel. It is a valid boundary and must
	// be reported as incomplete history, not as a conflict.
	afterSentinel := advertisement("version 1",
		pkt(zero1+" capabilities^{}\x00ofs-delta"),
		pkt("shallow "+sha1B),
	)
	wantError(t, afterSentinel, Options{}, ErrIncompleteHistory)

	// Validation still applies after the sentinel: a malformed record there
	// must not assert incomplete history either.
	malformed := advertisement("version 1",
		pkt(zero1+" capabilities^{}\x00ofs-delta"),
		pkt("shallow "+zero1),
	)
	failure := wantError(t, malformed, Options{}, ErrInvalidObjectID)
	if failure != nil && errors.Is(failure, ErrIncompleteHistory) {
		t.Fatal("a malformed shallow record after the sentinel must not assert incomplete history")
	}

	// An ordinary ref after the sentinel remains a conflict; only shallow is
	// permitted there.
	wantError(t, advertisement("version 1",
		pkt(zero1+" capabilities^{}\x00ofs-delta"),
		pkt(sha1A+" refs/heads/main"),
	), Options{}, ErrConflictingRefs)
}

func TestParseRejectsMalformedService(t *testing.T) {
	wrongService := pkt("# service=git-receive-pack") + flush + pkt(sha1A+" HEAD\x00ofs-delta") + flush
	wantError(t, wrongService, Options{}, ErrMalformedService)

	noAnnouncement := pkt(sha1A+" HEAD\x00ofs-delta") + flush
	wantError(t, noAnnouncement, Options{}, ErrMalformedService)

	noFlush := pkt("# service=git-upload-pack") + pkt(sha1A+" HEAD\x00ofs-delta") + flush
	wantError(t, noFlush, Options{}, ErrMalformedService)

	doubleFlush := pkt("# service=git-upload-pack") + flush + flush
	wantError(t, doubleFlush, Options{}, ErrInvalidRecord)

	wantError(t, "", Options{}, ErrTruncated)
}

func TestParseRejectsDumbHTTPResponse(t *testing.T) {
	// A dumb server answers the same request with plain text. Its leading hex
	// digits do read as a pkt-len, so the refusal comes from the framing that
	// follows rather than from the length itself. Either way it must never be
	// mistaken for an advertisement.
	short := sha1A + "\trefs/heads/main\n" + sha1B + "\trefs/tags/v1.0\n"
	wantError(t, short, Options{}, ErrTruncated)

	// Padded to the length its first four digits declare, so the first packet
	// completes and fails as a service announcement instead.
	declared := 0x1111
	padded := short + strings.Repeat("x", declared-len(short))
	wantError(t, padded, Options{}, ErrMalformedService)

	// A dumb body whose first four bytes are not hex is refused outright.
	wantError(t, "not a git response\n", Options{}, ErrMalformedPacket)
}

func TestParseRejectsV2ControlPackets(t *testing.T) {
	delimiter := pkt("# service=git-upload-pack") + flush + "0001" + flush
	wantError(t, delimiter, Options{}, ErrUnsupportedVersion)

	responseEnd := pkt("# service=git-upload-pack") + flush +
		pkt(sha1A+" HEAD\x00ofs-delta") + "0002" + flush
	wantError(t, responseEnd, Options{}, ErrUnsupportedVersion)

	beforeService := "0001" + pkt("# service=git-upload-pack")
	wantError(t, beforeService, Options{}, ErrUnsupportedVersion)
}

func TestParseRejectsMisplacedOrUnsupportedVersionPacket(t *testing.T) {
	// A supported version in the wrong position is broken framing, not an
	// unsupported version.
	inList := advertisement("version 1",
		pkt(sha1A+" HEAD\x00ofs-delta"),
		pkt("version 1"),
	)
	wantError(t, inList, Options{}, ErrInvalidRecord)

	repeated := advertisement("version 1", pkt("version 1"), pkt(sha1A+" HEAD\x00ofs-delta"))
	wantError(t, repeated, Options{}, ErrInvalidRecord)

	// "version 0" is not something a server sends, but it is not an
	// unsupported version either: v0 is exactly the no-version-packet case.
	wantError(t, advertisement("version 0", pkt(sha1A+" HEAD\x00ofs-delta")), Options{}, ErrInvalidRecord)

	// A version this parser does not implement stays unsupported wherever it
	// appears in the ref list.
	version99 := advertisement("version 99", pkt(sha1A+" HEAD\x00ofs-delta"))
	wantError(t, version99, Options{}, ErrUnsupportedVersion)

	v2InBody := advertisement("version 2", pkt(sha1A+" HEAD\x00ofs-delta"))
	wantError(t, v2InBody, Options{}, ErrUnsupportedVersion)

	v2AfterRefs := advertisement("version 1", pkt(sha1A+" HEAD\x00ofs-delta"), pkt("version 2"))
	wantError(t, v2AfterRefs, Options{}, ErrUnsupportedVersion)
}

func TestParseFirstPacketVersionClassification(t *testing.T) {
	// smart_reply puts the service announcement first, so a leading "version 1"
	// is a broken HTTP response from a version this parser supports. Calling it
	// unsupported would tell the caller to give up on a v1 source.
	v1First := pkt("version 1") + pkt(sha1A+" HEAD\x00ofs-delta") + flush
	wantError(t, v1First, Options{}, ErrMalformedService)

	v0First := pkt("version 0") + flush
	wantError(t, v0First, Options{}, ErrMalformedService)

	// A v2 or future first packet remains unsupported.
	wantError(t, pkt("version 2")+pkt("ls-refs")+flush, Options{}, ErrUnsupportedVersion)
	wantError(t, pkt("version 3")+flush, Options{}, ErrUnsupportedVersion)
}

func TestParseRejectsMalformedFraming(t *testing.T) {
	badLength := pkt("# service=git-upload-pack") + flush + "zzzz" + flush
	wantError(t, badLength, Options{}, ErrMalformedPacket)

	// The case-insensitivity rule covers object IDs only. gitprotocol-http(5)
	// states the packet length separately as "^[0-9a-f]{4}#", so an uppercase
	// length header stays malformed.
	uppercaseLength := "001E# service=git-upload-pack\n" + flush
	wantError(t, uppercaseLength, Options{}, ErrMalformedPacket)

	emptyDataPacket := pkt("# service=git-upload-pack") + flush + "0004" + flush
	wantError(t, emptyDataPacket, Options{}, ErrMalformedPacket)

	lengthThree := pkt("# service=git-upload-pack") + flush + "0003" + flush
	wantError(t, lengthThree, Options{}, ErrMalformedPacket)

	shortLengthHeader := "001"
	wantError(t, shortLengthHeader, Options{}, ErrTruncated)

	// A declared length beyond the protocol maximum is a violation, not a
	// local limit, so it must not be reported as ErrLimitExceeded.
	oversized := pkt("# service=git-upload-pack") + flush + "fff1" + strings.Repeat("x", 4)
	failure := wantError(t, oversized, Options{}, ErrMalformedPacket)
	if failure == nil || !strings.Contains(failure.Detail, "protocol maximum") {
		t.Fatalf("detail = %+v", failure)
	}
}

func TestParseRejectsTruncation(t *testing.T) {
	midPayload := pkt("# service=git-upload-pack") + flush + pktRaw(sha1A + " refs/heads/main\x00ofs")[:20]
	wantError(t, midPayload, Options{}, ErrTruncated)

	noTerminatingFlush := pkt("# service=git-upload-pack") + flush +
		pkt("version 1") + pkt(sha1A+" HEAD\x00ofs-delta")
	wantError(t, noTerminatingFlush, Options{}, ErrTruncated)

	serviceOnly := pkt("# service=git-upload-pack")
	wantError(t, serviceOnly, Options{}, ErrTruncated)
}

func TestParseRejectsTrailingContent(t *testing.T) {
	body := advertisement("version 1", pkt(sha1A+" HEAD\x00ofs-delta")) + pkt("extra")
	wantError(t, body, Options{}, ErrTrailingContent)

	// Even a single stray byte means the body was not the advertisement alone.
	wantError(t, advertisement("version 1", pkt(sha1A+" HEAD\x00ofs-delta"))+"\n",
		Options{}, ErrTrailingContent)
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
