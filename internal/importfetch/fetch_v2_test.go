package importfetch

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"owngit/internal/importgit"
)

// testV2Capabilities is GitHub's protocol v2 answer to discovery.
func testV2Capabilities(objectFormat string) string {
	return testPacket("# service=git-upload-pack\n") + "0000" + testPacket("version 2\n") +
		testPacket("agent=git/github-test\n") + testPacket("ls-refs=unborn\n") +
		testPacket("fetch=shallow wait-for-done filter\n") + testPacket("server-option\n") +
		testPacket("object-format="+objectFormat+"\n") + "0000"
}

// testBand frames one side-band packet.
func testBand(band byte, payload string) string {
	return testPacket(string([]byte{band}) + payload)
}

func v2Source(t *testing.T, lsRefs []string, packResponse string) *scriptedSource {
	return &scriptedSource{
		t: t, v2: true, advertisement: testV2Capabilities("sha1"),
		lsRefsResponse: strings.Join(lsRefs, "") + "0000", postResponse: packResponse,
	}
}

var testV2Refs = []string{
	testPacket(testSHA1A + " HEAD symref-target:refs/heads/main\n"),
	testPacket(testSHA1A + " refs/heads/main\n"),
	testPacket(testSHA1B + " refs/tags/v1 peeled:" + testSHA1A + "\n"),
}

