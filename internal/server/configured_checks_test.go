package server

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"owngit/internal/checkapi"
	"owngit/internal/hostmem"
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
// what a submission achieved. A test about a reported result must read this
// region and not the whole page: the page also states durable facts, such as
// the job list's "a cancellation was recorded" marker, that are true for a
// request that changed nothing. An absent block is an empty region.
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
// returns its CSRF token.
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

// validPolicyValues is a policy the backend accepts, the starting point of
// every refusal case.
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

// limitOnScreen reads the amount and selected unit the page shows for one
// numeric policy field.
func limitOnScreen(t *testing.T, body, field string) webui.LimitInput {
	t.Helper()
	input := regexp.MustCompile(`<input[^>]*name="` + regexp.QuoteMeta(field) + `"[^>]*>`).FindString(body)
	if input == "" {
		t.Fatalf("%s is not on the screen", field)
	}
	var got webui.LimitInput
	if m := regexp.MustCompile(`value="([^"]*)"`).FindStringSubmatch(input); m != nil {
		got.Amount = m[1]
	}
	menu := regexp.MustCompile(`(?s)<select name="` + regexp.QuoteMeta(field) + `_unit".*?</select>`).FindString(body)
	if m := regexp.MustCompile(`<option value="([^"]*)" selected`).FindStringSubmatch(menu); m != nil {
		got.Unit = m[1]
	}
	return got
}

func storedPolicy(t *testing.T, fixture apiFixture) (state.CheckPolicy, bool) {
	t.Helper()
	policy, exists, err := fixture.store.CheckPolicy(context.Background(), "project")
	noErr(t, err)
	return policy, exists
}

// enableForm asks to turn checks on for the policy generation it names.
func enableForm(csrf string, policy state.CheckPolicy) url.Values {
	return url.Values{
		"csrf": {csrf}, "action": {webui.ActionEnableChecks}, "admin_password": {"admin-password"},
		"policy_version": {strconv.FormatInt(policy.Version, 10)}, "policy_digest": {policy.Digest},
	}
}

// jobFacts are the immutable trigger facts of one admission. Two admissions in
// one test must differ here, because the store returns the first job for a
// request whose facts it has already admitted.
type jobFacts struct {
	ref       string
	sourceOID string
}

// admitEnabledJob saves a policy, grants consent for the exact generation it
// produced, and admits one job. It installs a new administrator session, so a
// caller that admits twice must use the CSRF token of the later call.
func admitEnabledJob(t *testing.T, fixture apiFixture, serverURL string, client *http.Client, jar http.CookieJar, name string, facts jobFacts) (string, state.CheckJob) {
	t.Helper()
	csrf := browserAdminSessionFor(t, fixture, serverURL, jar, name)
	if result := browserForm(t, client, serverURL+configuredChecksURL("project"), validPolicyValues(csrf), serverURL); result.status != http.StatusSeeOther {
		t.Fatalf("policy save status=%d", result.status)
	}
	policy, _ := storedPolicy(t, fixture)
	if result := browserForm(t, client, serverURL+configuredChecksURL("project"), enableForm(csrf, policy), serverURL); result.status != http.StatusSeeOther {
		t.Fatalf("enable status=%d", result.status)
	}
	job, deduped, err := fixture.store.AdmitCheckJob(context.Background(), state.CheckJobRequest{
		RepositoryID: "project", Trigger: "push",
		EventKey:  "refs/heads/" + facts.ref + "@" + facts.sourceOID,
		SourceOID: facts.sourceOID,
		// The workflow file is the same on every ref, so its digest stays fixed.
		TriggerRef:     facts.ref,
		WorkflowDigest: strings.Repeat("b", 64),
		Checks:         []state.CheckDefinition{{Name: "unit", Command: "go test ./..."}},
	}, fixture.app.now())
	// Deduping means the caller reused another admission's facts.
	if err != nil || deduped {
		t.Fatalf("admit job %s: err=%v deduped=%v", facts.ref, err, deduped)
	}
	return csrf, job
}

