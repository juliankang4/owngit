package webui

import (
	"fmt"
	"html/template"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// templateFuncs are the helpers the templates may call. Everything that
// reaches a template goes through html/template escaping; the only raw HTML
// this package produces is its own icon markup, never repository content.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"t":            Text,
		"bi":           bi,
		"biText":       biText,
		"biAttr":       biAttr,
		"biAttrText":   biAttrText,
		"biRelative":   biRelative,
		"biDateTime":   biDateTime,
		"biDayHeading": biDayHeading,
		"biCount":      biCount,
		"biNotice":     biNotice,
		"biRelease":    biRelease,
		"biN":          biN,
		"biF":          biF,
		"diffTotals":   diffTotals,
		"icon":         icon,
		"statusIcon":   statusIcon,
		"themeIcon":    themeIcon,
		"date":         formatDate,
		"dateTime":     formatDateTime,
		"relative":     formatRelative,
		"dayHeading":   formatDayHeading,
		"clock":        formatClock,
		"bytes":        formatBytes,
		"number":       formatNumber,
		"count":        formatCount,
		"withLang":     withLang,
		"withQuery":    withQuery,
		"level":        activityLevel,
		"importToken":  importToken,
		"importError":  func(lang Lang, class string) template.HTML { return bi(lang, ImportErrorCode(class)) },
		"weeks":        activityWeeks,
		"monthLabel":   monthLabel,
		"dayLabel":     dayLabel,
		"dayLabels":    weekdayLabels,
		"dict":         dict,
		"noteID":       noteID,
		"forAction":    noticesForAction,
		"firstAlert":   firstAlertField,
		"refValue":     refValue,
		"queryRef":     queryRef,
		"changeStatus": changeStatus,
		"deletions":    deletions,
		"pickedPaths":  pickedPaths,
		"selectionURL": restoreSelectionURL,
		// Evidence screens. The status mapping lives in Go so a template names
		// a value instead of repeating a status string.
		"checkState":  checkStateOf,
		"reviewState": reviewStateOf,
		// The pull request summary rows. Each names which revision the result
		// describes, which stays on the row rather than behind a disclosure.
		"checkRelevance":  checkRelevance,
		"reviewRelevance": reviewRelevance,
		"prCheckSummary":  prCheckSummary,
		"prReviewSummary": prReviewSummary,
		"attemptState":    attemptStateOf,
		"recordState":     attemptRecordState,
		"resultState":     resultLineState,
		"taskState":       taskStateOf,
		"prState":         pullRequestStateOf,
		"checkNotes":      checkNotes,
		"attemptNotes":    attemptNotes,
		"reviewNotes":     reviewNotes,
		"reviewOrigin":    reviewOrigin,
		"logState":        logState,
		"blockerNote":     blockerNote,
		"unknownBlocker":  unknownBlocker,
		"revisionNote":    revisionNote,
		"prNumber":        pullRequestNumber,
		"prSelectionURL":  pullRequestSelectionURL,
		"shortID":         shortID,
		"duration":        formatDuration,
		"budget":          budgetText,
		"exitCode":        exitCodeText,
		"notZero":         notZero,
		"tokenLines":      tokenLines,
		"code":            code,
		"prChangeStatus":  prChangeStatus,
		"issueAction":     issueAction,
		"revokeAction":    revokeAction,
		"forCredential":   forCredential,
		// Configured checks. The policy, job, and runner-token screens name a
		// recorded value and let Go decide what it means, exactly as the
		// evidence screens do.
		"jobState":            jobState,
		"consentState":        consentState,
		"policyState":         policyState,
		"runtimeState":        runtimeState,
		"runtimeReason":       runtimeReason,
		"executorName":        executorName,
		"triggerName":         triggerName,
		"networkName":         networkName,
		"savePolicyAction":    savePolicyAction,
		"fieldRange":          fieldRange,
		"fieldDefault":        fieldDefault,
		"limitFields":         limitFields,
		"limitsOpen":          limitsOpen,
		"biOption":            biOption,
		"unitMenuLabel":       unitMenuLabel,
		"checksRunning":       checksRunning,
		"checkFileState":      checkFileState,
		"checkFileCount":      checkFileCount,
		"checkFileExample":    checkFileExample,
		"nextCheckStep":       nextCheckStep,
		"stepNumber":          stepNumber,
		"enableChecksAction":  enableChecksAction,
		"disableChecksAction": disableChecksAction,
		"cancelJobAction":     cancelJobAction,
		"rerunJobAction":      rerunJobAction,
		"issueRunnerAction":   issueRunnerAction,
		"revokeRunnerAction":  revokeRunnerAction,
		"hostExecutor":        hostExecutor,
		"containerExecutor":   containerExecutor,
		"runnerExecutor":      runnerExecutor,
		"noneNetwork":         noneNetwork,
		"bridgeNetwork":       bridgeNetwork,
		"pushEvent":           pushEvent,
		"pullRequestEvent":    pullRequestEvent,
		"forRunnerCredential": forRunnerCredential,
		"hasPrefix":           strings.HasPrefix,
		"add":                 func(a, b int) int { return a + b },
		"sub":                 func(a, b int) int { return a - b },
	}
}

