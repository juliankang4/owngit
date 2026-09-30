package server

import (
	"context"
	"net/http"
	"strings"

	"owngit/internal/checkapi"
	"owngit/internal/state"
	"owngit/internal/webui"
)

const (
	codingToolsPath             = "/coding-tools"
	allHelperCredentialsAPIPath = "/api/v1/helper-credentials"

	// skillInstallCommand writes the skill shipped with owngit where Codex
	// and Pi look for a user's skills.
	skillInstallCommand = "owngit skill --install ~/.agents/skills"
)

// handleCodingTools shows how to connect a coding tool to this server, the
// tasks updated most recently, and, to a viewer who may see administrator
// data, every repository's helper credentials. It never shows a token: the
// server keeps only their hashes.
func (app *App) handleCodingTools(writer http.ResponseWriter, request *http.Request, settings state.Settings) {
	session, ok := app.requireGeneral(writer, request, settings)
	if !ok {
		return
	}
	chrome, err := app.chrome(writer, request, webui.SectionCoding, "", session.CSRF)
	if err != nil {
		app.answerUnavailable(writer, request, "page frame read", err)
		return
	}
	repositories, err := app.visibleRepositories(request)
	if err != nil {
		app.renderError(writer, request, unavailable(request, "repository list read", err), webui.MsgErrUnavailable, "")
		return
	}
	origin := app.serverOrigin(request)
	page := webui.CodingToolsPage{
		Chrome: chrome, Server: origin, PlainHTTP: strings.HasPrefix(origin, "http:"), PasswordFile: settings.AccessMode == "password",
		SkillCommand: skillInstallCommand, TasksLimit: maximumRecentTasks,
	}
	page.MCPCommand = mcpCommandFor(origin, page.PlainHTTP, page.PasswordFile)
	page.ClaudeCommand = "claude mcp add --scope user owngit -- " + page.MCPCommand
	page.CodexCommand = "codex mcp add owngit -- " + page.MCPCommand

	byID := make(map[string]state.Repository, len(repositories))
	for _, stored := range repositories {
		byID[stored.ID] = stored
	}
	status := http.StatusOK
	ctx := request.Context()
	if views, more, err := app.recentTaskViews(ctx, repositories); err != nil {
		page.TasksUnavailable, status = true, unavailable(request, "recent task read", err)
	} else {
		page.TasksTruncated = more
		for index, summary := range app.browserTaskSummaries(ctx, views) {
			page.Tasks = append(page.Tasks, webui.RecentTask{RepositoryName: byID[views[index].task.RepositoryID].Name, Task: summary})
		}
	}
	// The decision is the one every administrator page makes (see
	// fillAdminViewer); without it the credentials are not read at all.
	if chrome.Viewer.AdminConfirmed {
		page.CredentialsShown = true
		if credentials, err := app.allHelperCredentials(ctx, repositories); err != nil {
			page.CredentialsUnavailable, status = true, unavailable(request, "helper credential list read", err)
		} else {
			for _, credential := range credentials {
				page.Credentials = append(page.Credentials, webui.RepositoryHelperCredential{
					RepositoryName: byID[credential.RepositoryID].Name, ManageURL: baseHelperCredentialsURL(byID[credential.RepositoryID].Address),
					Credential: browserHelperCredential(credential),
				})
			}
		}
	}
	app.render(writer, request, status, page)
}

// mcpCommandFor is the owngit mcp command that reaches the server at origin
// with general access. The owner replaces PASSWORD_FILE with the file that
// holds the shared password.
func mcpCommandFor(origin string, plainHTTP, passwordFile bool) string {
	words := []string{"owngit", "mcp", "--server", shellWord(origin)}
	if plainHTTP {
		words = append(words, "--accept-insecure-http")
	}
	if passwordFile {
		words = append(words, "--password-file", "PASSWORD_FILE")
	}
	return strings.Join(words, " ")
}

// allHelperCredentials lists the helper credentials of repositories, in
// their order and in issue order within each, revoked ones included.
func (app *App) allHelperCredentials(ctx context.Context, repositories []state.Repository) ([]state.HelperCredential, error) {
	var credentials []state.HelperCredential
	for _, stored := range repositories {
		listed, err := app.Store.HelperCredentials(ctx, stored.ID)
		if err != nil {
			return nil, err
		}
		credentials = append(credentials, listed...)
	}
	return credentials, nil
}

// handleAllHelperCredentialsAPI answers GET /api/v1/helper-credentials with
// the administrator password: every repository's helper credentials, as the
// Coding tools page lists them.
func (app *App) handleAllHelperCredentialsAPI(writer http.ResponseWriter, request *http.Request) {
	if !app.authorizeAdminAPI(writer, request) {
		return
	}
	if request.Method != http.MethodGet {
		writeAPIMethodError(writer, http.MethodGet)
		return
	}
	repositories, err := app.visibleRepositories(request)
	if err != nil {
		writeAPIError(writer, unavailable(request, "repository list read", err), "state_unavailable", "OwnGit state is unavailable.", nil)
		return
	}
	credentials, err := app.allHelperCredentials(request.Context(), repositories)
	if err != nil {
		writeAPIError(writer, unavailable(request, "helper credential list read", err), "state_unavailable", "Helper credentials could not be read.", nil)
		return
	}
	addresses := make(map[string]string, len(repositories))
	for _, stored := range repositories {
		addresses[stored.ID] = stored.Address
	}
	response := checkapi.CredentialListResponse{OK: true, Credentials: make([]*checkapi.Credential, 0, len(credentials))}
	for _, credential := range credentials {
		item := credentialJSON(credential)
		item.RepositoryAddress = addresses[credential.RepositoryID]
		response.Credentials = append(response.Credentials, item)
	}
	writeAPIJSON(writer, http.StatusOK, response)
}
