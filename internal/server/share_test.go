package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// createdShare is what the owner API answers when it creates a link.
type createdShare struct {
	ShareLink struct {
		ID    string `json:"id"`
		State string `json:"state"`
	} `json:"share_link"`
	URL         string   `json:"url"`
	CloneURL    string   `json:"clone_url"`
	CloneSignIn string   `json:"clone_sign_in"`
	Warnings    []string `json:"warnings"`
}

func createShare(t *testing.T, server, repository string, input map[string]any) createdShare {
	t.Helper()
	response := adminAPIRequest(t, http.MethodPost, server+"/api/v1/repositories/"+repository+"/share-links", input, "admin-password")
	defer response.Body.Close()
	var created createdShare
	noErr(t, json.NewDecoder(response.Body).Decode(&created))
	if response.StatusCode != http.StatusCreated || created.ShareLink.ID == "" || !strings.HasPrefix(created.URL, server+"/share/") {
		t.Fatalf("create share link status=%d answer=%+v", response.StatusCode, created)
	}
	return created
}

// signInGeneral signs a browser in with the shared password and returns
// the answer's status.
func signInGeneral(t *testing.T, server string, client *http.Client, jar http.CookieJar) int {
	t.Helper()
	browserGET(t, client, server+"/login")
	values := url.Values{"csrf": {cookieValue(t, jar, server, preauthCookie)}, "password": {"shared-password"}, "next": {"/"}}
	return browserForm(t, client, server+"/login", values, server).status
}

// openShare follows a share link as a browser does and returns the client
// holding its cookie and the link's own address.
func openShare(t *testing.T, created createdShare) (*http.Client, string) {
	t.Helper()
	client, _ := newBrowserClient(t)
	opened := browserGET(t, client, created.URL)
	secret := strings.TrimPrefix(created.URL[strings.Index(created.URL, "/share/"):], "/share/")
	location := opened.header.Get("Location")
	cookie := opened.header.Get("Set-Cookie")
	if opened.status != http.StatusSeeOther || location != "/share/"+created.ShareLink.ID || strings.Contains(location, secret) ||
		opened.header.Get("Referrer-Policy") != "no-referrer" || opened.header.Get("Cache-Control") != "no-store" ||
		!strings.Contains(cookie, "Path=/share/"+created.ShareLink.ID) || !strings.Contains(cookie, "HttpOnly") || !strings.Contains(cookie, "SameSite=Lax") {
		t.Fatalf("opening the link status=%d location=%q headers=%v", opened.status, location, opened.header)
	}
	base, _ := url.Parse(created.URL)
	return client, base.Scheme + "://" + base.Host + location
}