// refValue is the value a ref picker option submits.
//
// Two refs can share a short name: a branch and a tag may both be called
// "release". The display label stays short, but submitting it would leave the
// backend guessing which one was meant. The backend already encodes the
// unambiguous ref in each option's URL, so the option submits exactly the
// value that URL would have requested.
//
// The fallback is deliberately conservative: an option with no URL, or one
// whose URL carries no ref, submits its display name, which is what an older
// caller or a display-only fixture expects.
func refValue(option RefOption) string {
	if ref := queryRef(option.URL); ref != "" {
		return ref
	}
	return option.Name
}

// queryRef reads the "ref" query value out of a link the backend built, or
// returns empty when the link has none. It is how the picker recovers the
// exact ref a page was asked for, including one that does not resolve.
func queryRef(link string) string {
	if link == "" {
		return ""
	}
	parsed, err := url.Parse(link)
	if err != nil {
		return ""
	}
	return parsed.Query().Get("ref")
}

// noteID builds the id of a field's note element. A page may repeat one field
// name across several forms, so the id is namespaced by the form's scope when
// one is given. Without unique ids, aria-describedby on one input would point
// at another form's message.
// Scope is optional: most pages show a field name once, so they omit it and
// the template passes a missing key, which arrives here as nil.
func noteID(scope any, field string) string {
	text, _ := scope.(string)
	if text == "" {
		return field + "-note"
	}
	return text + "-" + field + "-note"
}

// firstAlertField returns the index in notices of the error a refused request
// should place the reader on, or -1 when there is none. Fields are given in
// the order they are rendered.
//
// A read-only summary answers a refusal with a whole new document and has no
// input to carry that placement, so the notice itself is the focus target.
// The index identifies one notice even when a field carries two errors, where
// a field name would match both and produce two autofocus attributes.
func firstAlertField(notices []Notice, fields ...string) int {
	for _, field := range fields {
		for i, notice := range notices {
			if notice.Field == field && notice.Kind == NoticeError {
				return i
			}
		}
	}
	return -1
}

// noticesForAction keeps only the notices belonging to the form the reader
// actually submitted. Settings shows several forms that collect the same field
// name; without this, a rejected password in one form would light up the
// identically named input in every other form, including hidden ones.
//
// An empty pending action means nothing was submitted, so no form claims a
// field error.
func noticesForAction(pending, action string, notices []Notice) []Notice {
	if pending == "" || pending != action {
		return nil
	}
	return notices
}

// ---------------------------------------------------------------------------
// Restore selection
// ---------------------------------------------------------------------------

