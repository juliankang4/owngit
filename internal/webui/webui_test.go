package webui

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 3, 12, 14, 32, 0, 0, time.UTC)

func newRenderer(t *testing.T) *Renderer {
	t.Helper()
	r, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func render(t *testing.T, r *Renderer, page Page) string {
	t.Helper()
	var buf bytes.Buffer
	if err := r.Render(&buf, page); err != nil {
		t.Fatalf("Render %T: %v", page, err)
	}
	return buf.String()
}

// wantText is the catalog text as it appears in the rendered HTML, so a
// sentence containing an apostrophe or a quote still matches.
func wantText(lang Lang, code MessageCode) string {
	return template.HTMLEscapeString(Text(lang, code))
}

// fullChrome is a signed-in viewer with a repository in the sidebar.
func fullChrome(lang Lang) Chrome {
	return Chrome{
		Lang:       lang,
		Now:        testNow,
		CurrentURL: "/repositories/r1/code?ref=main&path=internal",
		CSRF:       "csrf-token-value",
		Viewer: Viewer{
			AccessMode:      AccessOpen,
			GeneralUnlocked: true,
			SetupComplete:   true,
		},
		// A running version the backend supplied. The renderer has no version
		// of its own, so this is the only place one can come from.
		Version:    "9.9.9-test",
		Connection: Connection{Encrypted: false, Host: "owngit.local:8080"},
		Nav: Nav{
			Section:      SectionOverview,
			Total:        2,
			OverviewURL:  "/",
			ActivityURL:  "/activity",
			SettingsURL:  "/settings",
			NewRepoURL:   "/repositories/new",
			ActiveRepoID: "r1",
			Repositories: []NavRepository{
				{ID: "r1", Name: "forge-cli", URL: "/repositories/r1", CommitCount: 12, CountKnown: true},
				{ID: "r2", Name: "cedar-config", URL: "/repositories/r2"},
			},
		},
	}
}

func sampleGraph() ActivityGraph {
	days := make([]ActivityDay, 0, 365)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for d := start; d.Year() == 2026; d = d.AddDate(0, 0, 1) {
		count := 0
		switch d.YearDay() % 7 {
		case 1:
			count = 3
		case 3:
			count = 11
		}
		days = append(days, ActivityDay{
			Date:   d,
			Count:  count,
			Future: d.After(testNow),
		})
	}
	return ActivityGraph{
		Year:            2026,
		Years:           []ActivityYear{{Year: 2026, URL: "/activity?year=2026"}, {Year: 2025, URL: "/activity?year=2025"}},
		Days:            days,
		Total:           412,
		RepositoryCount: 2,
		Complete:        true,
		Available:       true,
	}
}

// ---------------------------------------------------------------------------
// every screen renders in both languages with a complete document
// ---------------------------------------------------------------------------

func allPages(lang Lang) map[string]Page {
	c := fullChrome(lang)
	bare := Chrome{Lang: lang, Now: testNow, CurrentURL: "/setup", CSRF: "csrf-token-value"}

	return map[string]Page{
		"setup-welcome": SetupPage{
			Chrome: bare, Stage: SetupWelcome, RedeemURL: "/setup/redeem",
			Prerequisites: []Prerequisite{
				{Name: "git", Satisfied: true, Code: MsgPrereqGitFound, Detail: "2.54.0"},
				{Name: "git-http-backend", Satisfied: false, Code: MsgPrereqHTTPMiss},
			},
		},
		"setup-wizard": SetupPage{
			Chrome: bare, Stage: SetupWizard, SubmitURL: "/setup",
			Form: SetupForm{StoragePath: "/srv/git", SuggestedPath: "/srv/git", AccessMode: AccessPassword},
		},
		"setup-unavailable": SetupPage{
			Chrome: bare, Stage: SetupUnavailable,
			Reason: MsgSetupLinkExpired, RecoveryHint: MsgSetupReissueHint,
		},
		"auth-general": AuthPage{Chrome: bare, Scope: AuthGeneral, SubmitURL: "/login", Next: "/"},
		"auth-admin":   AuthPage{Chrome: bare, Scope: AuthAdmin, SubmitURL: "/admin/login"},
		"settings": SettingsPage{
			Chrome: c, SubmitURL: "/settings", AccessMode: AccessPassword,
			Storage:   StorageInfo{Visible: true, Label: "Home server", Path: "/volume1/git"},
			CloneHint: "http://owngit.local:8080/git/",
		},
		"overview": OverviewPage{
			Chrome: c, Activity: sampleGraph(), TotalCount: 2,
			Repositories: []RepositorySummary{
				{ID: "r1", Name: "forge-cli", URL: "/repositories/r1", DefaultBranch: "main",
					Head: CommitSummary{ShortOID: "a41c9e2", Subject: "Add JSON output", AuthorDate: testNow}},
				{ID: "r2", Name: "cedar-config", URL: "/repositories/r2", Empty: true},
			},
			Recent: []ActivityEntry{{
				RepositoryID: "r1", RepositoryName: "forge-cli", Ref: "main",
				Commit: CommitSummary{ShortOID: "a41c9e2", Subject: "Add JSON output", AuthorDate: testNow, URL: "/repositories/r1/commits/a41c9e2"},
			}},
			RecentMoreURL: "/activity",
		},
		"overview-empty": OverviewPage{Chrome: c, Activity: sampleGraph()},
		"activity": ActivityPage{
			Chrome: c, Activity: sampleGraph(),
			Days: []ActivityDayGroup{{Date: testNow, Entries: []ActivityEntry{{
				RepositoryName: "forge-cli", Ref: "fix/cursor", RefRetained: true,
				Commit: CommitSummary{ShortOID: "77b30d5", Subject: "Cap retry delays", AuthorDate: testNow, URL: "/x"},
			}}}},
		},
		"repo-overview":     repoPage(c, RepoTabOverview),
		"repo-code":         repoPage(c, RepoTabCode),
		"repo-commits":      repoPage(c, RepoTabCommits),
		"new-repository":    NewRepositoryPage{Chrome: c, SubmitURL: "/repositories"},
		"restore-choose":    restorePage(c, false),
		"restore-previewed": restorePage(c, true),
		// The evidence screens appear in every state that has to render
		// honestly, because the sweeps below check all of them: a list with
		// records and one whose records could not be read, a create screen
		// before and after the branch tips were observed, a pull request with
		// failing evidence and one refused by Git, a task list and one task's
		// runs, and a credential screen with and without a one-time secret.
		"pull-requests":          pullRequestsPage(c, false),
		"pull-requests-empty":    PullRequestsPage{Chrome: c, Repo: evidenceRepo(), Tabs: evidenceTabs(RepoTabPullRequests), NewURL: "/repositories/r1/pull-requests/new"},
		"pull-requests-unavail":  pullRequestsPage(c, true),
		"new-pull-request":       newPullRequestPage(c, false),
		"new-pull-request-cmp":   newPullRequestPage(c, true),
		"pull-request":           pullRequestPage(c, prFixtureFailing),
		"pull-request-blocked":   pullRequestPage(c, prFixtureBlocked),
		"pull-request-merged":    pullRequestPage(c, prFixtureMerged),
		"pull-request-unknown":   pullRequestPage(c, prFixtureUnknown),
		"tasks":                  tasksPage(c, false),
		"tasks-detail":           tasksPage(c, true),
		"pull-request-pending":   pullRequestPage(c, prFixturePending),
		"tasks-empty":            TasksPage{Chrome: c, Repo: evidenceRepo(), Tabs: evidenceTabs(RepoTabChecks), ListURL: "/repositories/r1/tasks", HelperURL: "/repositories/r1/helper-credentials"},
		"tasks-unclean":          uncleanTasksPage(c),
		"pull-request-unclean":   uncleanPullRequestPage(c),
		"helper-credentials":     helperPage(c, false),
		"helper-credentials-new": helperPage(c, true),

		// The configured-check screens appear in the states that have to
		// render honestly: a repository with no policy at all, one with a
		// saved and enabled policy plus recorded jobs, one opened job, a
		// closed runtime, and the runner tokens with and without a one-time
		// token.
		"configured-checks":      configuredChecksPage(c, ccFixtureEnabled),
		"configured-checks-none": configuredChecksPage(c, ccFixtureNoPolicy),
		"configured-checks-down": configuredChecksPage(c, ccFixtureRuntimeDown),
		"configured-checks-job":  configuredChecksPage(c, ccFixtureJobDetail),
		"configured-checks-bad":  configuredChecksPage(c, ccFixtureJobUnreadable),
		"runner-credentials":     runnerPage(c, false),
		"runner-credentials-new": runnerPage(c, true),
		"import": ImportPage{
			Chrome: c, Repo: evidenceRepo(), Tabs: evidenceTabs(RepoTabImport), SubmitURL: "/repositories/r1/import",
			SelfURL: "/repositories/r1/import", Available: true, Configured: true, URL: "https://example.invalid/team/project.git",
			Mode: "standalone", CredentialForm: "none", History: []ImportRunRow{{ID: "abc", Kind: "refresh", Status: "complete"}},
		},
		"new-import": NewImportPage{Chrome: c, SubmitURL: "/repositories/new-import", Name: "project"},

		"error": ErrorPage{Chrome: c, Status: 404, Code: MsgErrNotFound, Detail: "/nope", RetryURL: "/"},
	}
}

// ---------------------------------------------------------------------------
// evidence screen fixtures
// ---------------------------------------------------------------------------

func evidenceRepo() RepositoryHeader {
	return RepositoryHeader{ID: "r1", Name: "forge-cli", Description: "Command line tool",
		URL: "/repositories/r1", CloneURL: "http://owngit.local:8080/git/forge-cli.git"}
}

func evidenceTabs(active RepoTab) RepoTabs {
	return RepoTabs{
		OverviewURL:     "/repositories/r1",
		CodeURL:         "/repositories/r1/code",
		CommitsURL:      "/repositories/r1/commits",
		PullRequestsURL: "/repositories/r1/pull-requests",
		TasksURL:        "/repositories/r1/tasks",
		Active:          active,
	}
}

func sourceRevision() RevisionState {
	return RevisionState{Branch: "fix/cursor", OID: "7f2c1a0bb", ShortOID: "7f2c1a0", Status: RevisionCommit}
}

func targetRevision() RevisionState {
	return RevisionState{Branch: "main", OID: "a41c9e2ff", ShortOID: "a41c9e2", Status: RevisionCommit}
}

// failedChecks is a real failure on the current revision: the commit was
// tested, the tree was clean, and the commands did not pass.
func failedChecks() CheckEvidence {
	return CheckEvidence{
		Status: CheckFailed, Configured: true, Advisory: true,
		Summary:              "2 of 3 commands failed",
		RevisionOID:          "7f2c1a0bb",
		RevisionShortOID:     "7f2c1a0",
		WorktreeState:        WorktreeClean,
		TestedCommit:         true,
		FinishedAt:           testNow.Add(-40 * time.Minute),
		ConfigurationVersion: 4,
		LogStatus:            LogAvailable,
		LogExpiresAt:         testNow.AddDate(0, 0, 30),
		AttemptID:            "att_9f31c0d4e7a2",
		AttemptShortID:       "att_9f31c0d4",
		Protection:           ProtectionInherited,
		CredentialProvenance: ProvenanceAuthenticatedHelper,
		AttemptURL:           "/repositories/r1/tasks?task=t1",
	}
}

func diffFixture() []DiffFile {
	return []DiffFile{
		{Path: "internal/retry/backoff.go", Status: "modified", Additions: 4, Deletions: 2,
			Hunks: []DiffHunk{{Header: "@@ -12,7 +12,9 @@ func Backoff", Lines: []DiffLine{
				{Kind: "context", OldLine: 12, NewLine: 12, Text: "// Backoff caps the delay"},
				{Kind: "add", NewLine: 13, Text: "const maxDelay = 30 * time.Second"},
				{Kind: "del", OldLine: 13, Text: "const maxDelay = time.Hour"},
			}}}},
		{Path: "internal/retry/limits.go", Status: "added", Additions: 18},
	}
}

func pullRequestsPage(c Chrome, unavailable bool) PullRequestsPage {
	page := PullRequestsPage{
		Chrome: c, Repo: evidenceRepo(), Tabs: evidenceTabs(RepoTabPullRequests),
		NewURL: "/repositories/r1/pull-requests/new",
	}
	if unavailable {
		// Records that could not be read. The list must not render as empty,
		// which would claim this repository has no pull requests.
		page.Unavailable = true
		page.UnavailableReason = MsgPRFailed
		return page
	}
	page.Items = []PullRequestRow{
		{Number: 12, Title: "Cap retry delays", State: PullRequestOpen,
			URL: "/repositories/r1/pull-requests/12", UpdatedAt: testNow.Add(-2 * time.Hour),
			Source: sourceRevision(), Target: targetRevision(),
			Checks: failedChecks(),
			Review: ReviewEvidence{Status: ReviewPending, SourceOID: "7f2c1a0bb", BoundToCurrentRevision: true,
				Provenance: ReviewFromRequest, SubmittedAt: testNow.Add(-90 * time.Minute)}},
		// A pull request with no check and no review. Both absences have to
		// read as absences rather than as a clean result.
		{Number: 11, Title: "Use stable key in pagination cursor", State: PullRequestOpen,
			URL: "/repositories/r1/pull-requests/11", UpdatedAt: testNow.Add(-26 * time.Hour),
			Source: RevisionState{Branch: "feat/cursor", OID: "3b91e7c00", ShortOID: "3b91e7c", Status: RevisionCommit},
			Target: targetRevision(),
			Checks: CheckEvidence{Status: CheckAbsent, Configured: true, Advisory: true}},
		{Number: 9, Title: "Document the restore screen", State: PullRequestMerged,
			URL: "/repositories/r1/pull-requests/9", UpdatedAt: testNow.AddDate(0, 0, -5),
			Source: RevisionState{Branch: "docs/restore", Status: RevisionMissing},
			Target: targetRevision(),
			Checks: CheckEvidence{Status: CheckPassed, Configured: true, Advisory: true, TestedCommit: true,
				WorktreeState: WorktreeClean, RevisionShortOID: "5d0aa13", FinishedAt: testNow.AddDate(0, 0, -5),
				LogStatus: LogExpired},
			Review: ReviewEvidence{Status: ReviewApproved, BoundToCurrentRevision: true, ReviewerLabel: "codex",
				Provenance: ReviewFromExternalTool, SubmittedAt: testNow.AddDate(0, 0, -5)}},
	}
	return page
}

// newPullRequestPage is the create screen before and after the backend read
// both branch tips. The create form exists only in the second state, because
// there is nothing to create from until the tips are known.
func newPullRequestPage(c Chrome, observed bool) NewPullRequestPage {
	page := NewPullRequestPage{
		Chrome: c, Repo: evidenceRepo(), Tabs: evidenceTabs(RepoTabPullRequests),
		SelectURL: "/repositories/r1/pull-requests/new",
		SubmitURL: "/repositories/r1/pull-requests",
		CancelURL: "/repositories/r1/pull-requests",
		Branches: []RefOption{
			{Name: "main", IsDefault: true},
			{Name: "fix/cursor"},
			{Name: "feat/cursor"},
		},
		Source: RevisionState{Branch: "fix/cursor"},
		Target: RevisionState{Branch: "main"},
	}
	if !observed {
		return page
	}
	page.Observed = true
	page.Source = sourceRevision()
	page.Target = targetRevision()
	page.Title = "Cap retry delays"
	page.ReviewChoice = ReviewChoiceRequest
	page.Changes = diffFixture()
	return page
}

type prFixture int

const (
	// prFixtureFailing has a failed check and a review for an earlier
	// revision. Merging stays available: neither is a gate.
	prFixtureFailing prFixture = iota
	// prFixtureBlocked is refused by Git, which is the only thing that
	// disables the merge control.
	prFixtureBlocked
	prFixtureMerged
	// prFixtureUnknown carries statuses this package does not recognise, to
	// prove an unknown value never renders as a pass.
	prFixtureUnknown
	// prFixturePending is an attempt registered before it ran, which has a
	// record but no outcome.
	prFixturePending
)

func pullRequestPage(c Chrome, kind prFixture) PullRequestPage {
	page := PullRequestPage{
		Chrome: c, Repo: evidenceRepo(), Tabs: evidenceTabs(RepoTabPullRequests),
		Number: 12, Title: "Cap retry delays", State: PullRequestOpen,
		UpdatedAt: testNow.Add(-2 * time.Hour),
		Source:    sourceRevision(), Target: targetRevision(),
		Checks:           failedChecks(),
		Changes:          diffFixture(),
		SelfURL:          "/repositories/r1/pull-requests/12",
		ListURL:          "/repositories/r1/pull-requests",
		TasksURL:         "/repositories/r1/tasks",
		ReviewRequestURL: "/repositories/r1/pull-requests/12/review/request",
		ReviewSkipURL:    "/repositories/r1/pull-requests/12/review/skip",
		MergeURL:         "/repositories/r1/pull-requests/12/merge",
		Merge:            MergeAvailability{Eligible: true},
	}
	switch kind {
	case prFixtureFailing:
		// Approved, but for a commit that is no longer the tip. It must not
		// read as current approval.
		page.Review = ReviewEvidence{Status: ReviewApproved, SourceOID: "5d0aa1399", ShortSourceOID: "5d0aa13",
			ReviewerLabel: "codex", Provenance: ReviewFromExternalTool, SubmittedAt: testNow.AddDate(0, 0, -1)}
	case prFixtureBlocked:
		page.Merge = MergeAvailability{Blockers: []MergeBlocker{
			{Code: "merge_conflict", Detail: "internal/retry/backoff.go"},
			{Code: "some_new_backend_reason"},
		}}
		page.Review = ReviewEvidence{Status: ReviewUnavailable, BoundToCurrentRevision: true,
			Provenance: ReviewFromRequest, Detail: "provider returned 503", SubmittedAt: testNow.Add(-30 * time.Minute)}
	case prFixtureMerged:
		page.State = PullRequestMerged
		page.Merge = MergeAvailability{Blockers: []MergeBlocker{{Code: "already_merged"}}}
		page.Merged = &MergeRecord{Mode: "merge-commit", OID: "c07f4ab21", ShortOID: "c07f4ab",
			ReceiptRef: "refs/owngit/merges/12", MergedAt: testNow.Add(-10 * time.Minute)}
		page.Review = ReviewEvidence{Status: ReviewSkipped, BoundToCurrentRevision: true, Provenance: ReviewFromSkip,
			SubmittedAt: testNow.Add(-20 * time.Minute)}
	case prFixtureUnknown:
		page.Checks = CheckEvidence{Status: "quantum_superposition", Configured: true, Advisory: true,
			RevisionShortOID: "7f2c1a0", WorktreeState: "teleported", LogStatus: "shredded",
			Protection: "sandboxed", FinishedAt: testNow.Add(-5 * time.Minute)}
		page.Review = ReviewEvidence{Status: "vibes_ok", BoundToCurrentRevision: true, SubmittedAt: testNow}
	case prFixturePending:
		page.Checks = CheckEvidence{Status: CheckPending, Configured: true, Advisory: true,
			RevisionOID: "7f2c1a0bb", RevisionShortOID: "7f2c1a0", WorktreeState: WorktreeClean,
			RegisteredAt: testNow.Add(-2 * time.Minute), AttemptID: "att_pending01",
			AttemptShortID: "att_pending01", ConfigurationVersion: 4,
			Protection: ProtectionInherited, CredentialProvenance: ProvenanceAuthenticatedHelper}
		page.Review = ReviewEvidence{Status: ReviewPending, BoundToCurrentRevision: true,
			Provenance: ReviewFromRequest, SubmittedAt: testNow.Add(-2 * time.Minute)}
	}
	return page
}

func attemptFixture() AttemptRecord {
	return AttemptRecord{
		ID: "att_9f31c0d4e7a2", ShortID: "att_9f31c0d4", Status: CheckFailed,
		Summary:     "2 of 3 commands failed",
		RevisionOID: "7f2c1a0bb", RevisionShortOID: "7f2c1a0",
		WorktreeState: WorktreeClean, FinishedAt: testNow.Add(-40 * time.Minute),
		DurationMS: 94210, ConfigurationVersion: 4,
		LogStatus: LogAvailable, LogExpiresAt: testNow.AddDate(0, 0, 30),
		Protection: ProtectionInherited, CredentialProvenance: ProvenanceAuthenticatedHelper,
		Sequence: 3, CycleID: "cyc_2", TimeoutMS: 600000, OutputLimitBytes: 2 << 20,
		Results: []CheckResultLine{
			{Name: "build", Command: "go build ./...", Status: CheckPassed, ExitCode: 0, HasExitCode: true, DurationMS: 8120},
			{Name: "test", Command: "go test ./...", Status: CheckFailed, ExitCode: 1, HasExitCode: true,
				DurationMS: 81400, OutputExcerpt: "--- FAIL: TestBackoffCap\n    backoff_test.go:41: want 30s, got 1h", Truncated: true},
			// A command whose environment was missing. It is not a failure and
			// not a pass, and it kept no output.
			{Name: "lint", Command: "golangci-lint run", Status: CheckUnavailable, DurationMS: 90},
		},
	}
}

// uncleanAttemptFixture is a run whose checks passed and whose cleanup did
// not, which is the combination that must never read as a plain pass.
func uncleanAttemptFixture() AttemptRecord {
	attempt := attemptFixture()
	attempt.Status = CheckPassed
	attempt.Summary = "3 of 3 commands passed"
	attempt.Sequence = 47
	attempt.Results = []CheckResultLine{
		{Name: "build", Command: "go build ./...", Status: CheckPassed, ExitCode: 0, HasExitCode: true, DurationMS: 8120},
		{Name: "test", Command: "go test ./...", Status: CheckPassed, ExitCode: 0, HasExitCode: true,
			DurationMS: 81400, OutputExcerpt: "ok  owngit/internal/retry 0.4s",
			CleanupError: `killpg 40211: operation not permitted <&"'>`},
	}
	return attempt
}

func tasksPage(c Chrome, detail bool) TasksPage {
	page := TasksPage{
		Chrome: c, Repo: evidenceRepo(), Tabs: evidenceTabs(RepoTabChecks),
		ListURL:   "/repositories/r1/tasks",
		HelperURL: "/repositories/r1/helper-credentials",
		Configuration: CheckConfigurationView{
			Configured: true, Version: 4, RecordedAt: testNow.AddDate(0, 0, -3),
			Checks: []CheckDefinitionLine{
				{Name: "build", Command: "go build ./..."},
				{Name: "test", Command: "go test ./..."},
				{Name: "lint", Command: "golangci-lint run"},
			},
		},
	}
	if detail {
		// One task's runs. Its budget is the task's, so a new commit does not
		// grant more attempts.
		page.Detail = &TaskDetail{
			Task: TaskSummary{ID: "t1", ShortID: "t1", Title: "Cap retry delays", Status: TaskExhausted,
				CyclesUsed: 3, CycleLimit: 3, CreatedAt: testNow.AddDate(0, 0, -1), UpdatedAt: testNow.Add(-40 * time.Minute),
				InitialCheckDone: true, URL: "/repositories/r1/tasks?task=t1"},
			Attempts:          []AttemptRecord{attemptFixture()},
			AttemptsTruncated: true,
		}
		return page
	}
	page.Tasks = []TaskSummary{
		{ID: "t1", ShortID: "t1", Title: "Cap retry delays", Status: TaskExhausted,
			CyclesUsed: 3, CycleLimit: 3, CreatedAt: testNow.AddDate(0, 0, -1),
			UpdatedAt: testNow.Add(-40 * time.Minute), URL: "/repositories/r1/tasks?task=t1",
			InitialCheckDone: true, Latest: attemptFixture()},
		// A task with no run yet. The absence has to be visible.
		{ID: "t2", ShortID: "t2", Title: "Use stable key in pagination cursor", Status: TaskActive,
			CyclesUsed: 0, CycleLimit: 3, CreatedAt: testNow.Add(-3 * time.Hour),
			UpdatedAt: testNow.Add(-3 * time.Hour), URL: "/repositories/r1/tasks?task=t2"},
	}
	return page
}

// helperTestToken is the fixture secret. It is a synthetic value, never a real
// credential, and TestIssuedTokenIsNotPersisted checks where it may appear.
const helperTestToken = "ogh_FIXTUREvalue0000000000000000000000000000"

func helperPage(c Chrome, issued bool) HelperCredentialsPage {
	page := HelperCredentialsPage{
		Chrome: c, Repo: evidenceRepo(), Tabs: evidenceTabs(RepoTabChecks),
		SelfURL:   "/repositories/r1/helper-credentials",
		SubmitURL: "/repositories/r1/helper-credentials",
		Credentials: []HelperCredentialRow{
			{ID: "hc1", Label: "dev-mini", CreatedAt: testNow.AddDate(0, 0, -9),
				LastUsedAt: testNow.Add(-40 * time.Minute)},
			// Issued and never used, which is how an unused credential is
			// spotted and revoked.
			{ID: "hc2", Label: "laptop", CreatedAt: testNow.AddDate(0, 0, -2)},
			// Revoked credentials stay listed as a record.
			{ID: "hc0", Label: "old-ci", CreatedAt: testNow.AddDate(0, 0, -60),
				LastUsedAt: testNow.AddDate(0, 0, -30), RevokedAt: testNow.AddDate(0, 0, -20), Revoked: true},
		},
	}
	if issued {
		page.Issued = HelperCredentialRow{ID: "hc3", Label: "build-box", CreatedAt: testNow}
		page.IssuedToken = helperTestToken
		page.Credentials = append(page.Credentials, page.Issued)
	}
	return page
}

// restorePage is the restore screen before and after a preview. Previewed is
// the only difference, because the apply step exists only once the backend has
// computed a result.
func restorePage(c Chrome, previewed bool) RestorePage {
	page := RestorePage{
		Chrome: c,
		Repo: RepositoryHeader{ID: "r1", Name: "forge-cli", URL: "/repositories/r1",
			CloneURL: "http://owngit.local:8080/git/forge-cli.git"},
		Source: CommitSummary{OID: "7f2c1a0bb", ShortOID: "7f2c1a0", Subject: "Cap retry delays",
			AuthorName: "Dana", AuthorDate: testNow, URL: "/repositories/r1/commits/7f2c1a0bb"},
		TargetBranch: "main",
		Branches: []RefOption{
			{Name: "main", Selected: true, IsDefault: true},
			{Name: "fix/cursor"},
		},
		Mode: RestoreModeFiles,
		Paths: []RestorePath{
			{Path: "internal/retry/backoff.go", Status: "modified", Selected: true},
			{Path: "internal/retry/limits.go", Status: "added", Selected: true},
			{Path: "internal/retry/legacy.go", Status: "deleted", Selected: false},
		},
		PreviewURL: "/repositories/r1/restore/preview",
		ApplyURL:   "/repositories/r1/restore",
		CancelURL:  "/repositories/r1",
	}
	if !previewed {
		return page
	}
	page.Previewed = true
	page.CanApply = true
	page.ExpectedHead = "a41c9e2ff"
	page.Changes = []DiffFile{
		{Path: "internal/retry/backoff.go", Status: "modified", Additions: 4, Deletions: 2, Selected: true,
			Hunks: []DiffHunk{{Header: "@@ -12,7 +12,9 @@ func Backoff", Lines: []DiffLine{
				{Kind: "context", OldLine: 12, NewLine: 12, Text: "// Backoff caps the delay"},
				{Kind: "add", NewLine: 13, Text: "const maxDelay = 30 * time.Second"},
				{Kind: "del", OldLine: 13, Text: "const maxDelay = time.Hour"},
			}}}},
		{Path: "internal/retry/limits.go", Status: "added", Additions: 18},
		{Path: "internal/retry/legacy.go", Status: "deleted", Deletions: 44},
	}
	return page
}

func repoPage(c Chrome, tab RepoTab) RepositoryPage {
	head := CommitSummary{OID: "a41c9e2ff", ShortOID: "a41c9e2", Subject: "Use stable key in pagination cursor",
		AuthorName: "Dana", AuthorDate: testNow, URL: "/repositories/r1/commits/a41c9e2ff"}
	return RepositoryPage{
		Chrome: c, Tab: tab,
		Repo: RepositoryHeader{ID: "r1", Name: "forge-cli", Description: "Command line tool",
			URL: "/repositories/r1", CloneURL: "http://owngit.local:8080/git/forge-cli.git"},
		OverviewURL: "/repositories/r1", CodeURL: "/repositories/r1/code", CommitsURL: "/repositories/r1/commits",
		PullRequestsURL: "/repositories/r1/pull-requests", TasksURL: "/repositories/r1/tasks",
		RestoreURL: "/repositories/r1/restore",
		Ref: RefSelection{
			Name: "main", Kind: "branch", IsDefault: true, Revision: "a41c9e2ff", ShortRevision: "a41c9e2",
			Branches: []RefOption{{Name: "main", URL: "/repositories/r1/code?ref=main", Selected: true, IsDefault: true},
				{Name: "fix/cursor", URL: "/repositories/r1/code?ref=fix/cursor"}},
			Tags: []RefOption{{Name: "v1.2.0", URL: "/repositories/r1/code?ref=v1.2.0"}},
		},
		Overview: RepositoryOverview{
			Head:     head,
			Branches: []RefLine{{Name: "main", URL: "/x", Kind: "branch", IsDefault: true, Tip: head}},
			Tags:     []RefLine{{Name: "v1.2.0", URL: "/x", Kind: "tag", Annotated: true, Tip: head}},
			RetainedRefs: []RefLine{{Name: "old/main@7f2c1a", URL: "/x", Kind: "branch", Retained: true,
				RestoreURL: "/repositories/r1/restore?source=7f2c1a0bb",
				Tip:        CommitSummary{ShortOID: "7f2c1a0", Subject: "Replaced by force push", AuthorDate: testNow}}},
			Activity:     sampleGraph(),
			PushCommands: []string{"git remote add origin http://owngit.local:8080/git/forge-cli.git", "git push -u origin main"},
		},
		Code: CodeView{
			Path:   "internal/pagination/cursor.go",
			Crumbs: []Crumb{{Name: "forge-cli", URL: "/repositories/r1/code"}, {Name: "internal", URL: "/x"}, {Name: "cursor.go", Current: true}},
			UpURL:  "/repositories/r1/code?path=internal",
			Entries: []TreeEntry{
				{Name: "pagination", Path: "internal/pagination", URL: "/x", Kind: "dir"},
				{Name: "cursor.go", Path: "internal/pagination/cursor.go", URL: "/x", Kind: "file", Size: 1420},
			},
			File: &FileView{Path: "internal/pagination/cursor.go", Size: 1420,
				Lines:      []string{"package pagination", "", `// <script>alert(1)</script> not markup`},
				RawURL:     "/repositories/r1/raw/internal/pagination/cursor.go",
				RestoreURL: "/repositories/r1/restore?path=internal%2Fpagination%2Fcursor.go"},
		},
		Commits: CommitsView{
			List: []CommitSummary{head, {OID: "77b30d5aa", ShortOID: "77b30d5", Subject: "Add cursor boundary fixtures", AuthorDate: testNow, URL: "/y"}},
			Detail: &CommitDetail{
				Commit: head, Body: "Ties are broken by record id.",
				RestoreURL:    "/repositories/r1/restore?source=a41c9e2ff",
				CommitterName: "Rebase Bot", CommitterDate: testNow,
				Parents: []CommitSummary{{ShortOID: "77b30d5", URL: "/y"}},
				Files: []DiffFile{{Path: "internal/pagination/cursor.go", Status: "modified",
					Additions: 7, Deletions: 1, URL: "/z", Selected: true,
					Hunks: []DiffHunk{{Header: "@@ -9,7 +9,9 @@ type Cursor struct", Lines: []DiffLine{
						{Kind: "context", OldLine: 9, NewLine: 9, Text: "// Cursor points at the last record"},
						{Kind: "add", NewLine: 10, Text: "// RecordID breaks ties"},
						{Kind: "del", OldLine: 11, Text: "// removed line"},
					}}}}},
				SelectedPath: "internal/pagination/cursor.go",
			},
		},
	}
}

func TestAllScreensRenderInBothLanguages(t *testing.T) {
	r := newRenderer(t)
	for _, lang := range Langs() {
		for name, page := range allPages(lang) {
			out := render(t, r, page)
			if !strings.HasPrefix(out, "<!doctype html>") {
				t.Errorf("%s/%s: missing doctype", lang, name)
			}
			if !strings.Contains(out, `<html lang="`+string(lang)+`"`) {
				t.Errorf("%s/%s: html lang attribute not set", lang, name)
			}
			if !strings.Contains(out, "</html>") {
				t.Errorf("%s/%s: document not closed", lang, name)
			}
			// A raw message key reaching the page means a missing catalog entry.
			for _, key := range []string{"setup.", "login.", "repo.new.", "error.", "activity.", "restore."} {
				if strings.Contains(out, ">"+key) {
					t.Errorf("%s/%s: raw message key %q rendered", lang, name, key)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// localization
// ---------------------------------------------------------------------------

func TestCatalogIsCompleteInBothLanguages(t *testing.T) {
	if missing := MissingMessages(); len(missing) > 0 {
		t.Fatalf("catalog entries missing a translation: %v", missing)
	}
}

func TestEveryMessageCodeUsedByPagesExists(t *testing.T) {
	// Codes the page types carry must resolve, otherwise a handler reporting
	// them would render the generic fallback instead of the real explanation.
	codes := []MessageCode{
		MsgSetupLinkExpired, MsgSetupReissueHint, MsgPrereqGitMissing,
		MsgLoginFailed, MsgAdminFailed, MsgSettingsSaved,
		MsgRepoNameTaken, MsgRepoUnreadable, MsgCodePathMissing,
		MsgCommitDiffMerge, MsgActivityLimit, MsgErrCSRF,
	}
	for _, code := range codes {
		if !Has(code) {
			t.Errorf("message code %q is not in the catalog", code)
		}
	}
}

func TestKoreanUsesStandardGitTerms(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, repoPage(fullChrome(LangKO), RepoTabOverview))
	for _, term := range []string{"브랜치", "커밋", "태그", "저장소"} {
		if !strings.Contains(out, term) {
			t.Errorf("Korean repository page is missing the standard term %q", term)
		}
	}
	// Decorative separators were deliberately removed from the accepted design.
	for _, bad := range []string{"·", "—", "–"} {
		if strings.Contains(out, bad) {
			t.Errorf("interface text contains the decorative separator %q", bad)
		}
	}
}

func TestBothLanguagesAreCarriedForInPlaceSwitching(t *testing.T) {
	// Form pages must switch language without a reload, which is why the
	// server renders both languages onto the element.
	r := newRenderer(t)
	for _, page := range []Page{
		SetupPage{Chrome: Chrome{Lang: LangEN, Now: testNow, CurrentURL: "/setup"}, Stage: SetupWizard, SubmitURL: "/setup"},
		SettingsPage{Chrome: fullChrome(LangEN), SubmitURL: "/settings"},
		AuthPage{Chrome: Chrome{Lang: LangEN, Now: testNow}, Scope: AuthGeneral, SubmitURL: "/login"},
		NewRepositoryPage{Chrome: fullChrome(LangEN), SubmitURL: "/repositories"},
	} {
		out := render(t, r, page)
		if !strings.Contains(out, `data-ko="`) {
			t.Errorf("%T: no Korean text carried for in-place switching", page)
		}
		if !strings.Contains(out, `data-en="`) {
			t.Errorf("%T: no English text carried for in-place switching", page)
		}
		if !strings.Contains(out, `data-title-ko="`) {
			t.Errorf("%T: document title is not switchable", page)
		}
	}
}

func TestLanguageLinksKeepTheCurrentScreen(t *testing.T) {
	r := newRenderer(t)
	c := fullChrome(LangEN)
	out := render(t, r, repoPage(c, RepoTabCode))
	// The branch and path must survive a language change.
	if !strings.Contains(out, "lang=ko") {
		t.Fatal("no Korean language link rendered")
	}
	for _, keep := range []string{"ref=main", "path=internal"} {
		if !strings.Contains(out, keep) {
			t.Errorf("language link dropped %q from the current URL", keep)
		}
	}
}

func TestWithLangPreservesPathAndQuery(t *testing.T) {
	got := withLang("/repositories/r1/code?ref=fix%2Fcursor&path=internal%2Fx&lang=en", LangKO)
	for _, want := range []string{"/repositories/r1/code", "ref=fix%2Fcursor", "path=internal%2Fx", "lang=ko"} {
		if !strings.Contains(got, want) {
			t.Errorf("withLang(%q) = %q, missing %q", "…", got, want)
		}
	}
	if strings.Contains(got, "lang=en") {
		t.Errorf("withLang left the previous language in %q", got)
	}
}

func TestWithQueryDropsSchemeAndHost(t *testing.T) {
	// A caller must not be able to turn an interface link into an external one.
	got := withLang("https://evil.example/steal?x=1", LangKO)
	if strings.Contains(got, "evil.example") || strings.HasPrefix(got, "https:") {
		t.Fatalf("withLang kept an external target: %q", got)
	}
}

// ---------------------------------------------------------------------------
// secrets and escaping
// ---------------------------------------------------------------------------

func TestPasswordsAreNeverEchoedOnValidationFailure(t *testing.T) {
	r := newRenderer(t)
	const secret = "hunter2-should-never-appear"

	c := Chrome{Lang: LangEN, Now: testNow, CurrentURL: "/setup", CSRF: "csrf",
		Notices: []Notice{
			Error("access_password", MsgSetupAccessPassShort),
			Error("admin_password", MsgSetupAdminShort),
		}}

	pages := []Page{
		SetupPage{Chrome: c, Stage: SetupWizard, SubmitURL: "/setup",
			Form: SetupForm{StoragePath: "/srv/git", AccessMode: AccessPassword,
				AccessPasswordSet: true, AdminPasswordSet: true}},
		AuthPage{Chrome: c, Scope: AuthGeneral, SubmitURL: "/login"},
		AuthPage{Chrome: c, Scope: AuthAdmin, SubmitURL: "/admin/login"},
		SettingsPage{Chrome: fullChrome(LangEN), SubmitURL: "/settings", AccessMode: AccessPassword,
			PendingAction: ActionChangeAdminPassword},
	}
	for _, page := range pages {
		out := render(t, r, page)
		if strings.Contains(out, secret) {
			t.Errorf("%T: a password value reached the page", page)
		}
		// Every password input must render without a value attribute.
		for _, chunk := range strings.Split(out, "<input") {
			if !strings.Contains(chunk, `type="password"`) {
				continue
			}
			field := chunk
			if end := strings.Index(field, ">"); end >= 0 {
				field = field[:end]
			}
			if strings.Contains(field, "value=") {
				t.Errorf("%T: password input carries a value attribute: <input%s>", page, field)
			}
		}
	}
}

func TestNoticeDetailIsEscapedAndNotExecuted(t *testing.T) {
	r := newRenderer(t)
	c := fullChrome(LangEN)
	c.Notices = []Notice{Error("", MsgRepoCreateFail).WithDetail(`<img src=x onerror="alert(1)">`)}
	out := render(t, r, OverviewPage{Chrome: c, Activity: sampleGraph()})
	if strings.Contains(out, "<img src=x") {
		t.Fatal("notice detail was rendered as markup")
	}
	if !strings.Contains(out, "&lt;img") {
		t.Fatal("notice detail was not escaped")
	}
}

func TestRepositoryContentIsEscaped(t *testing.T) {
	r := newRenderer(t)
	page := repoPage(fullChrome(LangEN), RepoTabCode)
	out := render(t, r, page)
	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Fatal("file content was rendered as markup")
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatal("file content was not escaped")
	}
}

func TestRepositoryNameAndRefAreEscaped(t *testing.T) {
	r := newRenderer(t)
	c := fullChrome(LangEN)
	page := repoPage(c, RepoTabOverview)
	page.Repo.Name = `<b>bold</b>`
	page.Ref.Name = `"><script>x</script>`
	page.Ref.Detached = true
	out := render(t, r, page)
	if strings.Contains(out, "<b>bold</b>") || strings.Contains(out, "<script>x</script>") {
		t.Fatal("repository metadata was rendered as markup")
	}
}

func TestSetupWelcomeRedeemsOnlyByExplicitPost(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, SetupPage{
		Chrome:    Chrome{Lang: LangEN, Now: testNow, CurrentURL: "/setup", CSRF: "csrf"},
		Stage:     SetupWelcome,
		RedeemURL: "/setup/redeem",
	})
	if !strings.Contains(out, `method="post"`) || !strings.Contains(out, `action="/setup/redeem"`) {
		t.Fatal("the welcome page does not post to the redemption endpoint")
	}
	if !strings.Contains(out, `name="token" value=""`) {
		t.Fatal("the token field is not empty in the served HTML")
	}
	if !strings.Contains(out, "data-redeem-start") {
		t.Fatal("no explicit start control: redemption must be a deliberate action")
	}
	// The served document must not contain the secret in any form.
	if strings.Contains(out, "localStorage") || strings.Contains(out, "sessionStorage") {
		t.Fatal("the setup page references browser storage")
	}
}

func TestStorageLocationIsHiddenFromOrdinaryVisitors(t *testing.T) {
	r := newRenderer(t)
	c := fullChrome(LangEN)
	c.Storage = StorageInfo{Visible: false, Label: "Home server", Path: "/volume1/secret-git"}
	out := render(t, r, OverviewPage{Chrome: c, Activity: sampleGraph()})
	if strings.Contains(out, "/volume1/secret-git") || strings.Contains(out, "Home server") {
		t.Fatal("the storage location leaked to a viewer who should not see it")
	}

	c.Storage.Visible = true
	out = render(t, r, OverviewPage{Chrome: c, Activity: sampleGraph()})
	if !strings.Contains(out, "/volume1/secret-git") {
		t.Fatal("the storage location is not shown to the owner")
	}
}

func TestCSRFTokenIsPresentOnEveryMutatingForm(t *testing.T) {
	r := newRenderer(t)
	for _, page := range []Page{
		SetupPage{Chrome: Chrome{Lang: LangEN, Now: testNow, CSRF: "tok"}, Stage: SetupWelcome, RedeemURL: "/setup/redeem"},
		SetupPage{Chrome: Chrome{Lang: LangEN, Now: testNow, CSRF: "tok"}, Stage: SetupWizard, SubmitURL: "/setup"},
		AuthPage{Chrome: Chrome{Lang: LangEN, Now: testNow, CSRF: "tok"}, Scope: AuthGeneral, SubmitURL: "/login"},
		SettingsPage{Chrome: fullChrome(LangEN), SubmitURL: "/settings"},
		NewRepositoryPage{Chrome: fullChrome(LangEN), SubmitURL: "/repositories"},
	} {
		out := render(t, r, page)
		forms := strings.Count(out, `method="post"`)
		tokens := strings.Count(out, `name="csrf"`)
		if forms == 0 {
			t.Errorf("%T: no POST form rendered", page)
		}
		if tokens < forms {
			t.Errorf("%T: %d POST forms but only %d csrf fields", page, forms, tokens)
		}
	}
}

// ---------------------------------------------------------------------------
// settings contract
// ---------------------------------------------------------------------------

func TestSecuritySettingsAlwaysCollectTheAdminPassword(t *testing.T) {
	r := newRenderer(t)
	for _, mode := range []AccessMode{AccessOpen, AccessPassword} {
		out := render(t, r, SettingsPage{Chrome: fullChrome(LangEN), SubmitURL: "/settings", AccessMode: mode})
		// Split into forms and check each mutating one.
		for _, form := range strings.Split(out, `method="post"`)[1:] {
			if end := strings.Index(form, "</form>"); end >= 0 {
				form = form[:end]
			}
			if !strings.Contains(form, `name="action"`) {
				continue
			}
			if !strings.Contains(form, `name="admin_password"`) {
				t.Errorf("mode %s: a settings form does not verify the administrator password: %.160s", mode, form)
			}
		}
	}
}

func TestSettingsOffersTheExpectedActions(t *testing.T) {
	r := newRenderer(t)

	openOut := render(t, r, SettingsPage{Chrome: fullChrome(LangEN), SubmitURL: "/settings", AccessMode: AccessOpen})
	for _, want := range []string{ActionEnableAccessPassword, ActionChangeAdminPassword} {
		if !strings.Contains(openOut, `value="`+want+`"`) {
			t.Errorf("password-free mode is missing the %q action", want)
		}
	}
	if strings.Contains(openOut, `value="`+ActionDisableAccessPassword+`"`) {
		t.Error("password-free mode offers a disable action that does not apply")
	}

	passOut := render(t, r, SettingsPage{Chrome: fullChrome(LangEN), SubmitURL: "/settings", AccessMode: AccessPassword})
	for _, want := range []string{ActionChangeAccessPassword, ActionDisableAccessPassword} {
		if !strings.Contains(passOut, `value="`+want+`"`) {
			t.Errorf("password mode is missing the %q action", want)
		}
	}
}

func TestInsecureAcknowledgementIsAdministratorProtected(t *testing.T) {
	r := newRenderer(t)
	c := fullChrome(LangEN)
	c.Connection = Connection{Encrypted: false, Host: "owngit.local:8080"}
	out := render(t, r, SettingsPage{Chrome: c, SubmitURL: "/settings", AccessMode: AccessOpen})

	idx := strings.Index(out, `value="`+ActionAcknowledgeInsecure+`"`)
	if idx < 0 {
		t.Fatal("the plain-HTTP acknowledgement form is missing")
	}
	form := out[idx:]
	if end := strings.Index(form, "</form>"); end >= 0 {
		form = form[:end]
	}
	if !strings.Contains(form, `name="insecure_ack"`) {
		t.Error("the acknowledgement form does not collect insecure_ack")
	}
	if !strings.Contains(form, `name="admin_password"`) {
		t.Error("the acknowledgement form does not verify the administrator password")
	}
}

func TestAcknowledgedConnectionStopsPromptingAndShowsStatus(t *testing.T) {
	r := newRenderer(t)
	c := fullChrome(LangEN)
	c.Connection = Connection{Encrypted: false, InsecureAcknowledged: true}
	out := render(t, r, SettingsPage{Chrome: c, SubmitURL: "/settings", AccessMode: AccessOpen})
	if strings.Contains(out, `value="`+ActionAcknowledgeInsecure+`"`) {
		t.Error("the acknowledgement is asked again after it was already given")
	}
	if !strings.Contains(out, "conn--plain") {
		t.Error("the persistent connection indicator is missing")
	}
}

func TestConnectionIndicatorDoesNotOverclaim(t *testing.T) {
	r := newRenderer(t)

	plain := fullChrome(LangEN)
	plain.Connection = Connection{Encrypted: false, Host: "owngit.ts.net"}
	out := render(t, r, OverviewPage{Chrome: plain, Activity: sampleGraph()})
	if strings.Contains(out, "conn--secure") {
		t.Error("a plain request was reported as encrypted")
	}
	if !strings.Contains(out, wantText(LangEN, MsgConnNoProof)) {
		t.Error("the indicator does not say what the application actually knows")
	}

	secure := fullChrome(LangEN)
	secure.Connection = Connection{Encrypted: true, Host: "owngit.ts.net"}
	out = render(t, r, OverviewPage{Chrome: secure, Activity: sampleGraph()})
	if !strings.Contains(out, "conn--secure") {
		t.Error("an encrypted request was not reported as encrypted")
	}
}

// ---------------------------------------------------------------------------
// honest repository and activity states
// ---------------------------------------------------------------------------

func TestEmptyRepositoryExplainsItselfInsteadOfShowingBlankHistory(t *testing.T) {
	r := newRenderer(t)
	page := repoPage(fullChrome(LangEN), RepoTabCommits)
	page.Repo.Empty = true
	out := render(t, r, page)
	if !strings.Contains(out, wantText(LangEN, MsgRepoEmpty)) {
		t.Error("an empty repository does not say it is empty")
	}
	if !strings.Contains(out, "git push") {
		t.Error("an empty repository does not show how to fill it")
	}
	if strings.Contains(out, "Use stable key in pagination cursor") {
		t.Error("an empty repository rendered commit history anyway")
	}
}

func TestUnreadableRepositoryIsReportedNotHidden(t *testing.T) {
	r := newRenderer(t)
	page := repoPage(fullChrome(LangEN), RepoTabOverview)
	page.Repo.Unreadable = true
	page.Repo.UnreadableReason = MsgErrInternal
	out := render(t, r, page)
	if !strings.Contains(out, wantText(LangEN, MsgRepoUnreadable)) {
		t.Error("an unreadable repository was not reported")
	}
}

func TestMissingRefIsNamedRatherThanSubstituted(t *testing.T) {
	r := newRenderer(t)
	page := repoPage(fullChrome(LangEN), RepoTabCode)
	page.Ref.Missing = true
	page.Ref.Name = "deleted-branch"
	out := render(t, r, page)
	if !strings.Contains(out, wantText(LangEN, MsgRepoRefMissing)) {
		t.Error("a missing ref did not say so")
	}
	if strings.Contains(out, "package pagination") {
		t.Error("file content was shown for a ref that does not resolve")
	}
}

func TestDeletedDefaultBranchIsHandledHonestly(t *testing.T) {
	r := newRenderer(t)
	c := fullChrome(LangEN)
	out := render(t, r, OverviewPage{Chrome: c, TotalCount: 1, Activity: sampleGraph(),
		Repositories: []RepositorySummary{{ID: "r1", Name: "forge-cli", URL: "/repositories/r1",
			DefaultBranchMissing: true}}})
	// The row states the status; the badge is one short line, so it carries
	// the short wording rather than the full instruction.
	if !strings.Contains(out, wantText(LangEN, MsgRepoDefaultGoneShort)) {
		t.Error("a deleted default branch was not reported on the overview")
	}
}

func TestRetainedHistoryIsLabelledAndOffersOnlyRealControls(t *testing.T) {
	// Restoring from kept history is now implemented, so the control belongs
	// here when the backend can address the entry. What must still never
	// appear is a control for behaviour that does not exist.
	r := newRenderer(t)
	out := render(t, r, repoPage(fullChrome(LangEN), RepoTabOverview))
	if !strings.Contains(out, wantText(LangEN, MsgRepoRetainTitle)) {
		t.Fatal("retained history is not labelled")
	}
	if !strings.Contains(out, wantText(LangEN, MsgRepoRetainHelp)) {
		t.Error("retained history is not explained")
	}
	if !strings.Contains(out, wantText(LangEN, MsgRestoreOpen)) {
		t.Error("kept history does not offer the restore entry point the backend addressed")
	}
	for _, dead := range []string{"Run checks", "Review with AI"} {
		if strings.Contains(out, dead) {
			t.Errorf("a control for unimplemented behaviour is present: %q", dead)
		}
	}

	// Without a backend-supplied URL there is still no control and no dead
	// link, because this package never invents an address.
	bare := repoPage(fullChrome(LangEN), RepoTabOverview)
	bare.RestoreURL = ""
	for i := range bare.Overview.RetainedRefs {
		bare.Overview.RetainedRefs[i].RestoreURL = ""
	}
	for i := range bare.Overview.Branches {
		bare.Overview.Branches[i].RestoreURL = ""
	}
	for i := range bare.Overview.Tags {
		bare.Overview.Tags[i].RestoreURL = ""
	}
	out = render(t, r, bare)
	if strings.Contains(out, wantText(LangEN, MsgRestoreOpen)) {
		t.Error("a restore control appeared without an address behind it")
	}
	if strings.Contains(out, `href=""`) {
		t.Error("a restore entry point rendered as an empty link")
	}
}

func TestIncompleteActivityIsStatedNotShownAsZero(t *testing.T) {
	r := newRenderer(t)
	g := sampleGraph()
	g.Complete = false
	g.IncompleteReason = MsgActivityLimit
	out := render(t, r, ActivityPage{Chrome: fullChrome(LangEN), Activity: g})
	if !strings.Contains(out, wantText(LangEN, MsgActivityIncomplete)) {
		t.Error("an incomplete count was presented as final")
	}
	if !strings.Contains(out, wantText(LangEN, MsgActivityLimit)) {
		t.Error("the reason for the incomplete count is missing")
	}
}

func TestUnavailableActivityExplainsInsteadOfDrawingAnEmptyGraph(t *testing.T) {
	r := newRenderer(t)
	g := ActivityGraph{Year: 2026, Available: false, UnavailableReason: MsgActivityNotBuilt}
	out := render(t, r, OverviewPage{Chrome: fullChrome(LangEN), Activity: g})
	if !strings.Contains(out, wantText(LangEN, MsgActivityNotBuilt)) {
		t.Error("unavailable activity did not explain itself")
	}
	if strings.Contains(out, `class="hm__cell"`) {
		t.Error("an empty graph was drawn for activity that could not be computed")
	}
}

func TestActivityDoesNotClaimChecksRan(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, OverviewPage{Chrome: fullChrome(LangEN), Activity: sampleGraph()})
	if !strings.Contains(out, wantText(LangEN, MsgActivityNoChecks)) {
		t.Error("the activity graph does not say what it actually measures")
	}
	for _, claim := range []string{"Checks passed", "All checks", "Build succeeded"} {
		if strings.Contains(out, claim) {
			t.Errorf("the dashboard claims check results it does not have: %q", claim)
		}
	}
}

func TestEmptyDashboardInvitesWithoutForcingARepository(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, OverviewPage{Chrome: fullChrome(LangEN), Activity: sampleGraph()})
	if !strings.Contains(out, wantText(LangEN, MsgOverviewEmpty)) {
		t.Error("the empty dashboard does not say it is empty")
	}
	if !strings.Contains(out, "/repositories/new") {
		t.Error("the empty dashboard does not offer repository creation")
	}
}

func TestNoSyntheticMockDataReachesTheInterface(t *testing.T) {
	// The accepted mockup's sample repositories and check vocabulary must not
	// appear in the product.
	r := newRenderer(t)
	var all strings.Builder
	for _, lang := range Langs() {
		for _, page := range allPages(lang) {
			all.WriteString(render(t, r, page))
		}
	}
	out := all.String()
	for _, fixture := range []string{
		"Northstar Gateway", "Cedar Config", "Relay Queue", "Sentinel Auth",
		"Prism UI", "Atlas Migrations", "Sample commit activity",
		"Preview with sample data", "예시 데이터",
	} {
		if strings.Contains(out, fixture) {
			t.Errorf("mockup fixture %q reached the product interface", fixture)
		}
	}
}

// ---------------------------------------------------------------------------
// accessibility
// ---------------------------------------------------------------------------

func TestStatusIsNotConveyedByColourAlone(t *testing.T) {
	r := newRenderer(t)
	c := fullChrome(LangEN)
	c.Notices = []Notice{
		Error("", MsgRepoCreateFail),
		Success(MsgSettingsSaved),
		{Kind: NoticeWarning, Code: MsgActivityIncomplete},
	}
	out := render(t, r, OverviewPage{Chrome: c, Activity: sampleGraph()})
	// Each notice carries its own text and its own icon shape.
	for _, want := range []string{
		wantText(LangEN, MsgRepoCreateFail),
		wantText(LangEN, MsgSettingsSaved),
		wantText(LangEN, MsgActivityIncomplete),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("notice text %q is missing", want)
		}
	}
	if strings.Count(out, "<svg") < 3 {
		t.Error("notices do not carry distinct icon shapes")
	}
	if !strings.Contains(out, `role="alert"`) {
		t.Error("an error notice is not announced")
	}
}