// A browse link shows its repository's code, history and raw files, and
// nothing else: no dashboard, no other repository, no owner records such
// as pull requests, checks and kept history, and no clone. It keeps
// working after a rename and stops at once when revoked.
func TestShareLinkShowsOnlyItsRepositoryCode(t *testing.T) {
	serverLog := captureServerLog(t)
	fixture := newAPIFixture(t, true)
	server := serve(t, fixture.app.Handler())
	_, err := fixture.app.Repositories.Create(context.Background(), "other", "Another repository")
	noErr(t, err)
	// A branch that is deleted leaves its commit in kept history only.
	clone := filepath.Join(t.TempDir(), "clone")
	authenticated := strings.Replace(server.URL, "://", "://owngit:shared-password@", 1)
	apiRunGit(t, "", "clone", "-q", authenticated+"/git/project.git", clone)
	apiRunGit(t, clone, "config", "user.name", "Share Test")
	apiRunGit(t, clone, "config", "user.email", "share-test@example.invalid")
	apiRunGit(t, clone, "checkout", "-q", "-b", "gone")
	noErr(t, os.WriteFile(filepath.Join(clone, "gone.txt"), []byte("kept only\n"), 0o600))
	apiRunGit(t, clone, "add", ".")
	apiRunGit(t, clone, "commit", "-q", "-m", "only in kept history")
	apiRunGit(t, clone, "push", "-q", "origin", "gone")
	goneOID := apiGitOutput(t, clone, "rev-parse", "HEAD")
	apiRunGit(t, clone, "push", "-q", "origin", ":gone")

	created := createShare(t, server.URL, "project", map[string]any{"label": "Recruiter"})
	if created.CloneURL != "" || len(created.Warnings) != 1 {
		t.Fatalf("a browse link with an expiry answered clone=%q warnings=%v", created.CloneURL, created.Warnings)
	}
	secret := strings.TrimPrefix(created.URL, server.URL+"/share/")
	client, home := openShare(t, created)

	overview := browserGET(t, client, home)
	if overview.status != http.StatusOK || !strings.Contains(overview.body, "API fixture") || overview.header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("overview status=%d headers=%v", overview.status, overview.header)
	}
	pages := map[string]browserHTTPResult{"overview": overview}
	for name, path := range map[string]string{
		"file":    "/code?ref=refs%2Fheads%2Fmain&path=file.txt",
		"folder":  "/code",
		"commits": "/commits?ref=refs%2Fheads%2Ffeature",
		"commit":  "/commits/" + fixture.targetOID + "?ref=refs%2Fheads%2Fmain",
	} {
		page := browserGET(t, client, home+path)
		if page.status != http.StatusOK {
			t.Fatalf("%s status=%d", name, page.status)
		}
		pages[name] = page
	}
	if !strings.Contains(pages["file"].body, "base") || !strings.Contains(pages["commits"].body, "feature") {
		t.Fatal("the shared pages do not show the repository's content")
	}
	raw := browserGET(t, client, home+"/raw?ref=refs%2Fheads%2Fmain&path=file.txt")
	if raw.status != http.StatusOK || raw.body != "base\n" || !strings.HasPrefix(raw.header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("raw status=%d body=%q", raw.status, raw.body)
	}
	// Every link on a shared page stays inside the share.
	for name, page := range pages {
		for _, outside := range []string{`href="/repositories`, `href="/activity`, `href="/settings`, `href="/"`, "/git/project.git", "/pull-requests", "/restore", "/archive", secret, goneOID[:10]} {
			if strings.Contains(page.body, outside) {
				t.Errorf("%s page names %q", name, outside)
			}
		}
	}
	if strings.Contains(overview.body, webui.Text(webui.LangEN, webui.MsgRepoFactOpenPRs)) || strings.Contains(overview.body, webui.Text(webui.LangEN, webui.MsgRepoFactCheck)) {
		t.Error("the shared overview shows pull requests or checks")
	}

	for name, path := range map[string]string{
		"kept commit":     "/commits/" + goneOID,
		"kept commit ref": "/commits/" + goneOID + "?ref=refs%2Fheads%2Fmain",
		"pull requests":   "/pull-requests",
		"tasks":           "/tasks",
		"settings":        "/settings",
		"import":          "/import",
		"restore":         "/restore",
		"archive":         "/archive?ref=refs%2Fheads%2Fmain&format=zip",
		"delete":          "/delete",
	} {
		if page := browserGET(t, client, home+path); page.status != http.StatusNotFound {
			t.Errorf("%s status=%d", name, page.status)
		}
	}
	// The repository itself still holds that commit.
	if _, _, err := fixture.app.Repositories.CommitFiles(context.Background(), "project", goneOID); err != nil {
		t.Fatalf("the kept commit is missing from the repository: %v", err)
	}

	// The link reaches no dashboard page and no other repository, and its
	// cookie opens no other link.
	for _, path := range []string{"/", "/repositories/project", "/repositories/other", "/activity", "/api/v1/repositories"} {
		if page := browserGET(t, client, server.URL+path); page.status == http.StatusOK {
			t.Errorf("%s answered a share link's browser", path)
		}
	}
	other := createShare(t, server.URL, "other", map[string]any{"label": "Other"})
	request, _ := http.NewRequest(http.MethodGet, server.URL+"/share/"+other.ShareLink.ID, nil)
	request.AddCookie(&http.Cookie{Name: shareCookie, Value: secret})
	if page := browserRequest(t, &http.Client{}, request); page.status != http.StatusNotFound {
		t.Fatalf("one link's secret opened another link: %d", page.status)
	}
	// A browse link cannot clone.
	refs, _ := http.NewRequest(http.MethodGet, server.URL+"/share/"+created.ShareLink.ID+".git/info/refs?service=git-upload-pack", nil)
	refs.SetBasicAuth("anyone", secret)
	if answer := browserRequest(t, &http.Client{}, refs); answer.status != http.StatusForbidden {
		t.Fatalf("a browse link's Git request status=%d", answer.status)
	}

	// A rename keeps the link; revoking it ends it.
	status, _, _ := renameAPI(t, server.URL, "project", "Renamed", "admin-password")
	if status != http.StatusOK {
		t.Fatalf("rename status=%d", status)
	}
	if page := browserGET(t, client, home); page.status != http.StatusOK || !strings.Contains(page.body, "Renamed") {
		t.Fatalf("after a rename the link status=%d", page.status)
	}
	revoke := adminAPIRequest(t, http.MethodPost, server.URL+"/api/v1/repositories/renamed/share-links/"+created.ShareLink.ID+"/revoke", nil, "admin-password")
	revoke.Body.Close()
	if revoke.StatusCode != http.StatusOK {
		t.Fatalf("revoke status=%d", revoke.StatusCode)
	}
	for _, target := range []string{home, created.URL} {
		page := browserGET(t, client, target)
		if page.status != http.StatusNotFound || !strings.Contains(page.body, webui.Text(webui.LangEN, webui.MsgShareLinkNotFound)) {
			t.Fatalf("revoked link %s status=%d", target, page.status)
		}
	}
	if strings.Contains(serverLog.String(), secret) {
		t.Fatal("the server log holds a share link's secret")
	}
}