// changeStatus names what restoring a path would do to the target branch.
// The statuses are the ones RestorePath and DiffFile use; an unknown value
// renders nothing rather than a guess, because a wrong word here would
// describe a write.
func changeStatus(status string) MessageCode {
	switch status {
	case "added", "copied":
		return MsgRestoreStatusAdded
	case "modified", "renamed":
		return MsgRestoreStatusModified
	case "deleted":
		return MsgRestoreStatusDeleted
	default:
		return ""
	}
}

// deletions are the previewed changes that remove a file. They are listed
// again on their own, because a deletion is the one outcome a reader cannot
// undo by looking at the file afterwards, and it must not be something they
// only find by reading a long mixed list.
func deletions(changes []DiffFile) []DiffFile {
	var out []DiffFile
	for _, change := range changes {
		if change.Status == "deleted" {
			out = append(out, change)
		}
	}
	return out
}

// pickedPaths are the currently selected paths, which the apply form resubmits
// as the repeated "path" field.
func pickedPaths(paths []RestorePath) []RestorePath {
	var out []RestorePath
	for _, path := range paths {
		if path.Selected {
			out = append(out, path)
		}
	}
	return out
}

// restoreSelectionURL builds the canonical address of a restore selection.
//
// Previewing is a POST, so the request URL of a previewed page cannot be
// opened again: following it with a plain GET, which is what a language link
// or a new tab does, would reach a route that only accepts POST. This is the
// same screen expressed as a GET, carrying the choices the reader made so
// they survive a language switch or a step back.
//
// It deliberately does not carry the preview. Returning here re-opens the
// selection, and the reader previews again before anything can be applied.
func restoreSelectionURL(p RestorePage) string {
	base := p.ApplyURL
	if base == "" {
		return p.CancelURL
	}
	values := url.Values{}
	if p.Source.OID != "" {
		values.Set("source", p.Source.OID)
	}
	if p.TargetBranch != "" {
		values.Set("target", p.TargetBranch)
	}
	if p.Mode != "" {
		values.Set("mode", p.Mode)
	}
	// Only the active selection is carried. In whole-project mode the ticks
	// are not what decides the result, and a whole-project request that also
	// names paths describes two different restores, so the address states the
	// selection as it actually stands.
	if p.Mode == RestoreModeFiles {
		for _, picked := range pickedPaths(p.Paths) {
			values.Add("path", picked.Path)
		}
	}
	if len(values) == 0 {
		return base
	}
	return base + "?" + values.Encode()
}

// dict builds a map for template partials that need several values.
func dict(pairs ...any) (map[string]any, error) {
	if len(pairs)%2 != 0 {
		return nil, fmt.Errorf("dict: odd argument count")
	}
	out := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict: key %d is not a string", i)
		}
		out[key] = pairs[i+1]
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// URLs
// ---------------------------------------------------------------------------

// withLang returns raw with its lang parameter set to l. It keeps the path and
// every other parameter, so switching language stays on the same screen with
// the same branch, path, and page.
func withLang(raw string, l Lang) string {
	return withQuery(raw, "lang", string(l))
}

// withQuery returns raw with key set to value. An empty value removes the key.
// Only the path and query are preserved; a scheme or host in raw is dropped so
// a caller cannot turn an internal link into an external one.
func withQuery(raw, key, value string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Path == "" {
		parsed = &url.URL{Path: "/"}
	}
	query := parsed.Query()
	if value == "" {
		query.Del(key)
	} else {
		query.Set(key, value)
	}
	out := url.URL{Path: parsed.Path, RawQuery: query.Encode()}
	return out.String()
}

// ---------------------------------------------------------------------------
// Dates and numbers
// ---------------------------------------------------------------------------

var monthNamesKO = [...]string{
	"1월", "2월", "3월", "4월", "5월", "6월",
	"7월", "8월", "9월", "10월", "11월", "12월",
}

var weekdayNamesKO = [...]string{"일", "월", "화", "수", "목", "금", "토"}