func TestDiffLinesCarryATextMarkerNotOnlyColour(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, repoPage(fullChrome(LangEN), RepoTabCommits))
	if !strings.Contains(out, `class="difftable__s"`) {
		t.Fatal("diff rows have no +/- marker")
	}
	if !strings.Contains(out, "is-add") || !strings.Contains(out, "is-del") {
		t.Error("added and removed rows are not distinguished")
	}
}

func TestFieldErrorsAreLinkedToTheirInputs(t *testing.T) {
	r := newRenderer(t)
	c := Chrome{Lang: LangEN, Now: testNow, CSRF: "tok", Notices: []Notice{
		Error("storage_path", MsgSetupStorageDenied),
	}}
	out := render(t, r, SetupPage{Chrome: c, Stage: SetupWizard, SubmitURL: "/setup"})
	if !strings.Contains(out, `aria-invalid="true"`) {
		t.Error("the failing input is not marked invalid")
	}
	if !strings.Contains(out, `aria-describedby="storage_path-note"`) {
		t.Error("the input is not linked to its error message")
	}
	if !strings.Contains(out, `id="storage_path-note"`) {
		t.Error("the error message has no matching id")
	}
}

func TestEveryFieldErrorReachesItsScreen(t *testing.T) {
	// A handler reporting a field error must see it rendered, in both
	// languages, on the screen that owns that field.
	r := newRenderer(t)
	cases := []struct {
		name  string
		field string
		code  MessageCode
		build func(Chrome) Page
		// scope is the settings action whose form owns the field, empty on
		// pages that show the field only once.
		scope string
	}{
		{"setup storage", "storage_path", MsgSetupStorageNotDir,
			func(c Chrome) Page { return SetupPage{Chrome: c, Stage: SetupWizard, SubmitURL: "/setup"} }, ""},
		{"setup access password", "access_password", MsgSetupAccessPassShort,
			func(c Chrome) Page {
				return SetupPage{Chrome: c, Stage: SetupWizard, SubmitURL: "/setup",
					Form: SetupForm{AccessMode: AccessPassword}}
			}, ""},
		{"setup admin password", "admin_password", MsgSetupAdminSameAsGen,
			func(c Chrome) Page { return SetupPage{Chrome: c, Stage: SetupWizard, SubmitURL: "/setup"} }, ""},
		{"setup insecure ack", "insecure_ack", MsgSetupInsecureNeed,
			func(c Chrome) Page { return SetupPage{Chrome: c, Stage: SetupWizard, SubmitURL: "/setup"} }, ""},
		{"general login", "password", MsgLoginFailed,
			func(c Chrome) Page { return AuthPage{Chrome: c, Scope: AuthGeneral, SubmitURL: "/login"} }, ""},
		{"admin login", "admin_password", MsgAdminFailed,
			func(c Chrome) Page { return AuthPage{Chrome: c, Scope: AuthAdmin, SubmitURL: "/admin/login"} }, ""},
		{"repository name", "name", MsgRepoNameTaken,
			func(c Chrome) Page { return NewRepositoryPage{Chrome: c, SubmitURL: "/repositories"} }, ""},
		// Settings repeats one field name across several forms, so its notes
		// are scoped by the action that was submitted.
		{"settings access password", "access_password", MsgSetupAccessPassShort,
			func(c Chrome) Page {
				return SettingsPage{Chrome: c, SubmitURL: "/settings", AccessMode: AccessPassword,
					PendingAction: ActionChangeAccessPassword}
			}, ActionChangeAccessPassword},
		{"settings admin password", "admin_password", MsgAdminFailed,
			func(c Chrome) Page {
				return SettingsPage{Chrome: c, SubmitURL: "/settings", AccessMode: AccessOpen,
					PendingAction: ActionChangeAdminPassword}
			}, ActionChangeAdminPassword},
		{"settings new admin password", "new_admin_password", MsgSetupAdminShort,
			func(c Chrome) Page {
				return SettingsPage{Chrome: c, SubmitURL: "/settings", AccessMode: AccessOpen,
					PendingAction: ActionChangeAdminPassword}
			}, ActionChangeAdminPassword},
		{"settings insecure ack", "insecure_ack", MsgSetupInsecureNeed,
			func(c Chrome) Page {
				return SettingsPage{Chrome: c, SubmitURL: "/settings", AccessMode: AccessOpen,
					PendingAction: ActionAcknowledgeInsecure}
			}, ActionAcknowledgeInsecure},
		{"settings enable admin password", "admin_password", MsgAdminFailed,
			func(c Chrome) Page {
				return SettingsPage{Chrome: c, SubmitURL: "/settings", AccessMode: AccessOpen,
					PendingAction: ActionEnableAccessPassword}
			}, ActionEnableAccessPassword},
		{"settings disable admin password", "admin_password", MsgAdminFailed,
			func(c Chrome) Page {
				return SettingsPage{Chrome: c, SubmitURL: "/settings", AccessMode: AccessPassword,
					PendingAction: ActionDisableAccessPassword}
			}, ActionDisableAccessPassword},
	}

	for _, tc := range cases {
		for _, lang := range Langs() {
			c := fullChrome(lang)
			c.Notices = []Notice{Error(tc.field, tc.code)}
			out := render(t, r, tc.build(c))
			if !strings.Contains(out, wantText(lang, tc.code)) {
				t.Errorf("%s (%s): the error text is not rendered", tc.name, lang)
			}
			wantID := noteID(tc.scope, tc.field)
			if !strings.Contains(out, `id="`+wantID+`"`) {
				t.Errorf("%s (%s): the error is not attached to the %q field (no %s)", tc.name, lang, tc.field, wantID)
			}
			if !strings.Contains(out, `aria-describedby="`+wantID+`"`) {
				t.Errorf("%s (%s): the %q input does not point at its message", tc.name, lang, tc.field)
			}
		}
	}
}