// A clone link clones and fetches with its secret as the password and
// refuses pushes. A link with an extra password takes the secret as the
// user name and that password as the password; a wrong one is refused as
// any failed Git sign-in is.
func TestShareCloneLinkFetchesAndRefusesPushes(t *testing.T) {
	fixture := newAPIFixture(t, true)
	server := serve(t, fixture.app.Handler())
	plain := createShare(t, server.URL, "project", map[string]any{"label": "Contractor", "scope": "clone", "until_revoked": true})
	if plain.CloneURL != server.URL+"/share/"+plain.ShareLink.ID+".git" || len(plain.Warnings) != 3 {
		t.Fatalf("clone link answer=%+v", plain)
	}
	secret := strings.TrimPrefix(plain.URL, server.URL+"/share/")
	clone := filepath.Join(t.TempDir(), "clone")
	apiRunGit(t, "", "clone", "-q", strings.Replace(plain.CloneURL, "://", "://visitor:"+secret+"@", 1), clone)
	if apiGitOutput(t, clone, "rev-parse", "origin/main") != fixture.targetOID || apiGitOutput(t, clone, "rev-parse", "origin/feature") != fixture.sourceOID {
		t.Fatal("the clone does not hold the repository's branches")
	}
	apiRunGit(t, clone, "fetch", "-q", "origin")
	apiRunGit(t, clone, "config", "user.name", "Share Test")
	apiRunGit(t, clone, "config", "user.email", "share-test@example.invalid")
	// A commit that only kept history holds cannot be fetched by its ID.
	owner := filepath.Join(t.TempDir(), "owner")
	apiRunGit(t, "", "clone", "-q", strings.Replace(server.URL, "://", "://owngit:shared-password@", 1)+"/git/project.git", owner)
	apiRunGit(t, owner, "-c", "user.name=Share Test", "-c", "user.email=share-test@example.invalid", "commit", "-q", "--allow-empty", "-m", "kept only")
	apiRunGit(t, owner, "push", "-q", "origin", "HEAD:refs/heads/gone")
	goneOID := apiGitOutput(t, owner, "rev-parse", "HEAD")
	apiRunGit(t, owner, "push", "-q", "origin", ":refs/heads/gone")
	for _, protocol := range []string{"version=0", "version=2"} {
		if output, err := gitCombined(clone, "-c", "protocol."+protocol, "fetch", "origin", goneOID); err == nil {
			t.Fatalf("a kept commit was fetched through a share link with protocol %s:\n%s", protocol, output)
		}
	}
	noErr(t, os.WriteFile(filepath.Join(clone, "new.txt"), []byte("new\n"), 0o600))
	apiRunGit(t, clone, "add", ".")
	apiRunGit(t, clone, "commit", "-q", "-m", "not allowed")
	if output, err := gitCombined(clone, "push", "origin", "HEAD:refs/heads/main"); err == nil || !strings.Contains(output, "403") {
		t.Fatalf("a push through a share link was not refused: %v\n%s", err, output)
	}
	if head := apiGitOutput(t, fixture.remote, "rev-parse", "refs/heads/main"); head != fixture.targetOID {
		t.Fatal("the push changed the repository")
	}
	receive, _ := http.NewRequest(http.MethodGet, plain.CloneURL+"/info/refs?service=git-receive-pack", nil)
	receive.SetBasicAuth("visitor", secret)
	if answer := browserRequest(t, &http.Client{}, receive); answer.status != http.StatusForbidden {
		t.Fatalf("a push advertisement status=%d", answer.status)
	}

	guarded := createShare(t, server.URL, "project", map[string]any{"label": "Guarded", "scope": "clone", "password": "link-password"})
	guardedSecret := strings.TrimPrefix(guarded.URL, server.URL+"/share/")
	withUser := strings.Replace(guarded.CloneURL, "://", "://"+guardedSecret+":link-password@", 1)
	apiRunGit(t, "", "clone", "-q", withUser, filepath.Join(t.TempDir(), "guarded"))
	for name, credentials := range map[string][2]string{
		"secret as password": {"visitor", guardedSecret},
		"wrong password":     {guardedSecret, "wrong-password"},
		"another link":       {secret, "link-password"},
	} {
		refs, _ := http.NewRequest(http.MethodGet, guarded.CloneURL+"/info/refs?service=git-upload-pack", nil)
		refs.SetBasicAuth(credentials[0], credentials[1])
		answer := browserRequest(t, &http.Client{}, refs)
		if answer.status != http.StatusUnauthorized || answer.header.Get("WWW-Authenticate") == "" {
			t.Errorf("%s: status=%d", name, answer.status)
		}
	}
	// The link's ID alone, or a secret for another link's ID, is not a
	// credential.
	other, _ := http.NewRequest(http.MethodGet, server.URL+"/share/"+guarded.ShareLink.ID+".git/info/refs?service=git-upload-pack", nil)
	other.SetBasicAuth("visitor", secret)
	if answer := browserRequest(t, &http.Client{}, other); answer.status != http.StatusUnauthorized {
		t.Fatalf("a secret opened another link's Git address: %d", answer.status)
	}
}

