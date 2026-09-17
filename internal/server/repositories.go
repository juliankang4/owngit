package server

import (
	"errors"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/webui"
)

const maximumActivityCommits = 200_000

func (app *App) handleOverview(writer http.ResponseWriter, request *http.Request, settings state.Settings) {
	session, ok := app.requireGeneral(writer, request, settings)
	if !ok {
		return
	}
	chrome, err := app.chrome(writer, request, webui.SectionOverview, "", session.CSRF)
	if err != nil {
		app.writePlainError(writer, http.StatusServiceUnavailable)
		return
	}
	repositories, err := app.Store.Repositories(request.Context())
	if err != nil {
		app.renderError(writer, request, http.StatusServiceUnavailable, webui.MsgErrUnavailable, "")
		return
	}
	query := strings.ToLower(strings.TrimSpace(request.URL.Query().Get("q")))
	var summaries []webui.RepositorySummary
	var recent []webui.ActivityEntry
	remainingActivity := maximumActivityCommits
	for _, stored := range repositories {
		summary, summaryErr := app.repositorySummary(request, stored)
		if summaryErr != nil {
			app.renderError(writer, request, http.StatusServiceUnavailable, webui.MsgRepoUnreadable, stored.Name)
			return
		}
		if query == "" || strings.Contains(strings.ToLower(stored.Name), query) || strings.Contains(strings.ToLower(stored.Description), query) {
			summaries = append(summaries, summary)
		}
		if remainingActivity > 0 {
			entries, _, entriesErr := app.repositoryActivityEntries(request, stored, remainingActivity)
			if entriesErr == nil {
				recent = append(recent, entries...)
				remainingActivity -= len(entries)
			}
		}
	}
	sortActivityEntries(recent)
	if len(recent) > 8 {
		recent = recent[:8]
	}
	graph := app.aggregateActivity(request, repositories, selectedYear(request, app.now().Year()))
	app.render(writer, http.StatusOK, webui.OverviewPage{
		Chrome: chrome, Activity: graph, Repositories: summaries, Recent: recent,
		RecentMoreURL: "/activity", TotalCount: len(repositories),
	})
}

func (app *App) handleNewRepositoryGet(writer http.ResponseWriter, request *http.Request, settings state.Settings, name, description string, notices []webui.Notice) {
	session, ok := app.requireGeneral(writer, request, settings)
	if !ok {
		return
	}
	chrome, err := app.chrome(writer, request, webui.SectionOverview, "", session.CSRF)
	if err != nil {
		app.writePlainError(writer, http.StatusServiceUnavailable)
		return
	}
	chrome.Notices = notices
	status := http.StatusOK
	if len(notices) != 0 {
		status = http.StatusUnprocessableEntity
	}
	app.render(writer, status, webui.NewRepositoryPage{
		Chrome: chrome, SubmitURL: "/repositories", Name: name, Description: description, NameRules: webui.MsgRepoNameRules,
	})
}

func (app *App) handleCreateRepository(writer http.ResponseWriter, request *http.Request, settings state.Settings) {
	if _, ok := app.requireGeneral(writer, request, settings); !ok {
		return
	}
	if !parseForm(writer, request) {
		return
	}
	if !app.validCSRF(request, postValue(request, "csrf")) {
		app.renderError(writer, request, http.StatusForbidden, webui.MsgErrCSRF, "")
		return
	}
	name := strings.TrimSpace(postValue(request, "name"))
	description := strings.TrimSpace(postValue(request, "description"))
	created, err := app.Repositories.Create(request.Context(), name, description)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrNameTaken):
			app.handleNewRepositoryGet(writer, request, settings, name, description, []webui.Notice{webui.Error("name", webui.MsgRepoNameTaken)})
		case errors.Is(err, repository.ErrInvalidName):
			app.handleNewRepositoryGet(writer, request, settings, name, description, []webui.Notice{webui.Error("name", webui.MsgRepoNameInvalid)})
		case errors.Is(err, repository.ErrInvalidDescription):
			app.handleNewRepositoryGet(writer, request, settings, name, description, []webui.Notice{webui.Error("description", webui.MsgErrTooLarge)})
		default:
			app.renderError(writer, request, http.StatusServiceUnavailable, webui.MsgRepoCreateFail, "")
		}
		return
	}
	http.Redirect(writer, request, "/repositories/"+url.PathEscape(created.ID)+"?notice=repository_created", http.StatusSeeOther)
}