// A v2 source is asked only for HEAD, branches and tags, and its pack is read
// from side-band 1 of the packfile section.
func TestFetchV2ListsImportedRefsAndReadsSideBandPack(t *testing.T) {
	source := v2Source(t, testV2Refs, "000dpackfile\n"+testBand(1, "PA")+testBand(2, "Counting objects\n")+
		testBand(1, "")+testBand(1, "CKpayload")+"0000")
	_, request := startSource(t, source)
	var pack string
	result, err := Fetch(context.Background(), request, func(_ context.Context, advertisement *importgit.Advertisement, reader io.Reader) error {
		content, readErr := io.ReadAll(reader)
		pack = string(content)
		return readErr
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if pack != "PACKpayload" || result.PackBytes != int64(len(pack)) {
		t.Fatalf("pack = %q, bytes = %d", pack, result.PackBytes)
	}
	advertisement := result.Advertisement
	if advertisement.ProtocolVersion != 2 || len(advertisement.Refs) != 3 || advertisement.Head.SymrefTarget != "refs/heads/main" ||
		advertisement.Refs[2].PeeledOID != testSHA1A {
		t.Fatalf("advertisement = %+v", advertisement)
	}
	gets, posts, fetchBody := source.counts()
	wantLsRefs := testPacket("command=ls-refs\n") + testPacket("object-format=sha1\n") + "0001" +
		testPacket("symrefs\n") + testPacket("peel\n") + testPacket("ref-prefix HEAD\n") +
		testPacket("ref-prefix refs/heads/\n") + testPacket("ref-prefix refs/tags/\n") + "0000"
	wantFetch := testPacket("command=fetch\n") + testPacket("object-format=sha1\n") + "0001" +
		testPacket("ofs-delta\n") + testPacket("no-progress\n") +
		testPacket("want "+testSHA1A+"\n") + testPacket("want "+testSHA1B+"\n") + testPacket("done\n") + "0000"
	if gets != 1 || posts != 2 || source.lsRefsBody != wantLsRefs || fetchBody != wantFetch {
		t.Fatalf("requests %d/%d\nls-refs %q\nwant    %q\nfetch %q\nwant  %q", gets, posts, source.lsRefsBody, wantLsRefs, fetchBody, wantFetch)
	}
}

func TestFetchV2SHA256AndEmptySource(t *testing.T) {
	source := v2Source(t, []string{testPacket(testSHA256A + " refs/heads/main\n")}, "000dpackfile\n"+testBand(1, "PACK")+"0000")
	source.advertisement = testV2Capabilities("sha256")
	_, request := startSource(t, source)
	if _, err := Fetch(context.Background(), request, consumeAll); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if _, _, body := source.counts(); !strings.HasPrefix(body, testPacket("command=fetch\n")+testPacket("object-format=sha256\n")+"0001") ||
		!strings.HasPrefix(source.lsRefsBody, testPacket("command=ls-refs\n")+testPacket("object-format=sha256\n")+"0001") {
		t.Fatalf("commands do not name sha256: %q %q", source.lsRefsBody, body)
	}

	empty := v2Source(t, nil, "")
	_, request = startSource(t, empty)
	result, err := Fetch(context.Background(), request, func(context.Context, *importgit.Advertisement, io.Reader) error {
		t.Error("consumer invoked for an empty source")
		return nil
	})
	if gets, posts, _ := empty.counts(); err != nil || !result.Advertisement.Empty || gets != 1 || posts != 1 {
		t.Fatalf("empty source: %v, requests %d/%d", err, gets, posts)
	}
}

// The ls-refs answer has the advertisement's limits and validation.
func TestFetchV2RefListFailures(t *testing.T) {
	// A server may list refs outside the prefixes; they are neither wanted
	// nor returned.
	pull := v2Source(t, append([]string{testPacket(testSHA1B + " refs/pull/1/head\n")}, testV2Refs[:2]...), "000dpackfile\n"+testBand(1, "PACK")+"0000")
	_, request := startSource(t, pull)
	result, err := Fetch(context.Background(), request, consumeAll)
	if err != nil || len(result.Advertisement.Refs) != 2 {
		t.Fatalf("unrequested ref: %+v, %v", result, err)
	}
	if _, posts, body := pull.counts(); posts != 2 || strings.Contains(body, testSHA1B) {
		t.Fatalf("posts = %d, fetch %q wants the unrequested ref", posts, body)
	}
	malformed := v2Source(t, []string{testPacket(testZero1 + " refs/pull/1/head\n")}, "")
	_, request = startSource(t, malformed)
	if _, err := Fetch(context.Background(), request, consumeAll); !errors.Is(err, ErrAdvertisement) || !errors.Is(err, importgit.ErrInvalidObjectID) {
		t.Fatalf("malformed unrequested ref: %v", err)
	}

	many := v2Source(t, testV2Refs, "")
	_, request = startSource(t, many)
	request.Limits.Advertisement.MaxRefRecords = 2
	if _, err := Fetch(context.Background(), request, consumeAll); !errors.Is(err, ErrResponseTooLarge) || !errors.Is(err, importgit.ErrTooManyRefs) {
		t.Fatalf("ref limit: %v", err)
	}

	large := v2Source(t, testV2Refs, "")
	_, request = startSource(t, large)
	request.Limits.Advertisement.MaxTotalBytes = int64(len(large.lsRefsResponse)) - 1
	if _, err := Fetch(context.Background(), request, consumeAll); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("ls-refs byte limit: %v", err)
	}

	noFetch := v2Source(t, testV2Refs, "")
	noFetch.advertisement = testPacket("version 2\n") + testPacket("ls-refs\n") + "0000"
	_, request = startSource(t, noFetch)
	if _, err := Fetch(context.Background(), request, consumeAll); !errors.Is(err, ErrAdvertisement) {
		t.Fatalf("no fetch command: %v", err)
	}
	if _, posts, _ := noFetch.counts(); posts != 0 {
		t.Fatalf("posts = %d, want none", posts)
	}
}

func TestFetchV2PackResponseFailures(t *testing.T) {
	const section = "000dpackfile\n"
	for _, test := range []struct {
		name     string
		response string
	}{
		{"no section", testBand(1, "PACKbody") + "0000"},
		{"shallow-info", testPacket("shallow-info\n") + testPacket("shallow "+testSHA1A+"\n") + "0001" + section + testBand(1, "PACK") + "0000"},
		{"acknowledgments", testPacket("acknowledgments\n") + testPacket("NAK\n") + "0000"},
		{"remote error", section + testBand(3, "remote detail must not escape") + "0000"},
		{"remote error in pack", section + testBand(1, "PACKbo") + testBand(3, "remote detail must not escape") + "0000"},
		{"remote error after pack", section + testBand(1, "PACKbody") + testBand(3, "remote detail must not escape") + "0000"},
		{"unknown band", section + testBand(4, "PACK") + "0000"},
		{"empty packet", section + "0004" + "0000"},
		{"delimiter", section + testBand(1, "PACK") + "0001"},
		{"uppercase length", section + "000A\x01PACKb" + "0000"},
		{"oversized packet", section + "fff1\x01PACK"},
		{"no flush", section + testBand(1, "PACKbody")},
		{"cut packet", section + "0010\x01PACK"},
		{"trailing bytes", section + testBand(1, "PACKbody") + "0000" + "x"},
		{"not a pack", section + testBand(1, "JUNK") + "0000"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := v2Source(t, testV2Refs, test.response)
			_, request := startSource(t, source)
			_, err := Fetch(context.Background(), request, consumeAll)
			if !errors.Is(err, ErrUploadPackProtocol) {
				t.Fatalf("error = %v, want upload-pack protocol failure", err)
			}
			if strings.Contains(err.Error(), "remote detail") {
				t.Fatalf("error disclosed source response: %v", err)
			}
		})
	}
}