// runJobToAttempt drives one admitted job through the backend's own path until
// it holds a finished attempt, so the origin checks read real records.
func runJobToAttempt(t *testing.T, fixture apiFixture, job state.CheckJob, attemptID, protection string) state.CheckAttempt {
	t.Helper()
	ctx := context.Background()
	claimed, found, err := fixture.store.ClaimLocalCheckJob(ctx, job.RepositoryID, fixture.app.now())
	if err != nil || !found || claimed.ID != job.ID {
		t.Fatalf("claim job: err=%v found=%v claimed=%q want=%q", err, found, claimed.ID, job.ID)
	}
	started, registered, err := fixture.store.StartCheckJob(ctx, state.CheckJobStart{
		RepositoryID: claimed.RepositoryID, JobID: claimed.ID, LeaseID: claimed.LeaseID,
		CredentialID: claimed.CredentialID, CredentialGeneration: claimed.CredentialGeneration,
		Protection: protection, AttemptID: attemptID,
	}, fixture.app.now())
	noErrf(t, err, "start job")
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

// failJobBeforeStart takes the pending job to a terminal state through the
// backend's own path.
func failJobBeforeStart(t *testing.T, fixture apiFixture) {
	t.Helper()
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
}

// The editor on a repository without a policy: it offers working values and
// states the backend's ranges, each refusal lands beside its own control and
// stores nothing, and saved values come back in readable units and options.
func TestConfiguredChecksEditorJourney(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "cc-editor")
	policyURL := server.URL + configuredChecksURL("project")
	post := func(values url.Values) browserHTTPResult {
		return browserForm(t, client, policyURL, values, server.URL)
	}
	allowed := func(field string, bounds state.CheckPolicyBounds) string {
		return "Allowed: " + webui.LimitText(webui.LangEN, field, bounds.Min) + " to " + webui.LimitText(webui.LangEN, field, bounds.Max)
	}

	fresh := browserGET(t, client, policyURL)
	if fresh.status != http.StatusOK {
		t.Fatalf("policy screen status=%d", fresh.status)
	}
	// Every numeric control states the backend's own range.
	for _, field := range policyRangeFields {
		bounds, known := state.DefaultCheckCeilings.Bounds(field)
		if !known {
			t.Errorf("%s is drawn but publishes no bounds", field)
		} else if !bounds.HasFixedMinimum() {
			if high := webui.LimitText(webui.LangEN, field, bounds.Max); !strings.Contains(fresh.body, "up to "+high) {
				t.Errorf("%s does not show its maximum %s", field, high)
			}
		} else if !strings.Contains(fresh.body, allowed(field, bounds)) {
			t.Errorf("%s does not show the backend range", field)
		}
	}
	container := state.DefaultCheckContainerLimits()
	if want := webui.FormatLimit(webui.LimitSize, container.MemoryBytes, ""); !strings.Contains(fresh.body, want.Amount+" "+want.Unit) {
		t.Errorf("the container memory default %s %s is not stated", want.Amount, want.Unit)
	}

	queue, _ := state.DefaultCheckCeilings.Bounds(state.FieldQueueLimit)
	containerPolicy := func(v url.Values) {
		v.Set("executor", webui.ExecutorContainer)
		v.Set("container_image", "registry.local/checks@sha256:"+strings.Repeat("a", 64))
	}
	containerOptions := func(v url.Values) {
		v.Set("executor", webui.ExecutorContainer)
		v.Set("container_image", "registry.example:5000/team/checks:1")
		v.Set("container_allow_tags", "1")
		v.Set("container_network", webui.ContainerNetworkNamed)
	}
	for _, test := range []struct {
		name   string
		mutate func(url.Values)
		note   string                      // the field whose note must carry the refusal
		want   []string                    // text the refused page must carry
		inNote []string                    // text that must sit in the note's own block
		not    []string                    // text the refused page must not carry
		kept   map[string]webui.LimitInput // amounts the page must show as typed
	}{
		{"timeout below the range", func(v url.Values) { v.Set("max_timeout_ms", "5") }, "max_timeout_ms", nil, nil, nil, nil},
		{"queue above the ceiling", func(v url.Values) { v.Set("queue_limit", strconv.FormatInt(queue.Max+1, 10)) }, "queue_limit",
			[]string{browserText(webui.MsgCCFieldCeiling), allowed(state.FieldQueueLimit, queue)}, nil, nil, nil},
		{"no event chosen", func(v url.Values) { v.Del("event_push") }, "events", nil, nil, nil, nil},
		{"unknown executor", func(v url.Values) { v.Set("executor", "sandbox") }, "executor", nil, nil, nil, nil},
		{"image that is not immutable", func(v url.Values) {
			v.Set("executor", webui.ExecutorContainer)
			v.Set("container_image", "registry.example/checks:latest")
		}, "container_image", nil, nil, nil, nil},
		{"container mode without an image", func(v url.Values) {
			v.Set("executor", webui.ExecutorContainer)
			v.Set("container_image", "")
		}, "", nil, nil, nil, nil},
		{"non-numeric timeout", func(v url.Values) { v.Set("max_timeout_ms", "ten minutes") }, "", nil, nil, nil, nil},
		{"negative queue", func(v url.Values) { v.Set("queue_limit", "-5") }, "", nil, nil, nil, nil},
		{"fraction of the base unit", func(v url.Values) {
			v.Set("max_timeout_ms", "0.0005")
			v.Set("max_timeout_ms_unit", webui.UnitSeconds)
		}, "", []string{browserText(webui.MsgCCNumberFraction)}, nil, nil,
			map[string]webui.LimitInput{"max_timeout_ms": {Amount: "0.0005", Unit: webui.UnitSeconds}}},
		{"amount too long for any stored value", func(v url.Values) { v.Set("queue_limit", strings.Repeat("9", 41)) }, "queue_limit",
			nil, []string{browserText(webui.MsgCCFieldRange), allowed(state.FieldQueueLimit, queue)}, []string{browserText(webui.MsgCCNumberInvalid)}, nil},
		{"four decimals of a core", func(v url.Values) {
			containerPolicy(v)
			v.Set("container_cpu_millis", "1.2345")
			v.Set("container_cpu_millis_unit", webui.UnitCores)
		}, "", []string{browserText(webui.MsgCCNumberCores)}, nil, []string{browserText(webui.MsgCCNumberFraction)}, nil},
		{"cores shown back for an amount posted without a unit", func(v url.Values) {
			containerPolicy(v)
			v.Set("container_cpu_millis", "1500")
			v.Del("event_push")
		}, "", nil, nil, nil, map[string]webui.LimitInput{"container_cpu_millis": {Amount: "1.5"}}},
		{"named network without a name", containerOptions, "", []string{browserText(webui.MsgCCNetworkInvalid)}, nil, nil, nil},
		{"named host network", func(v url.Values) {
			containerOptions(v)
			v.Set("container_network_name", "host")
		}, "", []string{browserText(webui.MsgCCNetworkHost)}, nil, nil, nil},
	} {
		values := validPolicyValues(csrf)
		test.mutate(values)
		refused := post(values)
		if refused.status != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status=%d", test.name, refused.status)
		}
		if _, exists := storedPolicy(t, fixture); exists {
			t.Fatalf("%s: a refused policy was stored", test.name)
		}
		if strings.Contains(refused.body, `value="admin-password"`) {
			t.Fatalf("%s: the administrator password was echoed back", test.name)
		}
		for _, want := range test.want {
			if !strings.Contains(refused.body, want) {
				t.Errorf("%s: the refused page lacks %q", test.name, want)
			}
		}
		for _, not := range test.not {
			if strings.Contains(refused.body, not) {
				t.Errorf("%s: the refused page carries %q", test.name, not)
			}
		}
		for field, want := range test.kept {
			if got := limitOnScreen(t, refused.body, field); got != want {
				t.Errorf("%s: %s shows %+v, want %+v", test.name, field, got, want)
			}
		}
		if test.note == "" {
			continue
		}
		// Beside its control, with the invalid state and the description link
		// a reader who cannot see colour depends on.
		noteID := webui.ActionSaveCheckPolicy + "-" + test.note + "-note"
		start := strings.Index(refused.body, `id="`+noteID+`"`)
		if start < 0 || !strings.Contains(refused.body, `aria-describedby="`+noteID+`"`) || !strings.Contains(refused.body, `aria-invalid="true"`) {
			t.Errorf("%s: the refusal is not beside its own control", test.name)
			continue
		}
		block := refused.body[start:]
		if end := strings.Index(block, `class="f cclimit"`); end >= 0 {
			block = block[:end]
		}
		for _, want := range test.inNote {
			if !strings.Contains(block, want) {
				t.Errorf("%s: the note lacks %q", test.name, want)
			}
		}
	}

	// The untouched form is accepted: every required limit starts at a value
	// the backend accepts. The stated maximum is accepted too.
	untouched := url.Values{
		"csrf": {csrf}, "action": {webui.ActionSaveCheckPolicy}, "admin_password": {"admin-password"},
		"executor": {webui.ExecutorHost},
	}
	for _, event := range []string{"event_push", "event_pull_request"} {
		if !regexp.MustCompile(`name="` + event + `" value="1" checked`).MatchString(fresh.body) {
			t.Fatalf("%s does not start selected", event)
		}
		untouched.Set(event, "1")
	}
	for _, limit := range webui.PolicyLimitFields() {
		shown := limitOnScreen(t, fresh.body, limit.Field)
		untouched.Set(limit.Field, shown.Amount)
		untouched.Set(limit.UnitField(), shown.Unit)
	}
	if result := post(untouched); result.status != http.StatusSeeOther {
		t.Fatalf("the offered starting values were refused: status=%d\n%s", result.status, noticeRegion(t, result.body))
	}
	atBound := validPolicyValues(csrf)
	atBound.Set("queue_limit", strconv.FormatInt(queue.Max, 10))
	if result := post(atBound); result.status != http.StatusSeeOther {
		t.Fatalf("the stated maximum was refused: status=%d", result.status)
	}

	// Amounts entered in readable units are stored in the base unit and come
	// back in the largest unit that holds them exactly.
	values := validPolicyValues(csrf)
	for field, input := range map[string]webui.LimitInput{
		"max_timeout_ms":         {Amount: "1.5", Unit: webui.UnitMinutes},
		"max_output_limit_bytes": {Amount: "2", Unit: webui.UnitMB},
		"max_lease_ms":           {Amount: "45", Unit: webui.UnitSeconds},
	} {
		values.Set(field, input.Amount)
		values.Set(field+"_unit", input.Unit)
	}
	if result := post(values); result.status != http.StatusSeeOther {
		t.Fatalf("policy save status=%d", result.status)
	}
	if policy, _ := storedPolicy(t, fixture); policy.MaxTimeoutMS != 90_000 || policy.MaxOutputLimitBytes != 2<<20 || policy.MaxLeaseMS != 45_000 {
		t.Fatalf("stored timeout=%d output=%d lease=%d", policy.MaxTimeoutMS, policy.MaxOutputLimitBytes, policy.MaxLeaseMS)
	}
	page := browserGET(t, client, policyURL)
	for field, want := range map[string]webui.LimitInput{
		"max_timeout_ms":         {Amount: "90", Unit: webui.UnitSeconds},
		"max_output_limit_bytes": {Amount: "2", Unit: webui.UnitMB},
		"max_lease_ms":           {Amount: "45", Unit: webui.UnitSeconds},
	} {
		if got := limitOnScreen(t, page.body, field); got != want {
			t.Fatalf("%s shows %+v, want %+v", field, got, want)
		}
	}

	// Container options travel from the form to the stored policy and back.
	options := validPolicyValues(csrf)
	containerOptions(options)
	for _, name := range []string{"container_pull_missing", "container_image_volumes", "container_writable_root"} {
		options.Set(name, "1")
	}
	options["container_missing_enforcement"] = []string{"swap", "cpu"}
	options.Set("container_network_name", "checks-net")
	if result := post(options); result.status != http.StatusSeeOther {
		t.Fatalf("container options save status=%d body=%s", result.status, noticeRegion(t, result.body))
	}
	stored, _ := storedPolicy(t, fixture)
	execution := stored.Execution
	if !execution.ContainerAllowTags || !execution.ContainerPullMissing || !execution.ContainerImageVolumes || !execution.ContainerWritableRoot ||
		strings.Join(execution.ContainerMissingEnforcement, ",") != "cpu,swap" || execution.ContainerNetwork != "checks-net" {
		t.Fatalf("stored container options %+v", execution)
	}
	page = browserGET(t, client, policyURL)
	for _, want := range []string{
		`name="container_allow_tags" value="1" checked`, `name="container_pull_missing" value="1" checked`,
		`name="container_image_volumes" value="1" checked`, `name="container_writable_root" value="1" checked`,
		`name="container_missing_enforcement" value="swap" checked`, `name="container_missing_enforcement" value="cpu" checked`,
		`name="container_network" value="named" checked`, `value="checks-net"`, "registry.example:5000",
		// The registry sentence carries both languages, so the page can follow an edited image.
		`data-registry-en="For this image, the registry is %s."`,
	} {
		if !strings.Contains(page.body, want) {
			t.Errorf("the saved options are not shown: missing %s", want)
		}
	}
	if strings.Contains(page.body, `name="container_missing_enforcement" value="memory" checked`) {
		t.Error("an unaccepted limit is shown as accepted")
	}
	// A refused form names the registry of the image it submitted, not the saved one.
	options.Set("container_image", "second.example/checks:2")
	options.Set("container_network_name", "host")
	if refused := post(options); !strings.Contains(refused.body, "the registry is second.example.") ||
		strings.Contains(refused.body, "the registry is registry.example:5000.") {
		t.Error("a refused form does not name the submitted image's registry")
	}
	// Leaving container mode refuses nothing: its options are not sent.
	host := validPolicyValues(csrf)
	host.Set("container_allow_tags", "1")
	if result := post(host); result.status != http.StatusSeeOther {
		t.Fatalf("host policy with a hidden container option status=%d", result.status)
	}
	if policy, _ := storedPolicy(t, fixture); policy.Executor != state.CheckExecutorHost || policy.Execution.HasContainerOptions() {
		t.Fatalf("host policy stored %+v", policy)
	}
}

