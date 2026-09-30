package server

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"owngit/internal/state"
	"owngit/internal/webui"
)

// maximumActivityEntries bounds the commit list that the All activity page
// and the activity API show: the newest commits of the chosen year, or of
// the chosen day. The graph still counts every commit that was read.
const maximumActivityEntries = 1000

const activityAPIPath = "/api/v1/activity"

// errNoActivityRead is why activity could not be counted when no
// repository could be read. The server log names each repository's cause.
var errNoActivityRead = errors.New("no repository could be read")

// activityListing is what the All activity page and the activity API show
// for one year, or one day of it.
type activityListing struct {
	graph webui.ActivityGraph
	// entries are the listed commits, newest first.
	entries []webui.ActivityEntry
	// truncated is true when the year or day has more commits than
	// maximumActivityEntries.
	truncated bool
}

// readActivity reads the activity of repositories for year, and lists the
// commits of day when day is not zero. Repositories that could not be read
// are named in the graph, never counted as having no commits.
func (app *App) readActivity(request *http.Request, repositories []state.Repository, year int, day time.Time) activityListing {
	ctx := request.Context()
	observation := app.observeActivity(ctx, repositories, app.activityKeys(ctx, repositories), app.activityLimit())
	graph := buildActivityGraph(observation.counts, year, app.now(), len(repositories))
	observation.describe(&graph)
	graph.SelectedDate = day
	selected := ""
	if !day.IsZero() {
		selected = day.Format("2006-01-02")
	}
	var entries []webui.ActivityEntry
	for _, item := range observation.entries {
		if item.Commit.AuthorDate.Year() == year && (selected == "" || item.Commit.AuthorDate.Format("2006-01-02") == selected) {
			entries = append(entries, item)
		}
	}
	sortActivityEntries(entries)
	listing := activityListing{graph: graph, entries: entries}
	if len(entries) > maximumActivityEntries {
		listing.entries, listing.truncated = entries[:maximumActivityEntries], true
	}
	return listing
}

// activityReasons names each reason a count is incomplete in the API.
var activityReasons = map[webui.MessageCode]string{
	webui.MsgActivityCounting:   "counting",
	webui.MsgActivityPreparing:  "preparing",
	webui.MsgActivityUnreadable: "unreadable",
	webui.MsgActivitySkipped:    "preparing_or_unreadable",
	webui.MsgActivityLimit:      "limit",
}

type activityDayJSON struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

// activityEntryJSON is one listed commit. AuthorName and AuthorDate are what
// the commit itself records; OwnGit does not verify who made it.
type activityEntryJSON struct {
	Repository     string    `json:"repository"`
	RepositoryName string    `json:"repository_name"`
	Ref            string    `json:"ref"`
	RefRetained    bool      `json:"ref_retained"`
	OID            string    `json:"oid"`
	Subject        string    `json:"subject"`
	AuthorName     string    `json:"author_name"`
	AuthorDate     time.Time `json:"author_date"`
}

type activityResponse struct {
	OK   bool   `json:"ok"`
	Year int    `json:"year"`
	Date string `json:"date,omitempty"`
	// RepositoryCount is how many repositories were read or tried.
	RepositoryCount int  `json:"repository_count"`
	Total           int  `json:"total"`
	Complete        bool `json:"complete"`
	// IncompleteReason is set when Complete is false.
	IncompleteReason string `json:"incomplete_reason,omitempty"`
	// Unreadable names repositories left out because their Git data could
	// not be read.
	Unreadable []string `json:"unreadable"`
	// Days lists the days of Year with at least one commit.
	Days      []activityDayJSON   `json:"days"`
	Entries   []activityEntryJSON `json:"entries"`
	Truncated bool                `json:"truncated"`
}

// activityQueryAllowed reports whether request is an activity read whose
// query names only a year and a day.
func activityQueryAllowed(request *http.Request) bool {
	if request.URL.Path != activityAPIPath || request.Method != http.MethodGet {
		return false
	}
	for key, values := range request.URL.Query() {
		if (key != "year" && key != "date") || len(values) != 1 {
			return false
		}
	}
	return true
}

// handleActivityAPI answers GET /api/v1/activity with what the All activity
// page shows, with general access. year picks the calendar year, the
// current one by default, and date (YYYY-MM-DD) lists one day of it. When no
// repository could be read there is no count to give, so the answer is
// activity_unavailable instead of zero commits.
func (app *App) handleActivityAPI(writer http.ResponseWriter, request *http.Request, settings state.Settings) {
	if !app.authorizeAPI(writer, request, settings) {
		return
	}
	if request.Method != http.MethodGet {
		writeAPIMethodError(writer, http.MethodGet)
		return
	}
	query := request.URL.Query()
	year := app.now().Year()
	if query.Has("year") {
		value, err := strconv.Atoi(query.Get("year"))
		if err != nil || !activityYearInRange(value) {
			writeAPIError(writer, http.StatusBadRequest, "invalid_request", "year must be a year from 1970 to 9999.", nil)
			return
		}
		year = value
	}
	var day time.Time
	if query.Has("date") {
		parsed, err := time.ParseInLocation("2006-01-02", query.Get("date"), app.now().Location())
		if err != nil || !activityYearInRange(parsed.Year()) || (query.Has("year") && parsed.Year() != year) {
			writeAPIError(writer, http.StatusBadRequest, "invalid_request", "date must be a day (YYYY-MM-DD) of the chosen year.", nil)
			return
		}
		day, year = parsed, parsed.Year()
	}
	repositories, err := app.visibleRepositories(request)
	if err != nil {
		writeAPIError(writer, unavailable(request, "repository list read", err), "state_unavailable", "OwnGit state is unavailable.", nil)
		return
	}
	listing := app.readActivity(request, repositories, year, day)
	graph := listing.graph
	if !graph.Available {
		writeAPIError(writer, unavailable(request, "activity count", errNoActivityRead), "activity_unavailable", "No repository could be read, so there is no activity to count.",
			map[string][]string{"unreadable": graph.Unreadable})
		return
	}
	response := activityResponse{
		OK: true, Year: year, RepositoryCount: graph.RepositoryCount, Total: graph.Total, Complete: graph.Complete,
		IncompleteReason: activityReasons[graph.IncompleteReason], Unreadable: append([]string{}, graph.Unreadable...),
		Days: []activityDayJSON{}, Entries: []activityEntryJSON{}, Truncated: listing.truncated,
	}
	if !day.IsZero() {
		response.Date = day.Format("2006-01-02")
	}
	for _, graphDay := range graph.Days {
		if graphDay.Count > 0 {
			response.Days = append(response.Days, activityDayJSON{Date: graphDay.Date.Format("2006-01-02"), Count: graphDay.Count})
		}
	}
	for _, entry := range listing.entries {
		response.Entries = append(response.Entries, activityEntryJSON{
			Repository: entry.RepositoryID, RepositoryName: entry.RepositoryName, Ref: entry.Ref, RefRetained: entry.RefRetained,
			OID: entry.Commit.OID, Subject: entry.Commit.Subject, AuthorName: entry.Commit.AuthorName, AuthorDate: entry.Commit.AuthorDate,
		})
	}
	writeAPIJSON(writer, http.StatusOK, response)
}
