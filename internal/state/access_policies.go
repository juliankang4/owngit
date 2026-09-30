package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Access policies: whether deleting a repository asks for its typed name,
// how many wrong passwords pause an address, and whether the shared
// sign-in survives a link from another site. Like the other server-wide
// policies they are metadata rows, machine-local, read where they apply.

const (
	deleteRequiresNameKey = "delete_requires_name"
	loginLimitsKey        = "login_limits"
	crossSiteLinksKey     = "cross_site_links"
)

// DeleteRequiresName reports whether deleting a repository asks for its
// name to be typed. It is on while nothing was saved.
func (s *Store) DeleteRequiresName(ctx context.Context) (bool, error) {
	raw, found, err := policyValue(ctx, s.db, deleteRequiresNameKey)
	if err != nil || !found {
		return true, err
	}
	switch raw {
	case "on":
		return true, nil
	case "off":
		return false, nil
	}
	return false, &PolicyError{Key: deleteRequiresNameKey, Value: raw, Cause: errors.New(`neither "on" nor "off"`)}
}

// LoginLimits pause password checks from one address: the Attempts-th
// wrong password of one kind (shared or administrator) within Window
// refuses every check of that kind from the address for Pause. A pause
// already recorded keeps its end when the limits change.
type LoginLimits struct {
	Attempts int
	Window   time.Duration
	Pause    time.Duration
}

// The bounds of the login limits. Every limit stays finite: there is no
// value that turns the pause off.
const (
	MinimumLoginAttempts = 1
	MaximumLoginAttempts = 100
	MinimumLoginDuration = time.Minute
	MaximumLoginDuration = 24 * time.Hour
)

// DefaultLoginLimits apply while nothing was saved: 4 wrong passwords in
// 10 minutes pause the address for 15 minutes.
var DefaultLoginLimits = LoginLimits{Attempts: 4, Window: 10 * time.Minute, Pause: 15 * time.Minute}

// Validate checks the limits against their bounds.
func (l LoginLimits) Validate() error {
	switch {
	case l.Attempts < MinimumLoginAttempts || l.Attempts > MaximumLoginAttempts:
		return errors.New("the number of attempts is from 1 to 100")
	case !validLoginDuration(l.Window):
		return errors.New("the attempt window is a whole number of seconds from 1 minute to 24 hours")
	case !validLoginDuration(l.Pause):
		return errors.New("the pause is a whole number of seconds from 1 minute to 24 hours")
	}
	return nil
}

func validLoginDuration(d time.Duration) bool {
	return d >= MinimumLoginDuration && d <= MaximumLoginDuration && d%time.Second == 0
}

// LoginSeconds converts a number of seconds to a login window or pause,
// comparing it with both bounds before converting it so that no value can
// overflow into a valid one.
func LoginSeconds(seconds int64) (time.Duration, error) {
	if seconds < int64(MinimumLoginDuration/time.Second) || seconds > int64(MaximumLoginDuration/time.Second) {
		return 0, errors.New("a login window or pause is a whole number of seconds from 1 minute to 24 hours")
	}
	return time.Duration(seconds) * time.Second, nil
}

// Looser reports whether l lets an address try more passwords than the
// defaults in some way: more attempts, a shorter window or a shorter
// pause.
func (l LoginLimits) Looser() bool {
	return l.Attempts > DefaultLoginLimits.Attempts || l.Window < DefaultLoginLimits.Window || l.Pause < DefaultLoginLimits.Pause
}

// loginLimitsJSON is the stored form of LoginLimits; a missing field means
// its default.
type loginLimitsJSON struct {
	Attempts      *int   `json:"attempts,omitempty"`
	WindowSeconds *int64 `json:"window_seconds,omitempty"`
	PauseSeconds  *int64 `json:"pause_seconds,omitempty"`
}

// LoginLimits returns the limits a wrong password is counted with now.
func (s *Store) LoginLimits(ctx context.Context) (LoginLimits, error) {
	return loginLimits(ctx, s.db)
}

// loginLimits reads the limits with query, the store or a transaction.
func loginLimits(ctx context.Context, query querier) (LoginLimits, error) {
	raw, found, err := policyValue(ctx, query, loginLimitsKey)
	if err != nil || !found {
		return DefaultLoginLimits, err
	}
	limits := DefaultLoginLimits
	var stored loginLimitsJSON
	err = decodeStoredJSON(raw, &stored)
	if err == nil && stored.Attempts != nil {
		limits.Attempts = *stored.Attempts
	}
	if err == nil && stored.WindowSeconds != nil {
		limits.Window, err = LoginSeconds(*stored.WindowSeconds)
	}
	if err == nil && stored.PauseSeconds != nil {
		limits.Pause, err = LoginSeconds(*stored.PauseSeconds)
	}
	if err == nil {
		err = limits.Validate()
	}
	if err != nil {
		return LoginLimits{}, &PolicyError{Key: loginLimitsKey, Value: raw, Cause: err}
	}
	return limits, nil
}

func (l LoginLimits) stored() (string, error) {
	window, pause := int64(l.Window/time.Second), int64(l.Pause/time.Second)
	encoded, err := json.Marshal(loginLimitsJSON{Attempts: &l.Attempts, WindowSeconds: &window, PauseSeconds: &pause})
	return string(encoded), err
}

// CrossSiteLinks decides whether a link from another site opens OwnGit
// with the shared sign-in (the general session cookie's SameSite). The
// administrator, setup and form token cookies stay Strict either way.
type CrossSiteLinks string

const (
	// CrossSiteStrict asks for the page to be opened again from OwnGit:
	// the sign-in cookie is sent only by pages of OwnGit itself.
	CrossSiteStrict CrossSiteLinks = "strict"
	// CrossSiteLax keeps the shared sign-in on a link from another site
	// (SameSite=Lax): top-level navigation carries the cookie, forms and
	// embedded requests from another site still do not.
	CrossSiteLax CrossSiteLinks = "lax"

	DefaultCrossSiteLinks = CrossSiteStrict
)

// CrossSiteChoices lists the choices, the default first.
var CrossSiteChoices = []CrossSiteLinks{CrossSiteStrict, CrossSiteLax}

// ParseCrossSiteLinks returns the choice value names.
func ParseCrossSiteLinks(value string) (CrossSiteLinks, bool) {
	choice := CrossSiteLinks(value)
	return choice, choice == CrossSiteStrict || choice == CrossSiteLax
}

// CrossSiteLinks returns the choice a sign-in starting now uses.
func (s *Store) CrossSiteLinks(ctx context.Context) (CrossSiteLinks, error) {
	raw, found, err := policyValue(ctx, s.db, crossSiteLinksKey)
	if err != nil || !found {
		return DefaultCrossSiteLinks, err
	}
	if choice, valid := ParseCrossSiteLinks(raw); valid {
		return choice, nil
	}
	return "", &PolicyError{Key: crossSiteLinksKey, Value: raw, Cause: errors.New(`neither "strict" nor "lax"`)}
}

// decodeStoredJSON decodes one JSON object of known fields from a stored
// policy value. A field may be left out for its default, but the value
// must be a single object and no field may be null.
func decodeStoredJSON(raw string, value any) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return err
	}
	if fields == nil {
		return errors.New("not a JSON object")
	}
	for name, field := range fields {
		if string(field) == "null" {
			return fmt.Errorf("%s is null", name)
		}
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value)
}