// A clone link fetches branches and tags only, in either object format:
// not a ref in an extra namespace such as refs/notes/, and no commit by its
// ID alone, also when the repository's own config allows such wants. Its
// pages name no such ref or commit either. The owner still sees every ref.
func TestShareCloneLinkServesOnlyBranchesAndTags(t *testing.T) {
	fixture := newAPIFixture(t, true)
	server := serve(t, fixture.app.Handler())
	ctx := context.Background()
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			name := "refs-" + format
			_, err := fixture.app.Repositories.CreateWithOptions(ctx, name, "", repository.CreateOptions{ObjectFormat: format})
			noErr(t, err)
			prefixes := []string{"refs/notes/"}
			_, err = fixture.store.SaveRepositoryRefPolicy(ctx, name, state.RepositoryRefPolicyChange{ExtraRefPrefixes: &prefixes})
			noErr(t, err)
			ownerRemote := strings.Replace(server.URL, "://", "://owngit:shared-password@", 1) + "/git/" + name + ".git"
			owner := filepath.Join(t.TempDir(), "owner")
			apiRunGit(t, "", "init", "-q", "--object-format="+format, "--initial-branch=main", owner)
			for _, setting := range [][2]string{{"user.name", "Share Test"}, {"user.email", "share-test@example.invalid"}} {
				apiRunGit(t, owner, "config", setting[0], setting[1])
			}
			apiRunGit(t, owner, "commit", "-q", "--allow-empty", "-m", "base")
			apiRunGit(t, owner, "tag", "v1")
			apiRunGit(t, owner, "push", "-q", ownerRemote, "main", "v1")
			apiRunGit(t, owner, "commit", "-q", "--allow-empty", "-m", "kept only")
			keptOID := apiGitOutput(t, owner, "rev-parse", "HEAD")
			apiRunGit(t, owner, "push", "-q", ownerRemote, "HEAD:refs/heads/gone")
			apiRunGit(t, owner, "push", "-q", ownerRemote, ":refs/heads/gone")
			apiRunGit(t, owner, "checkout", "-q", "--orphan", "note")
			apiRunGit(t, owner, "commit", "-q", "--allow-empty", "-m", "extra namespace only")
			noteOID := apiGitOutput(t, owner, "rev-parse", "HEAD")
			apiRunGit(t, owner, "push", "-q", ownerRemote, "HEAD:refs/notes/private")
			if refs := apiGitOutput(t, "", "ls-remote", ownerRemote); !strings.Contains(refs, "refs/notes/private") {
				t.Fatalf("the owner does not see the extra ref:\n%s", refs)
			}

			created := createShare(t, server.URL, name, map[string]any{"label": "Contractor", "scope": "clone"})
			secret := strings.TrimPrefix(created.URL, server.URL+"/share/")
			shareRemote := strings.Replace(created.CloneURL, "://", "://visitor:"+secret+"@", 1)
			for _, protocol := range []string{"0", "2"} {
				for _, line := range strings.Split(apiGitOutput(t, "", "-c", "protocol.version="+protocol, "ls-remote", shareRemote), "\n") {
					_, ref, _ := strings.Cut(line, "\t")
					if ref != "HEAD" && !strings.HasPrefix(ref, "refs/heads/") && !strings.HasPrefix(ref, "refs/tags/") {
						t.Errorf("protocol %s advertises %q", protocol, ref)
					}
				}
			}
			clone := filepath.Join(t.TempDir(), "clone")
			apiRunGit(t, "", "clone", "-q", shareRemote, clone)
			repositoryPath, err := fixture.app.Repositories.Path(name)
			noErr(t, err)
			for _, allow := range []string{"", "uploadpack.allowTipSHA1InWant", "uploadpack.allowReachableSHA1InWant", "uploadpack.allowAnySHA1InWant"} {
				if allow != "" {
					apiRunGit(t, "", "--git-dir", repositoryPath, "config", allow, "true")
				}
				for _, protocol := range []string{"0", "2"} {
					for want, what := range map[string]string{"refs/notes/private": "an extra ref", noteOID: "an extra ref's commit", keptOID: "a kept commit"} {
						if output, err := gitCombined(clone, "-c", "protocol.version="+protocol, "fetch", "origin", "+"+want+":refs/fetched/x"); err == nil {
							t.Errorf("%s was fetched with %q set and protocol %s:\n%s", what, allow, protocol, output)
						}
					}
					apiRunGit(t, clone, "-c", "protocol.version="+protocol, "fetch", "-q", "origin", "main", "tag", "v1")
				}
				if allow != "" {
					apiRunGit(t, "", "--git-dir", repositoryPath, "config", "--unset", allow)
				}
			}

			client, home := openShare(t, created)
			for _, path := range []string{"/commits/" + noteOID, "/commits/" + keptOID, "/code?ref=refs%2Fnotes%2Fprivate", "/commits?ref=refs%2Fnotes%2Fprivate"} {
				if page := browserGET(t, client, home+path); page.status != http.StatusNotFound || strings.Contains(page.body, "extra namespace only") {
					t.Errorf("%s status=%d", path, page.status)
				}
			}
		})
	}
}

