// Package releasecheck asks GitHub whether a newer OwnGit release exists.
//
// Apart from imports, which reach only the source hosts an owner configures,
// this is OwnGit's only outbound connection. It sends one GET request with a
// User-Agent naming OwnGit and its version, and nothing about repositories.
// The result lives in memory only. Every failure is silent to users: it never
// affects Git, pages, or startup, and it is logged at most once per streak of
// failures.
package releasecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"
)

const (
	// LatestURL returns the newest published release. GitHub excludes drafts
	// and prereleases from this endpoint.
	LatestURL = "https://api.github.com/repos/juliankang4/owngit/releases/latest"
	// releasePageBase is where release notes live. The notes link is built
	// from the parsed tag, never taken from the response.
	releasePageBase = "https://github.com/juliankang4/owngit/releases/tag/"
	// UpdateGuideURL is the README Install section, which explains how each
	// install route updates.
	UpdateGuideURL = "https://github.com/juliankang4/owngit#install"

	// DefaultInitialDelay keeps the first request out of startup work.
	DefaultInitialDelay = 30 * time.Second
	// DefaultInterval is the time between checks.
	DefaultInterval = 24 * time.Hour
	// requestTimeout bounds one request, including reading the body.
	requestTimeout = 10 * time.Second
	// maxResponseBytes bounds the answer. A real answer is about 14 KiB.
	maxResponseBytes = 256 << 10
)

// Release is a published version newer than the running one.
type Release struct {
	// Version is "X.Y.Z" without the leading "v".
	Version string
	// NotesURL is the release page under the OwnGit releases path.
	NotesURL string
}

// Checker runs the periodic check and holds its latest result.
type Checker struct {
	// Current is the running version, "X.Y.Z".
	Current string
	// URL is the endpoint. Empty uses LatestURL. Tests point it at a local
	// server so they never contact GitHub.
	URL string
	// Client sends the request. Nil uses a client that follows no redirects.
	Client *http.Client
	// InitialDelay and Interval default to DefaultInitialDelay and
	// DefaultInterval when zero.
	InitialDelay time.Duration
	Interval     time.Duration
	// Enabled reports the saved on/off setting before each check. Nil means
	// always on. An error skips the check.
	Enabled func(context.Context) (bool, error)
	// Logf receives at most one line per failure streak.
	Logf func(string, ...any)

	mu      sync.Mutex
	newer   Release
	found   bool
	failing bool
	wake    chan struct{}
	once    sync.Once
}

// Newer returns the latest known release when it is newer than Current.
func (checker *Checker) Newer() (Release, bool) {
	checker.mu.Lock()
	defer checker.mu.Unlock()
	return checker.newer, checker.found
}

// Wake asks a running checker to check soon. It never blocks.
func (checker *Checker) Wake() {
	select {
	case checker.wakeChannel() <- struct{}{}:
	default:
	}
}

func (checker *Checker) wakeChannel() chan struct{} {
	checker.once.Do(func() { checker.wake = make(chan struct{}, 1) })
	return checker.wake
}

// Run checks after the initial delay, then once per interval or when woken,
// until ctx ends.
func (checker *Checker) Run(ctx context.Context) {
	timer := time.NewTimer(durationOr(checker.InitialDelay, DefaultInitialDelay))
	defer timer.Stop()
	wake := checker.wakeChannel()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-wake:
		}
		checker.CheckIfEnabled(ctx)
		timer.Reset(durationOr(checker.Interval, DefaultInterval))
	}
}

// CheckIfEnabled runs one check when the saved setting allows it. When the
// setting is off, it forgets any earlier result instead.
func (checker *Checker) CheckIfEnabled(ctx context.Context) {
	if checker.Enabled != nil {
		enabled, err := checker.Enabled(ctx)
		if err != nil {
			return
		}
		if !enabled {
			checker.mu.Lock()
			checker.newer, checker.found = Release{}, false
			checker.mu.Unlock()
			return
		}
	}
	_ = checker.Check(ctx)
}

// Check asks for the latest release once and records the answer. A failure
// keeps the earlier result, because a release that was newer stays newer.
func (checker *Checker) Check(ctx context.Context) error {
	release, newer, err := checker.fetch(ctx)
	checker.mu.Lock()
	defer checker.mu.Unlock()
	if err != nil {
		if !checker.failing && checker.Logf != nil && ctx.Err() == nil {
			checker.Logf("release check failed; OwnGit keeps working and tries again later: %v", err)
		}
		checker.failing = true
		return err
	}
	checker.failing = false
	checker.newer, checker.found = release, newer
	return nil
}

func (checker *Checker) fetch(ctx context.Context) (Release, bool, error) {
	current, err := parseVersion(checker.Current)
	if err != nil {
		return Release{}, false, fmt.Errorf("running version %q: %w", checker.Current, err)
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	endpoint := checker.URL
	if endpoint == "" {
		endpoint = LatestURL
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Release{}, false, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "OwnGit/"+checker.Current+" (+https://github.com/juliankang4/owngit)")
	client := checker.Client
	if client == nil {
		client = defaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return Release{}, false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Release{}, false, fmt.Errorf("unexpected status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return Release{}, false, err
	}
	if len(body) > maxResponseBytes {
		return Release{}, false, errTooLarge
	}
	var answer struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return Release{}, false, fmt.Errorf("decode answer: %w", err)
	}
	if answer.Draft || answer.Prerelease {
		return Release{}, false, errors.New("answer is a draft or prerelease")
	}
	if len(answer.TagName) < 2 || answer.TagName[0] != 'v' {
		return Release{}, false, fmt.Errorf("tag %q is not vX.Y.Z", answer.TagName)
	}
	latest, err := parseVersion(answer.TagName[1:])
	if err != nil {
		return Release{}, false, fmt.Errorf("tag %q: %w", answer.TagName, err)
	}
	if !latest.after(current) {
		return Release{}, false, nil
	}
	name := latest.String()
	return Release{Version: name, NotesURL: releasePageBase + "v" + name}, true, nil
}

var errTooLarge = fmt.Errorf("answer is larger than %d bytes", maxResponseBytes)

// defaultClient follows no redirects, so a moved endpoint is a failure rather
// than a request to an unreviewed host.
var defaultClient = &http.Client{
	Timeout:       requestTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func durationOr(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

// version is a strict "X.Y.Z" release number.
type version [3]int

// Each part is a decimal number without leading zeros and at most nine digits,
// so it always fits in an int.
var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$`)

func parseVersion(value string) (version, error) {
	match := versionPattern.FindStringSubmatch(value)
	if match == nil {
		return version{}, errors.New("not a X.Y.Z version")
	}
	var parsed version
	for index := range parsed {
		parsed[index], _ = strconv.Atoi(match[index+1])
	}
	return parsed, nil
}

// ValidVersion reports whether value is a strict "X.Y.Z" version.
func ValidVersion(value string) bool {
	_, err := parseVersion(value)
	return err == nil
}

func (left version) after(right version) bool {
	for index := range left {
		if left[index] != right[index] {
			return left[index] > right[index]
		}
	}
	return false
}

func (value version) String() string {
	return fmt.Sprintf("%d.%d.%d", value[0], value[1], value[2])
}
