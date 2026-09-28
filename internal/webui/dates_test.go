package webui

import (
	"html/template"
	"strings"
	"testing"
	"time"
)

// Which day a commit belongs to
//
// Git stores an author date as an instant plus the UTC offset the author was
// using. Both parts matter. A commit authored at 00:30 on 17 September at
// +14:00 is the same instant as 19:30 on 16 September at +09:00, so restating
// it in the server's zone silently moves it to a different calendar day.
//
// A real repository showed the result: the activity list said "어제" next to a
// clock reading 00:30, the row read "Yesterday 19:30", and the commit detail
// said 17 September 00:30. One commit, three answers.
//
// The interface therefore shows the calendar date and clock the author
// recorded, everywhere. Now is used only as the reference calendar deciding
// what counts as today, yesterday, and the current year.

var (
	plus14      = time.FixedZone("+14", 14*60*60)
	minus11     = time.FixedZone("-11", -11*60*60)
	seoulZone   = time.FixedZone("KST", 9*60*60)
	serverNow17 = time.Date(2026, 9, 17, 12, 0, 0, 0, seoulZone)
)

// dateTexts is how one commit's date reads in one language: the activity
// group heading, the list row, and the commit detail.
type dateTexts struct{ heading, row, detail string }

func TestCommitDatesUseTheAuthorsCalendar(t *testing.T) {
	nowJan := time.Date(2026, 1, 5, 9, 0, 0, 0, seoulZone)
	for _, tc := range []struct {
		name     string
		authored time.Time
		now      time.Time
		// serverDate is the date the server's zone gives the same instant.
		// Where it differs from the authored date, converting would move the
		// commit to another day.
		serverDate string
		clock      string
		en, ko     dateTexts
	}{
		{"the reported case: just after midnight ahead of the server",
			time.Date(2026, 9, 17, 0, 30, 0, 0, plus14), serverNow17, "2026-09-16", "00:30",
			dateTexts{"Today", "Today 00:30", "Sep 17, 2026 00:30"},
			dateTexts{"오늘", "오늘 00:30", "2026년 9월 17일 00:30"}},
		// The same instant as the reported case, recorded at +09:00, is a
		// different calendar date. Comparing instants would merge the two.
		{"the same instant recorded in the server's zone",
			time.Date(2026, 9, 16, 19, 30, 0, 0, seoulZone), serverNow17, "2026-09-16", "19:30",
			dateTexts{"Yesterday", "Yesterday 19:30", "Sep 16, 2026 19:30"},
			dateTexts{"어제", "어제 19:30", "2026년 9월 16일 19:30"}},
		{"behind the server, converting would move it forward",
			time.Date(2026, 9, 17, 23, 30, 0, 0, minus11), serverNow17, "2026-09-18", "23:30",
			dateTexts{"Today", "Today 23:30", "Sep 17, 2026 23:30"},
			dateTexts{"오늘", "오늘 23:30", "2026년 9월 17일 23:30"}},
		{"yesterday is the author's previous calendar day",
			time.Date(2026, 9, 16, 2, 0, 0, 0, plus14), serverNow17, "2026-09-15", "02:00",
			dateTexts{"Yesterday", "Yesterday 02:00", "Sep 16, 2026 02:00"},
			dateTexts{"어제", "어제 02:00", "2026년 9월 16일 02:00"}},
		// The row omits the year inside the reference year; the detail always
		// states the author's year.
		{"new year's day ahead of the server belongs to the author's year",
			time.Date(2026, 1, 1, 0, 30, 0, 0, plus14), nowJan, "2025-12-31", "00:30",
			dateTexts{"Thursday, Jan 1", "Jan 1 at 00:30", "Jan 1, 2026 00:30"},
			dateTexts{"1월 1일 목요일", "1월 1일 00:30", "2026년 1월 1일 00:30"}},
		{"the old year's last day behind the server keeps its year",
			time.Date(2025, 12, 31, 23, 30, 0, 0, minus11), nowJan, "2026-01-01", "23:30",
			dateTexts{"Wednesday, Dec 31, 2025", "Dec 31, 2025", "Dec 31, 2025 23:30"},
			dateTexts{"2025년 12월 31일", "2025년 12월 31일", "2025년 12월 31일 23:30"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.authored.In(tc.now.Location()).Format("2006-01-02"); got != tc.serverDate {
				t.Fatalf("the fixture's server date is %s, want %s", got, tc.serverDate)
			}
			// The clock is the author's, not a converted one.
			if got := formatClock(tc.authored); got != tc.clock {
				t.Errorf("clock = %q, want %q", got, tc.clock)
			}
			for lang, want := range map[Lang]dateTexts{LangEN: tc.en, LangKO: tc.ko} {
				got := dateTexts{
					formatDayHeading(lang, tc.now, tc.authored),
					formatRelative(lang, tc.now, tc.authored),
					formatDateTime(lang, tc.authored),
				}
				if got != want {
					t.Errorf("%s: heading, row, detail = %q, want %q", lang, got, want)
				}
			}
		})
	}
}

// The rendered page must agree with itself.