// A link with an extra password names nothing before the password is
// right. Wrong passwords are counted per address apart from sign-in, and
// the pause they start does not stop the owner's own sign-in.
func TestSharePasswordLinkAsksForItsPassword(t *testing.T) {
	fixture := newAPIFixture(t, true)
	server := serve(t, fixture.app.Handler())
	created := createShare(t, server.URL, "project", map[string]any{"label": "Guarded", "password": "link-password"})
	client, home := openShare(t, created)

	form := browserGET(t, client, home+"/code")
	if form.status != http.StatusForbidden || !strings.Contains(form.body, `name="share_password"`) || strings.Contains(form.body, "project") ||
		strings.Contains(form.body, "API fixture") || form.header.Get("Referrer-Policy") != "same-origin" {
		t.Fatalf("password form status=%d headers=%v", form.status, form.header)
	}
	wrong := browserForm(t, client, home+"/code", url.Values{"share_password": {"wrong-password"}}, server.URL)
	if wrong.status != http.StatusForbidden || !strings.Contains(wrong.body, webui.Text(webui.LangEN, webui.MsgSharePasswordWrong)) {
		t.Fatalf("wrong password status=%d", wrong.status)
	}
	right := browserForm(t, client, home+"/code", url.Values{"share_password": {"link-password"}}, server.URL)
	if right.status != http.StatusSeeOther || right.header.Get("Location") != "/share/"+created.ShareLink.ID+"/code" {
		t.Fatalf("right password status=%d location=%q", right.status, right.header.Get("Location"))
	}
	if page := browserGET(t, client, home+"/code"); page.status != http.StatusOK || !strings.Contains(page.body, "file.txt") {
		t.Fatalf("after the password status=%d", page.status)
	}

	// Another browser with the link but without the password is paused
	// after the saved number of wrong passwords.
	stranger, _ := openShare(t, created)
	status := 0
	for attempt := 0; attempt < 5; attempt++ {
		status = browserForm(t, stranger, home, url.Values{"share_password": {"wrong-password"}}, server.URL).status
	}
	if status != http.StatusTooManyRequests {
		t.Fatalf("after repeated wrong passwords status=%d", status)
	}
	if answer := browserForm(t, stranger, home, url.Values{"share_password": {"link-password"}}, server.URL); answer.status != http.StatusTooManyRequests {
		t.Fatalf("a paused address was let in: %d", answer.status)
	}
	if page := browserGET(t, client, home); page.status != http.StatusOK {
		t.Fatalf("a browser that gave the password was stopped: %d", page.status)
	}
	signIn, jar := newBrowserClient(t)
	if status := signInGeneral(t, server.URL, signIn, jar); status != http.StatusSeeOther {
		t.Fatalf("the owner's sign-in was paused by share link guesses: %d", status)
	}
}