// formatDate is a plain calendar date: "Mar 10" / "3월 10일".
func formatDate(lang Lang, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	if lang == LangKO {
		return fmt.Sprintf("%s %d일", monthNamesKO[int(t.Month())-1], t.Day())
	}
	return t.Format("Jan 2")
}

// formatClock is the time of day: "14:32".
func formatClock(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("15:04")
}

// formatDateTime is an absolute timestamp with the year, used in title
// attributes and detail views.
func formatDateTime(lang Lang, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	if lang == LangKO {
		return fmt.Sprintf("%d년 %s %d일 %s", t.Year(), monthNamesKO[int(t.Month())-1], t.Day(), t.Format("15:04"))
	}
	return t.Format("Jan 2, 2006 15:04")
}

// A commit's author date carries the calendar date and clock the author
// actually recorded, together with their UTC offset. The interface shows that
// recorded day everywhere: in the graph, the activity groups, the list rows,
// and the commit detail.
//
// Converting it into the server's zone would move a commit to a different
// calendar day. A commit authored at 00:30 on 17 September at +14:00 becomes
// 19:30 on 16 September for a server at +09:00, so the heading would read
// "Yesterday" next to a clock showing 00:30, and the list and the detail would
// name two different days for one commit.
//
// Now is therefore used only as the reference calendar that decides what
// counts as today, yesterday, and the current year. It never shifts a stored
// date.

// civil is a calendar date with no zone attached: what a person would read off
// a wall calendar.
type civil struct {
	year  int
	month time.Month
	day   int
}

func civilOf(t time.Time) civil {
	return civil{t.Year(), t.Month(), t.Day()}
}

// sameDay compares calendar dates rather than instants. Two midnights in
// different zones are different instants but can be the same date, and the
// same instant can fall on different dates.
func sameDay(a, b civil) bool {
	return a == b
}

// formatRelative is the list-row timestamp: today and yesterday get a clock,
// this year gets a date and clock, older gets the year too. The clock is the
// author's recorded time of day.
func formatRelative(lang Lang, now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	today := civilOf(now)
	yesterday := civilOf(now.AddDate(0, 0, -1))
	switch day := civilOf(t); {
	case sameDay(day, today):
		if lang == LangKO {
			return "오늘 " + t.Format("15:04")
		}
		return "Today " + t.Format("15:04")
	case sameDay(day, yesterday):
		if lang == LangKO {
			return "어제 " + t.Format("15:04")
		}
		return "Yesterday " + t.Format("15:04")
	case t.Year() == now.Year():
		if lang == LangKO {
			return fmt.Sprintf("%s %d일 %s", monthNamesKO[int(t.Month())-1], t.Day(), t.Format("15:04"))
		}
		return t.Format("Jan 2 at 15:04")
	default:
		if lang == LangKO {
			return fmt.Sprintf("%d년 %s %d일", t.Year(), monthNamesKO[int(t.Month())-1], t.Day())
		}
		return t.Format("Jan 2, 2006")
	}
}

// formatDayHeading labels a day group in the activity list. The group is the
// author's recorded calendar date, so it is read as such.
func formatDayHeading(lang Lang, now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	today := civilOf(now)
	day := civilOf(t)
	switch {
	case sameDay(day, today):
		if lang == LangKO {
			return "오늘"
		}
		return "Today"
	case sameDay(day, civilOf(now.AddDate(0, 0, -1))):
		if lang == LangKO {
			return "어제"
		}
		return "Yesterday"
	}
	if lang == LangKO {
		if t.Year() == now.Year() {
			return fmt.Sprintf("%s %d일 %s요일", monthNamesKO[int(t.Month())-1], t.Day(), weekdayNamesKO[int(t.Weekday())])
		}
		return fmt.Sprintf("%d년 %s %d일", t.Year(), monthNamesKO[int(t.Month())-1], t.Day())
	}
	if t.Year() == now.Year() {
		return t.Format("Monday, Jan 2")
	}
	return t.Format("Monday, Jan 2, 2006")
}

