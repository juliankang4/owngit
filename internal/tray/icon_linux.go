//go:build linux

package tray

import (
	"bufio"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"owngit/internal/bootstrap"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// The Linux icon is a StatusNotifierItem, which GNOME with the AppIndicator
// extension, KDE Plasma, the Omarchy bar and other desktop bars show. The
// item, its menu and the panel are drawn by panelProgram, run with the
// system's gjs and GTK 4, so this program stays free of C libraries. This
// file keeps the authority: it reads the status and the proof, keeps the
// owner's choices and opens the dashboard; the panel program only shows
// what it is sent and says what the owner chose.

//go:embed icon_linux.js
var panelProgram string

// toolkitCheck loads GTK 4 in gjs without opening a window.
const toolkitCheck = "imports.gi.versions.Gtk = '4.0'; imports.gi.Gtk;"

// ErrNoToolkit means this desktop lacks gjs or GTK 4, which draw the icon.
var ErrNoToolkit = errors.New("the OwnGit icon on Linux needs gjs with GTK 4 (the gjs package of the distribution); OwnGit keeps running without the icon")

// ErrUnsafeToolkit means the gjs on PATH is one that another account could
// replace, so running it would run that account's code as this one.
var ErrUnsafeToolkit = errors.New("the OwnGit icon does not start gjs")

// IconProblem says why the icon cannot show on this computer, or "" when it
// can: gjs must be on PATH, safe from other accounts, and load GTK 4.
func IconProblem() string {
	if _, err := toolkit(); err != nil {
		return err.Error()
	}
	return ""
}

// toolkit finds gjs on PATH and checks that it loads GTK 4. It returns the
// file the links lead to, and runs nothing that another account could
// change: that file and every folder and link on the way to it must pass
// state.RequireProtectedPath, as the system's /usr/bin/gjs does.
func toolkit() (string, error) {
	found, err := exec.LookPath("gjs")
	if err != nil {
		return "", ErrNoToolkit
	}
	gjs, err := protectedProgram(found)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, gjs, "-c", toolkitCheck).Run(); err != nil {
		return "", ErrNoToolkit
	}
	return gjs, nil
}

// protectedProgram returns the file that path leads to when no other
// account can change it or the way to it.
func protectedProgram(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%w at %s: %v; OwnGit keeps running without the icon", ErrUnsafeToolkit, path, err)
	}
	if err := state.RequireProtectedPath(absolute); err != nil {
		return "", fmt.Errorf("%w at %s: %v; OwnGit keeps running without the icon", ErrUnsafeToolkit, absolute, err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("%w at %s: %v; OwnGit keeps running without the icon", ErrUnsafeToolkit, absolute, err)
	}
	return resolved, nil
}