func TestActivityPageNamesOneDayForOneCommit(t *testing.T) {
	// Group heading, list row, and the graph all describe the same commit, so
	// a reader must not see two different days for it.
	authored := time.Date(2026, 9, 17, 0, 30, 0, 0, plus14)
	r := newRenderer(t)

	for _, lang := range Langs() {
		c := fullChrome(lang)
		c.Now = serverNow17

		entry := ActivityEntry{
			RepositoryID: "r1", RepositoryName: "forge-cli", Ref: "main",
			Commit: CommitSummary{
				ShortOID: "a41c9e2", Subject: "Use stable key", AuthorName: "Dana",
				AuthorDate: authored, URL: "/repositories/r1/commits/a41c9e2ff",
			},
		}
		page := ActivityPage{
			Chrome: c,
			// The backend groups by the author's recorded calendar date.
			Days: []ActivityDayGroup{{
				Date:    time.Date(2026, 9, 17, 0, 0, 0, 0, plus14),
				Entries: []ActivityEntry{entry},
			}},
			Activity: ActivityGraph{Year: 2026, Available: true, Complete: true, Total: 1, RepositoryCount: 1},
		}

		out := render(t, r, page)

		heading := template.HTMLEscapeString(formatDayHeading(lang, serverNow17, page.Days[0].Date))
		if !strings.Contains(out, heading) {
			t.Errorf("%s: the group is not headed %q", lang, heading)
		}
		// The row must carry the author's clock, not a converted one.
		if !strings.Contains(out, "00:30") {
			t.Errorf("%s: the author's recorded time is not shown", lang)
		}
		if strings.Contains(out, "19:30") {
			t.Errorf("%s: the commit was restated in the server's zone", lang)
		}
		// And nothing may call it yesterday.
		stale := "Yesterday"
		if lang == LangKO {
			stale = "어제"
		}
		if strings.Contains(out, stale) {
			t.Errorf("%s: a commit dated today is also called yesterday", lang)
		}
	}
}

func TestCommitDetailAndListAgreeOnTheDay(t *testing.T) {
	// The same commit is shown as a row in the list and in full on the detail
	// page. Both must name 17 September.
	authored := time.Date(2026, 9, 17, 0, 30, 0, 0, plus14)
	r := newRenderer(t)

	for _, lang := range Langs() {
		c := fullChrome(lang)
		c.Now = serverNow17

		head := CommitSummary{
			ShortOID: "a41c9e2", Subject: "Use stable key", AuthorName: "Dana",
			AuthorDate: authored, URL: "/repositories/r1/commits/a41c9e2ff",
		}
		page := repoPage(c, RepoTabCommits)
		page.Commits.List = []CommitSummary{head}
		page.Commits.Detail = &CommitDetail{Commit: head}

		out := render(t, r, page)

		day := "17"
		if lang == LangEN {
			day = "Sep 17"
		} else {
			day = "9월 17일"
		}
		if !strings.Contains(out, day) {
			t.Errorf("%s: the commit detail does not name the author's date (%s)", lang, day)
		}
		if strings.Contains(out, "19:30") {
			t.Errorf("%s: the commit was restated in the server's zone", lang)
		}
	}
}

func TestGraphCaptionSaysWhoseTimeZoneIsUsed(t *testing.T) {
	// The graph counts by author date, which is only unambiguous if the
	// caption says whose clock that is. It must also keep saying activity is
	// not a check result.
	for _, lang := range Langs() {
		caption := Text(lang, MsgActivityNoChecks)

		for _, part := range map[Lang][]string{
			LangEN: {"author date", "time zone the author recorded", "checks ran or passed"},
			LangKO: {"작성 날짜", "작성자가 기록한 시간대", "체크를 실행했거나 통과했다는 뜻은 아닙니다"},
		}[lang] {
			if !strings.Contains(caption, part) {
				t.Errorf("%s: the caption never says %q: %q", lang, part, caption)
			}
		}
	}

	// And it reaches the screen.
	r := newRenderer(t)
	for _, lang := range Langs() {
		out := render(t, r, OverviewPage{Chrome: fullChrome(lang), Activity: sampleGraph()})
		if !strings.Contains(out, wantText(lang, MsgActivityNoChecks)) {
			t.Errorf("%s: the caption is not rendered", lang)
		}
	}
}

// HasDistinctCommitter compares instants on purpose: one moment recorded with
// two offsets is not two committer times. That is a different question from
// which calendar day a commit is shown on, and must not be changed by this fix.

func TestCommitterComparisonStillUsesInstants(t *testing.T) {
	at := time.Date(2026, 9, 17, 0, 30, 0, 0, plus14)
	detail := CommitDetail{
		Commit:        CommitSummary{AuthorName: "Dana", AuthorDate: at},
		CommitterName: "Dana", CommitterDate: at.In(seoulZone),
	}
	if detail.HasDistinctCommitter() {
		t.Error("the same instant in two zones is reported as a different committer time")
	}

	// A genuinely different time is still reported.
	later := CommitDetail{
		Commit:        CommitSummary{AuthorName: "Dana", AuthorDate: at},
		CommitterName: "Dana", CommitterDate: at.Add(time.Hour),
	}
	if !later.HasDistinctCommitter() {
		t.Error("a different committer time is no longer reported")
	}
}
