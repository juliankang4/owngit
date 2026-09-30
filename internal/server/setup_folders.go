package server

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"owngit/internal/bidi"
	"owngit/internal/state"
	"owngit/internal/webui"
)

const (
	setupFoldersPath       = "/setup/folders"
	setupFolderCreatePath  = "/setup/folders/new"
	folderEntryLimit       = 2000
	folderOperationTimeout = 3 * time.Second
)

var (
	errFolderPath      = errors.New("folder path must be absolute")
	errFolderBusy      = errors.New("a folder operation is still running")
	errFolderSetupDone = errors.New("setup is already complete")
)

type folderEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type folderResult struct {
	Path            string            `json:"path"`
	Parent          string            `json:"parent"`
	ParentRoots     bool              `json:"parent_roots"`
	Folders         []folderEntry     `json:"folders"`
	Truncated       bool              `json:"truncated"`
	StartedAtParent bool              `json:"started_at_parent,omitempty"`
	SuggestedName   string            `json:"suggested_name,omitempty"`
	SkippedNames    bool              `json:"skipped_names,omitempty"`
	Error           webui.MessageCode `json:"error,omitempty"`
}

func isSetupFolderPath(path string) bool {
	return path == setupFoldersPath || path == setupFolderCreatePath
}

// Folder browsing has one authorization gate. Settings can later use its
// administrator gate here; no initialized installation is admitted now.
func (app *App) requireFolderOwner(writer http.ResponseWriter, request *http.Request, settings state.Settings) bool {
	if settings.Initialized {
		writeFolderResult(writer, request, http.StatusConflict, folderResult{Error: webui.MsgSetupAlreadyDone})
		return false
	}
	if !app.parseForm(writer, request) {
		return false
	}
	session, ok, err := app.setupSessionForHost(request)
	if err != nil {
		writeFolderResult(writer, request, unavailable(request, "folder session read", err), folderResult{Error: webui.MsgErrUnavailable})
		return false
	}
	if !ok {
		writeFolderResult(writer, request, http.StatusForbidden, folderResult{Error: webui.MsgSetupSessionEnded})
		return false
	}
	if !constantEqual(session.CSRF, postValue(request, "csrf")) {
		writeFolderResult(writer, request, http.StatusForbidden, folderResult{Error: webui.MsgErrCSRF})
		return false
	}
	return true
}

func (app *App) handleSetupFolders(writer http.ResponseWriter, request *http.Request, settings state.Settings) {
	if !app.requireFolderOwner(writer, request, settings) {
		return
	}
	path, name := postValue(request, "path"), postValue(request, "name")
	create := request.URL.Path == setupFolderCreatePath
	start := path == "" || formChecked(postValue(request, "start"))
	if len(path) > 32768 || !utf8.ValidString(path) || strings.ContainsRune(path, 0) {
		writeFolderResult(writer, request, http.StatusBadRequest, folderResult{Error: webui.MsgFolderInvalidPath})
		return
	}
	if create && !validFolderName(name) {
		writeFolderResult(writer, request, http.StatusBadRequest, folderResult{Error: webui.MsgFolderInvalidName})
		return
	}
	if path == "" && !create && !formChecked(postValue(request, "roots")) {
		path = app.SuggestedRepositoryRoot
		if path == "" {
			var err error
			path, err = os.UserHomeDir()
			if err != nil {
				writeFolderResult(writer, request, http.StatusUnprocessableEntity, folderResult{Error: webui.MsgFolderFailed})
				return
			}
		}
	}
	ctx, cancel := context.WithTimeout(request.Context(), folderOperationTimeout)
	defer cancel()
	result, err := app.folderOperation(ctx, func(ctx context.Context) (folderResult, error) {
		if !create && formChecked(postValue(request, "roots")) {
			return folderRoots(ctx)
		}
		if !filepath.IsAbs(path) {
			return folderResult{}, errFolderPath
		}
		path := filepath.Clean(path)
		if create {
			existing, _, err := nearestFolder(ctx, path)
			if err != nil {
				return folderResult{}, err
			}
			if existing != path {
				return folderResult{}, fs.ErrNotExist
			}
			parent, err := os.OpenRoot(path)
			if err != nil {
				return folderResult{}, err
			}
			defer parent.Close()
			return app.createFolder(ctx, parent, name)
		}
		return listFolders(ctx, path, formChecked(postValue(request, "hidden")), start)
	})
	if err != nil {
		status, code := folderProblem(request, err, create)
		failure := folderResult{Error: code}
		if filepath.IsAbs(path) {
			failure.Path = filepath.Clean(path)
			failure.Parent, failure.ParentRoots = folderParent(failure.Path)
		}
		writeFolderResult(writer, request, status, failure)
		return
	}
	writeFolderResult(writer, request, http.StatusOK, result)
}

func (app *App) createFolder(ctx context.Context, parent *os.Root, name string) (folderResult, error) {
	current, err := app.Store.Settings(ctx)
	if err != nil {
		return folderResult{}, err
	}
	if current.Initialized {
		return folderResult{}, errFolderSetupDone
	}
	if err := ctx.Err(); err != nil {
		return folderResult{}, err
	}
	// The opened parent stays the write target across pathname replacement.
	// Mkdir refuses an existing final entry and uses storage's creation mode.
	if err := parent.Mkdir(name, 0o700); err != nil {
		return folderResult{}, err
	}
	return folderResult{Path: filepath.Join(parent.Name(), name)}, nil
}