// Saving a policy and allowing it to run are separate decisions, and each
// change asks for the administrator password again. Consent binds to one
// exact policy generation, and every result reaches the screen.
func TestConfiguredChecksConsentJourney(t *testing.T) {
	fixture := newAPIFixture(t, false)
	askEveryTime(t, fixture.app)
	server, client, jar := openBrowser(t, fixture)
	policyURL := server.URL + configuredChecksURL("project")
	post := func(values url.Values) browserHTTPResult {
		return browserForm(t, client, policyURL, values, server.URL)
	}
	consent := func() bool { policy, _ := storedPolicy(t, fixture); return policy.ConsentActive }

	// A general visitor never reaches the screen.
	withoutAdmin := browserGET(t, client, policyURL)
	if withoutAdmin.status != http.StatusSeeOther || !strings.HasPrefix(withoutAdmin.header.Get("Location"), "/admin/login") {
		t.Fatalf("policy screen without admin status=%d location=%q", withoutAdmin.status, withoutAdmin.header.Get("Location"))
	}
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "cc-consent")
	listed := browserGET(t, client, policyURL)
	if listed.status != http.StatusOK || !strings.Contains(listed.body, `name="csrf" value="`+csrf+`"`) || listed.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("admin policy screen status=%d cache-control=%q", listed.status, listed.header.Get("Cache-Control"))
	}
	// A remembered session is not consent to grant execution authority.
	noPassword := validPolicyValues(csrf)
	noPassword.Del("admin_password")
	wrongCSRF := validPolicyValues(csrf)
	wrongCSRF.Set("csrf", "wrong-csrf")
	if result := post(noPassword); result.status != http.StatusUnauthorized {
		t.Fatalf("a save without a password status=%d", result.status)
	}
	if result := post(wrongCSRF); result.status != http.StatusForbidden {
		t.Fatalf("a save with a wrong csrf token status=%d", result.status)
	}
	if _, exists := storedPolicy(t, fixture); exists {
		t.Fatal("a refused save stored a policy")
	}

	saved := post(validPolicyValues(csrf))
	if saved.status != http.StatusSeeOther || saved.header.Get("Location") != configuredChecksURL("project")+"?notice=check_policy_saved" {
		t.Fatalf("policy save status=%d location=%q", saved.status, saved.header.Get("Location"))
	}
	policy, exists := storedPolicy(t, fixture)
	if !exists || policy.ConsentActive {
		t.Fatalf("saving stored exists=%v consent=%v", exists, policy.ConsentActive)
	}

	// Enabling an identity that is stale, partial or unreadable is refused.
	version := strconv.FormatInt(policy.Version, 10)
	for name, identity := range map[string]url.Values{
		"stale version": {"policy_version": {"0"}, "policy_digest": {policy.Digest}},
		"stale digest":  {"policy_version": {version}, "policy_digest": {"an-older-digest"}},
		"no digest":     {"policy_version": {version}},
		"no identity":   {},
		"unparsable":    {"policy_version": {"soon"}, "policy_digest": {policy.Digest}},
		"empty digest":  {"policy_version": {version}, "policy_digest": {""}},
	} {
		stale := enableForm(csrf, policy)
		stale.Del("policy_version")
		stale.Del("policy_digest")
		for key, value := range identity {
			stale[key] = value
		}
		if result := post(stale); result.status != http.StatusConflict || consent() {
			t.Fatalf("%s: status=%d consent=%v", name, result.status, consent())
		}
	}
	// A failed enable keeps the saved values on screen, in the largest unit
	// that holds each exactly, and never echoes the password.
	wrongPassword := enableForm(csrf, policy)
	wrongPassword.Set("admin_password", "not-the-password")
	refused := post(wrongPassword)
	if refused.status != http.StatusUnauthorized || strings.Contains(refused.body, `value="not-the-password"`) {
		t.Fatalf("wrong password status=%d", refused.status)
	}
	for field, want := range map[string]webui.LimitInput{
		"max_timeout_ms":  {Amount: "10", Unit: webui.UnitMinutes},
		"queue_limit":     {Amount: "20"},
		"max_active_jobs": {Amount: "2"},
		"max_lease_ms":    {Amount: "2", Unit: webui.UnitMinutes},
	} {
		if got := limitOnScreen(t, refused.body, field); got != want {
			t.Fatalf("a failed enable blanked %s: the screen shows %+v, want %+v", field, got, want)
		}
	}

	// Save and enable shows what would change and changes nothing.
	changed := validPolicyValues(csrf)
	changed.Set("action", webui.ActionReviewSaveAndEnable)
	changed.Del("admin_password")
	changed.Set("max_timeout_ms", "300000")
	review := post(changed)
	if review.status != http.StatusOK || !strings.Contains(review.body, browserText(webui.MsgCCReviewWarn)) ||
		!strings.Contains(review.body, `id="cc-review"`) || !strings.Contains(review.body, "10 minutes") || !strings.Contains(review.body, "5 minutes") {
		t.Fatalf("review status=%d body lacks the change or its warning", review.status)
	}
	if current, _ := storedPolicy(t, fixture); current.Digest != policy.Digest || current.ConsentActive {
		t.Fatalf("the review changed the stored policy: %+v", current)
	}
	confirm := validPolicyValues(csrf)
	confirm.Set("action", webui.ActionSaveAndEnable)
	confirm.Set("max_timeout_ms", "300000")
	carryReview := func(body string) {
		for _, name := range []string{"review_digest", "base_version", "base_digest"} {
			confirm.Set(name, formValue(t, body, name))
		}
	}
	carryReview(review.body)
	// A form edited after the review is reviewed again, not saved.
	edited := url.Values{}
	for key, value := range confirm {
		edited[key] = append([]string(nil), value...)
	}
	edited.Set("queue_limit", "21")
	if result := post(edited); result.status != http.StatusConflict || !strings.Contains(result.body, browserText(webui.MsgCCReviewChanged)) {
		t.Fatalf("edited after review status=%d", result.status)
	}
	// Another save between review and confirmation refuses the step.
	other := validPolicyValues(csrf)
	other.Set("queue_limit", "30")
	if result := post(other); result.status != http.StatusSeeOther {
		t.Fatalf("concurrent save status=%d", result.status)
	}
	if result := post(confirm); result.status != http.StatusConflict || !strings.Contains(result.body, browserText(webui.MsgCCPolicyStale)) {
		t.Fatalf("confirmation over another change status=%d", result.status)
	}
	if current, _ := storedPolicy(t, fixture); current.ConsentActive || current.QueueLimit != 30 {
		t.Fatalf("a stale confirmation changed the policy or consent: %+v", current)
	}
	// Reviewed again, the confirmation needs the password and then saves and
	// enables exactly the reviewed policy.
	carryReview(post(changed).body)
	withoutPassword := url.Values{}
	for key, value := range confirm {
		withoutPassword[key] = append([]string(nil), value...)
	}
	withoutPassword.Del("admin_password")
	if result := post(withoutPassword); result.status != http.StatusUnauthorized || !strings.Contains(result.body, browserText(webui.MsgCCReviewWarn)) {
		t.Fatalf("confirmation without a password status=%d", result.status)
	}
	done := post(confirm)
	if done.status != http.StatusSeeOther || done.header.Get("Location") != configuredChecksURL("project")+"?notice=check_policy_saved_turned_on" {
		t.Fatalf("confirmation status=%d location=%q", done.status, done.header.Get("Location"))
	}
	if enabled, _ := storedPolicy(t, fixture); !enabled.ConsentActive || enabled.ConsentDigest != confirm.Get("review_digest") || enabled.MaxTimeoutMS != 300000 || enabled.QueueLimit != 20 {
		t.Fatalf("save and enable stored %+v", enabled)
	}
	for _, workflows := range []bool{false, true} {
		t.Run("workflows_"+strconv.FormatBool(workflows), func(t *testing.T) {
			policy, _ := storedPolicy(t, fixture)
			input, notices := policyInputFrom("project", policyFormFrom(browserCheckPolicy(policy, true)))
			if len(notices) != 0 {
				t.Fatalf("stored policy form refused: %+v", notices)
			}
			input.RunWorkflows = &workflows
			if _, err := fixture.app.Store.SetCheckPolicy(context.Background(), input, fixture.app.now()); err != nil {
				t.Fatal(err)
			}
			carryReview(post(changed).body)
			if result := post(confirm); result.status != http.StatusSeeOther {
				t.Fatalf("confirmation status=%d", result.status)
			}
			enabled, _ := storedPolicy(t, fixture)
			if enabled.RunWorkflows != workflows || enabled.Digest != confirm.Get("review_digest") ||
				!enabled.ConsentActive || enabled.ConsentDigest != enabled.Digest {
				t.Fatalf("save and enable stored %+v, want workflows=%v digest=%s enabled", enabled, workflows, confirm.Get("review_digest"))
			}
		})
	}

	// Changing the policy clears the confirmation, and enabling the new
	// generation records it again.
	if result := post(validPolicyValues(csrf)); result.status != http.StatusSeeOther || consent() {
		t.Fatalf("policy change status=%d consent=%v", result.status, consent())
	}
	policy, _ = storedPolicy(t, fixture)
	enabled := post(enableForm(csrf, policy))
	if enabled.status != http.StatusSeeOther || enabled.header.Get("Location") != configuredChecksURL("project")+"?notice=checks_enabled" || !consent() {
		t.Fatalf("enable status=%d location=%q consent=%v", enabled.status, enabled.header.Get("Location"), consent())
	}
	// Resaving an unchanged policy leaves consent as it was, and the result
	// must not tell an owner with running checks that execution is off.
	resaved := post(validPolicyValues(csrf))
	location := resaved.header.Get("Location")
	if resaved.status != http.StatusSeeOther || location != configuredChecksURL("project")+"?notice=check_policy_saved_enabled" || !consent() {
		t.Fatalf("unchanged resave status=%d location=%q consent=%v", resaved.status, location, consent())
	}
	shown := browserGET(t, client, server.URL+location)
	if shown.status != http.StatusOK || !strings.Contains(shown.body, webui.Text(webui.LangEN, webui.MsgCCSavedEnabled)) ||
		strings.Contains(shown.body, webui.Text(webui.LangEN, webui.MsgCCSaved)) {
		t.Fatalf("the saved-while-enabled result status=%d is dropped or claims execution is off", shown.status)
	}

	// Each redirect key names a notice; a key with no case in the table is
	// dropped silently and leaves the owner without a statement.
	for key, code := range map[string]webui.MessageCode{
		"check_policy_saved": webui.MsgCCSaved, "checks_enabled": webui.MsgCCEnabled, "checks_disabled": webui.MsgCCDisabled,
		"check_job_cancelled": webui.MsgCCJobCancelled, "check_job_rerun": webui.MsgCCJobRerunQueued,
		"check_job_rerun_existing": webui.MsgCCJobRerunExisting, "check_job_already_finished": webui.MsgCCJobAlreadyFinished,
	} {
		afterAction(t, jar, server.URL, key)
		if shown := browserGET(t, client, policyURL+"?notice="+key); shown.status != http.StatusOK || !strings.Contains(shown.body, webui.Text(webui.LangEN, code)) {
			t.Fatalf("%s: the result was dropped instead of shown", key)
		}
	}

	// With nothing recorded the screen says so, an unknown job opens as not
	// found, and an unusable identity is escaped in the addresses it renders.
	if !strings.Contains(browserGET(t, client, policyURL).body, webui.Text(webui.LangEN, webui.MsgCCJobsNone)) {
		t.Error("an empty job list does not say that nothing has run yet")
	}
	for _, query := range []string{strings.Repeat("b", 32), "a%26b%3Cscript%3E"} {
		missing := browserGET(t, client, policyURL+"?job="+query)
		if missing.status != http.StatusOK || !strings.Contains(missing.body, webui.Text(webui.LangEN, webui.MsgCCJobNotFound)) || strings.Contains(missing.body, "<script>") {
			t.Errorf("job %q status=%d: not opened as not found, or reached the markup unescaped", query, missing.status)
		}
	}
	if page := browserGET(t, client, policyURL+"?job=a%26b%3Cscript%3E"); !strings.Contains(page.body, "job=a%26b%3Cscript%3E") {
		t.Error("the rendered addresses lost the escaped job identity")
	}
}