func (app *App) handleActivity(writer http.ResponseWriter, request *http.Request, settings state.Settings) {
	session, ok := app.requireGeneral(writer, request, settings)
	if !ok {
		return
	}
	chrome, err := app.chrome(writer, request, webui.SectionActivity, "", session.CSRF)
	if err != nil {
		app.writePlainError(writer, http.StatusServiceUnavailable)
		return
	}
	repositories, err := app.Store.Repositories(request.Context())
	if err != nil {
		app.renderError(writer, request, http.StatusServiceUnavailable, webui.MsgActivityUnavail, "")
		return
	}
	year := selectedYear(request, app.now().Year())
	graph := app.aggregateActivity(request, repositories, year)
	selectedDay := request.URL.Query().Get("date")
	if parsed, err := time.ParseInLocation("2006-01-02", selectedDay, app.now().Location()); err == nil && parsed.Year() == year {
		graph.SelectedDate = parsed
	} else {
		selectedDay = ""
	}
	var entries []webui.ActivityEntry
	complete := true
	remaining := maximumActivityCommits
	for _, stored := range repositories {
		if remaining <= 0 {
			complete = false
			break
		}
		items, incomplete, itemErr := app.repositoryActivityEntries(request, stored, remaining)
		if itemErr != nil {
			complete = false
			continue
		}
		if incomplete {
			complete = false
		}
		remaining -= len(items)
		for _, item := range items {
			if item.Commit.AuthorDate.Year() == year && (selectedDay == "" || item.Commit.AuthorDate.Format("2006-01-02") == selectedDay) {
				entries = append(entries, item)
			}
		}
	}
	if !complete {
		graph.Complete = false
		graph.IncompleteReason = webui.MsgActivityLimit
	}
	sortActivityEntries(entries)
	groups := groupActivity(entries)
	app.render(writer, http.StatusOK, webui.ActivityPage{Chrome: chrome, Activity: graph, Days: groups})
}

func (app *App) handleRepositoryRoute(writer http.ResponseWriter, request *http.Request, settings state.Settings) {
	session, ok := app.requireGeneral(writer, request, settings)
	if !ok {
		return
	}
	remainder := strings.TrimPrefix(request.URL.Path, "/repositories/")
	parts := strings.Split(remainder, "/")
	if len(parts) == 0 || parts[0] == "" {
		app.renderError(writer, request, http.StatusNotFound, webui.MsgRepoNotFound, "")
		return
	}
	id := parts[0]
	stored, exists, err := app.Store.Repository(request.Context(), id)
	if err != nil || !exists {
		app.renderError(writer, request, http.StatusNotFound, webui.MsgRepoNotFound, id)
		return
	}
	chrome, err := app.chrome(writer, request, webui.SectionRepository, id, session.CSRF)
	if err != nil {
		app.writePlainError(writer, http.StatusServiceUnavailable)
		return
	}
	summary, err := app.Repositories.Summary(request.Context(), id)
	if err != nil {
		page := app.baseRepositoryPage(request, chrome, stored, repository.Summary{})
		page.Repo.Unreadable = true
		page.Repo.UnreadableReason = webui.MsgRepoUnreadable
		app.render(writer, http.StatusServiceUnavailable, page)
		return
	}
	page := app.baseRepositoryPage(request, chrome, stored, summary)
	requestedRef := request.URL.Query().Get("ref")
	switch {
	case len(parts) == 1:
		page.Tab = webui.RepoTabOverview
		app.fillRepositoryOverview(request, &page, summary, requestedRef)
	case len(parts) == 2 && parts[1] == "code":
		page.Tab = webui.RepoTabCode
		app.fillCode(request, &page, summary, requestedRef, request.URL.Query().Get("path"))
	case len(parts) == 2 && parts[1] == "commits":
		page.Tab = webui.RepoTabCommits
		app.fillCommits(request, &page, summary, requestedRef, "")
	case len(parts) == 3 && parts[1] == "commits":
		page.Tab = webui.RepoTabCommits
		app.fillCommits(request, &page, summary, requestedRef, parts[2])
	default:
		app.renderError(writer, request, http.StatusNotFound, webui.MsgErrNotFound, request.URL.Path)
		return
	}
	app.render(writer, http.StatusOK, page)
}

