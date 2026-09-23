package importgit

import (
	"errors"
	"strings"
	"testing"
)

// FuzzParse checks the invariants that hold for every possible response body.
//
// The parser reads untrusted bytes from a remote server, so the properties
// worth fuzzing are that it never panics, never reports a malformed or
// unsupported response as an empty repository, and never returns a result
// holding a fact it did not validate.
func FuzzParse(f *testing.F) {
	seeds := []string{
		"",
		"0000",
		"not a git response\n",
		advertisement("version 1", pkt(sha1A+" HEAD\x00ofs-delta symref=HEAD:refs/heads/main"), pkt(sha1A+" refs/heads/main")),
		advertisement("version 1", pkt(zero1+" capabilities^{}\x00ofs-delta symref=HEAD:refs/heads/main")),
		advertisement("version 1", pkt(sha1A+" refs/tags/v1\x00ofs-delta"), pkt(sha1B+" refs/tags/v1^{}")),
		advertisement("version 1", pkt(sha2A+" HEAD\x00object-format=sha256")),
		advertisement("", pkt(sha1A+" HEAD\x00ofs-delta")),
		advertisement("version 1", pkt(sha1A+" HEAD\x00shallow"), pkt("shallow "+sha1B)),
		advertisement("version 1", pkt(sha1A+" HEAD\x00shallow"), pkt("shallow")),
		advertisement("version 1", pkt(zero1+" capabilities^{}\x00")),
		advertisement("version 1", pkt(zero1+" capabilities^{}\x00ofs-delta"), pkt("shallow "+sha1B)),
		advertisement("version 1", pkt(sha1A+" HEAD\x00symref=HEAD:refs/heads/main"), pkt(sha1B+" refs/heads/main")),
		pkt("version 1") + pkt(sha1A+" HEAD\x00ofs-delta") + "0000",
		advertisement("version 1", pkt(upper1A+" HEAD\x00symref=HEAD:refs/heads/main"), pkt(sha1A+" refs/heads/main")),
		advertisement("version 1", pkt(mixed1A+" refs/tags/v1\x00ofs-delta"), pkt(upper1B+" refs/tags/v1^{}")),
		advertisement("version 1", pkt(sha1A+" HEAD\x00ofs-delta"), pkt(sha1A+" refs/heads/main"), pkt("shallow "+upper1B)),
		pkt("version 2") + pkt("ls-refs") + "0000",
		pkt("# service=git-upload-pack") + "0000" + "0001" + "0000",
		pkt("# service=git-upload-pack") + "0000" + pkt("ERR no such repository") + "0000",
		sha1A + "\trefs/heads/main\n",
	}
	for _, seed := range seeds {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		// Small limits keep one fuzz case cheap while still exercising every
		// validation path.
		options := Options{Limits: Limits{
			MaxPacketBytes: 4096, MaxTotalBytes: 1 << 16, MaxRefRecords: 64,
			MaxNameBytes: 256, MaxCapabilities: 32, MaxCapabilityBytes: 256,
		}}
		result, err := Parse(strings.NewReader(string(body)), options)
		if err != nil {
			if result != nil {
				t.Fatalf("a failed parse must not return a result: %+v", result)
			}
			var parseError *ParseError
			if !errors.As(err, &parseError) {
				t.Fatalf("every protocol failure must be a *ParseError: %v", err)
			}
			if errors.Is(err, ErrInvalidLimits) || errors.Is(err, ErrInvalidOptions) {
				t.Fatalf("fixed valid options must not be rejected: %v", err)
			}
			return
		}
		if result.TotalBytes != int64(len(body)) {
			t.Fatalf("total bytes = %d, want the whole %d-byte body", result.TotalBytes, len(body))
		}
		if result.Empty && len(result.Refs) != 0 {
			t.Fatalf("an empty advertisement cannot hold %d refs", len(result.Refs))
		}
		if result.Empty && result.Head.Advertised {
			t.Fatal("an empty advertisement cannot advertise HEAD")
		}
		if !result.Empty && len(result.Refs) == 0 {
			t.Fatal("a non-empty advertisement must hold at least one ref")
		}
		if result.ProtocolVersion != 0 && result.ProtocolVersion != 1 {
			t.Fatalf("protocol version = %d, want 0 or 1", result.ProtocolVersion)
		}
		width := oidWidth(result.ObjectFormat)
		if result.ObjectFormat != FormatSHA1 && result.ObjectFormat != FormatSHA256 {
			t.Fatalf("object format = %q", result.ObjectFormat)
		}
		seen := map[string]bool{}
		for _, ref := range result.Refs {
			if seen[ref.Name] {
				t.Fatalf("duplicate ref name %q survived parsing", ref.Name)
			}
			seen[ref.Name] = true
			if err := validateRefName(ref.Name); err != nil {
				t.Fatalf("invalid ref name %q survived parsing: %v", ref.Name, err)
			}
			oids := []string{ref.OID}
			if ref.Peeled() {
				oids = append(oids, ref.PeeledOID)
			}
			for _, oid := range oids {
				// Canonicalization is an invariant of the result: whatever case
				// the server used, a surviving object ID is lowercase.
				if len(oid) != width || !isHexOID(oid) || oid != canonicalOID(oid) || isZeroOID(oid) {
					t.Fatalf("invalid object ID %q survived parsing for %q", oid, ref.Name)
				}
			}
			if ref.SymrefTarget != "" {
				if err := validateRefName(ref.SymrefTarget); err != nil {
					t.Fatalf("invalid symref target %q survived parsing: %v", ref.SymrefTarget, err)
				}
			}
		}
		if result.Head.Advertised != seen["HEAD"] {
			t.Fatalf("head.Advertised=%v contradicts the ref list", result.Head.Advertised)
		}
		// A surviving symref never contradicts an advertised target, and a
		// target the server did not advertise is never invented.
		oids := map[string]string{}
		for _, ref := range result.Refs {
			oids[ref.Name] = ref.OID
		}
		for _, symref := range result.Symrefs {
			source, hasSource := oids[symref.Name]
			target, hasTarget := oids[symref.Target]
			if hasSource && hasTarget && source != target {
				t.Fatalf("symref %q and its target %q kept different object IDs", symref.Name, symref.Target)
			}
		}
		if len(result.Capabilities) == 0 {
			t.Fatal("a parsed advertisement always carries at least one capability")
		}
	})
}