// Runner tokens: the value is shown once, revoking keeps the record, and no
// token or job action reaches across repositories.
func TestConfiguredChecksRunnerTokenJourney(t *testing.T) {
	fixture := newAPIFixture(t, false)
	askEveryTime(t, fixture.app)
	server, client, jar := openBrowser(t, fixture)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "rt-admin")
	tokenURL := server.URL + runnerTokensURL("project")
	ctx := context.Background()

	// Runner authority is scoped to a stored policy, so the screen says what
	// is missing instead of issuing a token that means nothing.
	empty := browserGET(t, client, tokenURL)
	if empty.status != http.StatusOK || !strings.Contains(empty.body, webui.Text(webui.LangEN, webui.MsgRTNeedPolicy)) ||
		strings.Contains(empty.body, `value="`+webui.ActionIssueRunnerToken+`"`) {
		t.Fatalf("runner screen without a policy status=%d", empty.status)
	}
	if result := browserForm(t, client, server.URL+configuredChecksURL("project"), validPolicyValues(csrf), server.URL); result.status != http.StatusSeeOther {
		t.Fatalf("policy save status=%d", result.status)
	}
	issue := url.Values{
		"csrf": {csrf}, "action": {webui.ActionIssueRunnerToken}, "admin_password": {"admin-password"},
		"label": {"browser runner"}, "creation_id": {formValue(t, browserGET(t, client, tokenURL).body, "creation_id")},
	}
	// A remembered session cannot mint a token on its own.
	noPassword := url.Values{}
	for key, value := range issue {
		noPassword[key] = value
	}
	noPassword.Del("admin_password")
	if result := browserForm(t, client, tokenURL, noPassword, server.URL); result.status != http.StatusUnauthorized {
		t.Fatalf("a token was issued without a password: status=%d", result.status)
	}
	if credentials, err := fixture.store.CheckRunnerCredentials(ctx, "project"); err != nil || len(credentials) != 0 {
		t.Fatalf("password bypass issued a token: count=%d err=%v", len(credentials), err)
	}

	issued := browserForm(t, client, tokenURL, issue, server.URL)
	if issued.status != http.StatusOK || issued.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("token issue status=%d cache-control=%q", issued.status, issued.header.Get("Cache-Control"))
	}
	token := issuedTokenFromBody(t, issued.body)
	if strings.Contains(issued.header.Get("Location"), token) {
		t.Fatal("the token reached a redirect location")
	}
	// A repeated submission mints no second token and hands the value out once.
	repeat := browserForm(t, client, tokenURL, issue, server.URL)
	if repeat.status != http.StatusOK || strings.Contains(repeat.body, token) {
		t.Fatalf("repeated issue status=%d, or the token was handed out again", repeat.status)
	}
	credentials, err := fixture.store.CheckRunnerCredentials(ctx, "project")
	if err != nil || len(credentials) != 1 {
		t.Fatalf("repeated issue credentials=%d err=%v", len(credentials), err)
	}
	revisit := browserGET(t, client, tokenURL)
	if revisit.status != http.StatusOK || !strings.Contains(revisit.body, "browser runner") || strings.Contains(revisit.body, token) {
		t.Fatalf("runner list status=%d: it lacks the credential or shows its value", revisit.status)
	}

	// Another repository sees nothing of it and cannot revoke it. The label
	// is a distinctive marker because ordinary words appear in the copy.
	const scopedLabel = "zz-scope-marker-7f31"
	scoped, _, _, err := fixture.store.IssueCheckRunnerToken(ctx, "project", scopedLabel, strings.Repeat("c", 32), time.Now().UTC())
	noErr(t, err)
	if _, err := fixture.app.Repositories.Create(ctx, "other", "Other repository"); err != nil {
		t.Fatal(err)
	}
	other := browserGET(t, client, server.URL+runnerTokensURL("other"))
	if other.status != http.StatusOK || strings.Contains(other.body, scopedLabel) || strings.Contains(other.body, scoped.ID) || strings.Contains(other.body, shortOpaqueID(scoped.ID)) {
		t.Fatalf("other repository token screen status=%d, or it leaks a token", other.status)
	}
	if owning := browserGET(t, client, tokenURL); !strings.Contains(owning.body, scopedLabel) {
		t.Fatal("the token is not listed on the repository that owns it")
	}
	crossRevoke := browserForm(t, client, server.URL+runnerTokensURL("other"), url.Values{
		"csrf": {csrf}, "action": {webui.ActionRevokeRunnerToken}, "credential_id": {scoped.ID}, "admin_password": {"admin-password"},
	}, server.URL)
	if crossRevoke.status != http.StatusConflict {
		t.Fatalf("cross-repository revoke status=%d", crossRevoke.status)
	}
	// A job identity that does not resolve in the repository is a refusal.
	unknownJob := browserForm(t, client, server.URL+configuredChecksURL("other"), url.Values{
		"csrf": {csrf}, "action": {webui.ActionCancelCheckJob}, "admin_password": {"admin-password"}, "job_id": {strings.Repeat("a", 32)},
	}, server.URL)
	if unknownJob.status != http.StatusNotFound {
		t.Fatalf("unknown job cancel status=%d", unknownJob.status)
	}

	revoke := url.Values{
		"csrf": {csrf}, "action": {webui.ActionRevokeRunnerToken}, "credential_id": {credentials[0].ID}, "admin_password": {"admin-password"},
	}
	revoked := browserForm(t, client, tokenURL, revoke, server.URL)
	if revoked.status != http.StatusSeeOther || revoked.header.Get("Location") != runnerTokensURL("project")+"?notice=runner_token_revoked" {
		t.Fatalf("revoke status=%d location=%q", revoked.status, revoked.header.Get("Location"))
	}
	after, err := fixture.store.CheckRunnerCredentials(ctx, "project")
	if err != nil || len(after) != 2 {
		t.Fatalf("credentials after revoke: count=%d err=%v", len(after), err)
	}
	for _, credential := range after {
		if (credential.ID == credentials[0].ID) != (credential.RevokedAt != nil) {
			t.Fatalf("credential %s revoked=%v: only the revoked one may carry a revocation", credential.ID, credential.RevokedAt != nil)
		}
	}
	// The record stays and the result reaches the screen.
	afterAction(t, jar, server.URL, "runner_token_revoked")
	final := browserGET(t, client, server.URL+revoked.header.Get("Location"))
	if final.status != http.StatusOK || !strings.Contains(final.body, "browser runner") || !strings.Contains(final.body, webui.Text(webui.LangEN, webui.MsgRTRevokedDone)) {
		t.Fatalf("revoked credential record or result missing: status=%d", final.status)
	}
}