func (app *App) baseRepositoryPage(request *http.Request, chrome webui.Chrome, stored state.Repository, summary repository.Summary) webui.RepositoryPage {
	base := "/repositories/" + url.PathEscape(stored.ID)
	clone := app.baseURL(request) + "/git/" + url.PathEscape(stored.ID) + ".git"
	page := webui.RepositoryPage{
		Chrome:      chrome,
		Repo:        webui.RepositoryHeader{ID: stored.ID, Name: stored.Name, Description: stored.Description, URL: base, CloneURL: clone, Empty: summary.Empty},
		OverviewURL: base, CodeURL: base + "/code", CommitsURL: base + "/commits",
	}
	return page
}

func (app *App) selectRef(request *http.Request, page *webui.RepositoryPage, summary repository.Summary, requested string) (string, bool) {
	selected := requested
	if selected == "" && summary.DefaultBranch != "" {
		selected = "refs/heads/" + summary.DefaultBranch
	}
	canonical, oid, resolveErr := "", "", errors.New("branch or tag not found")
	if selected != "" {
		canonical, oid, resolveErr = app.Repositories.ResolveRef(request.Context(), page.Repo.ID, selected)
	}
	if resolveErr == nil {
		selected = canonical
	}
	page.Ref.Name = displayRef(selected)
	page.Ref.Kind = refKind(selected)
	page.Ref.IsDefault = selected != "" && selected == "refs/heads/"+summary.DefaultBranch
	for _, branch := range summary.Branches {
		full := "refs/heads/" + branch.Name
		option := webui.RefOption{Name: branch.Name, URL: withRef(request.URL.Path, full), Selected: selected == full, IsDefault: branch.Name == summary.DefaultBranch}
		page.Ref.Branches = append(page.Ref.Branches, option)
	}
	for _, tag := range summary.Tags {
		full := "refs/tags/" + tag.Name
		page.Ref.Tags = append(page.Ref.Tags, webui.RefOption{Name: tag.Name, URL: withRef(request.URL.Path, full), Selected: selected == full})
	}
	if selected != "" {
		page.OverviewURL = withRef(page.OverviewURL, selected)
		page.CodeURL = withRef(page.CodeURL, selected)
		page.CommitsURL = withRef(page.CommitsURL, selected)
	}
	if summary.Empty {
		return "", false
	}
	if selected == "" || resolveErr != nil {
		page.Ref.Missing = true
		return "", false
	}
	page.Ref.Revision = oid
	page.Ref.ShortRevision = shortOID(oid)
	return canonical, true
}

func displayRef(ref string) string {
	for _, prefix := range []string{"refs/heads/", "refs/tags/"} {
		if strings.HasPrefix(ref, prefix) {
			return strings.TrimPrefix(ref, prefix)
		}
	}
	return ref
}

func refKind(ref string) string {
	if strings.HasPrefix(ref, "refs/tags/") {
		return "tag"
	}
	return "branch"
}

