package server

import (
	"context"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"owngit/internal/pullrequest"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// browserText is a catalog sentence as it appears in rendered HTML, so a
// sentence containing an apostrophe still matches the page.
func browserText(code webui.MessageCode) string {
	return template.HTMLEscapeString(webui.Text(webui.LangEN, code))
}

// noticeRegion is the page's notice block, which is where a result reports
// what a submission achieved.
//
// A test about the reported result has to read this region rather than the
// whole page. The rest of the page states durable facts about the records
// themselves, and one of those, the job list's "a cancellation was recorded"
// marker, is true for a job whose cancel intent was stored even when the
// request changed no result. Searching the whole document would make the
// truthful history indistinguishable from a wrong result message.
//
// The block is the fixed `<div class="notices">` from the layout, so this is a
// literal extraction rather than HTML parsing. An absent block is an empty
// region: a page that reported nothing.
func noticeRegion(t *testing.T, body string) string {
	t.Helper()
	const open = `<div class="notices">`
	start := strings.Index(body, open)
	if start < 0 {
		return ""
	}
	start += len(open)
	end := strings.Index(body[start:], "</div>")
	if end < 0 {
		t.Fatal("the notice block is not closed")
	}
	return body[start : start+end]
}

// browserAdminSessionFor installs an administrator session in the jar and
// returns its CSRF token. The tests need it because every screen under test is
// administrator only before it reads anything.
func browserAdminSessionFor(t *testing.T, fixture apiFixture, serverURL string, jar http.CookieJar, name string) string {
	t.Helper()
	settings, err := fixture.store.Settings(context.Background())
	noErr(t, err)
	token, csrf := name+"-session", name+"-csrf"
	noErr(t, fixture.store.CreateSession(context.Background(), token, "admin", csrf, settings.AdminSessionVersion, time.Now().Add(time.Hour)))
	parsed, _ := url.Parse(serverURL)
	jar.SetCookies(parsed, []*http.Cookie{{Name: adminCookie, Value: token, Path: "/"}})
	return csrf
}

// policyNoteID is the id the policy form gives a field's message. The form
// namespaces it by its action so aria-describedby on one form cannot point at
// another form's message on the same page.
func policyNoteID(field string) string {
	return webui.ActionSaveCheckPolicy + "-" + field + "-note"
}

// validPolicyValues is a policy the backend accepts, used as the starting
// point for the refusal cases below.
func validPolicyValues(csrf string) url.Values {
	return url.Values{
		"csrf":                   {csrf},
		"action":                 {webui.ActionSaveCheckPolicy},
		"admin_password":         {"admin-password"},
		"executor":               {webui.ExecutorHost},
		"event_push":             {"1"},
		"max_timeout_ms":         {"600000"},
		"max_output_limit_bytes": {"262144"},
		"queue_limit":            {"20"},
		"max_active_jobs":        {"2"},
		"max_lease_ms":           {"120000"},
	}
}

func TestBrowserConfiguredChecksRequireAdminSessionAndPasswordPerChange(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)

	policyURL := server.URL + configuredChecksURL("project")
	// A general visitor never reaches the screen, and the refusal happens
	// before anything is read rather than by hiding controls.
	withoutAdmin := browserGET(t, client, policyURL)
	if withoutAdmin.status != http.StatusSeeOther || !strings.HasPrefix(withoutAdmin.header.Get("Location"), "/admin/login") {
		t.Fatalf("policy screen without admin status=%d location=%q", withoutAdmin.status, withoutAdmin.header.Get("Location"))
	}

	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "cc-admin")
	listed := browserGET(t, client, policyURL)
	if listed.status != http.StatusOK || !strings.Contains(listed.body, `name="csrf" value="`+csrf+`"`) {
		t.Fatalf("admin policy screen status=%d", listed.status)
	}
	if listed.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("policy screen cache-control=%q", listed.header.Get("Cache-Control"))
	}

	// A remembered session is not consent to grant execution authority.
	noPassword := validPolicyValues(csrf)
	noPassword.Del("admin_password")
	if result := browserForm(t, client, policyURL, noPassword, server.URL); result.status != http.StatusUnauthorized {
		t.Fatalf("remembered session saved a policy without a password: status=%d", result.status)
	}
	if _, exists, err := fixture.store.CheckPolicy(context.Background(), "project"); err != nil || exists {
		t.Fatalf("password bypass stored a policy: exists=%v err=%v", exists, err)
	}

	wrongCSRF := validPolicyValues(csrf)
	wrongCSRF.Set("csrf", "wrong-csrf")
	if result := browserForm(t, client, policyURL, wrongCSRF, server.URL); result.status != http.StatusForbidden {
		t.Fatalf("policy saved with a wrong csrf token: status=%d", result.status)
	}
	if _, exists, err := fixture.store.CheckPolicy(context.Background(), "project"); err != nil || exists {
		t.Fatalf("csrf bypass stored a policy: exists=%v err=%v", exists, err)
	}
}

func TestBrowserPolicySaveDoesNotEnableExecution(t *testing.T) {
	// Saving settings and granting permission to run are separate decisions,
	// and a saved policy must never arrive with consent already attached.
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "cc-consent")
	policyURL := server.URL + configuredChecksURL("project")

	saved := browserForm(t, client, policyURL, validPolicyValues(csrf), server.URL)
	if saved.status != http.StatusSeeOther || saved.header.Get("Location") != configuredChecksURL("project")+"?notice=check_policy_saved" {
		t.Fatalf("policy save status=%d location=%q", saved.status, saved.header.Get("Location"))
	}
	policy, exists, err := fixture.store.CheckPolicy(context.Background(), "project")
	if err != nil || !exists {
		t.Fatalf("policy was not stored: exists=%v err=%v", exists, err)
	}
	if policy.ConsentActive {
		t.Fatal("saving a policy turned execution on by itself")
	}

	// Enabling binds to one exact policy generation. A stale identity is a
	// request to enable something the owner has not read, and so is a version
	// quoted without the digest that went with it.
	for name, identity := range map[string]url.Values{
		"stale version":  {"policy_version": {"0"}, "policy_digest": {policy.Digest}},
		"stale digest":   {"policy_version": {formatOptionalInt(policy.Version)}, "policy_digest": {"an-older-digest"}},
		"no digest":      {"policy_version": {formatOptionalInt(policy.Version)}},
		"no identity":    {},
		"unparsable":     {"policy_version": {"soon"}, "policy_digest": {policy.Digest}},
		"digest as name": {"policy_version": {formatOptionalInt(policy.Version)}, "policy_digest": {""}},
	} {
		stale := url.Values{
			"csrf": {csrf}, "action": {webui.ActionEnableChecks},
			"admin_password": {"admin-password"},
		}
		for key, value := range identity {
			stale[key] = value
		}
		if result := browserForm(t, client, policyURL, stale, server.URL); result.status != http.StatusConflict {
			t.Fatalf("%s enabled execution: status=%d", name, result.status)
		}
		if current, _, err := fixture.store.CheckPolicy(context.Background(), "project"); err != nil || current.ConsentActive {
			t.Fatalf("%s granted consent: consent=%v err=%v", name, current.ConsentActive, err)
		}
	}

	enable := url.Values{
		"csrf": {csrf}, "action": {webui.ActionEnableChecks},
		"admin_password": {"admin-password"},
		"policy_version": {formatOptionalInt(policy.Version)},
		"policy_digest":  {policy.Digest},
	}
	enabled := browserForm(t, client, policyURL, enable, server.URL)
	if enabled.status != http.StatusSeeOther || enabled.header.Get("Location") != configuredChecksURL("project")+"?notice=checks_enabled" {
		t.Fatalf("enable status=%d location=%q", enabled.status, enabled.header.Get("Location"))
	}
	if current, _, err := fixture.store.CheckPolicy(context.Background(), "project"); err != nil || !current.ConsentActive {
		t.Fatalf("enable did not record consent: consent=%v err=%v", current.ConsentActive, err)
	}

	// Changing the policy clears the confirmation, so a new policy never
	// inherits permission granted for the previous one.
	changed := validPolicyValues(csrf)
	changed.Set("max_timeout_ms", "300000")
	if result := browserForm(t, client, policyURL, changed, server.URL); result.status != http.StatusSeeOther {
		t.Fatalf("policy change status=%d", result.status)
	}
	after, _, err := fixture.store.CheckPolicy(context.Background(), "project")
	noErr(t, err)
	if after.ConsentActive {
		t.Fatal("a changed policy kept the consent granted for the previous one")
	}
}

