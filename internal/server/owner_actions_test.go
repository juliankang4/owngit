package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/state"
)

// The owner API routes for what Settings, the repository Settings tab, the
// delete page and the Automatic checks page change. Each is administrator
// only, applies the same rule as its page and answers a refused or failed
// change as an error.

// ownerRoutes are the added administrator routes with a valid body.
var ownerRoutes = []struct {
	method, path string
	body         any
}{
	{http.MethodPut, "/api/v1/settings/access", map[string]string{"mode": "password", "password": "new-shared-password"}},
	{http.MethodPut, "/api/v1/settings/admin-password", map[string]string{"password": "new-admin-password"}},
	{http.MethodPut, "/api/v1/settings/admin-confirmation", map[string]string{"admin_confirmation": "1h"}},
	{http.MethodPost, "/api/v1/repositories/project/default-branch", map[string]string{"branch": "feature"}},
	{http.MethodPost, "/api/v1/repositories/project/delete", map[string]string{"mode": "keep_files", "confirm_name": "project"}},
}

func TestOwnerRoutesRefuseEveryoneButTheAdministrator(t *testing.T) {
	fixture := newAPIFixture(t, true)
	// Every route is tried with wrong passwords, more than the default
	// login limits allow from one address.
	noErr(t, fixture.store.SavePolicies(context.Background(), state.PolicyChange{LoginLimits: &state.LoginLimits{Attempts: 100, Window: time.Minute, Pause: time.Minute}}))
	base, helperToken := helperAPI(t, fixture, "helper", time.Now())
	origin := strings.TrimSuffix(base, "/api/v1/repositories/project")
	for _, route := range ownerRoutes {
		for name, credential := range map[string]func(*http.Request){
			"none":            func(*http.Request) {},
			"shared":          basicAuth("owngit", "shared-password"),
			"shared as admin": basicAuth("admin", "shared-password"),
			"helper":          header("Authorization", "Bearer "+helperToken),
			"wrong admin":     basicAuth("admin", "wrong-password"),
		} {
			response := sendJSON(t, route.method, origin+route.path, route.body, credential)
			if status, code := checkStatus(t, response); status != http.StatusUnauthorized || !strings.Contains(code, "admin") {
				t.Fatalf("%s %s with %s credentials: status=%d code=%q", route.method, route.path, name, status, code)
			}
		}
	}
	// Nothing changed: the passwords, the choice, the default branch and the
	// repository are as they were.
	assertRepositoryIntact(t, fixture, "refused owner routes")
	settings, err := fixture.store.Settings(context.Background())
	noErr(t, err)
	accessHash, err := fixture.store.PasswordHash(context.Background(), "access")
	noErr(t, err)
	adminHash, err := fixture.store.PasswordHash(context.Background(), "admin")
	noErr(t, err)
	if settings.AccessMode != "password" || !auth.CheckPassword(accessHash, "shared-password") || !auth.CheckPassword(adminHash, "admin-password") {
		t.Fatal("a refused request changed a password")
	}
	if choice, _, err := fixture.store.AdminConfirmation(context.Background()); err != nil || choice != state.DefaultAdminConfirmation {
		t.Fatalf("confirmation=%q err=%v", choice, err)
	}
	if head := apiGitOutput(t, fixture.remote, "symbolic-ref", "HEAD"); head != "refs/heads/main" {
		t.Fatalf("default branch=%q", head)
	}
}

