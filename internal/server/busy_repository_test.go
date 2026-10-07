package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/gitexec"
	"owngit/internal/hostmem"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// A Git operation that holds one repository, like a push queued behind a
// long clone, must not stall the dashboard or other repositories, and a page
// of the busy repository answers with an explanation before its deadline
// instead of a dropped connection.
func TestBusyRepositoryDoesNotStallPagesPastTheirDeadline(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the 3 second page deadline")
	}
	app := newConfiguredApp(t)
	app.HTTPTimeout = 3 * time.Second
	addActivityRepository(t, app, "busy", 2)
	addActivityRepository(t, app, "free", 2)
	server := serve(t, app.Handler())
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 20 * time.Second}
	get := func(target string) (int, string, http.Header, time.Duration) {
		t.Helper()
		started := time.Now()
		response, err := client.Get(server.URL + target)
		noErr(t, err)
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		noErr(t, err)
		return response.StatusCode, string(body), response.Header, time.Since(started)
	}
	lock := app.Repositories.Locks.For("busy")
	held := false
	hold := func() { lock.Lock(); held = true }
	release := func() {
		if held {
			held = false
			lock.Unlock()
		}
	}
	t.Cleanup(release)

	// Never listed: the dashboard shows the repository as in use.
	hold()
	status, body, _, elapsed := get("/")
	if status != http.StatusOK || !strings.Contains(body, "In use") || !strings.Contains(body, "free") || elapsed > 2*time.Second {
		t.Fatalf("dashboard status=%d in %s, in use shown=%v", status, elapsed, strings.Contains(body, "In use"))
	}
	status, body, header, elapsed := get("/repositories/busy")
	if status != http.StatusServiceUnavailable || !strings.Contains(body, "is using the repository") || header.Get("Retry-After") != "10" || elapsed > app.HTTPTimeout {
		t.Fatalf("busy repository page status=%d in %s retry=%q", status, elapsed, header.Get("Retry-After"))
	}
	if status, _, _, _ = get("/repositories/free"); status != http.StatusOK {
		t.Fatalf("free repository page status=%d", status)
	}
	status, body, _, _ = get("/api/v1/repositories/busy/pull-requests")
	var problem struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	noErr(t, json.Unmarshal([]byte(body), &problem))
	if status != http.StatusServiceUnavailable || problem.Error.Code != "repository_busy" {
		t.Fatalf("API status=%d body=%s", status, body)
	}
	release()

	// Listed before a write: the dashboard uses that listing.
	if status, _, _, _ = get("/"); status != http.StatusOK {
		t.Fatalf("dashboard status=%d", status)
	}
	hold()
	release()
	hold()
	status, body, _, elapsed = get("/")
	if status != http.StatusOK || strings.Contains(body, "In use") || !strings.Contains(body, "/repositories/busy") || elapsed > 2*time.Second {
		t.Fatalf("dashboard with an earlier listing status=%d in %s, in use shown=%v", status, elapsed, strings.Contains(body, "In use"))
	}
	release()

	// A current listing needs no lock, but the page's other reads do: the
	// page still explains the wait instead of showing missing data. A page
	// whose Git reads are all cached by object ID needs no lock either and
	// shows the refs from before the running operation.
	if status, _, _, _ = get("/repositories/busy/code"); status != http.StatusOK {
		t.Fatalf("code page status=%d", status)
	}
	hold()
	status, body, _, elapsed = get("/repositories/busy/commits")
	if status != http.StatusServiceUnavailable || !strings.Contains(body, "is using the repository") || elapsed > app.HTTPTimeout {
		t.Fatalf("commits page of a busy repository status=%d in %s", status, elapsed)
	}
	status, _, _, elapsed = get("/repositories/busy/code")
	if status != http.StatusOK || elapsed > 2*time.Second {
		t.Fatalf("cached code page of a busy repository status=%d in %s", status, elapsed)
	}
	// The overview's reads, the language count included, wait no longer
	// than the page's deadline: the page explains the wait.
	status, body, _, elapsed = get("/repositories/busy")
	if status != http.StatusServiceUnavailable || !strings.Contains(body, "is using the repository") || elapsed > app.HTTPTimeout {
		t.Fatalf("overview of a busy repository status=%d in %s", status, elapsed)
	}
	release()
	if status, _, _, _ = get("/repositories/busy/commits"); status != http.StatusOK {
		t.Fatalf("commits page after the operation status=%d", status)
	}
	if status, _, _, _ = get("/repositories/busy"); status != http.StatusOK {
		t.Fatalf("overview after the operation status=%d", status)
	}
}

