package server

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"owngit/internal/auth"
	"owngit/internal/bootstrap"
	"owngit/internal/webui"
)

// SetupAnswers are the first-run answers, from the web setup form or from
// the terminal setup. Both go through CompleteSetup, so they follow the same
// rules and reach the same state.
type SetupAnswers struct {
	StoragePath      string
	AccessMode       string // "open" or "password"
	AccessPassword   string
	AdminPassword    string
	InsecureAccepted bool
	// KeepHost, when set, is a normalized Host name saved as an allowed Host
	// together with the answers.
	KeepHost string
}

var (
	// ErrSetupUnavailable means setup could not be attempted now, for
	// example because the setup lock was unavailable. Nothing was saved.
	ErrSetupUnavailable = errors.New("setup is temporarily unavailable")
	// ErrSetupNotSaved means the settings were not saved, usually because
	// setup was already completed by another browser or the terminal.
	ErrSetupNotSaved = errors.New("setup was not saved")
	// ErrSetupCleanup means setup was saved and is in effect, but the
	// obsolete owner setup files could not be removed.
	ErrSetupCleanup = errors.New("setup was saved but the owner setup files remain")
)

// AccessPasswordProblem applies the shared password rules. It returns the
// message for the first problem, or "" when the password is acceptable.
func AccessPasswordProblem(password string) webui.MessageCode {
	if password == "" {
		return webui.MsgSetupAccessPassEmpty
	}
	if err := auth.ValidatePassword(password); err != nil {
		return passwordRuleMessage(err, webui.MsgSetupAccessPassShort)
	}
	return ""
}

// AdminPasswordProblem applies the administrator password rules, including
// that it must differ from the shared password in password mode.
func AdminPasswordProblem(admin, accessMode, access string) webui.MessageCode {
	if problems := adminPasswordProblems(admin, accessMode, access); len(problems) != 0 {
		return problems[0]
	}
	return ""
}

func adminPasswordProblems(admin, accessMode, access string) []webui.MessageCode {
	var problems []webui.MessageCode
	if admin == "" {
		problems = append(problems, webui.MsgSetupAdminEmpty)
	} else if err := auth.ValidatePassword(admin); err != nil {
		problems = append(problems, passwordRuleMessage(err, webui.MsgSetupAdminShort))
	}
	if accessMode == "password" && access != "" && admin == access {
		problems = append(problems, webui.MsgSetupAdminSameAsGen)
	}
	return problems
}

// setupNotices applies every setup rule except the storage check, in the
// order the web form reports them.
func setupNotices(answers SetupAnswers, insecureRequired bool) []webui.Notice {
	var notices []webui.Notice
	if answers.StoragePath == "" {
		notices = append(notices, webui.Error("storage_path", webui.MsgSetupStorageMissing))
	}
	if answers.AccessMode != "open" && answers.AccessMode != "password" {
		notices = append(notices, webui.Error("access_mode", webui.MsgErrBadRequest))
	}
	if answers.AccessMode == "password" {
		if code := AccessPasswordProblem(answers.AccessPassword); code != "" {
			notices = append(notices, webui.Error("access_password", code))
		}
	}
	for _, code := range adminPasswordProblems(answers.AdminPassword, answers.AccessMode, answers.AccessPassword) {
		notices = append(notices, webui.Error("admin_password", code))
	}
	if insecureRequired && !answers.InsecureAccepted {
		notices = append(notices, webui.Error("insecure_ack", webui.MsgSetupInsecureNeed))
	}
	return notices
}

