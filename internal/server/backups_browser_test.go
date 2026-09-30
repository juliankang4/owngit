package server

import (
	"archive/tar"
	"bytes"
	"context"
	"html"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/backups"
	"owngit/internal/recovery"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// waitForBackups waits until no backup, verification or upload runs, and
// returns the status then.
func waitForBackups(t *testing.T, app *App) backups.Status {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for {
		status, err := app.Backups.Status(context.Background())
		noErr(t, err)
		busy := status.Running != nil || status.Check != nil && status.Check.Status == backups.CheckRunning ||
			status.Upload != nil && status.Upload.Status == backups.UploadVerifying
		if !busy {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("backups still busy: %+v", status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// uploadForm sends a backup file with the upload form.
func uploadForm(t *testing.T, client *http.Client, target, origin string, fields url.Values, archive []byte) browserHTTPResult {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, values := range fields {
		for _, value := range values {
			noErr(t, writer.WriteField(name, value))
		}
	}
	part, err := writer.CreateFormFile("backup_file", "backup.tar")
	noErr(t, err)
	_, err = part.Write(archive)
	noErr(t, err)
	noErr(t, writer.Close())
	request, err := http.NewRequest(http.MethodPost, target, &body)
	noErr(t, err)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Origin", origin)
	return browserRequest(t, client, request)
}

// The Backups groups save the schedule with its warnings, back up now,
// show the recorded result, verify a listed backup again, download it and
// take it back as an upload, each with the CSRF check and the
// administrator password.
func TestBackupsInTheDashboard(t *testing.T) {
	fixture := newAPIFixture(t, false)
	fixture.app.RestoreGuide = func(input, repositoryRoot string) *webui.BackupRestore {
		return &webui.BackupRestore{Command: "owngit restore --input " + input + " --repository-root " + repositoryRoot + " --verify"}
	}
	server, client, jar := openBrowser(t, fixture)
	storage := server.URL + webui.SettingsTabURL(webui.SettingsStorage)

	// A visitor who is not the administrator sees what backups are, and
	// neither the schedule nor any button.
	visitor := browserGET(t, client, storage)
	if visitor.status != http.StatusOK || !strings.Contains(visitor.body, `id="grp-backups"`) || strings.Contains(visitor.body, `name="backup_destination"`) || strings.Contains(visitor.body, `value="backup_now"`) {
		t.Fatalf("visitor page %d", visitor.status)
	}

	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "backups")
	askEveryTime(t, fixture.app)
	destination := filepath.Join(t.TempDir(), "backups")
	schedule := url.Values{"csrf": {csrf}, "action": {webui.ActionSaveBackupSchedule}, "backup_destination": {destination}, "backup_interval": {"1d"}, "backup_keep": {"7"}, "backup_verify": {"on"}}
	if result := browserForm(t, client, storage, schedule, server.URL); result.status != http.StatusUnauthorized {
		t.Fatalf("schedule without the administrator password: %d", result.status)
	}
	schedule.Set("admin_password", "admin-password")
	bad := url.Values{}
	for key, values := range schedule {
		bad[key] = values
	}
	bad.Set("backup_keep", "0")
	if result := browserForm(t, client, storage, bad, server.URL); result.status != http.StatusUnprocessableEntity || !strings.Contains(result.body, webui.Text(webui.LangEN, webui.MsgBackupKeepInvalid)) {
		t.Fatalf("keep 0: %d", result.status)
	}
	saved := browserForm(t, client, storage, schedule, server.URL)
	if saved.status != http.StatusSeeOther || !strings.Contains(saved.header.Get("Location"), "notice=backup_saved_off") {
		t.Fatalf("schedule saved: %d %s", saved.status, saved.header.Get("Location"))
	}
	page := browserGET(t, client, server.URL+saved.header.Get("Location"))
	for _, want := range []string{webui.Text(webui.LangEN, webui.MsgBackupSaved), "stop creating scheduled recovery copies", html.EscapeString(destination)} {
		if !strings.Contains(page.body, want) {
			t.Fatalf("saved page lacks %q", want)
		}
	}

	now := url.Values{"csrf": {csrf}, "action": {webui.ActionBackupNow}, "admin_password": {"admin-password"}}
	if result := browserForm(t, client, storage, url.Values{"csrf": {"wrong"}, "action": {webui.ActionBackupNow}, "admin_password": {"admin-password"}}, server.URL); result.status != http.StatusForbidden {
		t.Fatalf("back up now without the CSRF token: %d", result.status)
	}
	if result := browserForm(t, client, storage, now, server.URL); result.status != http.StatusSeeOther || !strings.Contains(result.header.Get("Location"), "notice=backup_started") {
		t.Fatalf("back up now: %d", result.status)
	}
	status := waitForBackups(t, fixture.app)
	run := status.LastRun
	if run == nil || run.Status != state.BackupSucceeded || run.Verification != state.BackupVerifyPassed || run.Path == "" {
		t.Fatalf("the backup: %+v", run)
	}
	page = browserGET(t, client, storage)
	for _, want := range []string{run.BackupName, webui.Text(webui.LangEN, webui.MsgBackupStatusSucceeded), webui.Text(webui.LangEN, webui.MsgBackupVerifyPassed),
		html.EscapeString("owngit restore --input " + run.Path)} {
		if !strings.Contains(page.body, want) {
			t.Fatalf("the page after the backup lacks %q", want)
		}
	}

	runURL := storage + "?run=" + run.ID
	verify := url.Values{"csrf": {csrf}, "action": {webui.ActionBackupVerify}, "admin_password": {"admin-password"}}
	if result := browserForm(t, client, runURL, verify, server.URL); result.status != http.StatusSeeOther || !strings.Contains(result.header.Get("Location"), "notice=backup_check_started") {
		t.Fatalf("verify again: %d %s", result.status, result.body)
	}
	if status := waitForBackups(t, fixture.app); status.Check == nil || status.Check.Status != backups.CheckPassed || status.Check.RunID != run.ID {
		t.Fatalf("verification: %+v", status.Check)
	}
	if result := browserForm(t, client, storage+"?run="+strings.Repeat("0", 32), verify, server.URL); result.status != http.StatusNotFound ||
		!strings.Contains(result.body, webui.Text(webui.LangEN, webui.MsgBackupNoBackup)) {
		t.Fatalf("verify an unknown run: %d", result.status)
	}

	download := url.Values{"csrf": {csrf}, "action": {webui.ActionBackupDownload}}
	if result := browserForm(t, client, runURL, download, server.URL); result.status != http.StatusUnauthorized {
		t.Fatalf("download without the administrator password: %d", result.status)
	}
	download.Set("admin_password", "admin-password")
	file := browserForm(t, client, runURL, download, server.URL)
	if file.status != http.StatusOK || file.header.Get("Content-Type") != "application/x-tar" ||
		file.header.Get("Content-Disposition") != `attachment; filename=`+run.BackupName+`.tar` {
		t.Fatalf("download: %d %v", file.status, file.header)
	}
	unpacked := t.TempDir()
	name, err := recovery.UnpackArchive(context.Background(), strings.NewReader(file.body), unpacked)
	noErr(t, err)
	if result, err := recovery.Verify(context.Background(), filepath.Join(unpacked, name), "", ""); err != nil || !result.Verified {
		t.Fatalf("the downloaded backup does not verify: %v %+v", err, result)
	}

	upload := url.Values{"csrf": {csrf}, "action": {webui.ActionBackupUpload}, "admin_password": {"admin-password"}}
	if result := uploadForm(t, client, storage, server.URL, url.Values{"csrf": {csrf}, "action": {webui.ActionBackupUpload}}, []byte(file.body)); result.status != http.StatusUnauthorized {
		t.Fatalf("upload without the administrator password: %d", result.status)
	}
	escaping := backupTar(t, tarEntry{name: "b/", kind: tar.TypeDir}, tarEntry{name: "b/../../escape.bundle", kind: tar.TypeReg, content: "x"})
	if result := uploadForm(t, client, storage, server.URL, upload, escaping); result.status != http.StatusBadRequest ||
		!strings.Contains(result.body, webui.Text(webui.LangEN, webui.MsgBackupUploadRefused)) {
		t.Fatalf("upload with a parent path: %d", result.status)
	}
	received := uploadForm(t, client, storage, server.URL, upload, []byte(file.body))
	if received.status != http.StatusSeeOther || !strings.Contains(received.header.Get("Location"), "notice=backup_upload_received") {
		t.Fatalf("upload: %d %s", received.status, received.body)
	}
	status = waitForBackups(t, fixture.app)
	if status.Upload == nil || status.Upload.Status != backups.UploadPassed || status.Upload.Name != run.BackupName {
		t.Fatalf("upload: %+v", status.Upload)
	}
	page = browserGET(t, client, storage)
	if !strings.Contains(page.body, html.EscapeString("owngit restore --input "+status.Upload.Path)) {
		t.Fatal("the page does not show how to restore the uploaded backup")
	}
}

type tarEntry struct {
	name, content, linkname string
	kind                    byte
}

func backupTar(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	archive := tar.NewWriter(&buffer)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Typeflag: entry.kind, Mode: 0o600, Linkname: entry.linkname}
		if entry.kind == tar.TypeReg {
			header.Size = int64(len(entry.content))
		}
		noErr(t, archive.WriteHeader(header))
		_, err := archive.Write([]byte(entry.content))
		noErr(t, err)
	}
	noErr(t, archive.Close())
	return buffer.Bytes()
}

// Every backup operation of the owner API needs the administrator
// password: general access, helper and runner credentials are refused.
func TestBackupOperationsNeedTheAdministrator(t *testing.T) {
	fixture := newAPIFixture(t, true)
	setRunnerPolicy(t, fixture.store)
	_, runnerToken, _, err := fixture.store.IssueCheckRunnerToken(context.Background(), "project", "runner", "", time.Now())
	noErr(t, err)
	_, helperToken := helperAPI(t, fixture, "helper", time.Now())
	server := serve(t, fixture.app.Handler())
	base := server.URL + "/api/v1/backups"
	run := strings.Repeat("a", 32)
	routes := []struct{ method, path string }{
		{http.MethodGet, ""}, {http.MethodGet, "/runs"}, {http.MethodPost, "/runs"}, {http.MethodGet, "/schedule"}, {http.MethodPatch, "/schedule"},
		{http.MethodPost, "/runs/" + run + "/verify"}, {http.MethodGet, "/runs/" + run + "/archive"}, {http.MethodPut, "/upload"},
	}
	callers := map[string]func(*http.Request){
		"general": basicAuth("owngit", "shared-password"),
		"helper":  header("Authorization", "Bearer "+helperToken),
		"runner":  header("Authorization", "Bearer "+runnerToken),
	}
	for _, route := range routes {
		for caller, authorize := range callers {
			if code, errorCode := checkStatus(t, sendJSON(t, route.method, base+route.path, nil, authorize)); code != http.StatusUnauthorized {
				t.Errorf("%s %s as %s: %d %s", route.method, route.path, caller, code, errorCode)
			}
		}
	}
	if code, _ := checkStatus(t, adminAPIRequest(t, http.MethodPut, base+"/upload", nil, "not-the-password")); code != http.StatusUnauthorized {
		t.Fatalf("upload with a wrong administrator password: %d", code)
	}
	if code, errorCode := checkStatus(t, adminAPIRequest(t, http.MethodPost, base+"/runs/"+run+"/verify", nil, "admin-password")); code != http.StatusNotFound || errorCode != "backup_not_found" {
		t.Fatalf("verify an unknown run: %d %s", code, errorCode)
	}
	if code, errorCode := checkStatus(t, adminAPIRequest(t, http.MethodGet, base+"/runs/"+run+"/archive", nil, "admin-password")); code != http.StatusNotFound || errorCode != "backup_not_found" {
		t.Fatalf("download an unknown run: %d %s", code, errorCode)
	}
}

// An uploaded archive is refused, and nothing of it kept, unless it holds
// only what a backup holds and fits on the disk.
func TestBackupUploadRefusesWhatIsNoBackup(t *testing.T) {
	fixture := newAPIFixture(t, false)
	handler := fixture.app.Handler()
	send := func(body []byte, length int64) (int, string) {
		t.Helper()
		request := httptest.NewRequest(http.MethodPut, "/api/v1/backups/upload", bytes.NewReader(body))
		request.ContentLength, request.Host, request.RemoteAddr = length, "127.0.0.1", "127.0.0.1:40000"
		request.SetBasicAuth("admin", "admin-password")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return checkStatus(t, recorder.Result())
	}
	dir, repos := tarEntry{name: "b/", kind: tar.TypeDir}, tarEntry{name: "b/repositories/", kind: tar.TypeDir}
	manifest := tarEntry{name: "b/manifest.json", kind: tar.TypeReg, content: "{}"}
	bundle := tarEntry{name: "b/repositories/one.bundle", kind: tar.TypeReg, content: strings.Repeat("bundle", 400)}
	good := backupTar(t, dir, manifest, repos, bundle)
	cases := map[string][]byte{
		"truncated":     good[:3000],
		"parent path":   backupTar(t, dir, manifest, repos, tarEntry{name: "b/repositories/../../x.bundle", kind: tar.TypeReg, content: "x"}),
		"absolute path": backupTar(t, tarEntry{name: "/b/", kind: tar.TypeDir}),
		"link":          backupTar(t, dir, manifest, repos, tarEntry{name: "b/repositories/one.bundle", kind: tar.TypeSymlink, linkname: "/etc/passwd"}),
		"extra file":    backupTar(t, dir, manifest, tarEntry{name: "b/notes.txt", kind: tar.TypeReg, content: "x"}),
	}
	uploads := filepath.Join(fixture.store.Dir(), backups.UploadsFolder)
	for name, body := range cases {
		if code, errorCode := send(body, int64(len(body))); code != http.StatusBadRequest || errorCode != "invalid_backup_archive" {
			t.Errorf("%s: %d %s", name, code, errorCode)
		}
		if _, err := os.Lstat(uploads); !os.IsNotExist(err) {
			t.Fatalf("%s left %s: %v", name, uploads, err)
		}
	}
	if code, errorCode := send(good, 1<<62); code != http.StatusBadRequest || errorCode != "invalid_backup_archive" {
		t.Fatalf("larger than the disk: %d %s", code, errorCode)
	}
	if code, errorCode := send(good, -1); code != http.StatusLengthRequired || errorCode != "length_required" {
		t.Fatalf("no declared size: %d %s", code, errorCode)
	}
}
