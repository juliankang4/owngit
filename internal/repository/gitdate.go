package repository

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// GitDateOption makes git log print %ad and %cd as Git's raw date, which
// ParseGitDate reads. for-each-ref prints the same form for
// %(authordate:raw). Every reader of commit dates uses both, so an import
// can refuse a commit whose date these pages could not show.
const GitDateOption = "--date=raw"

// maxZoneOffset is the largest offset a time.Time can carry into JSON:
// MarshalJSON refuses a zone of 24 hours or more.
const maxZoneOffset = 24*time.Hour - time.Minute

var errMalformedGitDate = errors.New("Git returned a malformed date")

// ParseGitDate reads a date Git printed in its raw form, "<seconds since the
// epoch> <offset>", such as "1312735823 +0530". The instant comes from the
// seconds, so it is always exact. Git reads the offset as hours and minutes,
// HHMM, and so does this. An offset of less than 24 hours keeps its zone.
// Git also accepts a larger one from old history, such as +051800 in
// rails/rails; that date is shown in UTC. Text in any other form is an
// error.
func ParseGitDate(raw []byte) (time.Time, error) {
	seconds, offset, found := strings.Cut(string(raw), " ")
	if !found || !allDigits(seconds) || len(offset) < 5 || (offset[0] != '+' && offset[0] != '-') || !allDigits(offset[1:]) {
		return time.Time{}, errMalformedGitDate
	}
	epoch, err := strconv.ParseInt(seconds, 10, 64)
	if err != nil {
		return time.Time{}, errMalformedGitDate
	}
	instant := time.Unix(epoch, 0).UTC()
	// Digits alone fail to parse only when they overflow, which is an offset
	// of 24 hours or more like any other.
	hhmm, err := strconv.ParseInt(offset[1:], 10, 64)
	if err != nil || hhmm/100 >= 24 {
		return instant, nil
	}
	zone := time.Duration(hhmm/100)*time.Hour + time.Duration(hhmm%100)*time.Minute
	if zone > maxZoneOffset {
		return instant, nil
	}
	if offset[0] == '-' {
		zone = -zone
	}
	if zone == 0 {
		return instant, nil
	}
	return instant.In(time.FixedZone("", int(zone/time.Second))), nil
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}
