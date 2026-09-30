package importfetch

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"owngit/internal/importgit"
)

func TestAdvertisementLimitNormalization(t *testing.T) {
	for _, input := range []importgit.Limits{{}, {MaxNameBytes: 25}, {MaxPacketBytes: 65520, MaxTotalBytes: 4096, MaxRefRecords: 3, MaxNameBytes: 99, MaxCapabilities: 2, MaxCapabilityBytes: 33}} {
		want, err := input.Effective()
		if err != nil {
			t.Fatal(err)
		}
		got, err := EffectiveLimits(Limits{Advertisement: input})
		if err != nil || got.Advertisement != want || got.MaxPackBytes != DefaultLimits().MaxPackBytes {
			t.Fatalf("effective limits = %+v, %v, want advertisement %+v", got, err, want)
		}
		body := testAdvertisement(testRef(testSHA1A, "HEAD", "ofs-delta"))
		parsed, err := importgit.Parse(strings.NewReader(body), importgit.Options{Limits: input})
		if err != nil || parsed.EffectiveLimits != want {
			t.Fatalf("parser limits = %+v, %v", parsed, err)
		}
	}
	source := &scriptedSource{}
	_, request := startSource(t, source)
	var calls atomic.Int32
	previous := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		calls.Add(1)
		return nil, errors.New("unexpected DNS lookup")
	}}
	t.Cleanup(func() { net.DefaultResolver = previous })
	for _, limits := range []importgit.Limits{
		{MaxPacketBytes: -1}, {MaxTotalBytes: -1}, {MaxRefRecords: -1}, {MaxNameBytes: -1},
		{MaxCapabilities: -1}, {MaxCapabilityBytes: -1}, {MaxPacketBytes: 65521},
	} {
		if _, err := importgit.Parse(strings.NewReader(""), importgit.Options{Limits: limits}); !errors.Is(err, importgit.ErrInvalidLimits) {
			t.Fatalf("parser invalid limits = %v", err)
		}
		for _, target := range []string{request.URL, "https://limits.invalid/repo.git", "invalid URL", "invalid authentication"} {
			input := request
			input.URL, input.Limits.Advertisement = target, limits
			if target == "invalid authentication" {
				input.URL = request.URL
				input.Authentication.BearerToken = "\n"
			}
			_, err := Fetch(context.Background(), input, nil)
			if !errors.Is(err, ErrInvalidRequest) || errors.Is(err, importgit.ErrInvalidLimits) || !strings.Contains(err.Error(), "validate advertisement limits") {
				t.Fatalf("fetch invalid limits = %v", err)
			}
		}
	}
	get, post, _ := source.counts()
	if calls.Load() != 0 || get != 0 || post != 0 {
		t.Fatalf("invalid limits reached network: DNS=%d GET=%d POST=%d", calls.Load(), get, post)
	}
}
