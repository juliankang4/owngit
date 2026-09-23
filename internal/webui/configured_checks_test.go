package webui

import (
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Fixtures and checks for the configured-check screens.
//
// What these tests protect is the wording, not the layout: a policy that is
// only stored must not read as running, a closed runtime must not read as a
// failed check, and a job's captured mode must not be replaced by the policy's
// current selection. None of that is visible from a screenshot.

// runnerTestToken is the fixture token. It is synthetic and is never a real
// credential, which is what lets the leak tests search for it by value.
const runnerTestToken = "ogr_FIXTUREvalue0000000000000000000000000000"

type ccFixture int

const (
	ccFixtureEnabled ccFixture = iota
	ccFixtureNoPolicy
	ccFixtureRuntimeDown
	ccFixtureJobDetail
	// A job whose attempt record could not be read. It is a separate fixture
	// because "the run is unreadable" must render differently from both "it
	// passed" and "nothing ran".
	ccFixtureJobUnreadable
)

// ccFixtureRanges supplies representative renderer inputs. Server tests verify
// that production views receive the backend's authoritative descriptors.
func ccFixtureRanges() map[string]FieldRange {
	return map[string]FieldRange{
		"max_timeout_ms":              {Min: 1000, Max: 86400000, Known: true},
		"max_output_limit_bytes":      {Min: 1024, Max: 67108864, Known: true},
		"queue_limit":                 {Min: 1, Max: 1000, Known: true},
		"max_active_jobs":             {Min: 1, Max: 100, Known: true},
		"max_lease_ms":                {Min: 1000, Max: 86400000, Known: true},
		"source_max_entries":          {Min: 1, Max: 100000, Known: true},
		"source_max_file_bytes":       {Min: 1, Max: 1073741824, Known: true},
		"source_max_path_depth":       {Min: 1, Max: 256, Known: true},
		"source_max_path_bytes":       {Min: 1, Max: 4096, Known: true},
		"source_max_name_bytes":       {Min: 1, Max: 1024, Known: true},
		"source_metadata_limit_bytes": {Min: 1024, Max: 67108864, Known: true},
		"container_cpu_millis":        {Min: 100, Max: 64000, Known: true},
		"container_memory_bytes":      {Min: 67108864, Max: 68719476736, Known: true},
		"container_pids":              {Min: 16, Max: 4096, Known: true},
		"container_scratch_bytes":     {Min: 1048576, Max: 17179869184, Known: true},
		// The total's floor moves with the per-file limit, so it names that
		// field instead of a number. Its ceiling is fixed like every other.
		"source_max_total_bytes": {MinLabel: MsgCCSrcFileBytes, Max: 4294967296, Known: true},
	}
}

func savedPolicyView() CheckPolicyView {
	return CheckPolicyView{
		Saved: true, Version: 4, Executor: ExecutorContainer,
		// The full identity is what the enable form quotes back, and the
		// abbreviation is what a reader compares. Both are rendered, so the
		// fixture carries both and they agree.
		Digest:              "9c41ab2f80e3d7561ac94b20e8f37d15",
		ShortDigest:         "9c41ab2f80e3",
		AllowedEvents:       []string{CheckEventPullRequest, CheckEventPush},
		MaxTimeoutMS:        600000,
		MaxOutputLimitBytes: 262144,
		QueueLimit:          20,
		MaxActiveJobs:       2,
		MaxLeaseMS:          120000,
		Source: CheckSourceLimitsView{
			MaxEntries: 20000, MaxFileBytes: 5242880, MaxTotalBytes: 134217728,
			MaxPathDepth: 32, MaxPathBytes: 1024, MaxNameBytes: 255, MetadataLimit: 65536,
		},
		Container: CheckContainerView{
			Image: "registry.local/owngit-checks@sha256:aaaa", Runtime: "docker",
			Network: ContainerNetworkNone, CPUMillis: 2000, MemoryBytes: 2147483648,
			PIDs: 512, ScratchBytes: 1073741824,
		},
		ConsentActive: true, ConsentVersion: 4, UpdatedAt: testNow.AddDate(0, 0, -3),
	}
}

func ccJobRows() []CheckJobRow {
	return []CheckJobRow{
		{
			ID: "job2", ShortID: "job2abc", URL: "/repositories/r1/configured-checks?job=job2",
			Status: JobStarted, Trigger: CheckEventPullRequest, TriggerRef: "refs/heads/fix/cursor",
			SourceOID: "7f2c1a0bb", SourceShortOID: "7f2c1a0", Executor: ExecutorContainer,
			PullRequestNumber: 14, PullRequestURL: "/repositories/r1/pull-requests/14",
			WorkflowPath: ".owngit/checks.json", ConfigurationVersion: 7, PolicyVersion: 4,
			AdmittedAt: testNow.Add(-4 * time.Minute), StartedAt: testNow.Add(-3 * time.Minute),
			Cancellable: true,
		},
		{
			// A cancellation was recorded. It is a request, not a stop.
			ID: "job1", ShortID: "job1abc", URL: "/repositories/r1/configured-checks?job=job1",
			Status: JobClaimed, Trigger: CheckEventPush, TriggerRef: "refs/heads/main",
			SourceOID: "1ab4c90ff", SourceShortOID: "1ab4c90", Executor: ExecutorContainer,
			ConfigurationVersion: 7, PolicyVersion: 4, CancelRequested: true,
			AdmittedAt: testNow.Add(-11 * time.Minute), Cancellable: false,
		},
		{
			// An older job admitted under a different mode. The row keeps what
			// applied to it rather than what the policy selects today.
			ID: "job0", ShortID: "job0abc", URL: "/repositories/r1/configured-checks?job=job0",
			Status: JobFailed, Trigger: CheckEventPush, TriggerRef: "refs/heads/main",
			SourceOID: "55d0e21aa", SourceShortOID: "55d0e21", Executor: ExecutorHost,
			ConfigurationVersion: 6, PolicyVersion: 3, Summary: "2 of 3 checks failed",
			AdmittedAt: testNow.AddDate(0, 0, -1), StartedAt: testNow.AddDate(0, 0, -1),
			FinishedAt: testNow.AddDate(0, 0, -1).Add(2 * time.Minute), Rerunnable: true,
		},
	}
}

func configuredChecksPage(c Chrome, fixture ccFixture) ConfiguredChecksPage {
	page := ConfiguredChecksPage{
		Chrome: c, Repo: evidenceRepo(), Tabs: evidenceTabs(RepoTabChecks),
		SelfURL:         "/repositories/r1/configured-checks",
		SubmitURL:       "/repositories/r1/configured-checks",
		TasksURL:        "/repositories/r1/tasks",
		RunnerTokensURL: "/repositories/r1/runner-tokens",
		Policy:          savedPolicyView(),
		Runtime:         CheckRuntimeView{Available: true},
		Jobs:            ccJobRows(),
	}
	page.Form = CheckPolicyForm{
		Executor: ExecutorContainer, PushSelected: true, PullRequestSelected: true,
		MaxTimeoutMS: "600000", MaxOutputLimitBytes: "262144", QueueLimit: "20",
		MaxActiveJobs: "2", MaxLeaseMS: "120000",
		SourceMaxEntries: "20000", SourceMaxFileBytes: "5242880",
		SourceMaxTotalBytes: "134217728", SourceMaxPathDepth: "32",
		SourceMaxPathBytes: "1024", SourceMaxNameBytes: "255", SourceMetadataLimitBytes: "65536",
		ContainerImage: "registry.local/owngit-checks@sha256:aaaa", ContainerNetwork: ContainerNetworkNone,
		ContainerCPUMillis: "2000", ContainerMemoryBytes: "2147483648",
		ContainerPIDs: "512", ContainerScratchBytes: "1073741824",
		Ranges: ccFixtureRanges(),
	}

	switch fixture {
	case ccFixtureNoPolicy:
		page.Policy = CheckPolicyView{}
		// An empty repository still shows the accepted ranges, because they
		// are what the first submission will be judged against.
		page.Form = CheckPolicyForm{
			Executor: ExecutorHost, ContainerNetwork: ContainerNetworkNone,
			Ranges: ccFixtureRanges(),
		}
		page.Jobs = nil
	case ccFixtureRuntimeDown:
		page.Runtime = CheckRuntimeView{Code: RuntimeWorkspaceUnavailable}
		// No jobs. A closed runtime has to be readable on its own, and a
		// recorded failure sitting in the list would make "this does not read as
		// a failed check" unprovable: the failure word would be on the page
		// either way. The job list has its own fixtures.
		page.Jobs = nil
	case ccFixtureJobDetail:
		job := ccJobRows()[2]
		job.TaskURL = "/repositories/r1/tasks?task=t9"
		page.Jobs = nil
		// An opened job is a screen of its own, and the controller states its
		// address here. The language links are built from SelfURL, so a fixture
		// that left it at the policy address would not represent this screen.
		page.SelfURL = "/repositories/r1/configured-checks?job=job0"
		page.Detail = &CheckJobDetail{
			Job: job,
			Checks: []CheckDefinitionLine{
				{Name: "build", Command: "go build ./..."},
				{Name: "unit", Command: "go test ./..."},
			},
			Attempt: &AttemptRecord{
				ID: "a9", ShortID: "a9abc12", Status: CheckFailed,
				RevisionOID: "55d0e21aa", RevisionShortOID: "55d0e21", WorktreeState: WorktreeClean,
				// This screen shows automatic jobs, so the attempt is the
				// server's own run for a job it admitted. It must not borrow the
				// manual helper's wording about the operator's own environment
				// and credential. It is still not a sandbox.
				Protection:           ProtectionAutomaticHost,
				CredentialProvenance: ProvenanceAutomaticJob,
				ConfigurationVersion: 6,
				LogStatus:            LogAvailable,
				LogExpiresAt:         testNow.AddDate(0, 0, 6),
				OutputTruncated:      true,
				StartedAt:            testNow.AddDate(0, 0, -1),
				FinishedAt:           testNow.AddDate(0, 0, -1).Add(2 * time.Minute),
				DurationMS:           120000, Summary: "2 of 3 checks failed",
				TimeoutMS: 600000, OutputLimitBytes: 262144,
			},
			Log: CheckJobLogView{
				Status: LogAvailable, Content: "unit: FAIL\n", Truncated: true, DisplayTruncated: true,
				ExpiresAt: testNow.AddDate(0, 0, 6),
			},
			SubmitURL: "/repositories/r1/configured-checks?job=job0",
			BackURL:   "/repositories/r1/configured-checks",
		}
	case ccFixtureJobUnreadable:
		detail := configuredChecksPage(c, ccFixtureJobDetail).Detail
		detail.Attempt, detail.AttemptUnreadable = nil, true
		detail.Checks, detail.ChecksUnreadable = nil, true
		detail.Log = CheckJobLogView{}
		page.Jobs = nil
		page.Detail = detail
	}
	return page
}

func runnerPage(c Chrome, issued bool) RunnerCredentialsPage {
	page := RunnerCredentialsPage{
		Chrome: c, Repo: evidenceRepo(), Tabs: evidenceTabs(RepoTabChecks),
		SelfURL:             "/repositories/r1/runner-tokens",
		SubmitURL:           "/repositories/r1/runner-tokens",
		ConfiguredChecksURL: "/repositories/r1/configured-checks",
		CreationID:          "creation-fixture-id",
		Credentials: []RunnerCredentialRow{
			{ID: "rc1", ShortID: "rc1abcd", Label: "build-host", Generation: 3,
				CreatedAt: testNow.AddDate(0, 0, -9), LastUsedAt: testNow.Add(-40 * time.Minute)},
			// Issued and never used, which is how an unused token is spotted.
			{ID: "rc2", ShortID: "rc2abcd", Label: "spare-runner", Generation: 3,
				CreatedAt: testNow.AddDate(0, 0, -2)},
			// Revoked tokens stay listed as a record of what had access.
			{ID: "rc0", ShortID: "rc0abcd", Label: "old-runner", Generation: 2,
				CreatedAt: testNow.AddDate(0, 0, -60), LastUsedAt: testNow.AddDate(0, 0, -30),
				RevokedAt: testNow.AddDate(0, 0, -20), Revoked: true},
		},
		Commands: []string{
			"owngit runner \\",
			"  --server http://owngit.local:8080 \\",
			"  --repository r1 \\",
			"  --token-file ./runner-token",
		},
	}
	if issued {
		page.Issued = RunnerCredentialRow{ID: "rc3", ShortID: "rc3abcd", Label: "new-runner",
			Generation: 3, CreatedAt: testNow}
		page.IssuedToken = runnerTestToken
		page.Credentials = append(page.Credentials, page.Issued)
	}
	return page
}

// ---------------------------------------------------------------------------
// the three states are not one state
// ---------------------------------------------------------------------------

func TestEveryNumericFieldShowsItsAcceptedRange(t *testing.T) {
	// The refusal message says a value is outside "the accepted range shown
	// with this field". That sentence is only true if the range is actually
	// there, on the ordinary view as well as after a refusal.
	r := newRenderer(t)
	ranges := ccFixtureRanges()
	for _, fixture := range []ccFixture{ccFixtureEnabled, ccFixtureNoPolicy} {
		for _, lang := range Langs() {
			out := render(t, r, configuredChecksPage(fullChrome(lang), fixture))
			for field, bounds := range ranges {
				want := string(fieldRange(lang, ranges, field))
				if want == "" {
					t.Fatalf("%s: the fixture publishes no range for %s", lang, field)
				}
				if !strings.Contains(out, want) {
					t.Errorf("fixture %d/%s: %s does not show its accepted range: %q",
						fixture, lang, field, want)
				}
				// Whatever the floor looks like, the maximum is always stated.
				if !strings.Contains(out, strconv.FormatInt(bounds.Max, 10)) {
					t.Errorf("fixture %d/%s: %s does not state its maximum %d",
						fixture, lang, field, bounds.Max)
				}
			}
		}
	}
}

func TestARefusedFieldStillShowsTheRangeItsMessageRefersTo(t *testing.T) {
	// A reader who has just been told the value is out of range needs the
	// range on the same screen, not only on the screen they came from.
	r := newRenderer(t)
	ranges := ccFixtureRanges()
	for _, lang := range Langs() {
		page := configuredChecksPage(fullChrome(lang), ccFixtureEnabled)
		page.Form.MaxTimeoutMS = "5"
		page.Chrome.Notices = []Notice{Error("max_timeout_ms", MsgCCFieldRange)}
		page.PendingAction = ActionSaveCheckPolicy
		out := render(t, r, page)
		if !strings.Contains(out, wantText(lang, MsgCCFieldRange)) {
			t.Errorf("%s: the refusal is not shown", lang)
		}
		if !strings.Contains(out, string(fieldRange(lang, ranges, "max_timeout_ms"))) {
			t.Errorf("%s: the refusal refers to a range the screen does not show", lang)
		}
		if !strings.Contains(out, `value="5"`) {
			t.Errorf("%s: the refused value was discarded", lang)
		}
	}
}

func TestEnableFormQuotesTheWholeIdentityItDrew(t *testing.T) {
	// The store compares version and digest together inside the transaction
	// that records the consent, so the form has to carry both. An abbreviation
	// is not a usable identity here: two generations can share a prefix, and
	// the backend compares the full value.
	r := newRenderer(t)
	for _, lang := range Langs() {
		page := configuredChecksPage(fullChrome(lang), ccFixtureEnabled)
		page.Policy.ConsentActive = false // draw the enable form
		out := render(t, r, page)
		if !strings.Contains(out, `name="policy_version" value="4"`) {
			t.Errorf("%s: the approval does not quote the generation it drew", lang)
		}
		if !strings.Contains(out, `name="policy_digest" value="`+page.Policy.Digest+`"`) {
			t.Errorf("%s: the approval does not quote the full policy digest", lang)
		}
		if strings.Contains(out, `name="policy_digest" value="`+page.Policy.ShortDigest+`"`) {
			t.Errorf("%s: the approval quotes an abbreviation the backend cannot verify", lang)
		}
	}
}

// ---------------------------------------------------------------------------
// jobs report what was recorded
// ---------------------------------------------------------------------------

func TestJobLogIsRenderedAsTextNotMarkup(t *testing.T) {
	r := newRenderer(t)
	page := configuredChecksPage(fullChrome(LangEN), ccFixtureJobDetail)
	page.Detail.Log.Content = `<img src=x onerror="alert(1)">`
	out := render(t, r, page)
	if strings.Contains(out, `<img src=x`) {
		t.Error("recorded log bytes reached the page as markup")
	}
	if !strings.Contains(out, "&lt;img src=x") {
		t.Error("the recorded log is not shown at all")
	}
}

// ---------------------------------------------------------------------------
// authority is asked for every time
// ---------------------------------------------------------------------------

func TestEveryConfiguredCheckMutationCollectsTheAdminPassword(t *testing.T) {
	// Holding a signed-in session is not consent to grant execution authority
	// or mint a token, so each form asks again.
	r := newRenderer(t)
	forms := regexp.MustCompile(`(?s)<form[^>]*method="post".*?</form>`)
	for name, page := range map[string]Page{
		"configured-checks":  configuredChecksPage(fullChrome(LangEN), ccFixtureEnabled),
		"job detail":         configuredChecksPage(fullChrome(LangEN), ccFixtureJobDetail),
		"runner-credentials": runnerPage(fullChrome(LangEN), false),
	} {
		out := render(t, r, page)
		found := forms.FindAllString(out, -1)
		if len(found) == 0 {
			t.Fatalf("%s: no mutating form rendered", name)
		}
		for _, form := range found {
			if !strings.Contains(form, `name="admin_password"`) {
				t.Errorf("%s: a mutating form does not collect the administrator password", name)
			}
			if !strings.Contains(form, `name="csrf"`) {
				t.Errorf("%s: a mutating form carries no CSRF token", name)
			}
		}
	}
}

func TestRefusedPolicyKeepsWhatWasTypedAndNeverThePassword(t *testing.T) {
	r := newRenderer(t)
	page := configuredChecksPage(fullChrome(LangEN), ccFixtureEnabled)
	page.PendingAction = ActionSaveCheckPolicy
	page.Form.MaxTimeoutMS = "99999999999"
	page.Form.ContainerImage = "registry.local/rejected-image"
	page.Chrome.Notices = []Notice{Error("max_timeout_ms", MsgCCNumberInvalid)}
	out := render(t, r, page)

	if !strings.Contains(out, `value="99999999999"`) {
		t.Error("a refused value was replaced instead of kept")
	}
	if !strings.Contains(out, `value="registry.local/rejected-image"`) {
		t.Error("an unrelated field lost what was typed")
	}
	wantID := noteID(ActionSaveCheckPolicy, "max_timeout_ms")
	if !strings.Contains(out, `aria-describedby="`+wantID+`"`) {
		t.Error("the refused field does not point at its message")
	}
	// Every password input renders without a value attribute, so nothing the
	// owner typed into one comes back in the markup.
	for _, chunk := range strings.Split(out, "<input") {
		if !strings.Contains(chunk, `type="password"`) {
			continue
		}
		field := chunk
		if end := strings.Index(field, ">"); end >= 0 {
			field = field[:end]
		}
		if strings.Contains(field, "value=") {
			t.Errorf("a password input carries a value attribute: <input%s>", field)
		}
	}
}

// ---------------------------------------------------------------------------
// runner tokens
// ---------------------------------------------------------------------------

func TestRunnerTokenAppearsOnceAndNeverInAURL(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, runnerPage(fullChrome(LangEN), true))
	if n := strings.Count(out, runnerTestToken); n != 1 {
		t.Fatalf("the token appears %d times, want exactly one", n)
	}
	links := regexp.MustCompile(`(?:href|src|action)="([^"]*)"`)
	for _, match := range links.FindAllStringSubmatch(out, -1) {
		if strings.Contains(match[1], runnerTestToken) {
			t.Errorf("the token reached a URL: %s", match[1])
		}
	}
	// A value attribute would put the token in the DOM as form data that a
	// later submission could resend.
	if strings.Contains(out, `value="`+runnerTestToken+`"`) {
		t.Error("the token is rendered as a form value")
	}
	if !strings.Contains(out, wantText(LangEN, MsgRTTokenOnce)) {
		t.Error("the one-time nature of the token is not stated")
	}
}