func TestOwnerAccessAPIFollowsTheSettingsRules(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server := serve(t, fixture.app.Handler())
	put := func(body any) *http.Response {
		return adminAPIRequest(t, http.MethodPut, server.URL+"/api/v1/settings/access", body, "admin-password")
	}
	for name, refused := range map[string]struct {
		body   map[string]string
		status int
		code   string
	}{
		"no password to turn on": {map[string]string{"mode": "password"}, http.StatusBadRequest, "invalid_settings"},
		"unknown mode":           {map[string]string{"mode": "closed"}, http.StatusBadRequest, "invalid_settings"},
		"open with a password":   {map[string]string{"mode": "open", "password": "new-shared-password"}, http.StatusBadRequest, "invalid_settings"},
		"too short":              {map[string]string{"mode": "password", "password": "short"}, http.StatusUnprocessableEntity, "invalid_password"},
		"the admin password":     {map[string]string{"mode": "password", "password": "admin-password"}, http.StatusUnprocessableEntity, "invalid_password"},
	} {
		if status, code := checkStatus(t, put(refused.body)); status != refused.status || code != refused.code {
			t.Fatalf("%s: status=%d code=%q", name, status, code)
		}
	}
	if settings, err := fixture.store.Settings(context.Background()); err != nil || settings.AccessMode != "open" {
		t.Fatalf("a refused change turned the shared password on: %+v err=%v", settings, err)
	}

	answer := decodeAPIObject(t, put(map[string]string{"mode": "password", "password": "new-shared-password"}))
	if answer["ok"] != true || answer["access_mode"] != "password" || answer["changed"] != true {
		t.Fatalf("turning on answered %v", answer)
	}
	accessHash, err := fixture.store.PasswordHash(context.Background(), "access")
	noErr(t, err)
	if !auth.CheckPassword(accessHash, "new-shared-password") {
		t.Fatal("the shared password was not saved")
	}
	// The general API now asks for it.
	if status, _ := checkStatus(t, sendJSON(t, http.MethodGet, server.URL+"/api/v1/repositories", nil)); status != http.StatusUnauthorized {
		t.Fatalf("general API without the shared password: status=%d", status)
	}
	if answer := decodeAPIObject(t, put(map[string]string{"mode": "password"})); answer["changed"] != false {
		t.Fatalf("keeping the shared password answered %v", answer)
	}
	if answer := decodeAPIObject(t, put(map[string]string{"mode": "open"})); answer["access_mode"] != "open" || answer["changed"] != true {
		t.Fatalf("turning off answered %v", answer)
	}
	shown := decodeAPIObject(t, adminAPIRequest(t, http.MethodGet, server.URL+"/api/v1/settings", nil, "admin-password"))
	if shown["access_mode"] != "open" || shown["admin_confirmation"] != string(state.DefaultAdminConfirmation) {
		t.Fatalf("settings show %v", shown)
	}
}

func TestOwnerAdminPasswordAPIReplacesThePassword(t *testing.T) {
	fixture := newAPIFixture(t, true)
	server := serve(t, fixture.app.Handler())
	target := server.URL + "/api/v1/settings/admin-password"
	for name, refused := range map[string]string{
		"too short":       "short",
		"unchanged":       "admin-password",
		"shared password": "shared-password",
	} {
		if status, code := checkStatus(t, adminAPIRequest(t, http.MethodPut, target, map[string]string{"password": refused}, "admin-password")); status != http.StatusUnprocessableEntity || code != "invalid_password" {
			t.Fatalf("%s: status=%d code=%q", name, status, code)
		}
	}
	if answer := decodeAPIObject(t, adminAPIRequest(t, http.MethodPut, target, map[string]string{"password": "new-admin-password"}, "admin-password")); answer["ok"] != true {
		t.Fatalf("change answered %v", answer)
	}
	if status, _ := checkStatus(t, adminAPIRequest(t, http.MethodGet, server.URL+"/api/v1/settings", nil, "admin-password")); status != http.StatusUnauthorized {
		t.Fatalf("the old administrator password still works: status=%d", status)
	}
	if status, _ := checkStatus(t, adminAPIRequest(t, http.MethodGet, server.URL+"/api/v1/settings", nil, "new-admin-password")); status != http.StatusOK {
		t.Fatalf("the new administrator password is refused: status=%d", status)
	}
}

// A password replaced after the request was authorized is no longer the
// administrator password, so it cannot replace its replacement.
func TestOwnerAdminPasswordChangeNeedsTheCurrentPassword(t *testing.T) {
	fixture := newAPIFixture(t, false)
	settings, err := fixture.store.Settings(context.Background())
	noErr(t, err)
	noErr(t, fixture.store.SetAdminPassword(context.Background(), fixturePasswordHash(t, "replaced-password")))
	err = fixture.app.changeAdminPassword(context.Background(), adminPasswordProof{password: "admin-password", version: settings.AdminSessionVersion}, "new-admin-password")
	if !errors.Is(err, state.ErrAccessChanged) {
		t.Fatalf("a stale administrator password: err=%v", err)
	}
	adminHash, err := fixture.store.PasswordHash(context.Background(), "admin")
	noErr(t, err)
	if !auth.CheckPassword(adminHash, "replaced-password") {
		t.Fatal("the current administrator password was replaced")
	}
}