// A deadline bounds the response, not an OS call on a stalled mount. One
// in-flight slot stays occupied until the OS call actually returns, so
// repeated requests cannot accumulate blocked filesystem goroutines.
func (app *App) folderOperation(ctx context.Context, operation func(context.Context) (folderResult, error)) (folderResult, error) {
	if err := ctx.Err(); err != nil {
		return folderResult{}, err
	}
	if !app.folderBusy.CompareAndSwap(false, true) {
		return folderResult{}, errFolderBusy
	}
	type answer struct {
		result folderResult
		err    error
	}
	done := make(chan answer, 1)
	go func() {
		var result folderResult
		err := ctx.Err()
		if err == nil {
			result, err = operation(ctx)
		}
		app.folderBusy.Store(false)
		done <- answer{result, err}
	}()
	select {
	case <-ctx.Done():
		return folderResult{}, ctx.Err()
	case reply := <-done:
		if err := ctx.Err(); err != nil {
			return folderResult{}, err
		}
		return reply.result, reply.err
	}
}

// nearestFolder also detects paths running through a file when the OS
// reports them as missing. Only a missing component permits walking upward.
func nearestFolder(ctx context.Context, path string) (string, string, error) {
	missingName := ""
	for {
		if err := ctx.Err(); err != nil {
			return "", "", err
		}
		info, err := os.Stat(path)
		if err == nil {
			if !info.IsDir() {
				return "", "", errNotDirectory
			}
			return path, missingName, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", "", err
		}
		missingName, path = filepath.Base(path), parent
	}
}

func listFolders(ctx context.Context, path string, showHidden, start bool) (folderResult, error) {
	existing, missingName, err := nearestFolder(ctx, path)
	if err != nil {
		return folderResult{}, err
	}
	if existing != path && !start {
		return folderResult{}, fs.ErrNotExist
	}
	result := folderResult{Path: existing, Folders: []folderEntry{}, StartedAtParent: existing != path}
	if result.StartedAtParent && validFolderName(missingName) {
		result.SuggestedName = missingName
	}
	path = existing
	result.Parent, result.ParentRoots = folderParent(path)
	if err := ctx.Err(); err != nil {
		return folderResult{}, err
	}
	folder, err := os.Open(path)
	if err != nil {
		return folderResult{}, err
	}
	defer folder.Close()
	// Bound scanned entries as well as returned folders and memory. The
	// retained subset is sorted; the notice does not claim it is complete.
	entries, err := folder.ReadDir(folderEntryLimit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return folderResult{}, err
	}
	if len(entries) > folderEntryLimit {
		result.Truncated = true
		entries = entries[:folderEntryLimit]
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return folderResult{}, err
		}
		name := entry.Name()
		child := filepath.Join(path, name)
		if !entry.IsDir() {
			if entry.Type()&(fs.ModeSymlink|fs.ModeIrregular) == 0 {
				continue
			}
			info, err := os.Stat(child)
			if errors.Is(err, fs.ErrNotExist) || folderLinkLoop(err) || folderNotDirectory(err) {
				continue
			}
			if err != nil {
				return folderResult{}, err
			}
			if !info.IsDir() {
				continue
			}
		}
		if !showHidden {
			hidden, err := chooserHidden(child, name)
			if err != nil {
				return folderResult{}, err
			}
			if hidden {
				continue
			}
		}
		if !utf8.ValidString(name) {
			result.SkippedNames = true
			continue
		}
		result.Folders = append(result.Folders, folderEntry{Name: name, Path: child})
	}
	sort.Slice(result.Folders, func(i, j int) bool { return result.Folders[i].Name < result.Folders[j].Name })
	return result, nil
}

func validFolderName(name string) bool {
	if name == "" || strings.TrimSpace(name) == "" || name == "." || name == ".." || !utf8.ValidString(name) || strings.ContainsAny(name, "/\\\x00") {
		return false
	}
	for _, char := range name {
		if unicode.IsControl(char) {
			return false
		}
	}
	return platformFolderName(name)
}

func folderProblem(request *http.Request, err error, create bool) (int, webui.MessageCode) {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		if create {
			return http.StatusGatewayTimeout, webui.MsgFolderCreateUnconfirmed
		}
		return http.StatusGatewayTimeout, webui.MsgFolderTimeout
	case errors.Is(err, errFolderBusy):
		return unavailable(request, "folder operation still running", err), webui.MsgFolderBusy
	case errors.Is(err, errFolderSetupDone):
		return http.StatusConflict, webui.MsgSetupAlreadyDone
	case errors.Is(err, errFolderPath):
		return http.StatusBadRequest, webui.MsgFolderInvalidPath
	case errors.Is(err, fs.ErrPermission):
		return http.StatusForbidden, webui.MsgFolderDenied
	case errors.Is(err, fs.ErrNotExist):
		return http.StatusNotFound, webui.MsgFolderMissing
	case errors.Is(err, fs.ErrExist):
		return http.StatusConflict, webui.MsgFolderExists
	case errors.Is(err, errNotDirectory), folderNotDirectory(err):
		return http.StatusUnprocessableEntity, webui.MsgFolderNotDirectory
	default:
		return http.StatusUnprocessableEntity, webui.MsgFolderFailed
	}
}

func writeFolderResult(writer http.ResponseWriter, request *http.Request, status int, result folderResult) {
	body, err := bidi.MarshalJSON(result)
	if err != nil {
		http.Error(writer, "response unavailable", internalError(request, "folder response encoding", err))
		return
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_, _ = writer.Write(body)
}