// The dashboard's fallback for a busy repository shows neither a repository
// deleted while the page waited, whether from its last listing or as in use,
// nor an older listing of a repository whose last read failed.
func TestBusyDashboardFallbackKeepsDeletionsAndUnreadableRepositories(t *testing.T) {
	if testing.Short() {
		t.Skip("holds locks while the dashboard waits out its page timeout")
	}
	app := newConfiguredApp(t)
	app.HTTPTimeout = 5 * time.Second
	for _, name := range []string{"listed", "unlisted", "broken"} {
		addActivityRepository(t, app, name, 1)
	}
	server := serve(t, app.Handler())
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 20 * time.Second}
	body, status := dashboardGET(t, client, server.URL+"/")
	if status != http.StatusOK {
		t.Fatalf("dashboard status=%d", status)
	}

	// broken is listed, then its refs become unreadable after a write.
	brokenPath, err := app.Repositories.Path("broken")
	noErr(t, err)
	noErr(t, os.WriteFile(filepath.Join(brokenPath, "packed-refs"), []byte("not a packed ref\n"), 0o600))
	brokenLock := app.Repositories.Locks.For("broken")
	brokenLock.Lock()
	brokenLock.Unlock()
	body, _ = dashboardGET(t, client, server.URL+"/")
	if row, _ := dashboardRow(body, "broken"); !strings.Contains(row, "Unreadable") {
		t.Fatalf("the unreadable repository is not marked: %s", row)
	}
	// While a Git operation holds it, it stays marked unreadable instead of
	// showing its listing from before or In use.
	brokenLock.Lock()
	body, _ = dashboardGET(t, client, server.URL+"/")
	brokenLock.UnlockWithoutRefChanges()
	if row, _ := dashboardRow(body, "broken"); !strings.Contains(row, "Unreadable") || strings.Contains(row, "In use") || strings.Contains(row, "row__refname") {
		t.Fatalf("the busy unreadable repository is not marked unreadable: %s", row)
	}

	// listed has a listing from before, so the page would show it from that
	// listing; unlisted has none, so it would be In use. Both are deleted
	// while the page waits for them.
	root, err := app.Repositories.CanonicalStorageRoot()
	noErr(t, err)
	app.Repositories.ForgetRefSnapshots([]string{"listed", "broken"})
	for _, name := range []string{"listed", "unlisted"} {
		// A write since the listing: the page cannot use it as current.
		lock := app.Repositories.Locks.For(name)
		lock.Lock()
		lock.Unlock()
		lock.Lock()
		defer lock.UnlockWithoutRefChanges()
	}
	done := make(chan string, 1)
	go func() {
		body, _ := dashboardGET(t, client, server.URL+"/")
		done <- body
	}()
	waitUntil(t, func() bool {
		return app.Repositories.Locks.For("listed").Waiting() && app.Repositories.Locks.For("unlisted").Waiting()
	})
	for _, name := range []string{"listed", "unlisted"} {
		// The commit point of a deletion, taken while the deletion holds the
		// repository lock.
		noErr(t, app.Store.BeginRepositoryDeletion(context.Background(), state.RepositoryDeletion{
			RepositoryID: name, Mode: state.RepositoryDeletionKeepFiles, Root: root,
			Moved: ".owngit-removed/" + name + "-20270115T080000Z.git", Marker: strings.Repeat("d", 32), CreatedAt: time.Now(),
		}))
	}
	body = <-done
	for _, name := range []string{"listed", "unlisted"} {
		if _, listed := dashboardRow(body, name); listed {
			t.Fatalf("the dashboard lists %s, deleted while it waited", name)
		}
	}
}

// A read that finds the memory this computer gives Git in use by other Git
// work answers with a retryable busy page, not with a repository that cannot
// be read: the same request succeeds shortly, and it says so.
func TestPageReadWithoutAMemorySlotAnswersBusy(t *testing.T) {
	app := newConfiguredApp(t)
	addActivityRepository(t, app, "bounded", 1)
	server := serve(t, app.Handler())
	client := &http.Client{}
	previousGate := hostmem.Shared.Load()
	gate := hostmem.NewGate(1)
	hostmem.Shared.Store(gate)
	held, err := gate.Acquire(context.Background())
	noErr(t, err)
	previousWait := gitexec.ReadSlotWait
	gitexec.ReadSlotWait = 100 * time.Millisecond
	t.Cleanup(func() {
		hostmem.Shared.Store(previousGate)
		gitexec.ReadSlotWait = previousWait
	})

	for _, target := range []string{"/code?ref=refs%2Fheads%2Fmain&path=file.txt", "/raw?ref=refs%2Fheads%2Fmain&path=file.txt"} {
		result := browserGET(t, client, server.URL+"/repositories/bounded"+target)
		if result.status != http.StatusServiceUnavailable || result.header.Get("Retry-After") != "10" {
			t.Fatalf("%s without a memory slot: status=%d Retry-After=%q", target, result.status, result.header.Get("Retry-After"))
		}
		for _, language := range []webui.Lang{webui.LangEN, webui.LangKO} {
			if !strings.Contains(result.body, webui.Text(language, webui.MsgRepoBusyMemory)) {
				t.Fatalf("%s does not say the computer is busy with other Git work in %s:\n%s", target, language, result.body)
			}
		}
	}
	held()
	result := browserGET(t, client, server.URL+"/repositories/bounded/code?ref=refs%2Fheads%2Fmain&path=file.txt")
	if result.status != http.StatusOK {
		t.Fatalf("the page with a free memory slot: status=%d", result.status)
	}
}