func TestSkipLinkAndMainLandmarkExist(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, OverviewPage{Chrome: fullChrome(LangEN), Activity: sampleGraph()})
	if !strings.Contains(out, `href="#main"`) || !strings.Contains(out, `id="main"`) {
		t.Error("the skip link has no target")
	}
	if !strings.Contains(out, `<main id="main" class="content" tabindex="-1">`) {
		t.Error("the main landmark is not focusable from the skip link")
	}
}

func TestRepositoryTabsUseSemanticNavigation(t *testing.T) {
	// The pull request and checks sections are optional, so the strip has the
	// three tabs a caller that does not offer them supplies, and five when it
	// does. A section without an address renders no tab rather than a link
	// that goes nowhere.
	r := newRenderer(t)
	for _, tab := range []RepoTab{RepoTabOverview, RepoTabCode, RepoTabCommits} {
		page := repoPage(fullChrome(LangEN), tab)
		page.PullRequestsURL = ""
		page.TasksURL = ""
		out := render(t, r, page)
		if got := strings.Count(out, `class="rtabs__btn"`); got != 3 {
			t.Errorf("tab %s: %d section links, want the three always offered", tab, got)
		}
		if got := strings.Count(out, `class="rtabs__btn" href="/repositories/r1`); got != 3 {
			t.Errorf("tab %s: %d section links are real URLs, want 3", tab, got)
		}
		if !strings.Contains(out, `aria-current="page"`) {
			t.Errorf("tab %s: the current section is not marked", tab)
		}
	}
}