// CompleteSetup validates the answers and, when they are acceptable, saves
// them and starts serving the configured installation. insecureRequired says
// whether the plain HTTP acknowledgement is required: always for a browser
// request without TLS, and for the terminal when OwnGit listens on a network
// address. A validation problem is returned as notices with nothing saved.
// After a successful save it applies the repository root, arranges the
// "Setup finished" notice for the next dashboard view, runs OnSetupComplete
// once, and voids every pending browser approval.
func (app *App) CompleteSetup(ctx context.Context, answers SetupAnswers, insecureRequired bool) ([]webui.Notice, error) {
	notices := setupNotices(answers, insecureRequired)
	canonical := ""
	if len(notices) == 0 {
		var err error
		canonical, err = app.repositoryRoot(answers.StoragePath, true)
		if err != nil {
			notices = append(notices, webui.Error("storage_path", webui.MsgSetupStorageInvalid))
		}
	}
	if len(notices) != 0 {
		return notices, nil
	}
	adminHash, err := auth.HashPassword(answers.AdminPassword)
	if err != nil {
		return []webui.Notice{webui.Error("admin_password", webui.MsgSetupAdminShort)}, nil
	}
	accessHash := ""
	if answers.AccessMode == "password" {
		accessHash, err = auth.HashPassword(answers.AccessPassword)
		if err != nil {
			return []webui.Notice{webui.Error("access_password", webui.MsgSetupAccessPassShort)}, nil
		}
	}
	unlock, err := bootstrap.AcquireSetupLock(ctx, app.Store.Dir())
	if err != nil {
		return nil, ErrSetupUnavailable
	}
	var keepHosts []string
	if answers.KeepHost != "" {
		keepHosts = append(keepHosts, answers.KeepHost)
	}
	completeErr := app.Store.CompleteSetup(ctx, canonical, answers.AccessMode, accessHash, adminHash, answers.InsecureAccepted, keepHosts...)
	var cleanupErr error
	if completeErr == nil {
		cleanupErr = bootstrap.RemoveOwnerSetupFiles(app.Store.Dir())
	}
	unlock()
	if completeErr != nil {
		return nil, ErrSetupNotSaved
	}
	// Setup is committed, so this process serves it even when removing the
	// obsolete owner setup files failed. The failure is still reported, and
	// the next capability issue removes those files.
	app.Repositories.SetRoot(canonical)
	app.setupFinished.Store(true)
	if app.Approvals != nil {
		app.Approvals.Void()
	}
	if app.OnSetupComplete != nil {
		app.OnSetupComplete()
	}
	if cleanupErr != nil {
		return nil, ErrSetupCleanup
	}
	return nil, nil
}

// CheckRepositoryFolder applies the repository folder rules without
// creating anything, so the terminal can check the folder before the other
// questions. It returns the message for the problem, or "". Setup
// completion checks the folder again and creates it.
func (app *App) CheckRepositoryFolder(value string) webui.MessageCode {
	_, err := app.repositoryRoot(value, false)
	switch {
	case err == nil:
		return ""
	case errors.Is(err, fs.ErrPermission), errors.Is(err, syscall.EROFS):
		return webui.MsgSetupStorageDenied
	case errors.Is(err, errNotDirectory), errors.Is(err, syscall.ENOTDIR):
		return webui.MsgSetupStorageNotDir
	case errors.Is(err, errOverlapsState):
		return webui.MsgSetupStorageOverlap
	}
	return webui.MsgSetupStorageInvalid
}

var (
	errNotDirectory  = errors.New("repository root is not a directory")
	errOverlapsState = errors.New("repository root must be separate from application state")
)

// repositoryRoot checks a repository folder: an absolute path to a writable
// directory that does not overlap the application state. With create it
// creates the folder and returns its canonical path. Without create a
// missing folder is checked through its nearest existing parent, and
// nothing is left behind.
func (app *App) repositoryRoot(value string, create bool) (string, error) {
	if !filepath.IsAbs(value) {
		return "", errors.New("repository root must be absolute")
	}
	clean := filepath.Clean(value)
	if create {
		if err := os.MkdirAll(clean, 0o700); err != nil {
			return "", err
		}
	}
	// existing is the folder itself or, when it does not exist yet and
	// nothing may be created, its nearest existing parent.
	existing, missing := clean, ""
	for !create {
		if _, err := os.Stat(existing); err == nil || !errors.Is(err, fs.ErrNotExist) {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		missing = filepath.Join(filepath.Base(existing), missing)
		existing = parent
	}
	info, err := os.Stat(existing)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errNotDirectory
	}
	existingCanonical, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	canonical := filepath.Join(existingCanonical, missing)
	stateDir, err := filepath.EvalSymlinks(app.Store.Dir())
	if err != nil {
		return "", err
	}
	if pathsOverlap(canonical, stateDir) {
		return "", errOverlapsState
	}
	probe, err := os.CreateTemp(existingCanonical, ".owngit-write-test-*")
	if err != nil {
		return "", err
	}
	probePath := probe.Name()
	if err := probe.Close(); err != nil {
		return "", err
	}
	if err := os.Remove(probePath); err != nil {
		return "", err
	}
	return canonical, nil
}
