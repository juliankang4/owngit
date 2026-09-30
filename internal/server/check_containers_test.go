package server

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
	"owngit/internal/webui"
)

// The Automatic checks page lists the container cleanup records of finished
// jobs that OwnGit could not remove, and forgets one once the owner
// confirmed its container is gone, as owngit forget-check-container does.

// leftoverContainer records a finished check job of repository whose
// container cleanup record stays, made on the Docker daemon "old-daemon",
// and returns the job.
func leftoverContainer(t *testing.T, store *state.Store, repository string) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	_, err := store.SetCheckPolicy(ctx, state.CheckPolicyInput{
		RepositoryID: repository, Executor: state.CheckExecutorExternalRunner, AllowedEvents: []string{"push"},
		MaxTimeoutMS: 60000, MaxOutputLimitBytes: 65536, QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60000,
	}, now)
	noErr(t, err)
	_, err = store.GrantCheckConsent(ctx, repository, now)
	noErr(t, err)
	oid := strings.Repeat("b", 40)
	job, _, err := store.AdmitCheckJob(ctx, state.CheckJobRequest{
		RepositoryID: repository, Trigger: "push", EventKey: "refs/heads/main@" + oid,
		SourceOID: oid, TriggerRef: "main", WorkflowDigest: strings.Repeat("d", 64),
		Checks: []state.CheckDefinition{{Name: "unit", Command: "true"}},
	}, now)
	noErr(t, err)
	noErr(t, store.Exec(ctx, `UPDATE check_jobs SET status=? WHERE id=?`, state.CheckJobInterrupted, job.ID))
	noErr(t, store.Exec(ctx, `INSERT INTO check_job_runtime_ownership(job_id,repository_id,container_name,container_id,daemon_id,created_at) VALUES(?,?,?,?,?,1)`,
		job.ID, repository, "owngit-check-"+job.ID, strings.Repeat("e", 64), "old-daemon"))
	return job.ID
}