func TestOptionalRepositoryTabsRenderWhenOffered(t *testing.T) {
	// The defect this covers: RepositoryPage accepted the two optional
	// addresses and rendered neither, so the new sections were unreachable
	// from the repository screens that are supposed to lead to them.
	r := newRenderer(t)
	for _, tab := range []RepoTab{RepoTabOverview, RepoTabCode, RepoTabCommits} {
		out := render(t, r, repoPage(fullChrome(LangEN), tab))
		if got := strings.Count(out, `class="rtabs__btn"`); got != 5 {
			t.Errorf("tab %s: %d section links, want five when both are offered", tab, got)
		}
		for _, want := range []string{
			`href="/repositories/r1/pull-requests"`,
			`href="/repositories/r1/tasks"`,
		} {
			if !strings.Contains(out, want) {
				t.Errorf("tab %s: the strip does not link to %s", tab, want)
			}
		}
		if strings.Contains(out, `href=""`) {
			t.Errorf("tab %s: an absent section rendered as an empty link", tab)
		}
	}

	// The evidence screens share the same strip, so the reader can move
	// between all five sections from either side.
	for name, page := range map[string]Page{
		"pull-requests":      pullRequestsPage(fullChrome(LangEN), false),
		"tasks":              tasksPage(fullChrome(LangEN), false),
		"helper-credentials": helperPage(fullChrome(LangEN), false),
	} {
		out := render(t, r, page)
		if got := strings.Count(out, `class="rtabs__btn"`); got != 5 {
			t.Errorf("%s: %d section links, want five", name, got)
		}
	}
}