func TestRunnerCommandsCarryNoSecret(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, runnerPage(fullChrome(LangEN), true))
	commands := regexp.MustCompile(`(?s)<pre class="clone__cmds">(.*?)</pre>`).FindStringSubmatch(out)
	if len(commands) != 2 {
		t.Fatal("the connect commands are not rendered")
	}
	for _, secret := range []string{runnerTestToken, "--password ", "--token ", "Authorization:"} {
		if strings.Contains(commands[1], secret) {
			t.Errorf("the example commands contain %q", secret)
		}
	}
	if !strings.Contains(commands[1], "--token-file") {
		t.Error("the example does not read the token from a file")
	}
}

// ---------------------------------------------------------------------------
// wording the backend has to be able to stand behind
// ---------------------------------------------------------------------------

func TestConfiguredCheckScreensClaimNoSandbox(t *testing.T) {
	// OwnGit runs host commands with the service account's access, applies the
	// limits the local container runtime enforces, and takes a runner's own
	// word for its protection. None of that is a sandbox.
	r := newRenderer(t)
	var all strings.Builder
	for _, lang := range Langs() {
		c := fullChrome(lang)
		for _, fixture := range []ccFixture{ccFixtureEnabled, ccFixtureNoPolicy, ccFixtureRuntimeDown, ccFixtureJobDetail} {
			all.WriteString(render(t, r, configuredChecksPage(c, fixture)))
		}
		all.WriteString(render(t, r, runnerPage(c, true)))
	}
	out := strings.ToLower(all.String())
	// Denying a sandbox is the point, so the word may appear only in a denial.
	// These are the affirmative forms, which nothing here may say.
	for _, claim := range []string{
		"is a sandbox", "in a sandbox", "sandboxed", "샌드박스에서", "샌드박스로",
		"fully isolated", "완전히 격리", "guaranteed safe", "안전이 보장", "cannot escape",
	} {
		if strings.Contains(out, claim) {
			t.Errorf("the screens claim %q", claim)
		}
	}
	// The host mode says plainly that it is not one. The comparison uses the
	// escaped rendering, because this sentence contains an apostrophe and the
	// raw catalog string therefore never appears verbatim in the markup.
	if !strings.Contains(out, strings.ToLower(wantText(LangEN, MsgCCExecHostHelp))) {
		t.Error("host mode does not state the access it grants")
	}
}

