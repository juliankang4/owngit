package actions

import (
	"errors"
	"testing"
	"time"
)

func TestCron(t *testing.T) {
	for _, test := range []struct {
		name, cron, at, next, latest, code string
		often                              bool
	}{
		{name: "five minute boundary", cron: "*/5 * * * *", at: "2026-10-09T12:04:59Z", next: "2026-10-09T12:05:00Z", latest: "2026-10-09T12:00:00Z"},
		{name: "strict next", cron: "0 * * * *", at: "2026-10-09T12:00:00Z", next: "2026-10-09T13:00:00Z", latest: "2026-10-09T12:00:00Z"},
		{name: "UTC conversion", cron: "0 0 * * *", at: "2026-10-10T09:01:00+09:00", next: "2026-10-11T00:00:00Z", latest: "2026-10-10T00:00:00Z"},
		{name: "day OR", cron: "0 0 15 * MON", at: "2026-10-12T00:00:00Z", next: "2026-10-15T00:00:00Z", latest: "2026-10-12T00:00:00Z"},
		{name: "weekday only", cron: "0 0 * * SUN", at: "2026-10-09T00:00:00Z", next: "2026-10-11T00:00:00Z", latest: "2026-10-04T00:00:00Z"},
		{name: "Sunday seven", cron: "0 0 * * 7", at: "2026-10-09T00:00:00Z", next: "2026-10-11T00:00:00Z"},
		{name: "names ranges lists", cron: "2,7-17/5 1-3 * JAN,MAR mon-fri", at: "2026-12-31T23:00:00Z", next: "2027-01-01T01:02:00Z"},
		{name: "number with step", cron: "3/10 0 * * *", at: "2026-10-09T00:04:00Z", next: "2026-10-09T00:13:00Z"},
		{name: "long leap gap catch-up", cron: "0 0 29 FEB *", at: "2102-03-01T00:00:00Z", next: "2104-02-29T00:00:00Z", latest: "2096-02-29T00:00:00Z"},
		{name: "leap day", cron: "0 0 29 FEB *", at: "2025-03-01T00:00:00Z", next: "2028-02-29T00:00:00Z"},
		{name: "month end", cron: "0 0 31 * *", at: "2026-04-01T00:00:00Z", next: "2026-05-31T00:00:00Z"},
		{name: "impossible day but weekday OR", cron: "0 0 31 FEB MON", at: "2026-02-01T00:00:00Z", next: "2026-02-02T00:00:00Z"},
		{name: "every minute", cron: "* * * * *", at: "2026-10-09T12:00:00Z", next: "2026-10-09T12:01:00Z", often: true},
		{name: "midnight close", cron: "0,59 0,23 * * *", at: "2026-10-09T23:59:00Z", next: "2026-10-10T00:00:00Z", often: true},
		{name: "weekly midnight not close", cron: "0,59 0,23 * * MON", at: "2026-10-12T23:59:00Z", next: "2026-10-19T00:00:00Z"},
		{name: "impossible date", cron: "0 0 31 FEB *", code: "workflow.cron_never"},
		{name: "six fields", cron: "0 0 0 * * *", code: "workflow.cron"},
		{name: "macro", cron: "@daily", code: "workflow.cron"},
		{name: "zero step", cron: "*/0 * * * *", code: "workflow.cron"},
		{name: "out of range", cron: "60 * * * *", code: "workflow.cron"},
		{name: "backwards range", cron: "9-2 * * * *", code: "workflow.cron"},
		{name: "empty list", cron: "1, * * * *", code: "workflow.cron"},
		{name: "unknown name", cron: "0 0 * FOO *", code: "workflow.cron"},
		{name: "multiple steps", cron: "*/2/3 * * * *", code: "workflow.cron"},
		{name: "five year bound", cron: "0 0 29 FEB *", at: "2096-03-01T00:00:00Z", code: "workflow.cron_never"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cron, err := ParseCron(test.cron)
			var next time.Time
			var at time.Time
			if err == nil {
				at, err = time.Parse(time.RFC3339, test.at)
				if err != nil {
					t.Fatal(err)
				}
				next, err = cron.Next(at)
			}
			if test.code != "" {
				var refusal *Refusal
				if !errors.As(err, &refusal) || refusal.Code != test.code {
					t.Fatalf("error=%v, want %s", err, test.code)
				}
				return
			}
			if err != nil || next.Format(time.RFC3339) != test.next {
				t.Fatalf("next=%s error=%v, want %s", next, err, test.next)
			}
			if cron.Often(at) != test.often {
				t.Fatalf("often=%v, want %v", cron.Often(at), test.often)
			}
			if test.latest != "" {
				latest, err := cron.Latest(at)
				if err != nil || latest.Format(time.RFC3339) != test.latest {
					t.Fatalf("latest=%s error=%v", latest, err)
				}
			}
		})
	}
}
