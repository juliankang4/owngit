package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"owngit/internal/auth"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// The owner's passwords, the shared access mode, the administrator
// confirmation choice and the new-release check. Settings and the owner
// API (/api/v1/settings/...) change each through the functions here, so both
// apply the same rules and refuse the same changes.

var (
	// errSharedSameAsAdmin refuses a shared password that is the
	// administrator password.
	errSharedSameAsAdmin = errors.New("the shared password must differ from the administrator password")
	// errAdminPasswordSame refuses a new administrator password that is the
	// current one.
	errAdminPasswordSame = errors.New("the new administrator password is the current one")
	// errAdminSameAsShared refuses an administrator password that is the
	// shared password.
	errAdminSameAsShared = errors.New("the administrator password must differ from the shared password")
)

// passwordRuleError reports whether err is a password that breaks the
// length rule (auth.ValidatePassword).
func passwordRuleError(err error) bool {
	return errors.Is(err, auth.ErrPasswordTooShort) || errors.Is(err, auth.ErrPasswordTooLong)
}

// setAccessPassword turns the shared password on, or replaces it, with
// password, which ends every general session. typedAdmin is the
// administrator password the request typed, or "" when it typed none.
func (app *App) setAccessPassword(ctx context.Context, typedAdmin, password string) error {
	if err := auth.ValidatePassword(password); err != nil {
		return err
	}
	same, err := app.sameAsAdminPassword(ctx, typedAdmin, password)
	if err != nil {
		return fmt.Errorf("administrator password read: %w", err)
	}
	if same {
		return errSharedSameAsAdmin
	}
	encoded, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	return app.Store.SetAccessPassword(ctx, encoded)
}

// sameAsAdminPassword reports whether password is the administrator
// password: the one the request typed, when it typed one, or else the
// saved one.
func (app *App) sameAsAdminPassword(ctx context.Context, typed, password string) (bool, error) {
	if typed != "" {
		return typed == password, nil
	}
	encoded, err := app.Store.PasswordHash(ctx, "admin")
	if err != nil {
		return false, err
	}
	return auth.CheckPassword(encoded, password), nil
}

// changeAdminPassword replaces the administrator password proof verified
// with newPassword, which ends every administrator session. A password
// changed since proof was verified is no longer the current one, so the
// change answers state.ErrAccessChanged.
func (app *App) changeAdminPassword(ctx context.Context, proof adminPasswordProof, newPassword string) error {
	if err := auth.ValidatePassword(newPassword); err != nil {
		return err
	}
	if proof.password == newPassword {
		return errAdminPasswordSame
	}
	accessHash, err := app.Store.PasswordHash(ctx, "access")
	if err != nil {
		return fmt.Errorf("shared password read: %w", err)
	}
	if accessHash != "" && auth.CheckPassword(accessHash, newPassword) {
		return errAdminSameAsShared
	}
	encoded, err := auth.HashPassword(newPassword)
	if err != nil {
		return err
	}
	return app.Store.ChangeAdminPassword(ctx, encoded, proof.version)
}

// savePolicies saves change. A new-release check turned on asks for an
// answer soon instead of waiting for the daily interval; turned off, it
// stops at once, because the dashboard and the checker read the saved value.
func (app *App) savePolicies(ctx context.Context, change state.PolicyChange) error {
	_, err := app.patchPolicies(ctx, change, state.PolicyFields{})
	return err
}

// handleOwnerSettingAPI answers PUT /api/v1/settings/{name} with the
// administrator password: "access" turns the shared password on, changes it
// or turns it off, "admin-password" replaces the administrator password and
// "admin-confirmation" chooses how long a browser remembers the
// administrator password. GET /api/v1/settings shows the current state of
// each.
func (app *App) handleOwnerSettingAPI(writer http.ResponseWriter, request *http.Request, name string) {
	if name != "access" && name != "admin-password" && name != "admin-confirmation" {
		writeAPIError(writer, http.StatusNotFound, "not_found", "The API endpoint does not exist.", nil)
		return
	}
	if request.Method != http.MethodPut {
		writeAPIMethodError(writer, http.MethodPut)
		return
	}
	proof, ok := app.adminAPIProof(writer, request)
	if !ok {
		return
	}
	switch name {
	case "access":
		app.putAccessAPI(writer, request, proof)
	case "admin-password":
		app.putAdminPasswordAPI(writer, request, proof)
	case "admin-confirmation":
		app.putAdminConfirmationAPI(writer, request)
	}
}