// dayLabel is the accessible label of one activity cell.
func dayLabel(lang Lang, d ActivityDay) string {
	if lang == LangKO {
		date := fmt.Sprintf("%d년 %s %d일 %s요일", d.Date.Year(), monthNamesKO[int(d.Date.Month())-1], d.Date.Day(), weekdayNamesKO[int(d.Date.Weekday())])
		if d.Future {
			return date + ", 아직 오지 않은 날"
		}
		return fmt.Sprintf("%s, 커밋 %d건", date, d.Count)
	}
	date := d.Date.Format("Monday, Jan 2, 2006")
	if d.Future {
		return date + ", not yet"
	}
	if d.Count == 1 {
		return date + ", 1 commit"
	}
	return fmt.Sprintf("%s, %d commits", date, d.Count)
}

// formatNumber groups thousands with commas, which both languages use.
func formatNumber(n int) string {
	s := strconv.Itoa(n)
	negative := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if negative {
		return "-" + b.String()
	}
	return b.String()
}

// countedForms are the words for one counted noun. English needs a singular
// and a plural; Korean has no plural agreement but each noun takes its own
// counter word, so the number is placed inside the phrase.
//
// Adding "s" is not a rule of English: "branch" becomes "branches". The
// interface counts a small fixed set of things, so each one states its own
// words instead of being inflected by a guess.
type countedForms struct {
	one, many string // English, with %s standing for the number.
	korean    string
}

var countedNouns = map[string]countedForms{
	"repository": {one: "%s repository", many: "%s repositories", korean: "저장소 %s곳"},
	"branch":     {one: "%s branch", many: "%s branches", korean: "브랜치 %s개"},
	"tag":        {one: "%s tag", many: "%s tags", korean: "태그 %s개"},
	"commit":     {one: "%s commit", many: "%s commits", korean: "커밋 %s건"},
	"file":       {one: "%s file", many: "%s files", korean: "파일 %s개"},
	"line":       {one: "%s line", many: "%s lines", korean: "%s줄"},
	"day":        {one: "%s day", many: "%s days", korean: "%s일"},
	"item":       {one: "%s item", many: "%s items", korean: "%s건"},
	"entry":      {one: "%s entry", many: "%s entries", korean: "항목 %s개"},
	"finding":    {one: "%s finding", many: "%s findings", korean: "지적 %s건"},
	"request":    {one: "%s request", many: "%s requests", korean: "요청 %s회"},
}

// countedNoun reports the stated forms for a noun. A template that counts
// something unlisted is a mistake to fix, not a word to invent; a test covers
// every noun the templates actually use.
func countedNoun(kind string) (countedForms, bool) {
	forms, ok := countedNouns[kind]
	return forms, ok
}

// formatCount renders a counted noun: "7 repositories" / "저장소 7곳".
func formatCount(lang Lang, kind string, n int) string {
	number := formatNumber(n)
	forms, ok := countedNoun(kind)
	if !ok {
		// Show the number alone rather than a made-up word.
		return number
	}
	if lang == LangKO {
		return fmt.Sprintf(forms.korean, number)
	}
	// English uses the plural for every count except exactly one, including
	// zero: "0 branches".
	if n == 1 {
		return fmt.Sprintf(forms.one, number)
	}
	return fmt.Sprintf(forms.many, number)
}

// formatBytes is a short file size.
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for size := n / unit; size >= unit; size /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

// ---------------------------------------------------------------------------
// Activity graph geometry
// ---------------------------------------------------------------------------

// activityLevel maps a daily count to one of five ramp steps.
func activityLevel(n int) int {
	switch {
	case n == 0:
		return 0
	case n <= 2:
		return 1
	case n <= 5:
		return 2
	case n <= 9:
		return 3
	default:
		return 4
	}
}