// A consumer that fails because the server broke the side-band is not an
// indexing failure; the pack bound and the body budget still apply.
func TestFetchV2PackLimitsAndConsumer(t *testing.T) {
	response := "000dpackfile\n" + testBand(1, "PACKextra") + "0000"
	start := func(t *testing.T, response string) Request {
		_, request := startSource(t, v2Source(t, testV2Refs, response))
		return request
	}
	failing := func(_ context.Context, _ *importgit.Advertisement, reader io.Reader) error {
		_, err := io.Copy(io.Discard, reader)
		if err == nil {
			err = errors.New("index-pack failed")
		}
		return err
	}
	request := start(t, "000dpackfile\n"+testBand(1, "PACKext")+testBand(3, "remote detail")+"0000")
	if _, err := Fetch(context.Background(), request, failing); !errors.Is(err, ErrUploadPackProtocol) || errors.Is(err, ErrConsumer) {
		t.Fatalf("band 3 during the pack = %v, want upload-pack protocol failure", err)
	}
	request = start(t, response)
	if _, err := Fetch(context.Background(), request, failing); !errors.Is(err, ErrConsumer) {
		t.Fatalf("consumer failure on a complete pack = %v", err)
	}
	request = start(t, response)
	request.Limits.MaxPackBytes = 4
	if _, err := Fetch(context.Background(), request, consumeAll); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("pack bound = %v", err)
	}
	// Progress text counts toward the body budget like any response byte.
	request = start(t, "000dpackfile\n"+strings.Repeat(testBand(2, strings.Repeat("p", 1000)), 20)+testBand(1, "PACK")+"0000")
	request.Limits.MaxTotalBodyBytes = 16 << 10
	if _, err := Fetch(context.Background(), request, consumeAll); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("body budget = %v", err)
	}
	request = start(t, response)
	if _, err := Fetch(context.Background(), request, func(context.Context, *importgit.Advertisement, io.Reader) error { return nil }); !errors.Is(err, ErrConsumerStoppedEarly) {
		t.Fatalf("consumer stopped early = %v", err)
	}
}

// The response bound in a Content-Length allows side-band framing around a
// pack of the largest size and no more.
func TestSidebandOverhead(t *testing.T) {
	for _, pack := range []int64{0, 1, 65515, 65516, 16 << 30} {
		// Full band-1 packets carry 65515 bytes each; count them by
		// repeated subtraction rather than the formula under test.
		packets := int64(0)
		for remaining := pack; remaining > 0; remaining -= 65515 {
			packets++
		}
		minimum := int64(len("000dpackfile\n")) + 5*packets + 4
		if got := sidebandOverhead(pack); got < minimum || got > minimum+5 {
			t.Fatalf("overhead(%d) = %d, want %d to %d", pack, got, minimum, minimum+5)
		}
	}
}
