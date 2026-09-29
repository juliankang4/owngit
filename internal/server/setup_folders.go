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
	Path        string            `json:"path"`
	Parent      string            `json:"parent"`
	ParentRoots bool              `json:"parent_roots"`
	Folders     []folderEntry     `json:"folders"`
	Truncated   bool              `json:"truncated"`
	Error       webui.MessageCode `json:"error,omitempty"`
}

func isSetupFolderPath(path string) bool {
	return path == setupFoldersPath || path == setupFolderCreatePath
}

// Folder browsing has one authorization gate. Settings can later use its
// administrator gate here; no initialized installation is admitted now.
func (app *App) requireFolderOwner(writer http.ResponseWriter, request *http.Request, settings state.Settings) bool {
	if settings.Initialized {
		writeFolderResult(writer, http.StatusConflict, folderResult{Error: webui.MsgSetupAlreadyDone})
		return false
	}
	if !app.parseForm(writer, request) {
		return false
	}
	session, ok, err := app.setupSessionForHost(request)
	if err != nil {
		writeFolderResult(writer, http.StatusServiceUnavailable, folderResult{Error: webui.MsgErrUnavailable})
		return false
	}
	if !ok {
		writeFolderResult(writer, http.StatusForbidden, folderResult{Error: webui.MsgSetupSessionEnded})
		return false
	}
	if !constantEqual(session.CSRF, postValue(request, "csrf")) {
		writeFolderResult(writer, http.StatusForbidden, folderResult{Error: webui.MsgErrCSRF})
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
	if len(path) > 32768 || !utf8.ValidString(path) || strings.ContainsRune(path, 0) {
		writeFolderResult(writer, http.StatusBadRequest, folderResult{Error: webui.MsgFolderInvalidPath})
		return
	}
	if create && !validFolderName(name) {
		writeFolderResult(writer, http.StatusBadRequest, folderResult{Error: webui.MsgFolderInvalidName})
		return
	}
	if path == "" && !create && !formChecked(postValue(request, "roots")) {
		path = app.SuggestedRepositoryRoot
		if path == "" {
			var err error
			path, err = os.UserHomeDir()
			if err != nil {
				writeFolderResult(writer, http.StatusUnprocessableEntity, folderResult{Error: webui.MsgFolderFailed})
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
			// Check again before the write, including a request whose setup
			// completed while it was checking the parent folder.
			info, err := os.Stat(path)
			if err != nil {
				return folderResult{}, err
			}
			if !info.IsDir() {
				return folderResult{}, errNotDirectory
			}
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
			child := filepath.Join(path, name)
			// Mkdir never replaces or follows an existing final entry,
			// including a symbolic link. Match storage creation permissions.
			if err := os.Mkdir(child, 0o700); err != nil {
				return folderResult{}, err
			}
			return folderResult{Path: child}, nil
		}
		return listFolders(ctx, path, formChecked(postValue(request, "hidden")))
	})
	if err != nil {
		status, code := folderProblem(err, create)
		failure := folderResult{Error: code}
		if filepath.IsAbs(path) {
			failure.Path = filepath.Clean(path)
			failure.Parent, failure.ParentRoots = folderParent(failure.Path)
		}
		writeFolderResult(writer, status, failure)
		return
	}
	writeFolderResult(writer, http.StatusOK, result)
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

func listFolders(ctx context.Context, path string, showHidden bool) (folderResult, error) {
	result := folderResult{Path: path, Folders: []folderEntry{}}
	result.Parent, result.ParentRoots = folderParent(path)
	info, err := os.Stat(path)
	if err != nil {
		return folderResult{}, err
	}
	if !info.IsDir() {
		return folderResult{}, errNotDirectory
	}
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
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !utf8.ValidString(name) {
			return folderResult{}, errors.New("folder name is not UTF-8")
		}
		child := filepath.Join(path, name)
		if !showHidden {
			hidden, err := chooserHidden(child, name)
			if err != nil {
				return folderResult{}, err
			}
			if hidden {
				continue
			}
		}
		result.Folders = append(result.Folders, folderEntry{Name: name, Path: child})
	}
	sort.Slice(result.Folders, func(i, j int) bool { return result.Folders[i].Name < result.Folders[j].Name })
	return result, nil
}

func validFolderName(name string) bool {
	if name == "" || strings.TrimSpace(name) == "" || name == "." || name == ".." || len(name) > 255 || !utf8.ValidString(name) || strings.ContainsAny(name, "/\\\x00") {
		return false
	}
	return platformFolderName(name)
}

func folderProblem(err error, create bool) (int, webui.MessageCode) {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		if create {
			return http.StatusGatewayTimeout, webui.MsgFolderCreateUnconfirmed
		}
		return http.StatusGatewayTimeout, webui.MsgFolderTimeout
	case errors.Is(err, errFolderBusy):
		return http.StatusServiceUnavailable, webui.MsgFolderBusy
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

func writeFolderResult(writer http.ResponseWriter, status int, result folderResult) {
	body, err := bidi.MarshalJSON(result)
	if err != nil {
		http.Error(writer, "response unavailable", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_, _ = writer.Write(body)
}