func TestOwnerConfirmationAndUpdateCheckAPIs(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server := serve(t, fixture.app.Handler())
	target := server.URL + "/api/v1/settings/admin-confirmation"
	if status, code := checkStatus(t, adminAPIRequest(t, http.MethodPut, target, map[string]any{"admin_confirmation": "2h"}, "admin-password")); status != http.StatusBadRequest || code != "invalid_settings" {
		t.Fatalf("unknown choice: status=%d code=%q", status, code)
	}
	if status, code := checkStatus(t, adminAPIRequest(t, http.MethodPut, target, map[string]any{"admin_confirmation": "never"}, "admin-password")); status != http.StatusUnprocessableEntity || code != "acknowledgement_required" {
		t.Fatalf("Do not ask without the acknowledgement: status=%d code=%q", status, code)
	}
	if choice, _, err := fixture.store.AdminConfirmation(context.Background()); err != nil || choice != state.DefaultAdminConfirmation {
		t.Fatalf("a refused choice was saved: %q err=%v", choice, err)
	}
	answer := decodeAPIObject(t, adminAPIRequest(t, http.MethodPut, target, map[string]any{"admin_confirmation": "never", "acknowledge_no_ask": true}, "admin-password"))
	if answer["admin_confirmation"] != "never" || answer["warnings"] == nil {
		t.Fatalf("Do not ask answered %v", answer)
	}
	if choice, _, err := fixture.store.AdminConfirmation(context.Background()); err != nil || choice != state.ConfirmNever {
		t.Fatalf("saved choice=%q err=%v", choice, err)
	}

	settingsURL := server.URL + "/api/v1/settings"
	if status, code := checkStatus(t, adminAPIRequest(t, http.MethodPatch, settingsURL, map[string]any{"update_check": "sometimes"}, "admin-password")); status != http.StatusBadRequest || code != "invalid_settings" {
		t.Fatalf("invalid update check: status=%d code=%q", status, code)
	}
	shown := decodeAPIObject(t, adminAPIRequest(t, http.MethodPatch, settingsURL, map[string]any{"update_check": "off"}, "admin-password"))
	if saved := shown["settings"].(map[string]any); saved["update_check"] != "off" || shown["update_check_forced_off"] != true {
		t.Fatalf("update check change answered %v", shown)
	}
	if settings, err := fixture.store.Settings(context.Background()); err != nil || settings.UpdateCheck {
		t.Fatalf("update check still on: err=%v", err)
	}
}

func TestOwnerDefaultBranchAPIMatchesTheSettingsTab(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server := serve(t, fixture.app.Handler())
	target := server.URL + "/api/v1/repositories/project/default-branch"
	if status, code := checkStatus(t, adminAPIRequest(t, http.MethodPost, target, map[string]string{"branch": "missing"}, "admin-password")); status != http.StatusUnprocessableEntity || code != "branch_not_found" {
		t.Fatalf("missing branch: status=%d code=%q", status, code)
	}
	if status, code := checkStatus(t, adminAPIRequest(t, http.MethodPost, server.URL+"/api/v1/repositories/absent/default-branch", map[string]string{"branch": "main"}, "admin-password")); status != http.StatusNotFound || code != "repository_not_found" {
		t.Fatalf("missing repository: status=%d code=%q", status, code)
	}
	answer := decodeAPIObject(t, adminAPIRequest(t, http.MethodPost, target, map[string]string{"branch": "feature"}, "admin-password"))
	if answer["ok"] != true || answer["default_branch"] != "feature" || answer["repository"] != "project" {
		t.Fatalf("change answered %v", answer)
	}
	if head := apiGitOutput(t, fixture.remote, "symbolic-ref", "HEAD"); head != "refs/heads/feature" {
		t.Fatalf("default branch=%q", head)
	}
}