// graphCell is one drawn position: either a day or padding before Jan 1.
// The label is kept in both languages so the accessible name can be swapped
// in place along with the rest of the interface.
type graphCell struct {
	Day     ActivityDay
	LabelEN string
	LabelKO string
	Level   int
	Blank   bool
}

// graphRow is one weekday row of the graph.
type graphRow struct {
	Weekday int
	LabelEN string
	LabelKO string
	Cells   []graphCell
}

// graphLayout is the full drawable graph: seven weekday rows by N week
// columns, plus the month labels above them.
type graphLayout struct {
	Rows   []graphRow
	Months []graphMonth
	Weeks  int
}

// graphMonth is one month label positioned over its first week column.
type graphMonth struct {
	LabelEN string
	LabelKO string
	Column  int
}

// activityWeeks lays out a year's days into the weekday-row grid the template
// draws. The DOM order is row-major so the visual order and the grid
// semantics agree for assistive technology.
func activityWeeks(graph ActivityGraph) graphLayout {
	if len(graph.Days) == 0 {
		return graphLayout{}
	}
	lead := int(graph.Days[0].Date.Weekday())
	total := lead + len(graph.Days)
	if rem := total % 7; rem != 0 {
		total += 7 - rem
	}
	weeks := total / 7

	cells := make([]graphCell, total)
	for i := range cells {
		cells[i].Blank = true
	}
	for i, day := range graph.Days {
		cells[lead+i] = graphCell{
			Day:     day,
			LabelEN: dayLabel(LangEN, day),
			LabelKO: dayLabel(LangKO, day),
			Level:   activityLevel(day.Count),
		}
	}

	rows := make([]graphRow, 7)
	for weekday := 0; weekday < 7; weekday++ {
		row := graphRow{
			Weekday: weekday,
			LabelEN: weekdayLabel(LangEN, weekday),
			LabelKO: weekdayLabel(LangKO, weekday),
		}
		row.Cells = make([]graphCell, 0, weeks)
		for week := 0; week < weeks; week++ {
			row.Cells = append(row.Cells, cells[week*7+weekday])
		}
		rows[weekday] = row
	}

	var months []graphMonth
	seen := map[time.Month]bool{}
	for week := 0; week < weeks; week++ {
		for weekday := 0; weekday < 7; weekday++ {
			cell := cells[week*7+weekday]
			if cell.Blank || cell.Day.Date.Day() > 7 || seen[cell.Day.Date.Month()] {
				continue
			}
			seen[cell.Day.Date.Month()] = true
			months = append(months, graphMonth{
				LabelEN: monthLabel(LangEN, int(cell.Day.Date.Month())),
				LabelKO: monthLabel(LangKO, int(cell.Day.Date.Month())),
				Column:  week + 1,
			})
		}
	}

	return graphLayout{Rows: rows, Months: months, Weeks: weeks}
}

func monthLabel(lang Lang, month int) string {
	if month < 1 || month > 12 {
		return ""
	}
	if lang == LangKO {
		return monthNamesKO[month-1]
	}
	return time.Month(month).String()[:3]
}

// weekdayLabel returns a label for Monday, Wednesday, and Friday only; the
// other rows stay unlabeled, matching the accepted design.
func weekdayLabel(lang Lang, weekday int) string {
	switch weekday {
	case 1, 3, 5:
	default:
		return ""
	}
	if lang == LangKO {
		return weekdayNamesKO[weekday]
	}
	return time.Weekday(weekday).String()[:3]
}

// weekdayLabels lists the seven row labels in order.
func weekdayLabels(lang Lang) []string {
	out := make([]string, 7)
	for i := range out {
		out[i] = weekdayLabel(lang, i)
	}
	return out
}

// diffTotals sums the line counts of the changed files.
func diffTotals(files []DiffFile) DiffTotals {
	var totals DiffTotals
	for _, file := range files {
		if file.Binary {
			continue
		}
		totals.Text = true
		totals.Additions += file.Additions
		totals.Deletions += file.Deletions
	}
	return totals
}