func TestActivityGraphIsKeyboardReachableAndLabelled(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, OverviewPage{Chrome: fullChrome(LangEN), Activity: sampleGraph()})
	if !strings.Contains(out, `role="grid"`) || !strings.Contains(out, `role="gridcell"`) {
		t.Error("the activity graph has no grid semantics")
	}
	if !strings.Contains(out, `role="row"`) {
		t.Error("the activity graph rows are not marked")
	}
	if !strings.Contains(out, `data-ko-aria-label="`) {
		t.Error("graph cells do not carry a Korean accessible name")
	}
	if !strings.Contains(out, `role="status"`) {
		t.Error("the graph readout is not a live region")
	}
	if !strings.Contains(out, `class="hm__scroll" tabindex="0"`) {
		t.Error("the horizontally scrolling graph is not keyboard reachable")
	}
}

// ---------------------------------------------------------------------------
// assets
// ---------------------------------------------------------------------------

func TestBrandRendersInBothLanguages(t *testing.T) {
	r := newRenderer(t)
	for _, lang := range []Lang{LangEN, LangKO} {
		out := render(t, r, OverviewPage{Chrome: fullChrome(lang), Activity: sampleGraph()})
		if !strings.Contains(out, "OwnGit") {
			t.Errorf("%s: the rendered page does not show the OwnGit brand", lang)
		}
	}
}