func (app *App) fillRepositoryOverview(request *http.Request, page *webui.RepositoryPage, summary repository.Summary, requested string) {
	selectedRef, resolved := app.selectRef(request, page, summary, requested)
	if page.Ref.Missing {
		code := webui.MsgRepoRefMissing
		if requested == "" {
			code = webui.MsgRepoDefaultGone
		}
		page.Chrome.Notices = append(page.Chrome.Notices, webui.Notice{Kind: webui.NoticeWarning, Code: code})
	}
	if page.Repo.Empty {
		page.Overview.PushCommands = []string{
			"git remote add origin " + page.Repo.CloneURL,
			"git push -u origin main",
		}
		page.Overview.Activity = emptyActivityGraph(selectedYear(request, app.now().Year()), app.now(), page.Repo.Name)
		return
	}
	if resolved {
		_, commits, err := app.Repositories.Commits(request.Context(), page.Repo.ID, selectedRef, 1)
		if err == nil && len(commits) == 1 {
			page.Overview.Head = app.commitSummary(page.Repo.ID, selectedRef, commits[0])
		}
	}
	for _, branch := range summary.Branches {
		full := "refs/heads/" + branch.Name
		line := webui.RefLine{Name: branch.Name, Kind: "branch", IsDefault: branch.Name == summary.DefaultBranch, URL: withRef(page.Repo.URL, full)}
		_, commits, err := app.Repositories.Commits(request.Context(), page.Repo.ID, full, 1)
		if err == nil && len(commits) == 1 {
			line.Tip = app.commitSummary(page.Repo.ID, full, commits[0])
		}
		page.Overview.Branches = append(page.Overview.Branches, line)
	}
	for _, tag := range summary.Tags {
		full := "refs/tags/" + tag.Name
		line := webui.RefLine{Name: tag.Name, Kind: "tag", URL: withRef(page.Repo.URL, full), Annotated: tag.Type == "tag"}
		_, commits, err := app.Repositories.Commits(request.Context(), page.Repo.ID, full, 1)
		if err == nil && len(commits) == 1 {
			line.Tip = app.commitSummary(page.Repo.ID, full, commits[0])
		}
		page.Overview.Tags = append(page.Overview.Tags, line)
	}
	retained, err := app.Repositories.RetainedRefs(request.Context(), page.Repo.ID)
	if err == nil {
		for _, ref := range retained {
			page.Overview.RetainedRefs = append(page.Overview.RetainedRefs, webui.RefLine{Name: shortOID(ref.OID), Kind: ref.Kind, Retained: true})
		}
	}
	activity, err := app.Repositories.Activity(request.Context(), page.Repo.ID, maximumActivityCommits)
	if err != nil {
		page.Overview.Activity = unavailableActivityGraph(selectedYear(request, app.now().Year()), page.Repo.Name)
	} else {
		page.Overview.Activity = activityGraph(activity, selectedYear(request, app.now().Year()), app.now(), page.Repo.Name)
	}
}

func (app *App) fillCode(request *http.Request, page *webui.RepositoryPage, summary repository.Summary, requested, requestedPath string) {
	selectedRef, resolved := app.selectRef(request, page, summary, requested)
	if !resolved {
		if requested == "" && page.Ref.Missing {
			page.Chrome.Notices = append(page.Chrome.Notices, webui.Notice{Kind: webui.NoticeWarning, Code: webui.MsgRepoDefaultGone})
		}
		return
	}
	if requestedPath != "" {
		_, blob, err := app.Repositories.ReadBlob(request.Context(), page.Repo.ID, selectedRef, requestedPath, 2<<20)
		if err == nil {
			binary := blob.Binary || !utf8.Valid(blob.Content)
			file := &webui.FileView{Path: requestedPath, Size: int64(len(blob.Content)), Binary: binary, Truncated: blob.Truncated}
			if !binary {
				content := strings.ReplaceAll(string(blob.Content), "\r\n", "\n")
				file.Lines = strings.Split(strings.TrimSuffix(content, "\n"), "\n")
			}
			parent := path.Dir(requestedPath)
			if parent == "." {
				parent = ""
			}
			_, siblings, _ := app.Repositories.Tree(request.Context(), page.Repo.ID, selectedRef, parent)
			view := webui.CodeView{Path: requestedPath, Crumbs: codeCrumbs(page.Repo.ID, selectedRef, requestedPath), File: file, Entries: treeViewEntries(page.Repo.ID, selectedRef, siblings)}
			view.UpURL = codeURL(page.Repo.ID, selectedRef, parent)
			for _, sibling := range siblings {
				if sibling.Path == requestedPath && sibling.Size >= 0 {
					file.Size = sibling.Size
				}
			}
			page.Code = view
			return
		}
	}
	_, entries, err := app.Repositories.Tree(request.Context(), page.Repo.ID, selectedRef, requestedPath)
	if err != nil || (requestedPath != "" && len(entries) == 0) {
		page.Code = webui.CodeView{Path: requestedPath, NotFound: true, Crumbs: codeCrumbs(page.Repo.ID, selectedRef, requestedPath)}
		return
	}
	view := webui.CodeView{Path: requestedPath, Crumbs: codeCrumbs(page.Repo.ID, selectedRef, requestedPath)}
	if requestedPath != "" {
		view.UpURL = codeURL(page.Repo.ID, selectedRef, path.Dir(requestedPath))
	}
	view.Entries = treeViewEntries(page.Repo.ID, selectedRef, entries)
	page.Code = view
}