func TestAFinishedJobIsNotOfferedAStopControl(t *testing.T) {
	// A finished job has no work left to stop. Drawing the control beside it
	// invites the reader to believe the result can still be prevented, and
	// pressing it changes nothing.
	r := newRenderer(t)
	page := configuredChecksPage(fullChrome(LangEN), ccFixtureJobDetail)
	if page.Detail.Job.FinishedAt.IsZero() {
		t.Fatal("the job detail fixture is no longer a finished job")
	}
	if page.Detail.Job.Cancellable {
		t.Error("the finished job in the fixture is marked cancellable")
	}
	out := render(t, r, page)
	if strings.Contains(out, `value="`+ActionCancelCheckJob+`"`) {
		t.Error("a finished job still offers to cancel work that already ended")
	}
	// Losing the stop must not cost the reader the action that does apply.
	if !strings.Contains(out, `value="`+ActionRerunCheckJob+`"`) {
		t.Error("a finished job no longer offers a rerun")
	}

	// Work that has not finished still offers the stop, so the rule did not
	// simply remove the control everywhere.
	running := page
	job := ccJobRows()[0]
	if !job.FinishedAt.IsZero() {
		t.Fatal("the running fixture row is no longer unfinished")
	}
	detail := *page.Detail
	detail.Job = job
	running.Detail = &detail
	if live := render(t, r, running); !strings.Contains(live, `value="`+ActionCancelCheckJob+`"`) {
		t.Error("running work offers no way to stop it")
	}
}