// Jobs: cancel belongs to work that has not reached a result, a cancel that
// loses to a completion says nothing changed, language links keep the opened
// job, and an automatic run is described as the server's own on every screen.
func TestConfiguredChecksJobJourney(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	jobsURL := configuredChecksURL("project")
	csrf, job := admitEnabledJob(t, fixture, server.URL, client, jar, "cc-job1", jobFacts{ref: "main", sourceOID: strings.Repeat("a", 40)})
	jobURL := jobsURL + "?job=" + job.ID

	// A pending job is still stoppable, so the control is drawn. The language
	// links keep the opened job, and the job actions post to it.
	page := browserGET(t, client, server.URL+jobURL+"&lang=ko")
	if page.status != http.StatusOK || !strings.Contains(page.body, webui.ActionCancelCheckJob) {
		t.Fatalf("pending job detail status=%d, or it offers no way to stop it", page.status)
	}
	for _, to := range []string{"en", "ko"} {
		if want := template.HTMLEscapeString(jobURL + "&lang=" + to); !strings.Contains(page.body, `href="`+want+`"`) {
			t.Errorf("the %s link is not the opened job's address (want %q)", to, want)
		}
		if strings.Contains(page.body, `href="`+jobsURL+"?lang="+to+`"`) {
			t.Errorf("the %s link drops the opened job", to)
		}
	}
	if strings.Contains(page.body, `name="action" value="`+webui.ActionSaveCheckPolicy+`"`) || !strings.Contains(page.body, `action="`+template.HTMLEscapeString(jobURL)+`"`) {
		t.Error("the opened job drew the policy editor, or its actions lost the job they act on")
	}
	policyScreen := browserGET(t, client, server.URL+jobsURL)
	for _, to := range []string{"en", "ko"} {
		if !strings.Contains(policyScreen.body, `href="`+jobsURL+`?lang=`+to+`"`) {
			t.Errorf("the policy screen lost its own %s link", to)
		}
	}
	if !strings.Contains(policyScreen.body, `action="`+jobsURL+`"`) || strings.Contains(policyScreen.body, `action="`+template.HTMLEscapeString(jobURL)+`"`) {
		t.Error("the policy form lost its POST route, or posts through the opened job's address")
	}

	// Once finished the job offers a rerun and no cancel, and a stale page
	// that still submits the cancel changes nothing.
	failJobBeforeStart(t, fixture)
	page = browserGET(t, client, server.URL+jobURL)
	if strings.Contains(page.body, webui.ActionCancelCheckJob) || !strings.Contains(page.body, webui.ActionRerunCheckJob) {
		t.Error("a finished job still offers to cancel, or no longer offers a rerun")
	}
	cancelForm := func(csrf, id string) url.Values {
		return url.Values{"csrf": {csrf}, "action": {webui.ActionCancelCheckJob}, "admin_password": {"admin-password"}, "job_id": {id}}
	}
	race := browserForm(t, client, server.URL+jobsURL, cancelForm(csrf, job.ID), server.URL)
	location := race.header.Get("Location")
	if race.status != http.StatusSeeOther || !strings.Contains(location, "notice=check_job_already_finished") || strings.Contains(location, "notice=check_job_cancelled") {
		t.Fatalf("racing cancel status=%d location=%q", race.status, location)
	}
	notices := noticeRegion(t, browserGET(t, client, server.URL+location).body)
	// Informational: a success chip would carry the claim the sentence refuses to make.
	if !strings.Contains(notices, browserText(webui.MsgCCJobAlreadyFinished)) || strings.Contains(notices, browserText(webui.MsgCCJobCancelled)) ||
		!strings.Contains(notices, `notice--info`) || strings.Contains(notices, `notice--success`) {
		t.Errorf("a cancel that lost to a completion is not reported as unchanged: %q", notices)
	}
	stored, exists, err := fixture.store.CheckJob(context.Background(), "project", job.ID)
	if err != nil || !exists || stored.Status != state.CheckJobError || stored.CancelRequestedAt != nil {
		t.Errorf("the finished job changed: %+v exists=%v err=%v", stored, exists, err)
	}
	apiCancel := importAPIRequest(t, http.MethodPost, server.URL+"/api/v1/repositories/project/check-jobs/"+job.ID+"/cancel", map[string]any{}, "admin-password")
	if apiCancel.StatusCode != http.StatusConflict || importAPICode(t, apiCancel) != "check_job_finished" {
		t.Errorf("API cancel of a finished job status=%d", apiCancel.StatusCode)
	}

	// Work that has not finished is cancelled and reports a cancellation. The
	// second admission has its own trigger facts, or it would dedup onto the
	// finished job, and it replaces the session, so it brings the CSRF token.
	liveCSRF, second := admitEnabledJob(t, fixture, server.URL, client, jar, "cc-job2", jobFacts{ref: "release", sourceOID: strings.Repeat("d", 40)})
	live := browserForm(t, client, server.URL+jobsURL, cancelForm(liveCSRF, second.ID), server.URL)
	if live.status != http.StatusSeeOther || !strings.Contains(live.header.Get("Location"), "notice=check_job_cancelled") {
		t.Fatalf("live cancel status=%d location=%q", live.status, live.header.Get("Location"))
	}
	liveNotices := noticeRegion(t, browserGET(t, client, server.URL+live.header.Get("Location")).body)
	if !strings.Contains(liveNotices, browserText(webui.MsgCCJobCancelled)) || strings.Contains(liveNotices, browserText(webui.MsgCCJobAlreadyFinished)) {
		t.Errorf("cancelling unfinished work is not reported as a cancellation: %q", liveNotices)
	}
	if stopped, _, err := fixture.store.CheckJob(context.Background(), "project", second.ID); err != nil || stopped.Status != state.CheckJobCancelled {
		t.Errorf("cancelling pending work left status %q err=%v", stopped.Status, err)
	}

	// An automatic run reads as the server's own on the job, task and pull
	// request screens, and a manual helper run keeps its own wording. The
	// admission names the branch the commit belongs to, so the pull request
	// reads evidence for its own source revision.
	_, third := admitEnabledJob(t, fixture, server.URL, client, jar, "cc-job3", jobFacts{ref: "feature", sourceOID: fixture.sourceOID})
	attempt := runJobToAttempt(t, fixture, third, strings.Repeat("7", 32), state.ProtectionHost)
	manualWording := []string{browserText(webui.MsgCheckProtectionInherited), browserText(webui.MsgCheckProvenanceHelper)}
	automaticWording := []string{browserText(webui.MsgCheckProtectionAutoHost), browserText(webui.MsgCheckProvenanceAutomatic)}
	created, err := fixture.app.PullRequests.Create(context.Background(), pullrequest.CreateInput{
		Repository: "project", Title: "Automatic evidence", SourceBranch: "feature", TargetBranch: "main",
		SourceOID: fixture.sourceOID, TargetOID: fixture.targetOID,
	})
	noErr(t, err)
	if view, err := fixture.app.PullRequests.Show(context.Background(), "project", created.Number); err != nil || view.Checks.JobID != third.ID {
		t.Fatalf("the pull request projection lost the job link: %q err=%v", view.Checks.JobID, err)
	}
	manualTask, err := fixture.store.CreateTask(context.Background(), "project", "Manual helper run", fixture.app.now())
	noErr(t, err)
	recordBrowserAttempt(t, fixture.store, manualTask.ID, fixture.targetOID, strings.Repeat("8", 32), state.WorktreeClean, "", true)
	for name, screen := range map[string]struct {
		url    string
		manual bool
	}{
		"job detail":   {jobsURL + "?job=" + third.ID, false},
		"task view":    {tasksURL("project", attempt.TaskID), false},
		"pull request": {pullRequestURL("project", created.Number), false},
		"manual task":  {tasksURL("project", manualTask.ID), true},
	} {
		got := browserGET(t, client, server.URL+screen.url)
		if got.status != http.StatusOK {
			t.Fatalf("%s status=%d", name, got.status)
		}
		want, unwanted := automaticWording, manualWording
		if screen.manual {
			want, unwanted = manualWording, automaticWording
		}
		for _, text := range want {
			if !strings.Contains(got.body, text) {
				t.Errorf("%s does not say %q", name, text)
			}
		}
		for _, text := range unwanted {
			if strings.Contains(got.body, text) {
				t.Errorf("%s borrows the other origin's wording %q", name, text)
			}
		}
	}
}