func (app *App) fillCommits(request *http.Request, page *webui.RepositoryPage, summary repository.Summary, requested, openedOID string) {
	selectedRef, resolved := app.selectRef(request, page, summary, requested)
	if !resolved && requested == "" && page.Ref.Missing {
		page.Chrome.Notices = append(page.Chrome.Notices, webui.Notice{Kind: webui.NoticeWarning, Code: webui.MsgRepoDefaultGone})
	}
	if resolved && openedOID != "" {
		reachable, err := app.Repositories.CommitReachableFrom(request.Context(), page.Repo.ID, page.Ref.Revision, openedOID)
		if err != nil {
			page.Commits.NotFound = true
			return
		}
		if !reachable {
			resolved = false
			selectedRef = ""
			page.OverviewURL = page.Repo.URL
			page.CodeURL = page.Repo.URL + "/code"
			page.CommitsURL = page.Repo.URL + "/commits"
		}
	}
	if resolved {
		_, commits, err := app.Repositories.Commits(request.Context(), page.Repo.ID, selectedRef, 100)
		if err != nil {
			page.Commits.NotFound = true
			return
		}
		for _, commit := range commits {
			page.Commits.List = append(page.Commits.List, app.commitSummary(page.Repo.ID, selectedRef, commit))
		}
	}
	if openedOID == "" {
		return
	}
	if !resolved {
		page.Ref = webui.RefSelection{Name: shortOID(openedOID), Kind: "revision", Detached: true, Revision: openedOID, ShortRevision: shortOID(openedOID)}
	}
	files, err := app.Repositories.ChangedFiles(request.Context(), page.Repo.ID, openedOID)
	if err != nil {
		page.Commits.NotFound = true
		return
	}
	selected := request.URL.Query().Get("path")
	if selected == "" && len(files) != 0 {
		selected = files[0].Path
	}
	found := selected == "" && len(files) == 0
	for _, file := range files {
		if file.Path == selected {
			found = true
			break
		}
	}
	if !found {
		page.Commits.NotFound = true
		return
	}
	detail, err := app.Repositories.Commit(request.Context(), page.Repo.ID, openedOID, selected)
	if err != nil {
		page.Commits.NotFound = true
		return
	}
	if len(page.Commits.List) == 0 {
		page.Commits.List = append(page.Commits.List, app.commitSummary(page.Repo.ID, selectedRef, detail.Commit))
	}
	view := webui.CommitDetail{
		Commit: app.commitSummary(page.Repo.ID, selectedRef, detail.Commit), Body: detail.Body,
		CommitterName: detail.CommitterName, CommitterDate: detail.CommittedAt,
		SelectedPath: selected, Truncated: detail.Truncated,
	}
	if len(detail.Parents) > 1 {
		view.Unavailable = true
		view.UnavailableReason = webui.MsgCommitDiffMerge
	}
	for _, parentOID := range detail.Parents {
		view.Parents = append(view.Parents, webui.CommitSummary{OID: parentOID, ShortOID: shortOID(parentOID), URL: commitURL(page.Repo.ID, selectedRef, parentOID, "")})
	}
	for _, file := range files {
		item := webui.DiffFile{
			Path: file.Path, OldPath: file.OldPath, Status: file.Status, Additions: file.Additions, Deletions: file.Deletions,
			Binary: file.Binary, URL: commitURL(page.Repo.ID, selectedRef, openedOID, file.Path), Selected: file.Path == selected,
		}
		if item.Selected && !item.Binary {
			item.Hunks = parsePatch(detail.Diff)
		}
		view.Files = append(view.Files, item)
	}
	page.Commits.Detail = &view
}