func TestBrowserSaveResultDoesNotClaimExecutionIsOffWhenItIsOn(t *testing.T) {
	// Resubmitting an unchanged policy leaves consent exactly as it was. The
	// result must not tell an owner with running checks that execution is off.
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "cc-resave")
	policyURL := server.URL + configuredChecksURL("project")

	if result := browserForm(t, client, policyURL, validPolicyValues(csrf), server.URL); result.status != http.StatusSeeOther {
		t.Fatalf("first save status=%d", result.status)
	}
	policy, _, err := fixture.store.CheckPolicy(context.Background(), "project")
	noErr(t, err)
	enable := url.Values{
		"csrf": {csrf}, "action": {webui.ActionEnableChecks},
		"admin_password": {"admin-password"},
		"policy_version": {formatOptionalInt(policy.Version)},
		"policy_digest":  {policy.Digest},
	}
	if result := browserForm(t, client, policyURL, enable, server.URL); result.status != http.StatusSeeOther {
		t.Fatalf("enable status=%d", result.status)
	}

	resaved := browserForm(t, client, policyURL, validPolicyValues(csrf), server.URL)
	location := resaved.header.Get("Location")
	if resaved.status != http.StatusSeeOther || location != configuredChecksURL("project")+"?notice=check_policy_saved_enabled" {
		t.Fatalf("unchanged resave status=%d location=%q", resaved.status, location)
	}
	current, _, err := fixture.store.CheckPolicy(context.Background(), "project")
	if err != nil || !current.ConsentActive {
		t.Fatalf("an unchanged resave turned execution off: consent=%v err=%v", current.ConsentActive, err)
	}

	// The follow-up page has to actually say it. A redirect key with no case
	// in the notice table renders nothing at all.
	shown := browserGET(t, client, server.URL+location)
	if shown.status != http.StatusOK {
		t.Fatalf("notice page status=%d", shown.status)
	}
	if !strings.Contains(shown.body, webui.Text(webui.LangEN, webui.MsgCCSavedEnabled)) {
		t.Fatal("the saved-while-enabled result was dropped instead of shown")
	}
	if strings.Contains(shown.body, webui.Text(webui.LangEN, webui.MsgCCSaved)) {
		t.Fatal("the screen claimed execution is still off while it was on")
	}
}

func TestBrowserEveryCheckResultReachesTheScreen(t *testing.T) {
	// Each redirect this package issues names a notice. A key with no case in
	// the table is dropped silently, leaving the owner with no statement about
	// what their submission did.
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	browserAdminSessionFor(t, fixture, server.URL, jar, "cc-notice")

	for key, code := range map[string]webui.MessageCode{
		"check_policy_saved":         webui.MsgCCSaved,
		"check_policy_saved_enabled": webui.MsgCCSavedEnabled,
		"checks_enabled":             webui.MsgCCEnabled,
		"checks_disabled":            webui.MsgCCDisabled,
		"check_job_cancelled":        webui.MsgCCJobCancelled,
		"check_job_rerun":            webui.MsgCCJobRerunQueued,
		"check_job_rerun_existing":   webui.MsgCCJobRerunExisting,
	} {
		shown := browserGET(t, client, server.URL+configuredChecksURL("project")+"?notice="+key)
		if shown.status != http.StatusOK {
			t.Fatalf("%s: status=%d", key, shown.status)
		}
		if !strings.Contains(shown.body, webui.Text(webui.LangEN, code)) {
			t.Fatalf("%s: the result was dropped instead of shown", key)
		}
	}
	shown := browserGET(t, client, server.URL+runnerTokensURL("project")+"?notice=runner_token_revoked")
	if shown.status != http.StatusOK || !strings.Contains(shown.body, webui.Text(webui.LangEN, webui.MsgRTRevokedDone)) {
		t.Fatalf("runner_token_revoked status=%d, result shown=%v",
			shown.status, strings.Contains(shown.body, webui.Text(webui.LangEN, webui.MsgRTRevokedDone)))
	}
}

func TestBrowserFailedConsentChangeKeepsTheSavedPolicyOnScreen(t *testing.T) {
	// The enable and disable forms carry no policy fields. A refusal on one of
	// them must not repaint the editor from that empty submission and blank
	// every saved setting the owner is looking at.
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "cc-keep")
	policyURL := server.URL + configuredChecksURL("project")
	if result := browserForm(t, client, policyURL, validPolicyValues(csrf), server.URL); result.status != http.StatusSeeOther {
		t.Fatalf("policy save status=%d", result.status)
	}
	policy, _, err := fixture.store.CheckPolicy(context.Background(), "project")
	noErr(t, err)

	wrongPassword := url.Values{
		"csrf": {csrf}, "action": {webui.ActionEnableChecks},
		"admin_password": {"not-the-password"},
		"policy_version": {formatOptionalInt(policy.Version)},
		"policy_digest":  {policy.Digest},
	}
	refused := browserForm(t, client, policyURL, wrongPassword, server.URL)
	if refused.status != http.StatusUnauthorized {
		t.Fatalf("wrong password status=%d", refused.status)
	}
	// The saved values are still the ones on screen.
	for field, value := range map[string]string{
		"max_timeout_ms":  "600000",
		"queue_limit":     "20",
		"max_active_jobs": "2",
		"max_lease_ms":    "120000",
	} {
		if !strings.Contains(refused.body, `value="`+value+`"`) {
			t.Fatalf("a failed enable blanked %s: %q is not on the screen", field, value)
		}
	}
	if strings.Contains(refused.body, `value="not-the-password"`) {
		t.Fatal("the administrator password was echoed back")
	}
}