// putAccessAPI saves {"mode": "open"} or {"mode": "password", "password":
// ...}. The password may be left out while the shared password is already
// on, which changes nothing.
func (app *App) putAccessAPI(writer http.ResponseWriter, request *http.Request, proof adminPasswordProof) {
	var input struct {
		Mode     string `json:"mode"`
		Password string `json:"password"`
	}
	if !decodeAPIJSON(writer, request, &input) {
		return
	}
	settings, err := app.Store.Settings(request.Context())
	if err != nil {
		writeAPIError(writer, unavailable(request, "settings read", err), "state_unavailable", "The settings could not be read. Try again later.", nil)
		return
	}
	passwordNow := settings.AccessMode == "password"
	changed := true
	switch {
	case input.Mode == "open" && input.Password != "":
		writeAPIError(writer, http.StatusBadRequest, "invalid_settings", "An open server takes no shared password.", nil)
		return
	case input.Mode == "open":
		changed = passwordNow
		if changed {
			err = app.Store.DisableAccessPassword(request.Context())
		}
	case input.Mode == "password" && input.Password == "":
		if !passwordNow {
			writeAPIError(writer, http.StatusBadRequest, "invalid_settings", "Turning the shared password on needs the password.", nil)
			return
		}
		changed = false
	case input.Mode == "password":
		err = app.setAccessPassword(request.Context(), proof.password, input.Password)
	default:
		writeAPIError(writer, http.StatusBadRequest, "invalid_settings", "mode must be open or password.", nil)
		return
	}
	switch {
	case passwordRuleError(err):
		writeAPIError(writer, http.StatusUnprocessableEntity, "invalid_password", webui.Text(webui.LangEN, passwordRuleMessage(err, webui.MsgSetupAccessPassShort)), nil)
		return
	case errors.Is(err, errSharedSameAsAdmin):
		writeAPIError(writer, http.StatusUnprocessableEntity, "invalid_password", webui.Text(webui.LangEN, webui.MsgSetupGenSameAsAdmin), nil)
		return
	case err != nil:
		writeAPIError(writer, unavailable(request, "shared password save", err), "state_unavailable", "The shared password setting could not be saved. Try again later.", nil)
		return
	}
	writeAPIJSON(writer, http.StatusOK, struct {
		OK         bool   `json:"ok"`
		AccessMode string `json:"access_mode"`
		Changed    bool   `json:"changed"`
	}{true, input.Mode, changed})
}

// putAdminPasswordAPI replaces the administrator password the request was
// authorized with by {"password": ...}.
func (app *App) putAdminPasswordAPI(writer http.ResponseWriter, request *http.Request, proof adminPasswordProof) {
	var input struct {
		Password string `json:"password"`
	}
	if !decodeAPIJSON(writer, request, &input) {
		return
	}
	err := app.changeAdminPassword(request.Context(), proof, input.Password)
	switch {
	case err == nil:
		writeAPIJSON(writer, http.StatusOK, struct {
			OK bool `json:"ok"`
		}{true})
	case passwordRuleError(err):
		writeAPIError(writer, http.StatusUnprocessableEntity, "invalid_password", webui.Text(webui.LangEN, passwordRuleMessage(err, webui.MsgSetupAdminShort)), nil)
	case errors.Is(err, errAdminPasswordSame):
		writeAPIError(writer, http.StatusUnprocessableEntity, "invalid_password", webui.Text(webui.LangEN, webui.MsgSettingsAdminSame), nil)
	case errors.Is(err, errAdminSameAsShared):
		writeAPIError(writer, http.StatusUnprocessableEntity, "invalid_password", webui.Text(webui.LangEN, webui.MsgSetupAdminSameAsGen), nil)
	case errors.Is(err, state.ErrAccessChanged):
		// The password this request was authorized with was replaced
		// meanwhile, so it is no longer the administrator password.
		writer.Header().Set("WWW-Authenticate", `Basic realm="OwnGit admin"`)
		writeAPIError(writer, http.StatusUnauthorized, "invalid_admin_credentials", "The administrator password is invalid.", nil)
	default:
		writeAPIError(writer, unavailable(request, "administrator password save", err), "state_unavailable", "The administrator password could not be saved. Try again later.", nil)
	}
}

// putAdminConfirmationAPI saves {"admin_confirmation": choice}. Do not ask
// ("never") also needs "acknowledge_no_ask": true, as Settings needs its
// acknowledgement.
func (app *App) putAdminConfirmationAPI(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		AdminConfirmation string `json:"admin_confirmation"`
		AcknowledgeNoAsk  bool   `json:"acknowledge_no_ask"`
	}
	if !decodeAPIJSON(writer, request, &input) {
		return
	}
	choice, valid := state.ParseAdminConfirmation(input.AdminConfirmation)
	if !valid {
		writeAPIError(writer, http.StatusBadRequest, "invalid_settings", "admin_confirmation must be one of "+choiceList(state.AdminConfirmations)+".", nil)
		return
	}
	if choice == state.ConfirmNever && !input.AcknowledgeNoAsk {
		writeAPIError(writer, http.StatusUnprocessableEntity, "acknowledgement_required", webui.Text(webui.LangEN, webui.MsgConfirmAckNeeded), nil)
		return
	}
	if err := app.Auth.SetAdminConfirmation(request.Context(), choice); err != nil {
		writeAPIError(writer, unavailable(request, "administrator confirmation save", err), "state_unavailable", "The administrator confirmation could not be saved. Try again later.", nil)
		return
	}
	response := struct {
		OK                bool     `json:"ok"`
		AdminConfirmation string   `json:"admin_confirmation"`
		Warnings          []string `json:"warnings,omitempty"`
	}{OK: true, AdminConfirmation: string(choice)}
	if choice == state.ConfirmNever {
		response.Warnings = []string{webui.Text(webui.LangEN, webui.MsgConfirmTurnedOff)}
	}
	writeAPIJSON(writer, http.StatusOK, response)
}

// ownerSettingName is the name of an owner setting route under
// /api/v1/settings/, and ok is false for any other path.
func ownerSettingName(path string) (name string, ok bool) {
	name, ok = strings.CutPrefix(path, "/api/v1/settings/")
	return name, ok && name != ""
}