func (app *App) repositorySummary(request *http.Request, stored state.Repository) (webui.RepositorySummary, error) {
	summary, err := app.Repositories.Summary(request.Context(), stored.ID)
	if err != nil {
		return webui.RepositorySummary{}, err
	}
	result := webui.RepositorySummary{
		ID: stored.ID, Name: stored.Name, Description: stored.Description,
		URL: "/repositories/" + url.PathEscape(stored.ID), CloneURL: app.baseURL(request) + "/git/" + url.PathEscape(stored.ID) + ".git",
		CreatedAt: stored.CreatedAt, Empty: summary.Empty, DefaultBranch: summary.DefaultBranch,
		DefaultBranchMissing: summary.DefaultBranch != "" && summary.DefaultOID == "",
		BranchCount:          len(summary.Branches), TagCount: len(summary.Tags), Counted: true,
	}
	if summary.DefaultOID != "" {
		defaultRef := "refs/heads/" + summary.DefaultBranch
		_, commits, err := app.Repositories.Commits(request.Context(), stored.ID, defaultRef, 1)
		if err == nil && len(commits) == 1 {
			result.Head = app.commitSummary(stored.ID, defaultRef, commits[0])
		}
	}
	return result, nil
}

func (app *App) commitSummary(repositoryID, ref string, commit repository.Commit) webui.CommitSummary {
	return webui.CommitSummary{
		OID: commit.OID, ShortOID: shortOID(commit.OID), Subject: commit.Subject,
		AuthorName: commit.AuthorName, AuthorDate: commit.AuthoredAt, URL: commitURL(repositoryID, ref, commit.OID, ""),
	}
}

func (app *App) repositoryActivityEntries(request *http.Request, stored state.Repository, maximum int) ([]webui.ActivityEntry, bool, error) {
	records, incomplete, err := app.Repositories.ActivityRecords(request.Context(), stored.ID, maximum)
	if err != nil {
		return nil, false, err
	}
	entries := make([]webui.ActivityEntry, 0, len(records))
	for _, record := range records {
		ref := displayRef(record.Source)
		linkRef := record.Source
		if record.Retained || record.Uncertain {
			linkRef = ""
			if ref == "" {
				ref = "retained " + shortOID(record.OID)
			}
		}
		entries = append(entries, webui.ActivityEntry{
			RepositoryID: stored.ID, RepositoryName: stored.Name, RepositoryURL: "/repositories/" + url.PathEscape(stored.ID),
			Ref: ref, RefRetained: record.Retained,
			Commit: webui.CommitSummary{OID: record.OID, ShortOID: shortOID(record.OID), Subject: record.Subject, AuthorName: record.AuthorName, AuthorDate: record.AuthoredAt, URL: commitURL(stored.ID, linkRef, record.OID, "")},
		})
	}
	return entries, incomplete, nil
}