// The origin of an attempt comes from the recorded job link, never from the
// protection or scope it happens to carry.
func TestBrowserAutomaticOriginComesFromTheRecordedJobLink(t *testing.T) {
	credential, jobID := strings.Repeat("c", 32), strings.Repeat("e", 32)
	for _, tc := range []struct {
		name, jobID, protection, scope, want, wantOrigin string
	}{
		{"host job", jobID, state.ProtectionHost, state.ExecutionScopeInherited, webui.ProtectionAutomaticHost, webui.ProvenanceAutomaticJob},
		{"container job", jobID, state.ProtectionContainer, state.ExecutionScopeContainer, webui.ProtectionAutomaticContainer, webui.ProvenanceAutomaticJob},
		{"external runner", jobID, state.ProtectionRunnerReported, state.ExecutionScopeExternalRunner, webui.ProtectionRunnerReported, webui.ProvenanceRunnerClaimed},
		// A job with no established protection is a missing fact about the
		// server's run, never evidence of a manual one.
		{"job with no established protection", jobID, state.ProtectionUnknown, state.ExecutionScopeInherited, webui.ProtectionUnknown, webui.ProvenanceAutomaticJob},
		{"container job before it reported protection", jobID, state.ProtectionUnknown, state.ExecutionScopeContainer, webui.ProtectionUnknown, webui.ProvenanceAutomaticJob},
		{"runner job before it reported protection", jobID, state.ProtectionUnknown, state.ExecutionScopeExternalRunner, webui.ProtectionUnknown, webui.ProvenanceRunnerClaimed},
		{"manual helper", "", state.ProtectionUnknown, state.ExecutionScopeInherited, webui.ProtectionInherited, webui.ProvenanceAuthenticatedHelper},
		// Nothing admitted these runs, so no automatic sentence may be drawn
		// from facts that merely look like an executor's.
		{"unlinked host-like facts", "", state.ProtectionHost, state.ExecutionScopeInherited, "", webui.ProvenanceAuthenticatedHelper},
		{"unlinked container-like facts", "", state.ProtectionContainer, state.ExecutionScopeContainer, "", webui.ProvenanceAuthenticatedHelper},
		{"unlinked runner-like facts", "", state.ProtectionRunnerReported, state.ExecutionScopeExternalRunner, "", webui.ProvenanceAuthenticatedHelper},
	} {
		if got := browserProtection(tc.jobID, tc.protection, tc.scope); got != tc.want {
			t.Errorf("%s: protection became %q, want %q", tc.name, got, tc.want)
		}
		if got := browserProvenance(tc.jobID, credential, tc.scope); got != tc.wantOrigin {
			t.Errorf("%s: provenance became %q, want %q", tc.name, got, tc.wantOrigin)
		}
	}
	// Without a credential nothing is claimed about how the attempt arrived.
	for _, id := range []string{"", jobID} {
		if origin := browserProvenance(id, "", state.ExecutionScopeInherited); origin != webui.ProvenanceUnstated {
			t.Errorf("an attempt with no credential claims the origin %q", origin)
		}
	}
}