func TestTheDisplayedRangeIsTheRangeTheBackendEnforces(t *testing.T) {
	// The screen must not print a range of its own. Each displayed bound is
	// checked against the backend's published value, and then against actual
	// behaviour: the boundary is accepted and one step outside it is refused.
	// If someone later changes a bound in only one place, this fails.
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "cc-ranges")
	policyURL := server.URL + configuredChecksURL("project")

	screen := browserGET(t, client, policyURL)
	if screen.status != http.StatusOK {
		t.Fatalf("policy screen status=%d", screen.status)
	}
	// Every numeric control the editor draws states its range, and the numbers
	// are the backend's.
	for _, field := range policyRangeFields {
		bounds, known := state.CheckPolicyBoundsFor(field)
		if !known {
			t.Errorf("%s is drawn but publishes no bounds", field)
			continue
		}
		high := strconv.FormatInt(bounds.Max, 10)
		if !bounds.HasFixedMinimum() {
			// A moving floor names the field it depends on and still states
			// the maximum. The ceiling is what must not go unstated.
			if !strings.Contains(screen.body, "up to "+high) {
				t.Errorf("%s does not show its maximum %s", field, high)
			}
			continue
		}
		low := strconv.FormatInt(bounds.Min, 10)
		if !strings.Contains(screen.body, "Accepted range: "+low+" to "+high) {
			t.Errorf("%s does not show the backend range %s to %s", field, low, high)
		}
	}

	// And the stated bound is the one that actually decides. The queue limit
	// stands for the group: its maximum is accepted and maximum+1 is not.
	bounds, known := state.CheckPolicyBoundsFor(state.FieldQueueLimit)
	if !known {
		t.Fatal("the queue limit publishes no range")
	}
	atBound := validPolicyValues(csrf)
	atBound.Set("queue_limit", strconv.FormatInt(bounds.Max, 10))
	if result := browserForm(t, client, policyURL, atBound, server.URL); result.status != http.StatusSeeOther {
		t.Fatalf("the stated maximum was refused: status=%d", result.status)
	}
	pastBound := validPolicyValues(csrf)
	pastBound.Set("queue_limit", strconv.FormatInt(bounds.Max+1, 10))
	refused := browserForm(t, client, policyURL, pastBound, server.URL)
	if refused.status != http.StatusUnprocessableEntity {
		t.Fatalf("one past the stated maximum was accepted: status=%d", refused.status)
	}
	// The refused screen still shows the range its message refers to.
	if !strings.Contains(refused.body, "Accepted range: "+strconv.FormatInt(bounds.Min, 10)+" to "+strconv.FormatInt(bounds.Max, 10)) {
		t.Fatal("the refusal refers to a range the screen does not show")
	}
}

func TestBrowserPolicyRefusalNamesTheFieldTheBackendRefused(t *testing.T) {
	// The backend decides what is acceptable. The screen's job is to put that
	// answer beside the control it belongs to, with the invalid state and the
	// description link a reader who cannot see the colour depends on.
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "cc-fields")
	policyURL := server.URL + configuredChecksURL("project")

	for name, test := range map[string]struct {
		mutate func(url.Values)
		noteID string
	}{
		"timeout below the accepted range": {
			func(v url.Values) { v.Set("max_timeout_ms", "5") },
			policyNoteID("max_timeout_ms"),
		},
		"queue limit above the accepted range": {
			func(v url.Values) { v.Set("queue_limit", "100000") },
			policyNoteID("queue_limit"),
		},
		"no event chosen": {
			func(v url.Values) { v.Del("event_push") },
			policyNoteID("events"),
		},
		"unknown executor": {
			func(v url.Values) { v.Set("executor", "sandbox") },
			policyNoteID("executor"),
		},
		"container image that is not immutable": {
			func(v url.Values) {
				v.Set("executor", webui.ExecutorContainer)
				v.Set("container_image", "registry.example/checks:latest")
			},
			policyNoteID("container_image"),
		},
	} {
		values := validPolicyValues(csrf)
		test.mutate(values)
		refused := browserForm(t, client, policyURL, values, server.URL)
		if refused.status != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status=%d", name, refused.status)
		}
		if !strings.Contains(refused.body, `id="`+test.noteID+`"`) {
			t.Fatalf("%s: the refusal did not appear beside its own control", name)
		}
		if !strings.Contains(refused.body, `aria-describedby="`+test.noteID+`"`) {
			t.Fatalf("%s: the control does not point at the message describing it", name)
		}
		if !strings.Contains(refused.body, `aria-invalid="true"`) {
			t.Fatalf("%s: the refused control is not marked invalid", name)
		}
		if _, exists, err := fixture.store.CheckPolicy(context.Background(), "project"); err != nil || exists {
			t.Fatalf("%s: a refused policy was stored: exists=%v err=%v", name, exists, err)
		}
	}
}

func TestBrowserRefusedPolicyIsNotStoredAndKeepsTheSubmittedValues(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "cc-refuse")
	policyURL := server.URL + configuredChecksURL("project")

	for name, mutate := range map[string]func(url.Values){
		"no event selected":   func(v url.Values) { v.Del("event_push") },
		"unknown executor":    func(v url.Values) { v.Set("executor", "sandbox") },
		"non-numeric timeout": func(v url.Values) { v.Set("max_timeout_ms", "ten minutes") },
		"negative queue":      func(v url.Values) { v.Set("queue_limit", "-5") },
	} {
		values := validPolicyValues(csrf)
		mutate(values)
		result := browserForm(t, client, policyURL, values, server.URL)
		if result.status != http.StatusUnprocessableEntity {
			t.Fatalf("%s: refused policy status=%d", name, result.status)
		}
		if _, exists, err := fixture.store.CheckPolicy(context.Background(), "project"); err != nil || exists {
			t.Fatalf("%s: a refused policy was stored: exists=%v err=%v", name, exists, err)
		}
		// The screen comes back with what was typed, and never with the
		// password that was typed with it.
		if strings.Contains(result.body, `value="admin-password"`) {
			t.Fatalf("%s: the administrator password was echoed back", name)
		}
	}

	// A container image only the container mode uses is refused there rather
	// than silently ignored.
	container := validPolicyValues(csrf)
	container.Set("executor", webui.ExecutorContainer)
	container.Set("container_image", "")
	if result := browserForm(t, client, policyURL, container, server.URL); result.status != http.StatusUnprocessableEntity {
		t.Fatalf("container mode without an image status=%d", result.status)
	}
	if _, exists, err := fixture.store.CheckPolicy(context.Background(), "project"); err != nil || exists {
		t.Fatalf("an incomplete container policy was stored: exists=%v err=%v", exists, err)
	}
}

