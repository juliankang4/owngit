// Package firstrun asks the first-run setup questions in the terminal that
// started `owngit serve`, or lets the owner approve a browser there to
// continue setup on the web. It saves answers only through the server's
// setup code, so the rules and the resulting state are the web setup's.
package firstrun

import (
	"context"
	"errors"
	"net"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"time"

	"owngit/internal/server"
)

// ErrStopped means the owner stopped setup, or the server was asked to stop,
// before setup was complete. Nothing was saved.
var ErrStopped = errors.New("setup stopped before it was complete")

// errBackground means OwnGit is not in the terminal's foreground, as after
// `owngit serve &`, so the terminal is left alone.
var errBackground = errors.New("OwnGit is not in the terminal's foreground")

// setupMode is the terminal in setup mode.
type setupMode struct {
	// restore returns the terminal to the mode it had before.
	restore func() error
	// resume applies setup mode again after OwnGit continues from a stop.
	resume func() error
	// escapes is true when escape sequences can be written.
	escapes bool
}

// Config describes the terminal and the server the setup runs against.
type Config struct {
	// Input and Output are the terminal. Both must be terminals.
	Input, Output *os.File
	// Getenv reads the environment; nil means os.Getenv.
	Getenv func(string) string
	// App saves the answers. App.Approvals must be set.
	App *server.App
	// Logs holds server log lines while questions are open.
	Logs *LogGate
	// Origin is the owner-facing address, such as http://127.0.0.1:7654.
	Origin string
	// Listen is the address the server listens on.
	Listen string
	// ListenSaved is true when Listen came from the saved network settings
	// rather than a --listen option.
	ListenSaved bool
	// SuggestedFolder is offered for the repository folder.
	SuggestedFolder string
	// OpenBrowser opens the setup page; nil only prints its address.
	OpenBrowser func(string) error
	// StateDir is added to the printed Tailscale command; "" when it is the
	// default.
	StateDir string
	// Tailscale detects Tailscale; nil means DetectTailscale.
	Tailscale func(context.Context) Tailscale
}

// Interactive reports whether setup can run in this terminal: both input
// and output must be terminals, and OwnGit must be in their foreground. A
// background job, such as `owngit serve &` from a shell, keeps the setup
// file flow, because using the terminal would stop it.
func Interactive(input, output *os.File) bool {
	return isTerminal(input) && isTerminal(output) && foreground(input) && foreground(output)
}

// Run asks the setup questions in the terminal. It returns nil once setup is
// complete, by the terminal or by an approved browser, and ErrStopped when
// the owner stopped. Any other error means the terminal could not be used
// and nothing was asked. The terminal mode is always restored.
func Run(ctx context.Context, config Config) error {
	mode, err := enterSetupMode(config.Input, config.Output)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	// Every goroutine that reads the terminal or changes its mode ends
	// before Run returns, and the mode is restored last. After setup the
	// server never touches the terminal again, so a later Ctrl-Z and `bg`
	// cannot stop it for reading from the background.
	var helpers sync.WaitGroup
	defer func() {
		cancel()
		helpers.Wait()
		_ = mode.restore()
	}()
	if len(continueSignals) != 0 {
		continued := make(chan os.Signal, 1)
		signal.Notify(continued, continueSignals...)
		helpers.Add(1)
		go func() {
			defer helpers.Done()
			defer signal.Stop(continued)
			for {
				select {
				case <-continued:
					_ = mode.resume()
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	if len(stopSignals) != 0 {
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, stopSignals...)
		helpers.Add(1)
		go func() {
			defer helpers.Done()
			defer signal.Stop(signals)
			select {
			case <-signals:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	input := make(chan []byte, 16)
	helpers.Add(1)
	go func() {
		defer helpers.Done()
		readTerminal(ctx.Done(), config.Input, input, func() { _ = mode.resume() })
	}()
	getenv := config.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	colorOK := colorDepth(getenv, mode.escapes)
	f, pending := newFlow(ctx, config, input, colorOK, getenv)
	f.console.pending = pending
	config.Logs.Hold()
	defer config.Logs.Release()
	if err := f.run(); err != nil {
		if errors.Is(err, errStopped) {
			return ErrStopped
		}
		return err
	}
	return nil
}

// readTick bounds how long the terminal reader waits before it checks
// whether setup ended, and how long Run waits for it.
const readTick = 100 * time.Millisecond

func newFlow(ctx context.Context, config Config, input <-chan []byte, colors depth, getenv func(string) string) (*flow, []byte) {
	con := &console{ctx: ctx, input: input, finished: config.App.Approvals.Done()}
	chosen, pending := backgroundTheme(con, config.Output, colors, getenv)
	s := &screen{
		out: config.Output, painter: painter{depth: colors, theme: chosen},
		columns: func() (int, bool) { return terminalColumns(config.Output) },
		flush:   config.Logs.Flush,
	}
	con.echo = s.write
	detect := config.Tailscale
	if detect == nil {
		detect = DetectTailscale
	}
	found := make(chan Tailscale, 1)
	go func() { found <- detect(ctx) }()
	f := &flow{
		ctx: ctx, lang: localeLanguage(getenv), screen: s, console: con, app: config.App,
		origin: config.Origin, listen: config.Listen, suggested: config.SuggestedFolder,
		openBrowser: config.OpenBrowser, stateDir: config.StateDir, tailscale: found,
	}
	f.network, f.otherDevices = networkReach(config.Listen, config.Origin)
	f.listenSaved = config.ListenSaved
	return f, pending
}

// backgroundTheme asks the terminal for its background color and returns
// the theme and the keys typed meanwhile, which must not be lost.
func backgroundTheme(con *console, output *os.File, colors depth, getenv func(string) string) (theme, []byte) {
	if colors == depthNone {
		return themeUnknown, nil
	}
	if _, err := output.WriteString(backgroundQuery); err != nil {
		return fallbackTheme(getenv), nil
	}
	var reply []byte
	deadline := time.NewTimer(300 * time.Millisecond)
	defer deadline.Stop()
wait:
	for !queryComplete(reply) {
		select {
		case chunk, ok := <-con.input:
			if !ok {
				return fallbackTheme(getenv), reply
			}
			reply = append(reply, chunk...)
		case <-deadline.C:
			break wait
		}
	}
	found, ok, rest := parseBackground(reply)
	if !ok {
		return fallbackTheme(getenv), rest
	}
	return found, rest
}

func fallbackTheme(getenv func(string) string) theme {
	if found, ok := colorFGBGTheme(getenv); ok {
		return found
	}
	return themeUnknown
}

// networkReach reports whether other devices can connect to listen, and the
// address they would use when OwnGit accepts it and it differs from origin.
func networkReach(listen, origin string) (bool, string) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return false, ""
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return host != "localhost", ""
	}
	if ip.IsLoopback() {
		return false, ""
	}
	if ip.IsUnspecified() {
		// Every address is served, but only approved host names are accepted,
		// so there is no address to print.
		return true, ""
	}
	if parsed, err := url.Parse(origin); err == nil {
		if originIP := net.ParseIP(parsed.Hostname()); originIP != nil && !originIP.IsLoopback() {
			return true, ""
		}
		if parsed.Hostname() != "localhost" && net.ParseIP(parsed.Hostname()) == nil {
			return true, ""
		}
	}
	return true, "http://" + net.JoinHostPort(ip.String(), port) + "/"
}