func TestActionBodiesRequireJSONObjects(t *testing.T) {
	fixture := newAPIFixture(t, false)
	ctx := context.Background()
	now := time.Unix(1_900_000_000, 0).UTC()
	_, err := fixture.store.SetCheckPolicy(ctx, state.CheckPolicyInput{
		RepositoryID: "project", Executor: state.CheckExecutorExternalRunner,
		AllowedEvents: []string{"push"}, MaxTimeoutMS: 60_000, MaxOutputLimitBytes: 64 << 10,
		QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60_000,
	}, now)
	noErr(t, err)
	_, token, _, err := fixture.store.IssueCheckRunnerToken(ctx, "project", "runner", "", now)
	noErr(t, err)
	server := serve(t, fixture.app.Handler())
	id := strings.Repeat("a", 32)
	base := server.URL + "/api/v1/repositories/project"
	routes := []struct{ name, method, path, token string }{
		{"policy enable", http.MethodPost, base + "/check-policy/enable", ""},
		{"policy disable", http.MethodPost, base + "/check-policy/disable", ""},
		{"job cancel", http.MethodPost, base + "/check-jobs/" + id + "/cancel", ""},
		{"job rerun", http.MethodPost, base + "/check-jobs/" + id + "/rerun", ""},
		{"runner claim", http.MethodPost, base + "/runner/claim", token},
		{"runner renew", http.MethodPost, base + "/runner/jobs/" + id + "/renew", token},
		{"share revoke", http.MethodPost, base + "/share-links/" + id + "/revoke", ""},
		{"backup run", http.MethodPost, server.URL + "/api/v1/backups/runs", ""},
		{"backup verify", http.MethodPost, server.URL + "/api/v1/backups/runs/" + id + "/verify", ""},
		{"runner credential revoke", http.MethodDelete, base + "/runner-credentials/" + id, ""},
		{"runner creation revoke", http.MethodDelete, base + "/runner-credentials/by-creation/" + id, ""},
	}
	for _, route := range routes {
		for _, body := range []struct {
			name, contentType, text string
			status                  int
			code                    string
		}{
			{"non JSON", "text/plain", "broken", http.StatusUnsupportedMediaType, "json_required"},
			{"malformed", "application/json", "{", http.StatusBadRequest, "invalid_json"},
			{"null", "application/json", "null", http.StatusBadRequest, "invalid_json"},
			{"array", "application/json", "[]", http.StatusBadRequest, "invalid_json"},
			{"unknown", "application/json", `{"extra":true}`, http.StatusBadRequest, "invalid_json"},
		} {
			t.Run(route.name+"/"+body.name, func(t *testing.T) {
				request, err := http.NewRequest(route.method, route.path, strings.NewReader(body.text))
				noErr(t, err)
				request.Header.Set("Content-Type", body.contentType)
				if route.token != "" {
					request.Header.Set("Authorization", "Bearer "+route.token)
				} else {
					request.SetBasicAuth("admin", "admin-password")
				}
				response, err := http.DefaultClient.Do(request)
				noErr(t, err)
				if status, code := checkStatus(t, response); status != body.status || code != body.code {
					t.Fatalf("status=%d code=%s, want %d %s", status, code, body.status, body.code)
				}
			})
		}
		t.Run(route.name+"/legacy empty body", func(t *testing.T) {
			request, err := http.NewRequest(route.method, route.path, nil)
			noErr(t, err)
			if route.token != "" {
				request.Header.Set("Authorization", "Bearer "+route.token)
			} else {
				request.SetBasicAuth("admin", "admin-password")
			}
			response, err := http.DefaultClient.Do(request)
			noErr(t, err)
			if status, code := checkStatus(t, response); code == "json_required" || code == "invalid_json" || status == http.StatusUnsupportedMediaType {
				t.Fatalf("legacy empty request: status=%d code=%s", status, code)
			}
		})
	}
}

func TestRunnerErrorsSeparateInvalidInputFromStaleLeaseAndStorage(t *testing.T) {
	for _, row := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"invalid input", state.ErrInvalidCheckJob, http.StatusUnprocessableEntity, "invalid_runner_request"},
		{"stale lease", state.ErrCheckJobLease, http.StatusConflict, "check_job_lease"},
		{"storage fault", errors.New("private state path"), http.StatusServiceUnavailable, "state_unavailable"},
	} {
		t.Run(row.name, func(t *testing.T) {
			answer := httptest.NewRecorder()
			writeRunnerError(answer, httptest.NewRequest(http.MethodPost, "/runner/jobs/invalid/renew", nil), row.err)
			if answer.Code != row.status || !strings.Contains(answer.Body.String(), `"code":"`+row.code+`"`) || strings.Contains(answer.Body.String(), "private state path") {
				t.Fatalf("status=%d body=%s", answer.Code, answer.Body.String())
			}
		})
	}
}

func TestConfiguredChecksKnownPathsAnswerWrongMethods(t *testing.T) {
	fixture := newAPIFixture(t, false)
	ctx := context.Background()
	now := time.Unix(1_900_000_000, 0).UTC()
	_, err := fixture.store.SetCheckPolicy(ctx, state.CheckPolicyInput{
		RepositoryID: "project", Executor: state.CheckExecutorExternalRunner,
		AllowedEvents: []string{"push"}, MaxTimeoutMS: 60_000, MaxOutputLimitBytes: 64 << 10,
		QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60_000,
	}, now)
	noErr(t, err)
	_, token, _, err := fixture.store.IssueCheckRunnerToken(ctx, "project", "runner", "", now)
	noErr(t, err)
	server := serve(t, fixture.app.Handler())
	id := strings.Repeat("a", 32)
	base := server.URL + "/api/v1/repositories/project"
	for _, row := range []struct {
		path, allow, token, method string
		status                     int
	}{
		{path: base + "/check-policy/enable", allow: "POST"},
		{path: base + "/check-policy/disable", allow: "POST"},
		{path: base + "/check-policy/save-and-enable", allow: "POST"},
		{path: base + "/check-jobs/" + id, allow: "GET"},
		{path: base + "/check-jobs/" + id + "/log", allow: "GET"},
		{path: base + "/check-jobs/" + id + "/cancel", allow: "POST"},
		{path: base + "/check-jobs/" + id + "/rerun", allow: "POST"},
		{path: base + "/runner-credentials", allow: "GET, POST"},
		{path: base + "/runner-credentials/" + id, allow: "DELETE"},
		{path: base + "/runner-credentials/by-creation/" + id, allow: "DELETE"},
		{path: base + "/runner-credentials/by-creation", method: http.MethodGet, status: http.StatusNotFound},
		{path: base + "/runner-credentials/by-creation", method: http.MethodDelete, status: http.StatusNotFound},
		{path: base + "/runner/claim", allow: "POST", token: token},
		{path: base + "/runner/jobs/" + id + "/source", allow: "GET", token: token},
		{path: base + "/runner/jobs/" + id + "/files/name/oid", allow: "GET", token: token},
		{path: base + "/runner/jobs/" + id + "/renew", allow: "POST", token: token},
		{path: base + "/runner/jobs/" + id + "/start", allow: "POST", token: token},
		{path: base + "/runner/jobs/" + id + "/complete", allow: "POST", token: token},
		{path: base + "/runner/jobs/" + id + "/unavailable", allow: "POST", token: token},
	} {
		method := row.method
		if method == "" {
			method = http.MethodPatch
		}
		t.Run(method+" "+strings.TrimPrefix(row.path, base), func(t *testing.T) {
			request, err := http.NewRequest(method, row.path, nil)
			noErr(t, err)
			if row.token != "" {
				request.Header.Set("Authorization", "Bearer "+row.token)
			} else {
				request.SetBasicAuth("admin", "admin-password")
			}
			response, err := http.DefaultClient.Do(request)
			noErr(t, err)
			wantStatus, wantCode := http.StatusMethodNotAllowed, "method_not_allowed"
			if row.status == http.StatusNotFound {
				wantStatus, wantCode = http.StatusNotFound, "not_found"
			}
			if status, code := checkStatus(t, response); status != wantStatus || code != wantCode || response.Header.Get("Allow") != row.allow {
				t.Fatalf("status=%d code=%s allow=%s, want %d %s allow=%s", status, code, response.Header.Get("Allow"), wantStatus, wantCode, row.allow)
			}
		})
	}
}