func TestBrowserRunnerTokensDeliverTheValueOnceAndRevokeIt(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "rt-admin")
	tokenURL := server.URL + runnerTokensURL("project")

	// Runner authority is scoped to a stored policy, so the screen says what
	// is missing instead of issuing a token that means nothing.
	empty := browserGET(t, client, tokenURL)
	if empty.status != http.StatusOK || !strings.Contains(empty.body, webui.Text(webui.LangEN, webui.MsgRTNeedPolicy)) {
		t.Fatalf("runner screen without a policy status=%d", empty.status)
	}
	if strings.Contains(empty.body, `value="`+webui.ActionIssueRunnerToken+`"`) {
		t.Fatal("a token could be issued before any policy existed")
	}

	if result := browserForm(t, client, server.URL+configuredChecksURL("project"), validPolicyValues(csrf), server.URL); result.status != http.StatusSeeOther {
		t.Fatalf("policy save status=%d", result.status)
	}

	page := browserGET(t, client, tokenURL)
	if page.status != http.StatusOK {
		t.Fatalf("runner screen status=%d", page.status)
	}
	creationID := formValue(t, page.body, "creation_id")

	issue := url.Values{
		"csrf": {csrf}, "action": {webui.ActionIssueRunnerToken},
		"admin_password": {"admin-password"}, "label": {"browser runner"},
		"creation_id": {creationID},
	}
	// A remembered session cannot mint a token on its own.
	noPassword := url.Values{}
	for key, value := range issue {
		noPassword[key] = value
	}
	noPassword.Del("admin_password")
	if result := browserForm(t, client, tokenURL, noPassword, server.URL); result.status != http.StatusUnauthorized {
		t.Fatalf("remembered session issued a token without a password: status=%d", result.status)
	}
	if credentials, err := fixture.store.CheckRunnerCredentials(context.Background(), "project"); err != nil || len(credentials) != 0 {
		t.Fatalf("password bypass issued a token: count=%d err=%v", len(credentials), err)
	}

	issued := browserForm(t, client, tokenURL, issue, server.URL)
	if issued.status != http.StatusOK {
		t.Fatalf("token issue status=%d", issued.status)
	}
	if issued.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("token response cache-control=%q", issued.header.Get("Cache-Control"))
	}
	token := issuedTokenFromBody(t, issued.body)
	if strings.Contains(issued.header.Get("Location"), token) {
		t.Fatal("the token reached a redirect location")
	}

	// A repeated submission of the same form does not mint a second token.
	repeat := browserForm(t, client, tokenURL, issue, server.URL)
	if repeat.status != http.StatusOK {
		t.Fatalf("repeated issue status=%d", repeat.status)
	}
	if strings.Contains(repeat.body, token) {
		t.Fatal("a repeated submission handed the token out again")
	}
	credentials, err := fixture.store.CheckRunnerCredentials(context.Background(), "project")
	if err != nil || len(credentials) != 1 {
		t.Fatalf("repeated issue credentials=%d err=%v", len(credentials), err)
	}

	// Returning to the screen shows the credential and not its value.
	revisit := browserGET(t, client, tokenURL)
	if revisit.status != http.StatusOK || !strings.Contains(revisit.body, "browser runner") {
		t.Fatalf("runner list status=%d", revisit.status)
	}
	if strings.Contains(revisit.body, token) {
		t.Fatal("the token survived into a later page load")
	}

	revoke := url.Values{
		"csrf": {csrf}, "action": {webui.ActionRevokeRunnerToken},
		"credential_id": {credentials[0].ID}, "admin_password": {"admin-password"},
	}
	revoked := browserForm(t, client, tokenURL, revoke, server.URL)
	if revoked.status != http.StatusSeeOther || revoked.header.Get("Location") != runnerTokensURL("project")+"?notice=runner_token_revoked" {
		t.Fatalf("revoke status=%d location=%q", revoked.status, revoked.header.Get("Location"))
	}
	after, err := fixture.store.CheckRunnerCredentials(context.Background(), "project")
	if err != nil || len(after) != 1 || after[0].RevokedAt == nil {
		t.Fatalf("revocation not recorded: count=%d err=%v", len(after), err)
	}
	// The record stays, because removing the row would erase what once had
	// access to this repository.
	final := browserGET(t, client, tokenURL)
	if final.status != http.StatusOK || !strings.Contains(final.body, "browser runner") {
		t.Fatalf("revoked credential disappeared from the record: status=%d", final.status)
	}
}

func TestBrowserRunnerTokensAreScopedToTheirRepository(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "rt-scope")
	if result := browserForm(t, client, server.URL+configuredChecksURL("project"), validPolicyValues(csrf), server.URL); result.status != http.StatusSeeOther {
		t.Fatalf("policy save status=%d", result.status)
	}
	// The label is a distinctive marker, not an ordinary word. "scoped"
	// appears in the screen's own explanatory copy ("Runner authority is
	// scoped to it."), so searching for it would report a leak on a page that
	// lists nothing at all.
	const scopedLabel = "zz-scope-marker-7f31"
	// The store requires a 32-character hex creation identity, the same rule
	// the browser applies before submitting one.
	credential, _, _, err := fixture.store.IssueCheckRunnerToken(context.Background(), "project", scopedLabel, strings.Repeat("c", 32), time.Now().UTC())
	noErr(t, err)
	if _, err := fixture.app.Repositories.Create(context.Background(), "other", "Other repository"); err != nil {
		t.Fatal(err)
	}

	// The marker and the credential identity are both absent from the other
	// repository's screen. Checking the identity as well as the label means a
	// row rendered without its label would still be caught.
	other := browserGET(t, client, server.URL+runnerTokensURL("other"))
	if other.status != http.StatusOK {
		t.Fatalf("other repository token screen status=%d", other.status)
	}
	if strings.Contains(other.body, scopedLabel) {
		t.Fatal("a token label leaked into another repository")
	}
	if strings.Contains(other.body, credential.ID) || strings.Contains(other.body, shortOpaqueID(credential.ID)) {
		t.Fatal("a token identity leaked into another repository")
	}

	// The assertion is only meaningful if the marker does appear where it
	// belongs. Without this, a screen that lists nothing would pass the test
	// above for the wrong reason.
	owning := browserGET(t, client, server.URL+runnerTokensURL("project"))
	if owning.status != http.StatusOK {
		t.Fatalf("owning repository token screen status=%d", owning.status)
	}
	if !strings.Contains(owning.body, scopedLabel) {
		t.Fatal("the token is not listed on the repository that owns it")
	}
	crossRevoke := browserForm(t, client, server.URL+runnerTokensURL("other"), url.Values{
		"csrf": {csrf}, "action": {webui.ActionRevokeRunnerToken},
		"credential_id": {credential.ID}, "admin_password": {"admin-password"},
	}, server.URL)
	if crossRevoke.status != http.StatusConflict {
		t.Fatalf("cross-repository revoke status=%d", crossRevoke.status)
	}
	credentials, err := fixture.store.CheckRunnerCredentials(context.Background(), "project")
	if err != nil || len(credentials) != 1 || credentials[0].RevokedAt != nil {
		t.Fatalf("cross-repository revoke changed the token: count=%d err=%v", len(credentials), err)
	}
}

func TestBrowserJobActionsAreRefusedForAnotherRepository(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "cc-job")
	if _, err := fixture.app.Repositories.Create(context.Background(), "other", "Other repository"); err != nil {
		t.Fatal(err)
	}
	// A job identity that does not resolve in this repository is a refusal,
	// never a silent success.
	result := browserForm(t, client, server.URL+configuredChecksURL("other"), url.Values{
		"csrf": {csrf}, "action": {webui.ActionCancelCheckJob},
		"admin_password": {"admin-password"}, "job_id": {strings.Repeat("a", 32)},
	}, server.URL)
	if result.status != http.StatusNotFound {
		t.Fatalf("unknown job cancel status=%d", result.status)
	}
}

func TestBrowserConfiguredCheckScreensReportAClosedRuntimeWithoutBlockingGit(t *testing.T) {
	fixture := newAPIFixture(t, false)
	// The backend observed a closed runtime at startup. That is not a check
	// result, and it must not stop ordinary repository work.
	fixture.app.CheckRuntimeUnavailableCode = webui.RuntimeWorkspaceUnavailable
	fixture.app.CheckRuntimeUnavailableReason = "The check workspace root could not be prepared."
	server, client, jar := openBrowser(t, fixture)
	browserAdminSessionFor(t, fixture, server.URL, jar, "cc-runtime")

	page := browserGET(t, client, server.URL+configuredChecksURL("project"))
	if page.status != http.StatusOK {
		t.Fatalf("policy screen status=%d", page.status)
	}
	if !strings.Contains(page.body, webui.RuntimeWorkspaceUnavailable) {
		t.Fatal("the observed runtime code is not reported")
	}
	// Ordinary browsing is unaffected by a closed check runtime.
	for _, target := range []string{"/repositories/project", "/repositories/project/pull-requests", "/repositories/project/tasks"} {
		if result := browserGET(t, client, server.URL+target); result.status != http.StatusOK {
			t.Fatalf("%s became unavailable with a closed check runtime: status=%d", target, result.status)
		}
	}
}

