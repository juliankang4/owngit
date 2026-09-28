package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"owngit/internal/pullrequest"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// apiProblem reads an API error answer.
func apiProblem(t *testing.T, response *http.Response) (int, string, string) {
	t.Helper()
	defer response.Body.Close()
	var envelope pullrequest.ErrorEnvelope
	noErrf(t, json.NewDecoder(response.Body).Decode(&envelope), "decode API error")
	return response.StatusCode, envelope.Error.Code, envelope.Error.Message
}

func setRunnerPolicy(t *testing.T, store *state.Store) {
	t.Helper()
	_, err := store.SetCheckPolicy(context.Background(), state.CheckPolicyInput{
		RepositoryID: "project", Executor: state.CheckExecutorExternalRunner,
		AllowedEvents: []string{"push"}, MaxTimeoutMS: 60_000, MaxOutputLimitBytes: 64 << 10,
		QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60_000,
	}, time.Now())
	noErr(t, err)
}

// Issuing a credential answers 4xx only when nothing was created. A storage
// failure is unavailable, with a fixed message that holds no internal text.
func TestCredentialIssuanceSeparatesInvalidInputFromStorageFailure(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server := serve(t, fixture.app.Handler())
	serverLog := captureServerLog(t)
	base := server.URL + "/api/v1/repositories/project"
	issue := func(resource string, input map[string]any) (int, string, string) {
		t.Helper()
		return apiProblem(t, adminAPIRequest(t, http.MethodPost, base+"/"+resource, input, "admin-password"))
	}

	status, code, _ := issue("runner-credentials", map[string]any{"label": "runner"})
	if status != http.StatusNotFound || code != "check_policy_not_found" {
		t.Fatalf("runner issue without a policy: %d %s", status, code)
	}
	setRunnerPolicy(t, fixture.store)
	for _, resource := range []string{"runner-credentials", "helper-credentials"} {
		for _, input := range []map[string]any{{"label": "two\nlines"}, {"label": "fine", "creation_id": "not-hex"}} {
			if status, code, message := issue(resource, input); status != http.StatusUnprocessableEntity || !strings.HasPrefix(code, "invalid_") || message != invalidCredentialInput {
				t.Fatalf("%s invalid input %v: %d %s %q", resource, input, status, code, message)
			}
		}
	}

	refuseWrites(t, fixture.store, "refuse_runner_insert", "INSERT ON check_runner_credentials")
	refuseWrites(t, fixture.store, "refuse_helper_insert", "INSERT ON helper_credentials")
	for resource, message := range map[string]string{
		"runner-credentials": "The runner credential could not be created.",
		"helper-credentials": "The helper credential could not be created.",
	} {
		status, code, got := issue(resource, map[string]any{"label": "refused"})
		if status != http.StatusServiceUnavailable || code != "state_unavailable" || got != message {
			t.Fatalf("%s storage failure: %d %s %q", resource, status, code, got)
		}
	}
	requireLogged(t, serverLog, "runner credential issue could not be completed: ", "helper credential issue could not be completed: ")
	runners, err := fixture.store.CheckRunnerCredentials(context.Background(), "project")
	noErr(t, err)
	helpers, err := fixture.store.HelperCredentials(context.Background(), "project")
	if err != nil || len(runners) != 0 || len(helpers) != 0 {
		t.Fatalf("a refused issue stored credentials: runners=%d helpers=%d err=%v", len(runners), len(helpers), err)
	}
}