func TestAssetURLsChangeWithContent(t *testing.T) {
	// Fixed asset URLs plus a long cache lifetime would keep serving the old
	// stylesheet and script after an update, so the URL carries a content
	// version.
	r := newRenderer(t)
	out := render(t, r, OverviewPage{Chrome: fullChrome(LangEN), Activity: sampleGraph()})
	for _, asset := range []string{"owngit.css", "owngit.js", "logo.svg"} {
		marker := "/assets/" + asset + "?v="
		if !strings.Contains(out, marker) {
			t.Errorf("%s is referenced without a content version", asset)
		}
	}

	css := r.prints["owngit.css"]
	js := r.prints["owngit.js"]
	if css == "" || js == "" {
		t.Fatal("assets were not fingerprinted")
	}
	if css == js {
		t.Error("different assets produced the same version")
	}
}

func TestAssetHandlerRevalidatesUnversionedRequests(t *testing.T) {
	r := newRenderer(t)
	version := r.prints["owngit.css"]

	stale := doAssetRequest(t, r, "/owngit.css?v=old")
	if !strings.Contains(stale, "must-revalidate") {
		t.Errorf("a stale asset URL was cached without revalidation: %q", stale)
	}

	bare := doAssetRequest(t, r, "/owngit.css")
	if !strings.Contains(bare, "must-revalidate") {
		t.Errorf("an unversioned asset URL was cached without revalidation: %q", bare)
	}

	current := doAssetRequest(t, r, "/owngit.css?v="+version)
	if !strings.Contains(current, "immutable") {
		t.Errorf("the current asset URL is not cacheable: %q", current)
	}
}