// Only an administrator manages share links: the screen and the owner API
// ask for the administrator, every change needs the session's CSRF token,
// and the new link is shown once.
func TestShareLinksAreManagedByTheAdministrator(t *testing.T) {
	fixture := newAPIFixture(t, true)
	server := serve(t, fixture.app.Handler())
	client, jar := newBrowserClient(t)
	if status := signInGeneral(t, server.URL, client, jar); status != http.StatusSeeOther {
		t.Fatalf("sign in status=%d", status)
	}
	if page := browserGET(t, client, server.URL+"/repositories/project/share-links"); page.status == http.StatusOK {
		t.Fatal("general access opened the Share links screen")
	}
	// A helper token is a repository's own credential, not the administrator.
	helperHash := sha256.Sum256([]byte("synthetic-helper-token"))
	_, _, err := fixture.store.CreateHelperCredential(context.Background(), "project", "helper", "", helperHash[:], time.Now())
	noErr(t, err)
	for name, password := range map[string]string{"general access": "shared-password", "wrong password": "wrong-password", "helper token": "synthetic-helper-token"} {
		response := adminAPIRequest(t, http.MethodGet, server.URL+"/api/v1/repositories/project/share-links", nil, password)
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: API status=%d", name, response.StatusCode)
		}
	}

	signInAdmin(t, fixture, server.URL, jar)
	screen := browserGET(t, client, server.URL+"/repositories/project/share-links")
	if screen.status != http.StatusOK || !strings.Contains(screen.body, `value="30" selected`) {
		t.Fatalf("screen status=%d", screen.status)
	}
	values := url.Values{"action": {webui.ActionCreateShareLink}, "label": {"Reviewer"}, "scope": {"clone"}, "expiry": {"never"}, "csrf": {"wrong"}}
	if refused := browserForm(t, client, server.URL+"/repositories/project/share-links", values, server.URL); refused.status != http.StatusForbidden {
		t.Fatalf("create without the CSRF token status=%d", refused.status)
	}
	values.Set("csrf", adminTestCSRF)
	values.Set("share_password", "short")
	if refused := browserForm(t, client, server.URL+"/repositories/project/share-links", values, server.URL); refused.status != http.StatusUnprocessableEntity || !strings.Contains(refused.body, `value="Reviewer"`) {
		t.Fatalf("a too short extra password status=%d", refused.status)
	}
	values.Del("share_password")
	created := browserForm(t, client, server.URL+"/repositories/project/share-links", values, server.URL)
	if created.status != http.StatusOK || !strings.Contains(created.body, server.URL+"/share/") || !strings.Contains(created.body, "/share/") ||
		!strings.Contains(created.body, webui.Text(webui.LangEN, webui.MsgShareWarnNever)) {
		t.Fatalf("create status=%d", created.status)
	}
	links, err := fixture.store.ShareLinks(context.Background(), "project")
	noErr(t, err)
	if len(links) != 1 || links[0].Scope != "clone" || links[0].ExpiresAt != nil || links[0].CreatedBy.Kind != "administrator" {
		t.Fatalf("links=%+v", links)
	}
	if again := browserGET(t, client, server.URL+"/repositories/project/share-links"); strings.Contains(again.body, server.URL+"/share/") {
		t.Fatal("the screen shows a link's secret again")
	}
	revoke := url.Values{"action": {webui.ActionRevokeShareLink}, "link_id": {links[0].ID}, "csrf": {adminTestCSRF}}
	if done := browserForm(t, client, server.URL+"/repositories/project/share-links", revoke, server.URL); done.status != http.StatusSeeOther {
		t.Fatalf("revoke status=%d", done.status)
	}
	if again := browserForm(t, client, server.URL+"/repositories/project/share-links", revoke, server.URL); again.status != http.StatusConflict {
		t.Fatalf("second revoke status=%d", again.status)
	}
}