// dockerReporting puts a docker executable first on PATH that reports
// daemonID, or fails like an unreachable daemon when daemonID is empty.
func dockerReporting(t *testing.T, daemonID string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the Docker stand-in is a POSIX shell script")
	}
	for _, name := range []string{"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"} {
		t.Setenv(name, "")
	}
	script := "#!/bin/sh\necho 'Cannot connect to the Docker daemon' >&2\nexit 1\n"
	if daemonID != "" {
		script = `#!/bin/sh
case "$*" in
  "context inspect"*) echo unix:///var/run/docker.sock ;;
  *" version "*) echo linux ;;
  *" info "*) echo ` + daemonID + ` ;;
  *) echo "unexpected docker call: $*" >&2; exit 1 ;;
esac
`
	}
	directory := t.TempDir()
	noErr(t, os.WriteFile(filepath.Join(directory, "docker"), []byte(script), 0o700))
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestBrowserForgetsALeftoverCheckContainer(t *testing.T) {
	fixture := newAPIFixture(t, false)
	askEveryTime(t, fixture.app)
	job := leftoverContainer(t, fixture.store, "project")
	server, client, jar := openBrowser(t, fixture)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "forget")
	page := server.URL + configuredChecksURL("project")

	listed := browserGET(t, client, page)
	if listed.status != http.StatusOK || !strings.Contains(listed.body, "owngit-check-"+job) || !strings.Contains(listed.body, "com.owngit.check-job="+job) {
		t.Fatalf("the leftover container is not listed: status=%d", listed.status)
	}
	// Deleting the repository is refused while the record stays, and the
	// API says why, as the page does.
	if status, code := checkStatus(t, adminAPIRequest(t, http.MethodPost, server.URL+"/api/v1/repositories/project/delete", map[string]string{"mode": "keep_files", "confirm_name": "project"}, "admin-password")); status != http.StatusConflict || code != "repository_busy" {
		t.Fatalf("deletion with a leftover container: status=%d code=%q", status, code)
	}

	form := func(edit func(url.Values)) browserHTTPResult {
		values := url.Values{"csrf": {csrf}, "action": {webui.ActionForgetCheckContainer}, "job_id": {job}, "container_removed": {"1"}, "admin_password": {"admin-password"}}
		edit(values)
		return browserForm(t, client, page, values, server.URL)
	}
	recorded := func() bool {
		_, exists, err := fixture.store.CheckContainerOwnershipForJob(context.Background(), job)
		noErr(t, err)
		return exists
	}
	dockerReporting(t, "old-daemon")
	for name, refused := range map[string]struct {
		edit   func(url.Values)
		status int
		shows  webui.MessageCode
	}{
		"no confirmation": {func(values url.Values) { values.Del("container_removed") }, http.StatusUnprocessableEntity, webui.MsgCCContainerRemovedNeeded},
		"no password":     {func(values url.Values) { values.Del("admin_password") }, http.StatusUnauthorized, ""},
		"another job":     {func(values url.Values) { values.Set("job_id", strings.Repeat("0", 32)) }, http.StatusNotFound, webui.MsgCCContainerMissing},
		"current daemon":  {func(url.Values) {}, http.StatusConflict, webui.MsgCCContainerCurrentDaemon},
	} {
		result := form(refused.edit)
		if result.status != refused.status || (refused.shows != "" && !strings.Contains(result.body, html.EscapeString(webui.Text(webui.LangEN, refused.shows)))) {
			t.Fatalf("%s: status=%d", name, result.status)
		}
		if !recorded() {
			t.Fatalf("%s forgot the record", name)
		}
	}
	// The refusal marks the checkbox of the form that was sent.
	unticked := form(func(values url.Values) { values.Del("container_removed") })
	if !strings.Contains(unticked.body, `aria-describedby="`+forgetContainerScope(job)+`-container_removed-note"`) {
		t.Fatal("the refused checkbox is not marked")
	}
	if result := form(func(values url.Values) { values.Set("csrf", "wrong") }); result.status != http.StatusForbidden || !recorded() {
		t.Fatalf("a wrong CSRF token: status=%d", result.status)
	}

	dockerReporting(t, "")
	result := form(func(url.Values) {})
	if result.status != http.StatusSeeOther || !strings.Contains(result.header.Get("Location"), "notice=check_container_forgotten") || recorded() {
		t.Fatalf("forget status=%d location=%q", result.status, result.header.Get("Location"))
	}
	shown := browserGET(t, client, server.URL+result.header.Get("Location"))
	if !strings.Contains(shown.body, html.EscapeString(webui.Text(webui.LangEN, webui.MsgCCContainerForgotten))) || strings.Contains(shown.body, "owngit-check-"+job) {
		t.Fatal("the result is not shown, or the record is still listed")
	}
}

// A record of another repository's job is not forgotten from this page.
func TestBrowserForgetsOnlyThisRepositorysContainers(t *testing.T) {
	fixture := newAPIFixture(t, false)
	askEveryTime(t, fixture.app)
	_, err := fixture.app.Repositories.Create(context.Background(), "other", "")
	noErr(t, err)
	job := leftoverContainer(t, fixture.store, "other")
	server, client, jar := openBrowser(t, fixture)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "forget-other")
	dockerReporting(t, "")
	result := browserForm(t, client, server.URL+configuredChecksURL("project"), url.Values{
		"csrf": {csrf}, "action": {webui.ActionForgetCheckContainer}, "job_id": {job}, "container_removed": {"1"}, "admin_password": {"admin-password"},
	}, server.URL)
	if result.status != http.StatusNotFound {
		t.Fatalf("status=%d", result.status)
	}
	if _, exists, err := fixture.store.CheckContainerOwnershipForJob(context.Background(), job); err != nil || !exists {
		t.Fatalf("the other repository's record was forgotten: exists=%v err=%v", exists, err)
	}
}