// A revoke that could not be stored is unavailable. Only a credential that is
// missing or already revoked gets the not-found answer.
func TestCredentialRevokeFailureIsNotReportedAsAbsence(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	serverLog := captureServerLog(t)
	ctx := context.Background()
	setRunnerPolicy(t, fixture.store)
	runner, _, _, err := fixture.store.IssueCheckRunnerToken(ctx, "project", "runner", "", time.Now())
	noErr(t, err)
	hash := sha256.Sum256([]byte("synthetic-helper-token"))
	helper, _, err := fixture.store.CreateHelperCredential(ctx, "project", "helper", "", hash[:], time.Now())
	noErr(t, err)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "revoke-admin")
	apiBase := server.URL + "/api/v1/repositories/project"

	for _, test := range []struct {
		name, trigger, table, apiPath, pageURL, action, id string
		apiCode, apiMessage                                string
		pageMessage                                        webui.MessageCode
		revoked                                            func() bool
	}{
		{"runner", "refuse_runner_revoke", "check_runner_credentials", "/runner-credentials/", runnerTokensURL("project"), webui.ActionRevokeRunnerToken, runner.ID,
			"runner_credential_not_found", "The runner credential could not be revoked.", webui.MsgRTRevokeFailed, func() bool {
				credentials, err := fixture.store.CheckRunnerCredentials(ctx, "project")
				return err == nil && credentials[0].RevokedAt != nil
			}},
		{"helper", "refuse_helper_revoke", "helper_credentials", "/helper-credentials/", baseHelperCredentialsURL("project"), webui.ActionRevokeHelperCredential, helper.ID,
			"credential_not_found", "The helper credential could not be revoked.", webui.MsgHelperFailed, func() bool {
				credentials, err := fixture.store.HelperCredentials(ctx, "project")
				return err == nil && credentials[0].RevokedAt != nil
			}},
	} {
		refuseWrites(t, fixture.store, test.trigger, "UPDATE ON "+test.table)
		status, code, message := apiProblem(t, adminAPIRequest(t, http.MethodDelete, apiBase+test.apiPath+test.id, nil, "admin-password"))
		if status != http.StatusServiceUnavailable || code != "state_unavailable" || message != test.apiMessage {
			t.Fatalf("%s API revoke failure: %d %s %q", test.name, status, code, message)
		}
		endFailureWindows()
		page := browserForm(t, client, server.URL+test.pageURL, url.Values{
			"csrf": {csrf}, "action": {test.action}, "credential_id": {test.id}, "admin_password": {"admin-password"},
		}, server.URL)
		if notices := noticeRegion(t, page.body); page.status != http.StatusServiceUnavailable || !strings.Contains(notices, browserText(test.pageMessage)) {
			t.Fatalf("%s browser revoke failure: status=%d notices=%s", test.name, page.status, notices)
		}
		if test.revoked() {
			t.Fatalf("%s credential was revoked by a failed request", test.name)
		}
		requireLogged(t, serverLog, "DELETE /api/v1/repositories/project"+test.apiPath+test.id+": "+test.name+" credential revoke could not be completed: ",
			"POST "+test.pageURL+": "+test.name+" credential revoke could not be completed: ")

		noErr(t, fixture.store.Exec(ctx, `DROP TRIGGER `+test.trigger))
		if status, _, _ := apiProblem(t, adminAPIRequest(t, http.MethodDelete, apiBase+test.apiPath+test.id, nil, "admin-password")); status != http.StatusOK || !test.revoked() {
			t.Fatalf("%s revoke after recovery: %d", test.name, status)
		}
		if status, code, _ := apiProblem(t, adminAPIRequest(t, http.MethodDelete, apiBase+test.apiPath+test.id, nil, "admin-password")); status != http.StatusConflict || code != test.apiCode {
			t.Fatalf("%s repeated revoke: %d %s", test.name, status, code)
		}
	}
}

// elsewhereZone is a location one hour ahead of the server's local time.
func elsewhereZone() *time.Location {
	_, offset := time.Now().Zone()
	return time.FixedZone("elsewhere", offset+3600)
}

// Every credential row shows its times in the server's local time, whatever
// location the issuing clock or the producer gave them.
func TestCredentialRowsShowLocalTime(t *testing.T) {
	issuedAt := time.Now().Truncate(time.Second)
	for _, row := range []time.Time{
		browserRunnerCredential(state.RunnerCredential{CreatedAt: issuedAt.In(elsewhereZone())}).CreatedAt,
		browserHelperCredential(state.HelperCredential{CreatedAt: issuedAt.In(elsewhereZone())}).CreatedAt,
	} {
		if row.Location() != time.Local || !row.Equal(issuedAt) {
			t.Fatalf("row time %v is not the local time of %v", row, issuedAt)
		}
	}
}