func (app *App) aggregateActivity(request *http.Request, repositories []state.Repository, year int) webui.ActivityGraph {
	counts := make(map[string]int)
	complete := true
	available := true
	remaining := maximumActivityCommits
	for _, stored := range repositories {
		if remaining <= 0 {
			complete = false
			break
		}
		activity, err := app.Repositories.Activity(request.Context(), stored.ID, remaining)
		if err != nil {
			available = false
			continue
		}
		if activity.Incomplete {
			complete = false
		}
		remaining -= activity.Commits
		for _, day := range activity.Days {
			counts[day.Day] += day.Count
		}
	}
	graph := buildActivityGraph(counts, year, app.now(), len(repositories))
	graph.Complete = complete
	graph.Available = available
	if !complete {
		graph.IncompleteReason = webui.MsgActivityLimit
	}
	if !available {
		graph.UnavailableReason = webui.MsgActivityScanFail
	}
	return graph
}

func activityGraph(activity repository.Activity, year int, now time.Time, scope string) webui.ActivityGraph {
	counts := make(map[string]int, len(activity.Days))
	for _, day := range activity.Days {
		counts[day.Day] = day.Count
	}
	graph := buildActivityGraph(counts, year, now, 1)
	graph.Scope = scope
	graph.Complete = !activity.Incomplete
	if activity.Incomplete {
		graph.IncompleteReason = webui.MsgActivityLimit
	}
	return graph
}

func buildActivityGraph(counts map[string]int, year int, now time.Time, repositoryCount int) webui.ActivityGraph {
	graph := webui.ActivityGraph{Year: year, RepositoryCount: repositoryCount, Complete: true, Available: true}
	start := time.Date(year, time.January, 1, 0, 0, 0, 0, now.Location())
	end := start.AddDate(1, 0, 0)
	for day := start; day.Before(end); day = day.AddDate(0, 0, 1) {
		key := day.Format("2006-01-02")
		count := counts[key]
		graph.Days = append(graph.Days, webui.ActivityDay{Date: day, Count: count, Future: day.After(now), URL: "/activity?date=" + key + "&year=" + strconv.Itoa(year)})
		graph.Total += count
	}
	yearSet := map[int]struct{}{now.Year(): {}, year: {}}
	for day := range counts {
		if candidate, err := strconv.Atoi(strings.SplitN(day, "-", 2)[0]); err == nil {
			yearSet[candidate] = struct{}{}
		}
	}
	years := make([]int, 0, len(yearSet))
	for candidate := range yearSet {
		years = append(years, candidate)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(years)))
	for _, candidate := range years {
		graph.Years = append(graph.Years, webui.ActivityYear{Year: candidate, URL: "/activity?year=" + strconv.Itoa(candidate)})
	}
	return graph
}

func emptyActivityGraph(year int, now time.Time, scope string) webui.ActivityGraph {
	graph := buildActivityGraph(nil, year, now, 1)
	graph.Scope = scope
	return graph
}

func unavailableActivityGraph(year int, scope string) webui.ActivityGraph {
	return webui.ActivityGraph{Year: year, Scope: scope, Available: false, Complete: false, UnavailableReason: webui.MsgActivityScanFail}
}

func selectedYear(request *http.Request, fallback int) int {
	year, err := strconv.Atoi(request.URL.Query().Get("year"))
	if err != nil || year < 1970 || year > 9999 {
		return fallback
	}
	return year
}

func sortActivityEntries(entries []webui.ActivityEntry) {
	sort.SliceStable(entries, func(left, right int) bool {
		leftDay := entries[left].Commit.AuthorDate.Format("2006-01-02")
		rightDay := entries[right].Commit.AuthorDate.Format("2006-01-02")
		if leftDay != rightDay {
			return leftDay > rightDay
		}
		return entries[left].Commit.AuthorDate.After(entries[right].Commit.AuthorDate)
	})
}

func groupActivity(entries []webui.ActivityEntry) []webui.ActivityDayGroup {
	var groups []webui.ActivityDayGroup
	for _, entry := range entries {
		day := time.Date(entry.Commit.AuthorDate.Year(), entry.Commit.AuthorDate.Month(), entry.Commit.AuthorDate.Day(), 0, 0, 0, 0, entry.Commit.AuthorDate.Location())
		if len(groups) == 0 || groups[len(groups)-1].Date.Format("2006-01-02") != day.Format("2006-01-02") {
			groups = append(groups, webui.ActivityDayGroup{Date: day})
		}
		groups[len(groups)-1].Entries = append(groups[len(groups)-1].Entries, entry)
	}
	return groups
}