// The secret in a link's address never reaches the log, also when opening
// the link fails.
func TestShareLinkSecretStaysOutOfTheLog(t *testing.T) {
	serverLog := captureServerLog(t)
	app := newConfiguredApp(t)
	server := serve(t, app.Handler())
	noErr(t, app.Store.Exec(context.Background(), `DROP TABLE share_links`))
	const secret = "not-a-real-share-link-secret"
	client, _ := newBrowserClient(t)
	answer := browserGET(t, client, server.URL+"/share/"+secret)
	endFailureWindows()
	if answer.status != http.StatusServiceUnavailable || strings.Contains(answer.body, secret) {
		t.Fatalf("status=%d", answer.status)
	}
	if logged := serverLog.String(); !strings.Contains(logged, "GET /share/: share link read") || strings.Contains(logged, secret) {
		t.Fatalf("log:\n%s", logged)
	}
}

// An answer that comes before the share link is looked up, such as a
// refused Host or a settings read that failed, sends no Referer either:
// the browser's address still holds the secret. Other pages keep theirs.
func TestShareAddressSendsNoRefererAlsoWhenRefusedEarly(t *testing.T) {
	fixture := newAPIFixture(t, false)
	answer := func(target, host string, header http.Header) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		if host != "" {
			request.Host = host
		}
		for name, values := range header {
			request.Header[name] = values
		}
		request.RemoteAddr = "127.0.0.1:12345"
		recorder := httptest.NewRecorder()
		fixture.app.Handler().ServeHTTP(recorder, request)
		return recorder
	}
	const opening = "http://127.0.0.1/share/synthetic-share-secret"
	if other := answer("http://127.0.0.1/", "", nil); other.Header().Get("Referrer-Policy") != "same-origin" {
		t.Fatalf("dashboard policy=%q", other.Header().Get("Referrer-Policy"))
	}
	for name, got := range map[string]*httptest.ResponseRecorder{
		"refused Host":   answer(opening, "unapproved.example.invalid", nil),
		"Funnel request": answer(opening, "", http.Header{"Tailscale-Funnel-Request": {"?1"}}),
	} {
		if got.Code < 400 || got.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s: status=%d policy=%q", name, got.Code, got.Header().Get("Referrer-Policy"))
		}
	}
	noErr(t, fixture.store.Exec(context.Background(), `DROP TABLE metadata`))
	if got := answer(opening, "", nil); got.Code != http.StatusServiceUnavailable || got.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("settings read failure: status=%d policy=%q", got.Code, got.Header().Get("Referrer-Policy"))
	}
}