func TestAnOpenedJobKeepsItsAddressWhenTheLanguageChanges(t *testing.T) {
	// Regression. The language links are built from the page's own GET
	// address, and the configured-check controller used to state the policy
	// address for every render. Switching language on an opened job therefore
	// pointed at /configured-checks?lang=ko: the script rewrote the address
	// bar from that href, and the next reload returned the reader to the
	// policy screen with the job they were reading gone.
	r := newRenderer(t)
	const jobURL = "/repositories/r1/configured-checks?job=job0"

	for _, from := range Langs() {
		c := fullChrome(from)
		// The request URL already carries the job. The bug was that SelfURL
		// took precedence over it and did not.
		c.CurrentURL = jobURL
		page := configuredChecksPage(c, ccFixtureJobDetail)
		if page.SelfURL != jobURL {
			t.Fatalf("the opened-job fixture states %q as its own address", page.SelfURL)
		}
		if page.SubmitURL != "/repositories/r1/configured-checks" {
			t.Fatalf("the opened-job fixture replaced the POST route with %q", page.SubmitURL)
		}
		out := render(t, r, page)

		for _, to := range Langs() {
			want := attrEscape(withLang(jobURL, to))
			if !strings.Contains(out, `href="`+want+`"`) {
				t.Errorf("from %s the %s link is not %q", from, to, want)
			}
			// The policy address with only a language is the defect: it
			// silently drops the job on the next ordinary navigation.
			if losing := attrEscape(withLang("/repositories/r1/configured-checks", to)); strings.Contains(out, `href="`+losing+`"`) {
				t.Errorf("from %s the %s link drops the opened job: %q", from, to, losing)
			}
		}
		if strings.Contains(out, `name="action" value="`+ActionSaveCheckPolicy+`"`) {
			t.Errorf("from %s the opened job drew the policy editor", from)
		}
		if !strings.Contains(out, `action="`+attrEscape(jobURL)+`"`) {
			t.Errorf("from %s the job actions lost the job they act on", from)
		}
	}
}