func TestBrowserPolicyScreenShowsRecordedJobsWithTheirOwnFacts(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "cc-jobs")
	if result := browserForm(t, client, server.URL+configuredChecksURL("project"), validPolicyValues(csrf), server.URL); result.status != http.StatusSeeOther {
		t.Fatalf("policy save status=%d", result.status)
	}
	policy, _, err := fixture.store.CheckPolicy(context.Background(), "project")
	noErr(t, err)
	// The approval quotes the whole identity the screen renders, version and
	// digest, because that is what the store compares.
	enable := url.Values{
		"csrf": {csrf}, "action": {webui.ActionEnableChecks},
		"admin_password": {"admin-password"},
		"policy_version": {formatOptionalInt(policy.Version)},
		"policy_digest":  {policy.Digest},
	}
	if result := browserForm(t, client, server.URL+configuredChecksURL("project"), enable, server.URL); result.status != http.StatusSeeOther {
		t.Fatalf("enable status=%d", result.status)
	}

	// With nothing recorded the screen says so, which is a different fact
	// from records that could not be read.
	page := browserGET(t, client, server.URL+configuredChecksURL("project"))
	if page.status != http.StatusOK {
		t.Fatalf("policy screen status=%d", page.status)
	}
	if !strings.Contains(page.body, webui.Text(webui.LangEN, webui.MsgCCJobsNone)) {
		t.Fatal("an empty job list does not say that nothing has run yet")
	}

	// An unknown job identity opens as not found rather than as a blank page.
	missing := browserGET(t, client, server.URL+configuredChecksURL("project")+"?job="+strings.Repeat("b", 32))
	if missing.status != http.StatusOK || !strings.Contains(missing.body, webui.Text(webui.LangEN, webui.MsgCCJobNotFound)) {
		t.Fatalf("unknown job detail status=%d", missing.status)
	}
}

// formValue reads one rendered hidden input, so a test can resubmit the exact
// identity the page carried.
func formValue(t *testing.T, body, name string) string {
	t.Helper()
	marker := `name="` + name + `" value="`
	start := strings.Index(body, marker)
	if start < 0 {
		t.Fatalf("the rendered page carries no %q field", name)
	}
	start += len(marker)
	end := strings.Index(body[start:], `"`)
	if end < 0 {
		t.Fatalf("the %q field is incomplete", name)
	}
	return body[start : start+end]
}

// jobFacts are the immutable trigger facts of one admission.
//
// Two admissions in the same test must differ here. The store dedups a request
// whose facts match one it already admitted, which is the behaviour the queue
// depends on, so reusing one set would return the first job instead of a
// second one.
type jobFacts struct {
	ref       string
	sourceOID string
}

// admitEnabledJob saves a policy, grants consent for the exact generation it
// produced, and admits one job. It returns the CSRF token and the job, so a
// test can act on a real recorded job instead of a hand-built row.
//
// It installs a new administrator session, so a caller that admits twice must
// use the CSRF token returned by the later call for any form it submits
// afterwards. The earlier token belongs to a session the jar no longer holds.
func admitEnabledJob(t *testing.T, fixture apiFixture, serverURL string, client *http.Client, jar http.CookieJar, name string, facts jobFacts) (string, state.CheckJob) {
	t.Helper()
	csrf := browserAdminSessionFor(t, fixture, serverURL, jar, name)
	if result := browserForm(t, client, serverURL+configuredChecksURL("project"), validPolicyValues(csrf), serverURL); result.status != http.StatusSeeOther {
		t.Fatalf("policy save status=%d", result.status)
	}
	policy, _, err := fixture.store.CheckPolicy(context.Background(), "project")
	noErr(t, err)
	enable := url.Values{
		"csrf": {csrf}, "action": {webui.ActionEnableChecks},
		"admin_password": {"admin-password"},
		"policy_version": {formatOptionalInt(policy.Version)},
		"policy_digest":  {policy.Digest},
	}
	if result := browserForm(t, client, serverURL+configuredChecksURL("project"), enable, serverURL); result.status != http.StatusSeeOther {
		t.Fatalf("enable status=%d", result.status)
	}
	job, deduped, err := fixture.store.AdmitCheckJob(context.Background(), state.CheckJobRequest{
		RepositoryID: "project", Trigger: "push",
		EventKey:  "refs/heads/" + facts.ref + "@" + facts.sourceOID,
		SourceOID: facts.sourceOID,
		// The workflow content is the same file on both refs, so its digest
		// stays fixed. The ref and the commit are what actually differ.
		TriggerRef:     facts.ref,
		WorkflowDigest: strings.Repeat("b", 64),
		Checks:         []state.CheckDefinition{{Name: "unit", Command: "go test ./..."}},
	}, fixture.app.now())
	// Deduping here would mean the caller reused another admission's facts and
	// is about to act on the wrong job, so it stays a hard failure.
	if err != nil || deduped {
		t.Fatalf("admit job %s: err=%v deduped=%v", facts.ref, err, deduped)
	}
	return csrf, job
}

// runJobToAttempt drives one admitted job through the backend's own path until
// it holds a finished attempt, and returns that stored attempt.
//
// It exists so the origin tests read real records rather than hand-built rows:
// the execution scope and the protection on the attempt are the ones the store
// derived from the job, not values a test chose. An empty protection is the
// honest case of a job that established none, which the store accepts for any
// executor.
func runJobToAttempt(t *testing.T, fixture apiFixture, job state.CheckJob, attemptID, protection string) state.CheckAttempt {
	t.Helper()
	ctx := context.Background()
	claimed, found, err := fixture.store.ClaimLocalCheckJob(ctx, job.RepositoryID, fixture.app.now())
	if err != nil || !found || claimed.ID != job.ID {
		t.Fatalf("claim job: err=%v found=%v claimed=%q want=%q", err, found, claimed.ID, job.ID)
	}
	started, err := fixture.store.StartCheckJob(ctx, state.CheckJobStart{
		RepositoryID: claimed.RepositoryID, JobID: claimed.ID, LeaseID: claimed.LeaseID,
		CredentialID: claimed.CredentialID, CredentialGeneration: claimed.CredentialGeneration,
		Protection: protection,
	}, fixture.app.now())
	noErrf(t, err, "start job")
	configuration, exists, err := fixture.store.CheckConfiguration(ctx, started.RepositoryID, started.ConfigurationVersion)
	if err != nil || !exists {
		t.Fatalf("read job configuration: err=%v exists=%v", err, exists)
	}
	_, registered, err := fixture.store.RegisterCheckAttempt(ctx, state.CheckAttempt{
		ID: attemptID, TaskID: started.TaskID, RepositoryID: started.RepositoryID,
		RevisionOID: started.SourceOID, WorktreeState: state.WorktreeClean,
		JobID: started.ID, CredentialID: started.CredentialID, Checks: configuration.Checks,
		StartedAt: *started.StartedAt, CreatedAt: fixture.app.now(),
	})
	noErrf(t, err, "register job attempt")
	exit := 0
	_, stored, err := fixture.store.CompleteCheckJobAttempt(ctx, state.CheckCompletion{
		AttemptID: registered.ID, RepositoryID: registered.RepositoryID, TaskID: registered.TaskID,
		FinishedAt: fixture.app.now(), WorktreeState: state.WorktreeClean, Log: "synthetic automatic output",
		Results: []state.CheckResult{{
			Name: "unit", Command: "go test ./...", Status: state.AttemptPassed,
			ExitCode: &exit, DurationMS: 25, OutputExcerpt: "ok",
		}},
	}, state.CheckJobCompletionAuthority{
		JobID: started.ID, LeaseID: started.LeaseID,
		CredentialID: started.CredentialID, CredentialGeneration: started.CredentialGeneration,
	}, fixture.app.now())
	noErrf(t, err, "complete job attempt")
	if stored.JobID != started.ID {
		t.Fatalf("the stored attempt lost its job link: %q", stored.JobID)
	}
	return stored
}