// The row of a token just issued shows the same time as every later visit.
// The helper case is exact on any host; the runner producer stores UTC, so its
// case shows only on a server whose local time is not UTC.
func TestIssuedTokenRowShowsTheSameTimeAsLaterVisits(t *testing.T) {
	elsewhere := elsewhereZone()
	fixture := newAPIFixture(t, false)
	fixture.app.Now = func() time.Time { return time.Now().In(elsewhere) }
	server, client, jar := openBrowser(t, fixture)
	setRunnerPolicy(t, fixture.store)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "issued-time-admin")
	for _, test := range []struct {
		pageURL string
		values  url.Values
		stored  func() time.Time
	}{
		{runnerTokensURL("project"), url.Values{"action": {webui.ActionIssueRunnerToken}, "creation_id": {strings.Repeat("a", 32)}}, func() time.Time {
			credentials, err := fixture.store.CheckRunnerCredentials(context.Background(), "project")
			noErr(t, err)
			return credentials[0].CreatedAt
		}},
		{baseHelperCredentialsURL("project"), url.Values{"action": {webui.ActionIssueHelperCredential}}, func() time.Time {
			credentials, err := fixture.store.HelperCredentials(context.Background(), "project")
			noErr(t, err)
			return credentials[0].CreatedAt
		}},
	} {
		test.values.Set("csrf", csrf)
		test.values.Set("admin_password", "admin-password")
		test.values.Set("label", "timed")
		issued := browserForm(t, client, server.URL+test.pageURL, test.values, server.URL)
		stored := test.stored()
		local := stored.Format("Jan 2, 2006 15:04")
		for _, page := range []browserHTTPResult{issued, browserGET(t, client, server.URL+test.pageURL)} {
			if page.status != http.StatusOK || !strings.Contains(page.body, ">"+local+"<") {
				t.Fatalf("%s does not show the issued time %q: status=%d", test.pageURL, local, page.status)
			}
		}
	}
}

// A token screen whose records cannot be read says so. It does not report a
// failed operation that nobody asked for.
func TestCredentialScreensSayTheirRecordsCouldNotBeRead(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	ctx := context.Background()
	setRunnerPolicy(t, fixture.store)
	_, _, _, err := fixture.store.IssueCheckRunnerToken(ctx, "project", "runner", "", time.Now())
	noErr(t, err)
	hash := sha256.Sum256([]byte("synthetic-helper-token"))
	_, _, err = fixture.store.CreateHelperCredential(ctx, "project", "helper", "", hash[:], time.Now())
	noErr(t, err)
	browserAdminSessionFor(t, fixture, server.URL, jar, "unreadable-admin")

	for _, test := range []struct {
		table, pageURL      string
		unreadable, failure webui.MessageCode
	}{
		{"check_runner_credentials", runnerTokensURL("project"), webui.MsgRTUnreadable, webui.MsgRTFailed},
		{"helper_credentials", baseHelperCredentialsURL("project"), webui.MsgHelperUnreadable, webui.MsgHelperFailed},
	} {
		noErr(t, fixture.store.Exec(ctx, `UPDATE `+test.table+` SET created_at='unreadable'`))
		page := browserGET(t, client, server.URL+test.pageURL)
		if page.status != http.StatusServiceUnavailable || !strings.Contains(page.body, browserText(test.unreadable)) || strings.Contains(page.body, browserText(test.failure)) {
			t.Fatalf("%s with unreadable records: status=%d", test.pageURL, page.status)
		}
		noErr(t, fixture.store.Exec(ctx, `UPDATE `+test.table+` SET created_at=0`))
		if page := browserGET(t, client, server.URL+test.pageURL); page.status != http.StatusOK {
			t.Fatalf("%s after repair: status=%d", test.pageURL, page.status)
		}
	}
}