func TestTheLanguageClickKeepsTheOpenedJobInTheAddressBar(t *testing.T) {
	// The anchor is only half of it. The shipped script intercepts the click
	// and rewrites the address itself, and that address is what a reload or a
	// shared link uses. This drives the real script.
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not available to run the shipped language-click script")
	}
	r := newRenderer(t)
	const jobURL = "/repositories/r1/configured-checks?job=job0"
	c := fullChrome(LangEN)
	c.CurrentURL = jobURL
	out := render(t, r, configuredChecksPage(c, ccFixtureJobDetail))

	address, lang := clickLanguage(t, out, c.CurrentURL)
	if lang != string(LangKO) {
		t.Errorf("the click did not switch the language: %q", lang)
	}
	if !strings.Contains(address, "job=job0") {
		t.Errorf("switching language dropped the opened job: %q", address)
	}
	if !strings.Contains(address, "lang=ko") {
		t.Errorf("the address bar does not carry the chosen language: %q", address)
	}
	if !strings.HasPrefix(address, "/repositories/r1/configured-checks?") {
		t.Errorf("the address bar left the configured-check screen: %q", address)
	}
}

func TestSavingAChangedPolicyDoesNotClaimExecutionWasAlreadyOff(t *testing.T) {
	// Saving a changed policy revokes an active consent. Reporting that as
	// "execution is still off" tells the operator whose checks were running
	// that nothing changed, which is the opposite of what just happened.
	for _, lang := range Langs() {
		saved := Text(lang, MsgCCSaved)
		if saved == "" {
			t.Fatalf("%s: the save result has no wording", lang)
		}
		for _, claim := range []string{"still", "아직"} {
			if strings.Contains(saved, claim) {
				t.Errorf("%s: the save result claims the previous state with %q: %q", lang, claim, saved)
			}
		}
		// It still has to say where execution now stands, because that is the
		// consequence the operator needs.
		stated := false
		for _, state := range []string{"off", "꺼져"} {
			if strings.Contains(saved, state) {
				stated = true
			}
		}
		if !stated {
			t.Errorf("%s: the save result no longer says execution is off: %q", lang, saved)
		}
		// The unchanged-resubmission result is a different outcome and keeps
		// its own sentence, so one cannot be mistaken for the other.
		if unchanged := Text(lang, MsgCCSavedEnabled); unchanged == saved {
			t.Errorf("%s: the changed and unchanged save results share one sentence", lang)
		}
	}
}