func TestBrowserCancelIsOfferedOnlyWhileWorkCanStillBeStopped(t *testing.T) {
	// A control beside a finished job invites the reader to believe the run
	// can still be stopped. Cancel belongs to work that has not reached a
	// result.
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	_, job := admitEnabledJob(t, fixture, server.URL, client, jar, "cc-cancel-offer",
		jobFacts{ref: "main", sourceOID: strings.Repeat("a", 40)})

	// A pending job is still stoppable, so the control is drawn.
	page := browserGET(t, client, server.URL+configuredChecksURL("project")+"?job="+job.ID)
	if page.status != http.StatusOK {
		t.Fatalf("job detail status=%d", page.status)
	}
	if !strings.Contains(page.body, webui.ActionCancelCheckJob) {
		t.Fatal("a pending job offers no way to stop it")
	}

	// Drive the job to a terminal state through the backend's own path.
	claimed, found, err := fixture.store.ClaimLocalCheckJob(context.Background(), "project", fixture.app.now())
	if err != nil || !found {
		t.Fatalf("claim: err=%v found=%v", err, found)
	}
	finished, err := fixture.store.FailCheckJobBeforeStart(context.Background(), state.CheckJobCompletionAuthority{
		JobID: claimed.ID, LeaseID: claimed.LeaseID,
		CredentialID: claimed.CredentialID, CredentialGeneration: claimed.CredentialGeneration,
	}, state.CheckJobError, "The run ended before execution began.", fixture.app.now())
	noErr(t, err)
	if finished.FinishedAt == nil {
		t.Fatal("the job did not reach a terminal state")
	}

	page = browserGET(t, client, server.URL+configuredChecksURL("project")+"?job="+job.ID)
	if page.status != http.StatusOK {
		t.Fatalf("finished job detail status=%d", page.status)
	}
	if strings.Contains(page.body, webui.ActionCancelCheckJob) {
		t.Error("a finished job still offers to cancel work that already ended")
	}
	// Losing cancel must not cost the reader the action that does apply.
	if !strings.Contains(page.body, webui.ActionRerunCheckJob) {
		t.Error("a finished job no longer offers a rerun")
	}
}

func TestBrowserCancelThatLosesToACompletionSaysNothingChanged(t *testing.T) {
	// The backend accepts a cancel on a finished job and records the intent
	// without reversing the result. That is the right contract for a racing
	// request, and the wrong thing to report as a cancellation: the reader
	// would expect the outcome to change.
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	csrf, job := admitEnabledJob(t, fixture, server.URL, client, jar, "cc-cancel-race",
		jobFacts{ref: "main", sourceOID: strings.Repeat("a", 40)})

	claimed, found, err := fixture.store.ClaimLocalCheckJob(context.Background(), "project", fixture.app.now())
	if err != nil || !found {
		t.Fatalf("claim: err=%v found=%v", err, found)
	}
	if _, err := fixture.store.FailCheckJobBeforeStart(context.Background(), state.CheckJobCompletionAuthority{
		JobID: claimed.ID, LeaseID: claimed.LeaseID,
		CredentialID: claimed.CredentialID, CredentialGeneration: claimed.CredentialGeneration,
	}, state.CheckJobError, "The run ended before execution began.", fixture.app.now()); err != nil {
		t.Fatal(err)
	}

	// A stale page, or a racing request, can still submit the cancel.
	result := browserForm(t, client, server.URL+configuredChecksURL("project"), url.Values{
		"csrf": {csrf}, "action": {webui.ActionCancelCheckJob},
		"admin_password": {"admin-password"}, "job_id": {job.ID},
	}, server.URL)
	if result.status != http.StatusSeeOther {
		t.Fatalf("racing cancel status=%d", result.status)
	}
	// The redirect names the result, so the notice key is checked directly
	// rather than inferred from the page that follows it.
	if location := result.header.Get("Location"); !strings.Contains(location, "notice=check_job_already_finished") ||
		strings.Contains(location, "notice=check_job_cancelled") {
		t.Errorf("the racing cancel redirected with %q", location)
	}
	page := browserGET(t, client, server.URL+result.header.Get("Location"))
	if page.status != http.StatusOK {
		t.Fatalf("redirect status=%d", page.status)
	}
	// Only the notice region reports what this request achieved. The job list
	// elsewhere on the page truthfully marks the recorded cancel intent, and
	// that history stays.
	notices := noticeRegion(t, page.body)
	if !strings.Contains(notices, browserText(webui.MsgCCJobAlreadyFinished)) {
		t.Errorf("a cancel that lost to a completion does not say the result was unchanged: %q", notices)
	}
	if strings.Contains(notices, browserText(webui.MsgCCJobCancelled)) {
		t.Errorf("a cancel that stopped nothing is reported as a cancellation: %q", notices)
	}
	// It is deliberately informational. A success chip beside it would carry
	// the claim the sentence is refusing to make.
	if !strings.Contains(notices, `notice--info`) || strings.Contains(notices, `notice--success`) {
		t.Errorf("the unchanged-result notice is not informational: %q", notices)
	}

	// The recorded result is untouched: the backend kept its own status and
	// only noted the intent.
	stored, exists, err := fixture.store.CheckJob(context.Background(), "project", job.ID)
	if err != nil || !exists {
		t.Fatalf("read job: err=%v exists=%v", err, exists)
	}
	if stored.Status != state.CheckJobError {
		t.Errorf("the job status became %q, want the result it already had", stored.Status)
	}
	if stored.CancelRequestedAt == nil {
		t.Error("the cancel intent was not recorded")
	}

	// A cancel on work that has not finished still reports a cancellation.
	//
	// The second admission carries its own trigger facts, because a repeat of
	// the first job's facts would dedup onto the job that already finished and
	// this case would silently stop testing live work. It also replaces the
	// administrator session, so the request below uses the CSRF token that
	// admission returned rather than the earlier one.
	liveCSRF, second := admitEnabledJob(t, fixture, server.URL, client, jar, "cc-cancel-live",
		jobFacts{ref: "release", sourceOID: strings.Repeat("d", 40)})
	if second.ID == job.ID {
		t.Fatal("the second admission returned the job that had already finished")
	}
	live := browserForm(t, client, server.URL+configuredChecksURL("project"), url.Values{
		"csrf": {liveCSRF}, "action": {webui.ActionCancelCheckJob},
		"admin_password": {"admin-password"}, "job_id": {second.ID},
	}, server.URL)
	if live.status != http.StatusSeeOther {
		t.Fatalf("live cancel status=%d", live.status)
	}
	if location := live.header.Get("Location"); !strings.Contains(location, "notice=check_job_cancelled") {
		t.Errorf("the live cancel redirected with %q", location)
	}
	livePage := browserGET(t, client, server.URL+live.header.Get("Location"))
	liveNotices := noticeRegion(t, livePage.body)
	if !strings.Contains(liveNotices, browserText(webui.MsgCCJobCancelled)) {
		t.Errorf("cancelling work that had not finished no longer reports a cancellation: %q", liveNotices)
	}
	if strings.Contains(liveNotices, browserText(webui.MsgCCJobAlreadyFinished)) {
		t.Errorf("a real cancellation is reported as a request that changed nothing: %q", liveNotices)
	}
	// Cancelling unfinished work does record the terminal state, which is the
	// half of the contract the racing case above must not claim.
	stoppedJob, exists, err := fixture.store.CheckJob(context.Background(), "project", second.ID)
	if err != nil || !exists {
		t.Fatalf("read cancelled job: err=%v exists=%v", err, exists)
	}
	if stoppedJob.Status != state.CheckJobCancelled {
		t.Errorf("cancelling pending work left status %q, want %q", stoppedJob.Status, state.CheckJobCancelled)
	}
}