// panelMessage is a message between this program and the panel program.
// Only the fields of its type are set.
type panelMessage struct {
	Type    string `json:"type"`
	Name    string `json:"name,omitempty"`
	Icons   string `json:"icons,omitempty"`
	Icon    string `json:"icon,omitempty"`
	Symbol  string `json:"symbol,omitempty"`
	Panel   *Panel `json:"panel,omitempty"`
	Text    string `json:"text,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Open    bool   `json:"open,omitempty"`
}

// panelProcess is a running panel program. messages closes when the
// program ends, and ended then holds why.
type panelProcess struct {
	command  *exec.Cmd
	input    io.WriteCloser
	messages chan panelMessage
	ended    chan error
}

type linuxIcon struct {
	*poller
	icons string
}

// Run shows the icon of the server of options.StateDir until the icon is
// quit or options.Stop closes. It returns ErrAlreadyRunning when the icon
// of that state directory already runs in this desktop session, and
// ErrNoToolkit when gjs or GTK 4 is missing, and ErrUnsafeToolkit when
// another account could replace gjs.
func Run(options Options) error {
	gjs, err := toolkit()
	if err != nil {
		return err
	}
	icons, err := writeIcons()
	if err != nil {
		return fmt.Errorf("start the OwnGit icon: %w", err)
	}
	icon := &linuxIcon{poller: newPoller(options, DesktopLanguage()), icons: icons}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	readings := make(chan reading)
	go icon.poll(ctx, func(next reading) {
		select {
		case <-ctx.Done():
		case readings <- next:
		}
	})
	// opening is true while a click waits for the proof of the dashboard's
	// address and the browser; a second click meanwhile opens nothing more.
	opening := false
	opened := make(chan error, 1)
	var current *panelProcess
	defer func() { current.stop() }()
	for {
		var messages <-chan panelMessage
		if current != nil {
			messages = current.messages
		}
		select {
		case <-options.Stop:
			return nil
		case next := <-readings:
			if !next.show {
				current.stop()
				current = nil
				continue
			}
			if current == nil {
				if current, err = icon.startPanel(gjs); err != nil {
					return err
				}
			}
			current.send(icon.stateMessage(next.report, false))
		case message, ok := <-messages:
			if !ok {
				err := <-current.ended
				current = nil
				return fmt.Errorf("the OwnGit icon's panel program ended: %w", err)
			}
			switch message.Type {
			case "open":
				if !opening {
					opening = true
					go func() { opened <- icon.openDashboard() }()
				}
			case "hide":
				if err := icon.hide(); err != nil {
					current.send(panelMessage{Type: "notice", Text: fmt.Sprintf(webui.Text(icon.lang, webui.MsgTrayHideFailed), err)})
					continue
				}
				current.stop()
				current = nil
				icon.askAgain()
			case "quit":
				return nil
			case "panel":
				icon.panelOpen.Store(message.Open)
				if message.Open {
					icon.askAgain()
				}
			}
		case err := <-opened:
			opening = false
			switch {
			case current == nil:
			case err == nil:
				current.send(panelMessage{Type: "opened"})
			case errors.Is(err, errNotProven):
				// Show that at once, as the next reading will, which is
				// asked for now.
				current.send(icon.stateMessage(Report{Condition: Unavailable}, true))
			default:
				current.send(panelMessage{Type: "notice", Text: fmt.Sprintf(webui.Text(icon.lang, webui.MsgTrayOpenFailed), err)})
			}
			icon.askAgain()
		}
	}
}

// stateMessage tells the panel program what to show for report, and to
// open the panel when open is set.
func (icon *linuxIcon) stateMessage(report Report, open bool) panelMessage {
	panel := NewPanel(report, icon.lang, time.Now())
	name := conditionNames[report.Condition]
	return panelMessage{Type: "state", Icon: "owngit-" + name + "-symbolic", Symbol: "owngit-state-" + name + "-symbolic", Panel: &panel, Open: open}
}

// startPanel starts the panel program and waits until its item is on the
// session bus.
func (icon *linuxIcon) startPanel(gjs string) (*panelProcess, error) {
	command := exec.Command(gjs, "-c", panelProgram)
	command.Stderr = os.Stderr
	input, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start the OwnGit icon's panel program: %w", err)
	}
	process := &panelProcess{command: command, input: input, messages: make(chan panelMessage), ended: make(chan error, 1)}
	go func() {
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			var message panelMessage
			if json.Unmarshal(scanner.Bytes(), &message) == nil {
				process.messages <- message
			}
		}
		err := command.Wait()
		if err == nil {
			err = errors.New("it closed")
		}
		process.ended <- err
		close(process.messages)
	}()
	sum := sha256.Sum256([]byte(icon.stateDir))
	process.send(panelMessage{Type: "init", Name: "app.owngit.Icon.S" + hex.EncodeToString(sum[:8]), Icons: icon.icons})
	first, ok := <-process.messages
	switch {
	case !ok:
		return nil, fmt.Errorf("the OwnGit icon's panel program ended: %w", <-process.ended)
	case first.Type == "ready":
		return process, nil
	}
	process.stop()
	switch {
	case first.Type == "error" && first.Code == "already_running":
		return nil, ErrAlreadyRunning
	case first.Type == "error" && first.Code == "toolkit":
		return nil, ErrNoToolkit
	case first.Type == "error":
		return nil, fmt.Errorf("the OwnGit icon cannot show: %s", first.Message)
	}
	return nil, errors.New("the OwnGit icon's panel program did not start")
}

func (process *panelProcess) send(message panelMessage) {
	line, err := json.Marshal(message)
	if err == nil {
		// A panel program that stopped reading reports its end on ended.
		_, _ = process.input.Write(append(line, '\n'))
	}
}

// stop closes the panel program's input, which ends it, and kills it if it
// does not end soon.
func (process *panelProcess) stop() {
	if process == nil {
		return
	}
	process.input.Close()
	timer := time.AfterFunc(5*time.Second, func() { _ = process.command.Process.Kill() })
	defer timer.Stop()
	for range process.messages {
	}
}

// hide is Hide the icon: the same choice as "owngit tray off" and the
// dashboard switch.
func (icon *linuxIcon) hide() error {
	held, err := state.OpenStateDirectory(icon.stateDir)
	if err != nil {
		return err
	}
	defer held.Close()
	return state.SetTrayHidden(held, true)
}

// errNotProven means the server did not prove the dashboard's address when
// the owner asked to open it, so nothing was opened.
var errNotProven = errors.New("the dashboard's address was not proven")

// openDashboard opens the dashboard after the server proves, right now,
// that it still answers there. The browser brings the owner's OwnGit
// sign-in along, so a program that took the address after an earlier
// reading must not get it.
func (icon *linuxIcon) openDashboard() error {
	target, err := NewClient(icon.stateDir, nil).Dashboard(context.Background(), string(icon.lang))
	if err != nil {
		return fmt.Errorf("%w: %v", errNotProven, err)
	}
	return bootstrap.Open(target)
}

// writeIcons writes the icon files the desktop and the panel draw into a
// folder of this account's cache and returns the folder.
func writeIcons() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cache, "owngit", "icons")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	files := map[string]string{"owngit-tile.svg": TileSVG()}
	for condition, name := range conditionNames {
		files["owngit-"+name+"-symbolic.svg"] = GlyphSVG(condition)
		files["owngit-state-"+name+"-symbolic.svg"] = SymbolSVG(condition)
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if existing, err := os.ReadFile(path); err == nil && string(existing) == content {
			continue
		}
		temporary := path + ".new"
		if err := os.WriteFile(temporary, []byte(content), 0o600); err != nil {
			return "", err
		}
		if err := os.Rename(temporary, path); err != nil {
			return "", err
		}
	}
	return dir, nil
}