// cc is a configured-check fixture in lang after edit.
func cc(lang Lang, fixture ccFixture, edit func(*ConfiguredChecksPage)) ConfiguredChecksPage {
	return with(configuredChecksPage(fullChrome(lang), fixture), edit)
}

// checkResultWords are the job result words a non-result state must not borrow.
var checkResultWords = []MessageCode{MsgCCJobStateFailed, MsgCCJobStateError, MsgCCJobStatePassed}

// TestConfiguredCheckScreenStates protects the wording, not the layout: a
// stored policy must not read as running, a closed runtime must not read as a
// failed check, and a record that could not be read is neither a result nor
// an absence.
func TestConfiguredCheckScreenStates(t *testing.T) {
	screens := []screen{
		// Saving a policy is not turning execution on, and neither is a
		// working runtime; with consent off only enabling is offered.
		screen{name: "policy, consent and runtime are separate statements",
			page:     cc(LangEN, ccFixtureEnabled, func(p *ConfiguredChecksPage) { p.Policy.ConsentActive = false }),
			want:     []MessageCode{MsgCCStatePolicy, MsgCCStateConsent, MsgCCStateRuntime, MsgCCConsentOff, MsgCCPolicySaved},
			markup:   []string{`value="` + ActionEnableChecks + `"`},
			noMarkup: []string{`value="` + ActionDisableChecks + `"`}},
		screen{name: "an unrecognised runtime code is shown as recorded",
			page: cc(LangEN, ccFixtureRuntimeDown, func(p *ConfiguredChecksPage) {
				p.Runtime = CheckRuntimeView{Code: "some_new_backend_condition"}
			}),
			want: []MessageCode{MsgCCRuntimeOther}, markup: []string{"some_new_backend_condition"}},
		// The policy selects the container mode now; a job admitted earlier
		// ran on the host and keeps saying so.
		screen{name: "a job keeps the mode it was admitted with", page: cc(LangEN, ccFixtureJobDetail, unchanged),
			want: []MessageCode{MsgCCExecHost}, markup: []string{"55d0e21"}},
		screen{name: "unfinished jobs are not results", page: cc(LangEN, ccFixtureEnabled, unchanged),
			want: []MessageCode{MsgCCJobStateStarted, MsgCCJobStateClaimed}, absent: []MessageCode{MsgCCJobStatePassed}},
		screen{name: "only backend-offered job actions are drawn",
			page: cc(LangEN, ccFixtureJobDetail, func(p *ConfiguredChecksPage) {
				p.Detail.Job.Cancellable, p.Detail.Job.Rerunnable = false, false
			}),
			noMarkup: []string{`value="` + ActionCancelCheckJob + `"`, `value="` + ActionRerunCheckJob + `"`}},
		screen{name: "the job log says when it was cut and when it expires", page: cc(LangEN, ccFixtureJobDetail, unchanged),
			want: []MessageCode{MsgCheckOutputCut, MsgCCJobLogCut, MsgCheckLogExpiresAt}},
		// Every row submits the same action, so without the credential
		// identity a refused password would mark all of them.
		screen{name: "a refused revocation stays on its own row",
			page: with(runnerPage(fullChrome(LangEN), false), func(p *RunnerCredentialsPage) {
				p.PendingAction, p.PendingCredentialID = ActionRevokeRunnerToken, "rc1"
				p.Chrome.Notices = []Notice{Error("admin_password", MsgAdminFailed)}
			}),
			markup:   []string{`aria-describedby="` + noteID("rc1", "admin_password") + `"`},
			noMarkup: []string{`aria-describedby="` + noteID("rc2", "admin_password") + `"`}},
		screen{name: "returning to runner tokens shows no token", page: runnerPage(fullChrome(LangEN), false),
			absent: []MessageCode{MsgRTTokenOnce}, noMarkup: []string{runnerTestToken}},
	}
	registered := map[string]struct {
		apply func(*CheckJobDetail)
		want  MessageCode
	}{
		"the record could not be read":   {func(d *CheckJobDetail) { d.Attempt, d.AttemptUnreadable = nil, true }, MsgCCAttemptUnread},
		"the record is no longer stored": {func(d *CheckJobDetail) { d.Attempt, d.AttemptMissing = nil, true }, MsgCCAttemptGone},
	}
	for _, lang := range Langs() {
		l := string(lang) + " "
		screens = append(screens,
			// Without a stored policy there is nothing consent could bind to.
			screen{name: l + "saving the policy does not claim execution started", lang: lang,
				page: configuredChecksPage(with(fullChrome(lang), func(c *Chrome) { c.Notices = []Notice{Success(MsgCCSaved)} }), ccFixtureNoPolicy),
				want: []MessageCode{MsgCCSaved, MsgCCEnableBlocked}, absent: []MessageCode{MsgCCConsentOn}},
			// Ordinary Git work is unaffected, the bounded code is shown, and
			// the fixture records no job, so a result word could only come
			// from the runtime block describing itself.
			screen{name: l + "a closed runtime is not a failed check and names the repair", lang: lang,
				page:   cc(lang, ccFixtureRuntimeDown, unchanged),
				want:   []MessageCode{MsgCCRuntimeWork, MsgCCRuntimeRepair, MsgCCRuntimeSep},
				absent: checkResultWords, markup: []string{RuntimeWorkspaceUnavailable}},
			screen{name: l + "a cancellation is a request, not a stop", lang: lang, page: cc(lang, ccFixtureEnabled, unchanged),
				want: []MessageCode{MsgCCJobCancelAsk}, absent: []MessageCode{MsgCCJobStateCancelled}},
			// "None" would claim this repository never ran anything.
			screen{name: l + "unreadable job records are not an empty list", lang: lang,
				page: cc(lang, ccFixtureEnabled, func(p *ConfiguredChecksPage) { p.Jobs, p.JobsUnavailable = nil, true }),
				want: []MessageCode{MsgCCJobsUnavailable}, absent: []MessageCode{MsgCCJobsNone}},
			screen{name: l + "a missing job is said rather than shown empty", lang: lang,
				page: cc(lang, ccFixtureJobDetail, func(p *ConfiguredChecksPage) {
					p.Detail = &CheckJobDetail{NotFound: true, BackURL: "/repositories/r1/configured-checks"}
				}),
				want: []MessageCode{MsgCCJobNotFound}},
			// "Does not exist" and "could not be read" send a reader in
			// opposite directions, and neither is a check result.
			screen{name: l + "an unreadable job is not a missing one", lang: lang,
				page: cc(lang, ccFixtureJobDetail, func(p *ConfiguredChecksPage) {
					p.Detail = &CheckJobDetail{Unreadable: true, BackURL: "/repositories/r1/configured-checks"}
				}),
				want: []MessageCode{MsgCCJobUnreadable}, absent: append([]MessageCode{MsgCCJobNotFound}, checkResultWords...)},
			screen{name: l + "unreadable commands are reported", lang: lang,
				page: cc(lang, ccFixtureJobDetail, func(p *ConfiguredChecksPage) { p.Detail.Checks, p.Detail.ChecksUnreadable = nil, true }),
				want: []MessageCode{MsgCCChecksUnread}},
			screen{name: l + "a discarded configuration is not unreadable", lang: lang,
				page: cc(lang, ccFixtureJobDetail, func(p *ConfiguredChecksPage) { p.Detail.Checks, p.Detail.ChecksMissing = nil, true }),
				want: []MessageCode{MsgCCChecksGone}, absent: []MessageCode{MsgCCChecksUnread}},
			// An abbreviation cannot be pasted into a Git command.
			screen{name: l + "the full commit is available", lang: lang,
				page: cc(lang, ccFixtureJobDetail, func(p *ConfiguredChecksPage) {
					p.Detail.Job.SourceOID, p.Detail.Job.SourceShortOID = "7f2c1a0bb4d3e9c81f06a25d7b9e34cc5a1d80fe", "7f2c1a0"
					p.Detail.Job.BaseOID, p.Detail.Job.BaseShortOID = "1ab4c90ff0e7d2a53b8c46190af7d25e3c0b9187", "1ab4c90"
				}),
				want:   []MessageCode{MsgCCJobFullOID},
				markup: []string{"7f2c1a0bb4d3e9c81f06a25d7b9e34cc5a1d80fe", "1ab4c90ff0e7d2a53b8c46190af7d25e3c0b9187"}},
			// Three rows, and only the two active ones offer revocation.
			screen{name: l + "revoked runner tokens stay listed without a control", lang: lang,
				page: runnerPage(fullChrome(lang), false),
				want: []MessageCode{MsgRTRevoked, MsgRTNeverUsed, MsgRTNotPassword, MsgRTScope}, markup: []string{"old-runner"},
				extra: countIs(`value="`+ActionRevokeRunnerToken+`"`, 2)},
			screen{name: l + "the policy screen states the advisory contract and the manual path", lang: lang,
				page: cc(lang, ccFixtureEnabled, unchanged), want: []MessageCode{MsgCCAdvisory, MsgCCManual}},
			screen{name: l + "host mode states the access it grants and no fallback", lang: lang,
				page: cc(lang, ccFixtureNoPolicy, unchanged), want: []MessageCode{MsgCCExecHostHelp, MsgCCNoFallback}},
		)
		// A job that names an attempt has a run registered; whether its record
		// can be read is a separate question, and without a readable run there
		// is no log to report.
		for name, test := range registered {
			page := cc(lang, ccFixtureJobDetail, func(p *ConfiguredChecksPage) { test.apply(p.Detail) })
			if !page.Detail.AttemptRegistered() {
				t.Fatalf("%s: the fixture does not describe a registered run", name)
			}
			screens = append(screens, screen{name: l + "a registered run is never no run: " + name, lang: lang, page: page,
				want: []MessageCode{test.want}, absent: []MessageCode{MsgCCJobNoAttempt, MsgCCJobLog}})
		}
	}
	checkScreens(t, screens...)
}