func TestBrowserAutomaticOriginComesFromTheRecordedJobLink(t *testing.T) {
	// The job link is the recorded fact that separates the server's own work
	// from a person's manual run. Reading the protection and scope instead is
	// a guess: the store admits a job with no established protection, and a
	// helper payload is not prevented from describing host-like facts.
	credential := strings.Repeat("c", 32)
	jobID := strings.Repeat("e", 32)

	for _, tc := range []struct {
		name       string
		jobID      string
		protection string
		scope      string
		want       string
		wantOrigin string
	}{
		{"host job", jobID, state.ProtectionHost, state.ExecutionScopeInherited,
			webui.ProtectionAutomaticHost, webui.ProvenanceAutomaticJob},
		{"container job", jobID, state.ProtectionContainer, state.ExecutionScopeContainer,
			webui.ProtectionAutomaticContainer, webui.ProvenanceAutomaticJob},
		{"external runner", jobID, state.ProtectionRunnerReported, state.ExecutionScopeExternalRunner,
			webui.ProtectionRunnerReported, webui.ProvenanceRunnerClaimed},
		// The store permits an admitted job to record no protection: a job is
		// created with "unknown" and only a start reports more. That is a
		// missing fact about the server's run, never evidence of a manual one,
		// so it stays automatic and says the protection is unknown.
		{"job with no established protection", jobID, state.ProtectionUnknown, state.ExecutionScopeInherited,
			webui.ProtectionUnknown, webui.ProvenanceAutomaticJob},
		{"container job before it reported protection", jobID, state.ProtectionUnknown, state.ExecutionScopeContainer,
			webui.ProtectionUnknown, webui.ProvenanceAutomaticJob},
		{"runner job before it reported protection", jobID, state.ProtectionUnknown, state.ExecutionScopeExternalRunner,
			webui.ProtectionUnknown, webui.ProvenanceRunnerClaimed},
		// A manual helper submission keeps the wording that actually describes
		// it. Nothing about this repair changes a helper run.
		{"manual helper", "", state.ProtectionUnknown, state.ExecutionScopeInherited,
			webui.ProtectionInherited, webui.ProvenanceAuthenticatedHelper},
		// Nothing admitted these runs, so no automatic sentence may be drawn
		// from facts that merely look like an executor's.
		{"unlinked host-like facts", "", state.ProtectionHost, state.ExecutionScopeInherited,
			"", webui.ProvenanceAuthenticatedHelper},
		{"unlinked container-like facts", "", state.ProtectionContainer, state.ExecutionScopeContainer,
			"", webui.ProvenanceAuthenticatedHelper},
		{"unlinked runner-like facts", "", state.ProtectionRunnerReported, state.ExecutionScopeExternalRunner,
			"", webui.ProvenanceAuthenticatedHelper},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := browserProtection(tc.jobID, tc.protection, tc.scope); got != tc.want {
				t.Errorf("protection job=%q %q/%q became %q, want %q", tc.jobID, tc.protection, tc.scope, got, tc.want)
			}
			if got := browserProvenance(tc.jobID, credential, tc.scope); got != tc.wantOrigin {
				t.Errorf("provenance job=%q scope=%q became %q, want %q", tc.jobID, tc.scope, got, tc.wantOrigin)
			}
		})
	}

	// The same facts, differing only in the job link, must not describe the
	// same origin. This is the defect the repair is for: without the link the
	// two cases below were indistinguishable.
	for _, facts := range []struct {
		name       string
		protection string
		scope      string
	}{
		{"host facts", state.ProtectionHost, state.ExecutionScopeInherited},
		{"unknown protection on the inherited scope", state.ProtectionUnknown, state.ExecutionScopeInherited},
	} {
		t.Run("only the job link differs: "+facts.name, func(t *testing.T) {
			linked := browserProvenance(jobID, credential, facts.scope)
			unlinked := browserProvenance("", credential, facts.scope)
			if linked == unlinked {
				t.Errorf("a job-linked and an unlinked attempt share the origin %q", linked)
			}
			if linked != webui.ProvenanceAutomaticJob || unlinked != webui.ProvenanceAuthenticatedHelper {
				t.Errorf("linked=%q unlinked=%q", linked, unlinked)
			}
			if browserProtection("", facts.protection, facts.scope) == webui.ProtectionAutomaticHost {
				t.Error("an unlinked attempt is described as an automatic host job")
			}
			if browserProtection(jobID, facts.protection, facts.scope) == webui.ProtectionInherited {
				t.Error("a job-linked attempt is described as the operator's own environment")
			}
		})
	}

	// Without a credential nothing is claimed about how the attempt arrived,
	// job link or not.
	for _, id := range []string{"", jobID} {
		if origin := browserProvenance(id, "", state.ExecutionScopeInherited); origin != webui.ProvenanceUnstated {
			t.Errorf("an attempt with no credential claims the origin %q", origin)
		}
	}
}