// A closed check runtime is reported on the screen, over the policy API and
// to a runner, and it does not stop ordinary repository work.
func TestConfiguredChecksClosedRuntimeIsReportedWithoutBlockingGit(t *testing.T) {
	fixture := newAPIFixture(t, false)
	fixture.app.CheckRuntimeUnavailableCode = webui.RuntimeWorkspaceUnavailable
	fixture.app.CheckRuntimeUnavailableReason = "Configured checks are unavailable. Repair the workspace and restart OwnGit."
	server, client, jar := openBrowser(t, fixture)
	browserAdminSessionFor(t, fixture, server.URL, jar, "cc-runtime")

	page := browserGET(t, client, server.URL+configuredChecksURL("project"))
	if page.status != http.StatusOK || !strings.Contains(page.body, webui.RuntimeWorkspaceUnavailable) {
		t.Fatalf("policy screen status=%d, or it does not report the runtime code", page.status)
	}
	for _, target := range []string{"/repositories/project", "/repositories/project/pull-requests", "/repositories/project/tasks"} {
		if result := browserGET(t, client, server.URL+target); result.status != http.StatusOK {
			t.Fatalf("%s became unavailable with a closed check runtime: status=%d", target, result.status)
		}
	}

	ctx := context.Background()
	now := time.Unix(1_900_000_000, 0).UTC()
	if _, err := fixture.store.SetCheckPolicy(ctx, state.CheckPolicyInput{
		RepositoryID: "project", Executor: state.CheckExecutorExternalRunner,
		AllowedEvents: []string{"push"}, MaxTimeoutMS: 60_000, MaxOutputLimitBytes: 64 << 10,
		QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60_000,
	}, now); err != nil {
		t.Fatal(err)
	}
	_, token, _, err := fixture.store.IssueCheckRunnerToken(ctx, "project", "runner", "", now.Add(time.Second))
	noErr(t, err)

	policyResponse := httptest.NewRecorder()
	fixture.app.handleCheckPolicy(policyResponse, httptest.NewRequest(http.MethodGet, "/api/v1/repositories/project/check-policy", nil), "project", "")
	var decoded checkapi.PolicyResponse
	noErr(t, json.Unmarshal(policyResponse.Body.Bytes(), &decoded))
	if policyResponse.Code != http.StatusOK || decoded.Policy == nil || decoded.Runtime.Available ||
		decoded.Runtime.UnavailableCode != "workspace_unavailable" || !strings.Contains(decoded.Runtime.UnavailableReason, "restart OwnGit") {
		t.Fatalf("policy status=%d runtime=%+v", policyResponse.Code, decoded.Runtime)
	}
	claim := httptest.NewRequest(http.MethodPost, "/api/v1/repositories/project/runner/claim", nil)
	claim.Header.Set("Authorization", "Bearer "+token)
	runnerResponse := httptest.NewRecorder()
	fixture.app.handleRunnerAPI(runnerResponse, claim, "project", "claim")
	if body := runnerResponse.Body.String(); runnerResponse.Code != http.StatusServiceUnavailable || !strings.Contains(body, "check_runtime_unavailable") || !strings.Contains(body, "workspace_unavailable") {
		t.Fatalf("runner status=%d body=%s", runnerResponse.Code, body)
	}
	wrongMethod := httptest.NewRequest(http.MethodPatch, "/api/v1/repositories/project/runner/claim", nil)
	wrongMethod.Header.Set("Authorization", "Bearer "+token)
	methodResponse := httptest.NewRecorder()
	fixture.app.handleRunnerAPI(methodResponse, wrongMethod, "project", "claim")
	if methodResponse.Code != http.StatusMethodNotAllowed || methodResponse.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("wrong method with unavailable runtime: status=%d allow=%s", methodResponse.Code, methodResponse.Header().Get("Allow"))
	}
}

// The screen reads the check file on the default branch with the real parser
// and the file page's read bound.
func TestConfiguredChecksFileStatusReadsTheDefaultBranch(t *testing.T) {
	// A known ceiling makes the read bound real on every platform.
	saved := hostmem.Ceiling
	hostmem.Ceiling = func() uint64 { return 512 << 20 }
	defer func() { hostmem.Ceiling = saved }()

	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	browserAdminSessionFor(t, fixture, server.URL, jar, "cc-file")
	policyURL := server.URL + configuredChecksURL("project")

	if page := browserGET(t, client, policyURL); !strings.Contains(page.body, browserText(webui.MsgCCFileMissing)) {
		t.Fatal("a branch without a check file was not reported as missing")
	}
	push := func(content string) string {
		t.Helper()
		apiRunGit(t, fixture.work, "checkout", "main")
		noErr(t, os.MkdirAll(filepath.Join(fixture.work, ".owngit"), 0o700))
		noErr(t, os.WriteFile(filepath.Join(fixture.work, ".owngit", "checks.json"), []byte(content), 0o600))
		apiRunGit(t, fixture.work, "add", ".")
		apiRunGit(t, fixture.work, "commit", "-m", "checks")
		apiRunGit(t, fixture.work, "push", server.URL+"/git/project.git", "HEAD:refs/heads/main")
		return browserGET(t, client, policyURL).body
	}

	// The example the page offers is a file the real parser accepts.
	if body := push(webui.CheckFileExample); !strings.Contains(body, browserText(webui.MsgCCFileFound)) || !strings.Contains(body, `data-en="1 check"`) {
		t.Fatal("the offered example was not recognised as a valid check file")
	}
	if body := push(`{"version": 2}`); !strings.Contains(body, browserText(webui.MsgCCFileInvalid)) ||
		!regexp.MustCompile(`class="ccstatus__problem mono">[^<]*version`).MatchString(body) {
		t.Fatal("an unparseable check file was not reported as invalid with the parser's reason")
	}
	// Above the read bound the file page's too-large notice is shown, not the
	// parser's reason and not a size rule.
	bound := fixture.app.Repositories.Git.ReadBound()
	if bound == 0 {
		t.Fatal("the forced ceiling gave no read bound")
	}
	above := push(strings.Repeat("a", int(bound)+1))
	if !strings.Contains(above, browserText(webui.MsgCodeTooLarge)) || !strings.Contains(above, browserText(webui.MsgCCFileInvalid)) ||
		strings.Contains(above, "regular file of at most 64 KiB") {
		t.Fatal("a check file above the read bound is not reported as too large")
	}
}

// Save and enable over the API turns checks on for exactly the submitted
// policy, and refuses when the stored policy is not the one named.
func TestSaveAndEnableAPIBindsToTheReviewedPolicy(t *testing.T) {
	_, server, _ := newConfirmationFixture(t, false, state.Confirm30Days)
	target := server.URL + "/api/v1/repositories/project/check-policy/save-and-enable"
	policy := checkapi.PolicyInput{
		Executor: state.CheckExecutorExternalRunner, AllowedEvents: []string{"push"},
		MaxTimeoutMS: 5_000, MaxOutputLimitBytes: 64 << 10, QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 10_000,
	}
	var first checkapi.PolicyResponse
	decodeCheckJSON(t, adminAPIRequest(t, http.MethodPost, target, checkapi.SaveAndEnableInput{Policy: policy, Expected: &checkapi.ExpectedPolicy{}}, "admin-password"), &first)
	if first.Policy == nil || !first.Policy.ConsentActive || first.Policy.Version != 1 {
		t.Fatalf("first save and enable=%+v", first.Policy)
	}

	policy.QueueLimit = 6
	stale := checkapi.SaveAndEnableInput{Policy: policy, Expected: &checkapi.ExpectedPolicy{}}
	if status, code := checkStatus(t, adminAPIRequest(t, http.MethodPost, target, stale, "admin-password")); status != http.StatusConflict || code != "check_policy_stale" {
		t.Fatalf("stale save and enable status=%d code=%q", status, code)
	}
	invalid := checkapi.SaveAndEnableInput{Policy: checkapi.PolicyInput{Executor: state.CheckExecutorContainer}}
	if status, code := checkStatus(t, adminAPIRequest(t, http.MethodPost, target, invalid, "admin-password")); status != http.StatusUnprocessableEntity || code != "invalid_check_policy" {
		t.Fatalf("invalid save and enable status=%d code=%q", status, code)
	}

	current := checkapi.SaveAndEnableInput{Policy: policy, Expected: &checkapi.ExpectedPolicy{Version: first.Policy.Version, Digest: first.Policy.Digest}}
	var saved checkapi.PolicyResponse
	decodeCheckJSON(t, adminAPIRequest(t, http.MethodPost, target, current, "admin-password"), &saved)
	if saved.Policy == nil || !saved.Policy.ConsentActive || saved.Policy.QueueLimit != 6 || saved.Policy.Version != 2 {
		t.Fatalf("save and enable=%+v", saved.Policy)
	}
}