func withRef(base, ref string) string {
	return base + "?ref=" + url.QueryEscape(ref)
}

func codeURL(repositoryID, ref, filePath string) string {
	values := url.Values{"ref": []string{ref}}
	if filePath != "" && filePath != "." {
		values.Set("path", filePath)
	}
	return "/repositories/" + url.PathEscape(repositoryID) + "/code?" + values.Encode()
}

func commitURL(repositoryID, ref, oid, filePath string) string {
	values := make(url.Values)
	if ref != "" {
		values.Set("ref", ref)
	}
	if filePath != "" {
		values.Set("path", filePath)
	}
	result := "/repositories/" + url.PathEscape(repositoryID) + "/commits/" + url.PathEscape(oid)
	if len(values) != 0 {
		result += "?" + values.Encode()
	}
	return result
}

func treeViewEntries(repositoryID, ref string, entries []repository.TreeEntry) []webui.TreeEntry {
	views := make([]webui.TreeEntry, 0, len(entries))
	for _, entry := range entries {
		kind := "file"
		switch {
		case entry.Type == "tree":
			kind = "dir"
		case entry.Type == "commit":
			kind = "submodule"
		case entry.Mode == "120000":
			kind = "symlink"
		}
		views = append(views, webui.TreeEntry{Name: entry.Name, Path: entry.Path, URL: codeURL(repositoryID, ref, entry.Path), Kind: kind, Size: entry.Size})
	}
	sort.SliceStable(views, func(left, right int) bool {
		leftDir := views[left].Kind == "dir"
		rightDir := views[right].Kind == "dir"
		if leftDir != rightDir {
			return leftDir
		}
		return strings.ToLower(views[left].Name) < strings.ToLower(views[right].Name)
	})
	return views
}

func codeCrumbs(repositoryID, ref, filePath string) []webui.Crumb {
	crumbs := []webui.Crumb{{Name: repositoryID, URL: codeURL(repositoryID, ref, ""), Current: filePath == ""}}
	if filePath == "" {
		return crumbs
	}
	parts := strings.Split(filePath, "/")
	for index, part := range parts {
		currentPath := strings.Join(parts[:index+1], "/")
		crumbs = append(crumbs, webui.Crumb{Name: part, URL: codeURL(repositoryID, ref, currentPath), Current: index == len(parts)-1})
	}
	return crumbs
}

func shortOID(oid string) string {
	if len(oid) > 10 {
		return oid[:10]
	}
	return oid
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

func parsePatch(patch string) []webui.DiffHunk {
	var hunks []webui.DiffHunk
	var current *webui.DiffHunk
	oldLine, newLine := 0, 0
	for _, line := range strings.Split(strings.ReplaceAll(patch, "\r\n", "\n"), "\n") {
		match := hunkHeader.FindStringSubmatch(line)
		if match != nil {
			oldLine, _ = strconv.Atoi(match[1])
			newLine, _ = strconv.Atoi(match[3])
			hunks = append(hunks, webui.DiffHunk{Header: line})
			current = &hunks[len(hunks)-1]
			continue
		}
		if current == nil || line == "\\ No newline at end of file" || line == "" {
			continue
		}
		diffLine := webui.DiffLine{Kind: "context", Text: line}
		switch line[0] {
		case '+':
			diffLine.Kind, diffLine.NewLine, diffLine.Text = "add", newLine, line[1:]
			newLine++
		case '-':
			diffLine.Kind, diffLine.OldLine, diffLine.Text = "del", oldLine, line[1:]
			oldLine++
		case ' ':
			diffLine.OldLine, diffLine.NewLine, diffLine.Text = oldLine, newLine, line[1:]
			oldLine++
			newLine++
		default:
			continue
		}
		current.Lines = append(current.Lines, diffLine)
	}
	return hunks
}