// Every unavailable answer of the token screens leaves one cause line in the
// server log: a visit whose records cannot be read, an issue whose earlier
// read fails, and an issue that cannot be stored.
func TestCredentialScreenFailuresAreLogged(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	serverLog := captureServerLog(t)
	ctx := context.Background()
	setRunnerPolicy(t, fixture.store)
	_, _, _, err := fixture.store.IssueCheckRunnerToken(ctx, "project", "runner", "", time.Now())
	noErr(t, err)
	hash := sha256.Sum256([]byte("synthetic-helper-token"))
	_, _, err = fixture.store.CreateHelperCredential(ctx, "project", "helper", "", hash[:], time.Now())
	noErr(t, err)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "logged-admin")

	for _, test := range []struct {
		kind, table, pageURL string
		values               url.Values
	}{
		{"runner", "check_runner_credentials", runnerTokensURL("project"), url.Values{"action": {webui.ActionIssueRunnerToken}, "creation_id": {strings.Repeat("b", 32)}}},
		{"helper", "helper_credentials", baseHelperCredentialsURL("project"), url.Values{"action": {webui.ActionIssueHelperCredential}}},
	} {
		test.values.Set("csrf", csrf)
		test.values.Set("admin_password", "admin-password")
		test.values.Set("label", "logged")
		noErr(t, fixture.store.Exec(ctx, `UPDATE `+test.table+` SET created_at='unreadable'`))
		if page := browserGET(t, client, server.URL+test.pageURL); page.status != http.StatusServiceUnavailable {
			t.Fatalf("%s visit with unreadable records: status=%d", test.kind, page.status)
		}
		endFailureWindows()
		if page := browserForm(t, client, server.URL+test.pageURL, test.values, server.URL); page.status != http.StatusServiceUnavailable {
			t.Fatalf("%s issue with an unreadable list: status=%d", test.kind, page.status)
		}
		noErr(t, fixture.store.Exec(ctx, `UPDATE `+test.table+` SET created_at=0`))
		refuseWrites(t, fixture.store, "refuse_"+test.kind+"_issue", "INSERT ON "+test.table)
		if page := browserForm(t, client, server.URL+test.pageURL, test.values, server.URL); page.status != http.StatusServiceUnavailable {
			t.Fatalf("%s issue that cannot be stored: status=%d", test.kind, page.status)
		}
		noErr(t, fixture.store.Exec(ctx, `DROP TRIGGER refuse_`+test.kind+`_issue`))

		logged := serverLog.String()
		for _, line := range []string{
			"GET " + test.pageURL + ": " + test.kind + " credential list read could not be completed: ",
			"POST " + test.pageURL + ": " + test.kind + " credential list read could not be completed: ",
			"POST " + test.pageURL + ": " + test.kind + " credential issue could not be completed: ",
		} {
			if count := strings.Count(logged, line); count != 1 {
				t.Fatalf("%s: %q logged %d times:\n%s", test.kind, line, count, logged)
			}
		}
		requireLogged(t, serverLog)
	}
}

// A token that was issued is delivered on its one response, even when the
// list shown around it can no longer be read.
func TestIssuedTokenIsDeliveredWhenTheListCannotBeReadAfterward(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	ctx := context.Background()
	setRunnerPolicy(t, fixture.store)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "deliver-admin")

	for _, test := range []struct {
		name, table, pageURL, label string
		values                      url.Values
		usable                      func(token string) bool
	}{
		{"runner", "check_runner_credentials", runnerTokensURL("project"), "delivered runner",
			url.Values{"action": {webui.ActionIssueRunnerToken}, "creation_id": {strings.Repeat("f", 32)}},
			func(token string) bool {
				_, ok, err := fixture.store.RunnerCredentialByToken(ctx, "project", token, time.Now())
				return err == nil && ok
			}},
		{"helper", "helper_credentials", baseHelperCredentialsURL("project"), "delivered helper",
			url.Values{"action": {webui.ActionIssueHelperCredential}},
			func(token string) bool {
				hash := sha256.Sum256([]byte(token))
				_, ok, err := fixture.store.HelperCredentialByToken(ctx, hash[:], time.Now())
				return err == nil && ok
			}},
	} {
		// The new row becomes unreadable as soon as it is stored, so any list
		// read after the issue fails.
		noErr(t, fixture.store.Exec(ctx, `CREATE TRIGGER break_`+test.name+`_list AFTER INSERT ON `+test.table+
			` BEGIN UPDATE `+test.table+` SET created_at='unreadable' WHERE id=NEW.id; END`))
		test.values.Set("csrf", csrf)
		test.values.Set("admin_password", "admin-password")
		test.values.Set("label", test.label)
		issued := browserForm(t, client, server.URL+test.pageURL, test.values, server.URL)
		if issued.status != http.StatusOK || !strings.Contains(issued.body, test.label) {
			t.Fatalf("%s issue with a later unreadable list: status=%d", test.name, issued.status)
		}
		token := issuedTokenFromBody(t, issued.body)

		noErr(t, fixture.store.Exec(ctx, `DROP TRIGGER break_`+test.name+`_list`))
		noErr(t, fixture.store.Exec(ctx, `UPDATE `+test.table+` SET created_at=0 WHERE created_at='unreadable'`))
		if !test.usable(token) {
			t.Fatalf("%s delivered token is not the stored credential", test.name)
		}
		if later := browserGET(t, client, server.URL+test.pageURL); later.status != http.StatusOK || strings.Contains(later.body, token) {
			t.Fatalf("%s token reached a later page: status=%d", test.name, later.status)
		}
	}
}