func TestBrowserAutomaticJobDetailNamesTheServerNotTheOperator(t *testing.T) {
	// The job screen shows the server's own automatic runs. Describing one as
	// running in the operator's environment, reported by their check helper,
	// names the wrong machine and the wrong authority. This drives the real
	// screens over a real admitted job rather than the adapters alone.
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	// The admission names the branch the commit actually belongs to, so the
	// pull request below reads evidence for its own source revision.
	_, job := admitEnabledJob(t, fixture, server.URL, client, jar, "cc-origin",
		jobFacts{ref: "feature", sourceOID: fixture.sourceOID})
	attempt := runJobToAttempt(t, fixture, job, strings.Repeat("7", 32), state.ProtectionHost)

	manualEnvironment := browserText(webui.MsgCheckProtectionInherited)
	manualCredential := browserText(webui.MsgCheckProvenanceHelper)
	automaticEnvironment := browserText(webui.MsgCheckProtectionAutoHost)
	automaticOrigin := browserText(webui.MsgCheckProvenanceAutomatic)

	// The configured-check job detail, which is the screen Main found wrong.
	detail := browserGET(t, client, server.URL+configuredChecksURL("project")+"?job="+job.ID)
	if detail.status != http.StatusOK {
		t.Fatalf("job detail status=%d", detail.status)
	}
	for _, want := range []string{automaticEnvironment, automaticOrigin} {
		if !strings.Contains(detail.body, want) {
			t.Errorf("the job detail does not say %q", want)
		}
	}
	for _, unwanted := range []string{manualEnvironment, manualCredential} {
		if strings.Contains(detail.body, unwanted) {
			t.Errorf("the job detail borrows the manual wording %q", unwanted)
		}
	}

	// The task view renders the same attempt through browserAttemptRecord.
	task := browserGET(t, client, server.URL+tasksURL("project", attempt.TaskID))
	if task.status != http.StatusOK {
		t.Fatalf("task detail status=%d", task.status)
	}
	if !strings.Contains(task.body, automaticEnvironment) || !strings.Contains(task.body, automaticOrigin) {
		t.Error("the task view does not describe the automatic run as the server's own")
	}
	if strings.Contains(task.body, manualEnvironment) || strings.Contains(task.body, manualCredential) {
		t.Error("the task view describes an automatic job as the operator's own run")
	}

	// The pull request evidence for the same commit reads the projected job
	// link rather than guessing from the protection it happens to carry.
	created, err := fixture.app.PullRequests.Create(context.Background(), pullrequest.CreateInput{
		Repository: "project", Title: "Automatic evidence", SourceBranch: "feature", TargetBranch: "main",
		SourceOID: fixture.sourceOID, TargetOID: fixture.targetOID,
	})
	noErr(t, err)
	view, err := fixture.app.PullRequests.Show(context.Background(), "project", created.Number)
	noErrf(t, err, "show pull request")
	if view.Checks.JobID != job.ID {
		t.Fatalf("the pull request projection lost the job link: %q", view.Checks.JobID)
	}
	pullRequest := browserGET(t, client, server.URL+pullRequestURL("project", created.Number))
	if pullRequest.status != http.StatusOK {
		t.Fatalf("pull request detail status=%d", pullRequest.status)
	}
	if !strings.Contains(pullRequest.body, automaticEnvironment) || !strings.Contains(pullRequest.body, automaticOrigin) {
		t.Error("the pull request evidence does not describe the automatic run as the server's own")
	}
	if strings.Contains(pullRequest.body, manualEnvironment) || strings.Contains(pullRequest.body, manualCredential) {
		t.Error("the pull request evidence credits the manual check helper for an automatic job")
	}

	// A manual helper attempt on the same repository still reads as manual,
	// which is the compatibility this repair must not cost.
	manualTask, err := fixture.store.CreateTask(context.Background(), "project", "Manual helper run", fixture.app.now())
	noErr(t, err)
	recordBrowserAttempt(t, fixture.store, manualTask.ID, fixture.targetOID,
		strings.Repeat("8", 32), state.WorktreeClean, "", true)
	manual := browserGET(t, client, server.URL+tasksURL("project", manualTask.ID))
	if manual.status != http.StatusOK {
		t.Fatalf("manual task status=%d", manual.status)
	}
	if !strings.Contains(manual.body, manualEnvironment) || !strings.Contains(manual.body, manualCredential) {
		t.Error("a manual helper run lost the wording that describes it")
	}
	if strings.Contains(manual.body, automaticEnvironment) || strings.Contains(manual.body, automaticOrigin) {
		t.Error("a manual helper run is described as the server's own automatic job")
	}
}

func TestBrowserOpenedJobStatesItsOwnAddressForTheLanguageLinks(t *testing.T) {
	// Regression for an observed defect. The language links are built from the
	// page's own GET address, and this controller stated the policy address on
	// every render. Switching language on an opened job produced
	// /configured-checks?lang=ko, so the address bar lost the job and the next
	// reload returned the reader to the policy screen.
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	_, job := admitEnabledJob(t, fixture, server.URL, client, jar, "cc-lang",
		jobFacts{ref: "main", sourceOID: strings.Repeat("a", 40)})

	jobURL := configuredChecksURL("project") + "?job=" + job.ID
	policyURL := configuredChecksURL("project")

	// Both directions: the reader may arrive in either language.
	for _, from := range []string{"en", "ko"} {
		page := browserGET(t, client, server.URL+jobURL+"&lang="+from)
		if page.status != http.StatusOK {
			t.Fatalf("job detail in %s status=%d", from, page.status)
		}
		for _, to := range []string{"en", "ko"} {
			want := template.HTMLEscapeString(jobURL + "&lang=" + to)
			if !strings.Contains(page.body, `href="`+want+`"`) {
				t.Errorf("from %s the %s link is not the opened job's address (want %q)", from, to, want)
			}
			// The policy address carrying only a language is the defect.
			if losing := policyURL + "?lang=" + to; strings.Contains(page.body, `href="`+losing+`"`) {
				t.Errorf("from %s the %s link drops the opened job: %q", from, to, losing)
			}
		}
		// An opened job is its own screen. The policy editor posts to the
		// base route on the policy screen below.
		if strings.Contains(page.body, `name="action" value="`+webui.ActionSaveCheckPolicy+`"`) {
			t.Errorf("from %s the opened job drew the policy editor", from)
		}
		if !strings.Contains(page.body, `action="`+template.HTMLEscapeString(jobURL)+`"`) {
			t.Errorf("from %s the job actions lost the job they act on", from)
		}
	}

	// The policy screen has no job to keep. Its forms post to the base route.
	policy := browserGET(t, client, server.URL+policyURL)
	if policy.status != http.StatusOK {
		t.Fatalf("policy screen status=%d", policy.status)
	}
	for _, to := range []string{"en", "ko"} {
		if !strings.Contains(policy.body, `href="`+policyURL+`?lang=`+to+`"`) {
			t.Errorf("the policy screen lost its own %s link", to)
		}
	}
	if !strings.Contains(policy.body, `action="`+policyURL+`"`) {
		t.Error("the policy form lost its POST route")
	}
	if strings.Contains(policy.body, `action="`+template.HTMLEscapeString(jobURL)+`"`) {
		t.Error("the policy screen posted through the opened job's address")
	}
}

func TestBrowserJobQueryIsEscapedInTheAddressesTheScreenRenders(t *testing.T) {
	// The opened identifier comes from the request, so the address the screen
	// states is escaped rather than trusted. An unusable identifier renders as
	// not found at that same address instead of breaking the markup.
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	browserAdminSessionFor(t, fixture, server.URL, jar, "cc-lang-escape")

	page := browserGET(t, client, server.URL+configuredChecksURL("project")+"?job=a%26b%3Cscript%3E")
	if page.status != http.StatusOK {
		t.Fatalf("unusable job status=%d", page.status)
	}
	if !strings.Contains(page.body, webui.Text(webui.LangEN, webui.MsgCCJobNotFound)) {
		t.Error("an unusable job identity does not open as not found")
	}
	if strings.Contains(page.body, "<script>") {
		t.Error("the opened identifier reached the markup unescaped")
	}
	// The escaped identifier is what the addresses carry.
	if !strings.Contains(page.body, "job=a%26b%3Cscript%3E") {
		t.Error("the rendered addresses lost the escaped job identity")
	}
}