func TestOwnerDeleteAPIAsksForTheNameAsSettingsSay(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server := serve(t, fixture.app.Handler())
	target := server.URL + "/api/v1/repositories/project/delete"
	for name, refused := range map[string]struct {
		body   map[string]string
		status int
		code   string
	}{
		"no mode":    {map[string]string{"confirm_name": "project"}, http.StatusBadRequest, "invalid_request"},
		"no name":    {map[string]string{"mode": "keep_files"}, http.StatusUnprocessableEntity, "name_mismatch"},
		"wrong name": {map[string]string{"mode": "delete_files", "confirm_name": "other"}, http.StatusUnprocessableEntity, "name_mismatch"},
	} {
		if status, code := checkStatus(t, adminAPIRequest(t, http.MethodPost, target, refused.body, "admin-password")); status != refused.status || code != refused.code {
			t.Fatalf("%s: status=%d code=%q", name, status, code)
		}
		assertRepositoryIntact(t, fixture, name)
	}
	// An unreadable name setting stops the deletion, as on the page.
	noErr(t, fixture.store.Exec(context.Background(), `INSERT INTO metadata(key,value) VALUES('delete_requires_name','maybe') ON CONFLICT(key) DO UPDATE SET value=excluded.value`))
	if status, code := checkStatus(t, adminAPIRequest(t, http.MethodPost, target, map[string]string{"mode": "keep_files", "confirm_name": "project"}, "admin-password")); status != http.StatusConflict || code != "setting_unreadable" {
		t.Fatalf("unreadable setting: status=%d code=%q", status, code)
	}
	assertRepositoryIntact(t, fixture, "unreadable setting")

	// With the name check off, no name is needed, and the files are kept.
	noErr(t, fixture.store.SavePolicies(context.Background(), state.PolicyChange{DeleteRequiresName: pointer(false)}))
	answer := decodeAPIObject(t, adminAPIRequest(t, http.MethodPost, target, map[string]string{"mode": "keep_files"}, "admin-password"))
	kept, _ := answer["kept_path"].(string)
	if answer["ok"] != true || answer["mode"] != "keep_files" || answer["incomplete"] != nil || filepath.Base(filepath.Dir(kept)) != removedFolderName {
		t.Fatalf("deletion answered %v", answer)
	}
	if _, err := os.Stat(filepath.Join(kept, "HEAD")); err != nil {
		t.Fatalf("the kept files are missing: %v", err)
	}
	if fixtureRepositoryExists(t, fixture, "project") {
		t.Fatal("the repository still exists")
	}
	if status, code := checkStatus(t, adminAPIRequest(t, http.MethodPost, target, map[string]string{"mode": "keep_files"}, "admin-password")); status != http.StatusNotFound || code != "repository_not_found" {
		t.Fatalf("a second deletion: status=%d code=%q", status, code)
	}
	for _, mode := range []string{"keep_files", "delete_files"} {
		for _, folder := range []string{"missing folder", "unconfirmed storage"} {
			t.Run(folder+"/"+mode, func(t *testing.T) {
				fixture := newAPIFixture(t, false)
				server := serve(t, fixture.app.Handler())
				saved := filepath.Join(t.TempDir(), "absent.git")
				noErr(t, os.Rename(fixture.remote, saved))
				unrelated := filepath.Join(filepath.Dir(fixture.remote), "unrelated")
				if folder == "unconfirmed storage" {
					noErr(t, os.WriteFile(unrelated, []byte("not the mounted share"), 0o600))
				}
				response := adminAPIRequest(t, http.MethodPost, server.URL+"/api/v1/repositories/project/delete",
					map[string]string{"mode": mode, "confirm_name": "project"}, "admin-password")
				answer := decodeAPIObject(t, response)
				if _, err := os.Stat(filepath.Join(saved, "HEAD")); err != nil {
					t.Fatalf("absent repository changed: %v", err)
				}
				if folder == "unconfirmed storage" {
					problem, _ := answer["error"].(map[string]any)
					if response.StatusCode != http.StatusServiceUnavailable || problem["code"] != "delete_failed" ||
						problem["message"] != "The storage folder could not be confirmed. Check that the drive or share is mounted. The repository records were not removed." ||
						!fixtureRepositoryExists(t, fixture, "project") {
						t.Fatalf("unconfirmed storage deletion status=%d answer=%v", response.StatusCode, answer)
					}
					content, err := os.ReadFile(unrelated)
					if err != nil || string(content) != "not the mounted share" {
						t.Fatalf("unrelated file changed: %q err=%v", content, err)
					}
					if _, exists, err := fixture.store.RepositoryDeletion(context.Background(), "project"); err != nil || exists {
						t.Fatalf("refused deletion recorded an intent: exists=%v err=%v", exists, err)
					}
					return
				}
				if response.StatusCode != http.StatusOK || answer["ok"] != true || answer["mode"] != mode || answer["folder_missing"] != true ||
					answer["message"] != "The repository folder was already missing. Only its OwnGit records were removed." ||
					answer["kept_path"] != nil || answer["incomplete"] != nil {
					t.Fatalf("missing-folder deletion answered %v", answer)
				}
				if fixtureRepositoryExists(t, fixture, "project") {
					t.Fatal("missing-folder deletion left its record")
				}
			})
		}
	}
}
