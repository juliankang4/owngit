package actions

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron is a five-field UTC schedule. Day fields use cron's OR rule when both
// are restricted. Forward searches stop after five calendar years.
type Cron struct {
	fields            [5][]bool
	dayStar, weekStar bool
	text              string
}

func ParseCron(text string) (Cron, error) {
	var cron Cron
	parts := strings.Fields(text)
	if len(parts) != 5 || len(text) > 512 {
		return cron, refuse("workflow.cron", "on.schedule.cron", 0, "Use a five-field UTC cron expression.")
	}
	bounds := [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	for i, part := range parts {
		values, err := cronField(part, bounds[i][0], bounds[i][1], i)
		if err != nil {
			return Cron{}, refuse("workflow.cron", "on.schedule.cron", 0, err.Error())
		}
		cron.fields[i] = values
	}
	cron.text = text
	cron.dayStar, cron.weekStar = strings.HasPrefix(parts[2], "*"), strings.HasPrefix(parts[4], "*")
	if cron.fields[4][7] {
		cron.fields[4][0] = true
	}
	if _, err := cron.Next(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		return Cron{}, err
	}
	return cron, nil
}

func cronField(text string, low, high, field int) ([]bool, error) {
	values := make([]bool, high+1)
	number := func(s string) (int, error) {
		if field == 3 || field == 4 {
			names := []string{"JAN", "FEB", "MAR", "APR", "MAY", "JUN", "JUL", "AUG", "SEP", "OCT", "NOV", "DEC"}
			base := 1
			if field == 4 {
				names, base = []string{"SUN", "MON", "TUE", "WED", "THU", "FRI", "SAT"}, 0
			}
			for i, name := range names {
				if strings.EqualFold(s, name) {
					return i + base, nil
				}
			}
		}
		if s == "" {
			return 0, fmt.Errorf("Cron has an empty value.")
		}
		for _, ch := range s {
			if ch < '0' || ch > '9' {
				return 0, fmt.Errorf("Cron values must be numbers or month and weekday names.")
			}
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < low || n > high {
			return 0, fmt.Errorf("Cron value %q is outside %d to %d.", s, low, high)
		}
		return n, nil
	}
	for _, item := range strings.Split(text, ",") {
		rangeText, stepText, stepped := strings.Cut(item, "/")
		step := 1
		if stepped {
			var err error
			step, err = strconv.Atoi(stepText)
			if err != nil || step < 1 || step > high-low+1 {
				return nil, fmt.Errorf("Cron step must be between 1 and %d.", high-low+1)
			}
		}
		start, end := low, high
		if rangeText != "*" {
			first, last, ranged := strings.Cut(rangeText, "-")
			var err error
			start, err = number(first)
			if err != nil {
				return nil, err
			}
			end = start
			if ranged {
				end, err = number(last)
				if err != nil {
					return nil, err
				}
			} else if stepped {
				end = high
			}
			if start > end {
				return nil, fmt.Errorf("Cron ranges must increase.")
			}
		}
		for n := start; n <= end; n += step {
			values[n] = true
		}
	}
	return values, nil
}

func (cron Cron) matchesDay(day time.Time) bool {
	if !cron.fields[3][int(day.Month())] {
		return false
	}
	dom, dow := cron.fields[2][day.Day()], cron.fields[4][int(day.Weekday())]
	if !cron.dayStar && !cron.weekStar {
		return dom || dow
	}
	return dom && dow
}

// Next returns the first minute strictly after after.
func (cron Cron) Next(after time.Time) (time.Time, error) { return cron.search(after, false) }

// Latest returns the last matching minute at or before at, without replaying
// each missed slot after an outage.
func (cron Cron) Latest(at time.Time) (time.Time, error) { return cron.search(at, true) }

func (cron Cron) search(at time.Time, backwards bool) (time.Time, error) {
	at = at.UTC()
	day := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
	limit, direction := at.AddDate(5, 0, 0), 1
	if backwards {
		// Leap days can be eight years apart across a non-leap century.
		limit, direction = at.AddDate(-8, 0, 0), -1
	}
	for (!backwards && !day.After(limit)) || (backwards && !day.Add(24*time.Hour).Before(limit)) {
		if cron.matchesDay(day) {
			for hi := 0; hi < 24; hi++ {
				h := hi
				if backwards {
					h = 23 - hi
				}
				if !cron.fields[1][h] {
					continue
				}
				for mi := 0; mi < 60; mi++ {
					m := mi
					if backwards {
						m = 59 - mi
					}
					if !cron.fields[0][m] {
						continue
					}
					candidate := day.Add(time.Duration(h*60+m) * time.Minute)
					if backwards && !candidate.After(at) && !candidate.Before(limit) || !backwards && candidate.After(at) && !candidate.After(limit) {
						return candidate, nil
					}
				}
			}
		}
		day = day.AddDate(0, 0, direction)
	}
	return time.Time{}, refuse("workflow.cron_never", "on.schedule.cron", 0, "This cron has no matching time within five years. Use a possible UTC date.", map[string]string{"cron": cron.text})
}

// Often reports whether this cron can request two admissions less than five
// minutes apart within the next five years.
func (cron Cron) Often(now time.Time) bool {
	first, last := -1, -1
	for h := 0; h < 24; h++ {
		if cron.fields[1][h] {
			for m := 0; m < 60; m++ {
				if cron.fields[0][m] {
					slot := h*60 + m
					if last >= 0 && slot-last < 5 {
						return true
					}
					if first < 0 {
						first = slot
					}
					last = slot
				}
			}
		}
	}
	if first < 0 || 1440-last+first >= 5 {
		return false
	}
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	for end := day.AddDate(5, 0, 0); !day.After(end); day = day.AddDate(0, 0, 1) {
		if cron.matchesDay(day) && cron.matchesDay(day.AddDate(0, 0, 1)) {
			return true
		}
	}
	return false
}