func TestFontLicenceShipsWithTheFont(t *testing.T) {
	r := newRenderer(t)
	if _, ok := r.prints["fonts/PretendardVariable.woff2"]; !ok {
		t.Fatal("the bundled font is missing")
	}
	if _, ok := r.prints["fonts/PRETENDARD-LICENSE.txt"]; !ok {
		t.Fatal("the font licence is not bundled with the font")
	}
}

func TestStylesheetHasNoOutboundDependency(t *testing.T) {
	data, err := assetFS.ReadFile("assets/owngit.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(data)
	for _, outbound := range []string{"http://", "https://", "//fonts.", "@import url(http"} {
		if strings.Contains(css, outbound) {
			t.Errorf("the stylesheet reaches outside this installation: %q", outbound)
		}
	}
}

func TestScriptStoresNoSecrets(t *testing.T) {
	data, err := assetFS.ReadFile("assets/owngit.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(data)
	// The appearance preference is the only thing allowed in local storage.
	for _, line := range strings.Split(js, "\n") {
		if !strings.Contains(line, "localStorage") && !strings.Contains(line, "sessionStorage") {
			continue
		}
		if strings.Contains(line, "APPEARANCE_KEY") || strings.Contains(line, "//") || strings.Contains(line, "* ") {
			continue
		}
		t.Errorf("browser storage is used for something other than appearance: %s", strings.TrimSpace(line))
	}
	for _, forbidden := range []string{"password", "csrf", "admin_password"} {
		if strings.Contains(strings.ToLower(js), `"`+forbidden+`"`) {
			t.Errorf("the script references %q", forbidden)
		}
	}
}

func doAssetRequest(t *testing.T, r *Renderer, target string) string {
	t.Helper()
	req := newAssetRequest(t, target)
	rec := newRecorder()
	r.Assets().ServeHTTP(rec, req)
	return rec.Header().Get("Cache-Control")
}

// ---------------------------------------------------------------------------
// formatting
// ---------------------------------------------------------------------------

func TestRelativeTimesUseTheAuthorsRecordedDate(t *testing.T) {
	// A commit keeps the calendar date and clock its author recorded. Now is
	// only the reference calendar for "today" and "yesterday"; it never moves
	// a stored date into another zone.
	seoul := time.FixedZone("KST", 9*60*60)
	now := time.Date(2026, 3, 12, 9, 0, 0, 0, seoul)

	sameDay := time.Date(2026, 3, 12, 1, 30, 0, 0, seoul)
	if got := formatRelative(LangEN, now, sameDay); !strings.HasPrefix(got, "Today") {
		t.Errorf("same-day time rendered as %q", got)
	}
	if got := formatRelative(LangKO, now, now.AddDate(0, 0, -1)); !strings.HasPrefix(got, "\uc5b4\uc81c") {
		t.Errorf("previous-day time rendered as %q", got)
	}

	// An author who recorded 11 March in UTC is shown on 11 March, with the
	// clock they wrote. Restating it as 12 March 05:00 would report a day the
	// author never recorded.
	utcInstant := time.Date(2026, 3, 11, 20, 0, 0, 0, time.UTC)
	got := formatRelative(LangEN, now, utcInstant)
	if !strings.Contains(got, "20:00") {
		t.Errorf("the author's recorded clock was changed: %q", got)
	}
	if strings.HasPrefix(got, "Today") {
		t.Errorf("a commit recorded on 11 March is presented as today: %q", got)
	}
}

func TestActivityLevelsSpanTheRamp(t *testing.T) {
	for _, tc := range []struct{ count, level int }{{0, 0}, {1, 1}, {2, 1}, {5, 2}, {9, 3}, {40, 4}} {
		if got := activityLevel(tc.count); got != tc.level {
			t.Errorf("activityLevel(%d) = %d, want %d", tc.count, got, tc.level)
		}
	}
}

func TestGraphLayoutCoversTheWholeYear(t *testing.T) {
	layout := activityWeeks(sampleGraph())
	if len(layout.Rows) != 7 {
		t.Fatalf("expected 7 weekday rows, got %d", len(layout.Rows))
	}
	drawn := 0
	for _, row := range layout.Rows {
		if len(row.Cells) != layout.Weeks {
			t.Fatalf("row has %d cells, expected %d", len(row.Cells), layout.Weeks)
		}
		for _, cell := range row.Cells {
			if !cell.Blank {
				drawn++
			}
		}
	}
	if drawn != 365 {
		t.Errorf("drew %d days of 2026, expected 365", drawn)
	}
	if len(layout.Months) != 12 {
		t.Errorf("expected 12 month labels, got %d", len(layout.Months))
	}
}

func TestCountedNounsReadNaturallyInBothLanguages(t *testing.T) {
	cases := []struct {
		lang Lang
		kind string
		n    int
		want string
	}{
		{LangEN, "repository", 1, "1 repository"},
		{LangEN, "repository", 7, "7 repositories"},
		{LangEN, "commit", 1, "1 commit"},
		{LangKO, "repository", 7, "저장소 7곳"},
		{LangKO, "commit", 200, "커밋 200건"},
		{LangKO, "branch", 3, "브랜치 3개"},
	}
	for _, tc := range cases {
		if got := formatCount(tc.lang, tc.kind, tc.n); got != tc.want {
			t.Errorf("formatCount(%s, %s, %d) = %q, want %q", tc.lang, tc.kind, tc.n, got, tc.want)
		}
	}
}

func TestParseLangRejectsUnknownValues(t *testing.T) {
	for _, bad := range []string{"", "fr", "en-US", "ko-KR", "<script>"} {
		if got, ok := ParseLang(bad); ok || got != DefaultLang {
			t.Errorf("ParseLang(%q) = (%q, %v), want (%q, false)", bad, got, ok, DefaultLang)
		}
	}
	for _, good := range []Lang{LangEN, LangKO} {
		if got, ok := ParseLang(string(good)); !ok || got != good {
			t.Errorf("ParseLang(%q) = (%q, %v)", good, got, ok)
		}
	}
}

func TestRenderRejectsAnUnknownPage(t *testing.T) {
	r := newRenderer(t)
	var buf bytes.Buffer
	if err := r.Render(&buf, nil); err == nil {
		t.Error("rendering a nil page succeeded")
	}
}

// uncleanTasksPage shows one attempt that passed its checks and failed to
// clean up, with a second attempt carrying only the aggregate.
func uncleanTasksPage(c Chrome) TasksPage {
	page := tasksPage(c, true)
	aggregateOnly := uncleanAttemptFixture()
	aggregateOnly.ID = "att_1b0c77ae"
	aggregateOnly.ShortID = "att_1b0c77a"
	aggregateOnly.Sequence = 46
	aggregateOnly.Results = nil
	aggregateOnly.CleanupFailed = true
	page.Detail.Attempts = []AttemptRecord{uncleanAttemptFixture(), aggregateOnly}
	return page
}

// uncleanPullRequestPage shows check evidence whose status says passed while
// the cleanup aggregate says a process was left behind.
func uncleanPullRequestPage(c Chrome) PullRequestPage {
	page := pullRequestPage(c, prFixtureFailing)
	page.Checks.Status = CheckPassed
	page.Checks.Passed = true
	page.Checks.TestedCommit = true
	page.Checks.WorktreeState = WorktreeClean
	page.Checks.Stale = false
	page.Checks.ReadFailure = nil
	page.Checks.CleanupFailed = true
	page.Checks.Summary = "3 of 3 commands passed"
	return page
}
