package webui

import (
	"html/template"
	"os"
	"regexp"
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

func TestCommitAuthoredJustAfterMidnightAheadOfTheServer(t *testing.T) {
	// The reported case. In the server's zone this instant is 16 September
	// 19:30, a different day from the one the author recorded.
	authored := time.Date(2026, 9, 17, 0, 30, 0, 0, plus14)

	if got := authored.In(serverNow17.Location()).Day(); got != 16 {
		t.Fatalf("the fixture no longer crosses the server's day boundary (server day %d)", got)
	}

	for _, tc := range []struct {
		lang        Lang
		wantHeading string
		wantRow     string
		wantDetail  string
	}{
		{LangEN, "Today", "Today 00:30", "Sep 17, 2026 00:30"},
		{LangKO, "오늘", "오늘 00:30", "2026년 9월 17일 00:30"},
	} {
		if got := formatDayHeading(tc.lang, serverNow17, authored); got != tc.wantHeading {
			t.Errorf("%s: group heading = %q, want %q", tc.lang, got, tc.wantHeading)
		}
		if got := formatRelative(tc.lang, serverNow17, authored); got != tc.wantRow {
			t.Errorf("%s: list row = %q, want %q", tc.lang, got, tc.wantRow)
		}
		if got := formatDateTime(tc.lang, authored); got != tc.wantDetail {
			t.Errorf("%s: commit detail = %q, want %q", tc.lang, got, tc.wantDetail)
		}
	}

	// The clock must be the author's, not a converted one.
	if got := formatClock(authored); got != "00:30" {
		t.Errorf("clock = %q, want the author's recorded 00:30", got)
	}
}

func TestCommitAuthoredBehindTheServerCrossesTheOtherWay(t *testing.T) {
	// The opposite direction: a negative offset. 17 September 23:30 at -11:00
	// is 18 September 09:30 in Seoul, so converting would push the commit
	// forward a day instead of back.
	authored := time.Date(2026, 9, 17, 23, 30, 0, 0, minus11)

	if got := authored.In(serverNow17.Location()).Day(); got != 18 {
		t.Fatalf("the fixture no longer crosses the boundary forward (server day %d)", got)
	}

	for _, tc := range []struct {
		lang         Lang
		heading, row string
	}{
		{LangEN, "Today", "Today 23:30"},
		{LangKO, "오늘", "오늘 23:30"},
	} {
		if got := formatDayHeading(tc.lang, serverNow17, authored); got != tc.heading {
			t.Errorf("%s: group heading = %q, want %q", tc.lang, got, tc.heading)
		}
		if got := formatRelative(tc.lang, serverNow17, authored); got != tc.row {
			t.Errorf("%s: list row = %q, want %q", tc.lang, got, tc.row)
		}
	}
}

func TestYesterdayIsTheAuthorsPreviousCalendarDay(t *testing.T) {
	// 16 September recorded at +14:00 is 15 September in Seoul. It must still
	// read as yesterday, the day the author wrote.
	authored := time.Date(2026, 9, 16, 2, 0, 0, 0, plus14)

	for _, tc := range []struct {
		lang         Lang
		heading, row string
	}{
		{LangEN, "Yesterday", "Yesterday 02:00"},
		{LangKO, "어제", "어제 02:00"},
	} {
		if got := formatDayHeading(tc.lang, serverNow17, authored); got != tc.heading {
			t.Errorf("%s: group heading = %q, want %q", tc.lang, got, tc.heading)
		}
		if got := formatRelative(tc.lang, serverNow17, authored); got != tc.row {
			t.Errorf("%s: list row = %q, want %q", tc.lang, got, tc.row)
		}
	}
}

func TestYearBoundaryUsesTheAuthorsYear(t *testing.T) {
	// 1 January 00:30 at +14:00 is still 31 December in Seoul. The commit
	// belongs to the author's new year, and the current-year wording follows
	// the reference calendar.
	newYear := time.Date(2026, 1, 1, 0, 30, 0, 0, plus14)
	nowJan := time.Date(2026, 1, 5, 9, 0, 0, 0, seoulZone)

	if got := newYear.In(seoulZone).Year(); got != 2025 {
		t.Fatalf("the fixture no longer crosses the year boundary (server year %d)", got)
	}

	// Within the reference year: no year is repeated in the row.
	if got := formatRelative(LangEN, nowJan, newYear); strings.Contains(got, "2026") || strings.Contains(got, "2025") {
		t.Errorf("a commit in the current year names a year: %q", got)
	}
	// The detail always states the author's year.
	if got := formatDateTime(LangEN, newYear); !strings.Contains(got, "2026") {
		t.Errorf("the detail does not use the author's year: %q", got)
	}
	if got := formatDateTime(LangKO, newYear); !strings.Contains(got, "2026년") {
		t.Errorf("the Korean detail does not use the author's year: %q", got)
	}

	// The last day of the old year keeps its own year once the reference
	// calendar has moved on.
	oldYear := time.Date(2025, 12, 31, 23, 30, 0, 0, minus11)
	if got := formatRelative(LangEN, nowJan, oldYear); !strings.Contains(got, "2025") {
		t.Errorf("a commit from last year does not name its year: %q", got)
	}
}

func TestMidnightInAnotherZoneIsNotTheSameDay(t *testing.T) {
	// Comparing midnight instants would make these equal even though they are
	// different calendar dates, and would separate dates that are the same.
	a := time.Date(2026, 9, 17, 0, 0, 0, 0, plus14)    // 17 Sep, +14
	b := time.Date(2026, 9, 17, 0, 0, 0, 0, seoulZone) // 17 Sep, +09

	if a.Equal(b) {
		t.Fatal("the fixtures are the same instant; the test proves nothing")
	}
	if !sameDay(civilOf(a), civilOf(b)) {
		t.Error("two records of the same calendar date are treated as different days")
	}

	// And the same instant written with two offsets is two different dates.
	instant := time.Date(2026, 9, 17, 0, 30, 0, 0, plus14)
	shifted := instant.In(seoulZone)
	if !instant.Equal(shifted) {
		t.Fatal("the fixtures are not the same instant")
	}
	if sameDay(civilOf(instant), civilOf(shifted)) {
		t.Error("one instant written in two zones is treated as one calendar date")
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
			LangKO: {"작성 날짜", "작성자가 기록한 시간대", "검사를 실행했거나 통과했다는 뜻은 아닙니다"},
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

func TestNoFormatterRestatesAStoredTimeInAnotherZone(t *testing.T) {
	// The defect was a single conversion. Guard against it returning.
	data, err := os.ReadFile("format.go")
	if err != nil {
		t.Fatal(err)
	}
	if m := regexp.MustCompile(`\.In\(now\.Location\(\)\)`).FindString(string(data)); m != "" {
		t.Errorf("a formatter converts a stored date into the reference zone (%s), "+
			"which moves commits to a different calendar day", m)
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
