package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"owngit/internal/server"
	"owngit/internal/state"
)

// healthCommand checks that the OwnGit server of a state directory
// answers its liveness check. It exits 0 when it does.
func healthCommand(arguments []string) error {
	flags := flag.NewFlagSet("health", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateDir := flags.String("state-dir", defaultStateDir(), "host-local state directory")
	if err := parseFlags(flags, arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("health takes no positional arguments")
	}
	target, _, err := healthAddress(*stateDir)
	if err != nil {
		return err
	}
	if err := checkHealth(target); err != nil {
		return fmt.Errorf("OwnGit does not answer at http://%s: %w", target, err)
	}
	fmt.Printf("OwnGit answers at http://%s\n", target)
	return nil
}

// healthAddress returns the host:port to check on this computer, and
// whether a running server published it. Without a published address it
// is the saved listen address or the default.
func healthAddress(stateDir string) (string, bool, error) {
	observed, savedListen := state.RunningObservation{}, ""
	if state.RequireExisting(stateDir) == nil {
		ctx := context.Background()
		store, err := openLiveState(ctx, stateDir)
		if err != nil {
			return "", false, err
		}
		defer store.Close()
		if observed, err = store.ObserveRunningNetwork(ctx); err != nil {
			return "", false, err
		}
		if saved, err := store.NetworkSettings(ctx); err == nil {
			savedListen = saved.Listen
		}
	}
	address, running := healthTarget(observed, savedListen)
	target, err := localTarget(address)
	return target, running, err
}

// healthTarget returns the address where the server of a state directory
// answers, and whether a running server published it: its bound address in
// the family of its listen setting, or else savedListen or the default.
func healthTarget(observed state.RunningObservation, savedListen string) (string, bool) {
	if record := observed.Record; observed.Server == state.ServerRunning && record != nil && record.Address != "" {
		// The listen setting says which family to use: 0.0.0.0 is
		// reached at 127.0.0.1 even when the socket reports [::].
		// The bound address has the actual port.
		listenHost, _, listenErr := net.SplitHostPort(record.Listen)
		_, port, addressErr := net.SplitHostPort(record.Address)
		if listenErr == nil && addressErr == nil {
			return net.JoinHostPort(listenHost, port), true
		}
		return record.Address, true
	}
	return cmp.Or(savedListen, server.DefaultListenAddress), false
}

// listenOf returns the address the server listens on, by its running
// record, or will listen on, by savedListen or the default.
func listenOf(observed state.RunningObservation, savedListen string) string {
	if record := observed.Record; observed.Server == state.ServerRunning && record != nil {
		return cmp.Or(record.Listen, record.Address, server.DefaultListenAddress)
	}
	return cmp.Or(savedListen, server.DefaultListenAddress)
}

// serverListen returns the address the server of stateDir listens on, or
// will listen on by its saved setting.
func serverListen(stateDir string) (string, error) {
	if err := state.RequireExisting(stateDir); errors.Is(err, state.ErrNotExist) {
		return server.DefaultListenAddress, nil
	} else if err != nil {
		return "", err
	}
	ctx := context.Background()
	store, err := openLiveState(ctx, stateDir)
	if err != nil {
		return "", err
	}
	defer store.Close()
	observed, err := store.ObserveRunningNetwork(ctx)
	if err != nil {
		return "", err
	}
	saved, err := store.NetworkSettings(ctx)
	if err != nil {
		return "", err
	}
	return listenOf(observed, saved.Listen), nil
}

// localTarget turns a listen address into one this computer can connect
// to: every-address listeners are reached through loopback.
func localTarget(address string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", fmt.Errorf("listen address %q: %w", address, err)
	}
	if ip, err := netip.ParseAddr(host); host == "" || err == nil && ip.IsUnspecified() {
		host = "127.0.0.1"
		if err == nil && ip.Is6() {
			host = "::1"
		}
	}
	return net.JoinHostPort(host, port), nil
}

var healthClient = &http.Client{
	Timeout:       5 * time.Second,
	Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// checkHealth asks the liveness check at target once.
func checkHealth(target string) error {
	response, err := healthClient.Get("http://" + target + server.HealthPath)
	if err != nil {
		return err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", response.StatusCode)
	}
	return nil
}

// serveErrorFile, in the state directory, holds the error that ended the
// last serve that could not start, so "owngit service install" can report it
// without reading the service log, which the installing account may not be
// allowed to read. A serve that starts listening removes it.
const serveErrorFile = "serve-error.txt"

// recordServeError writes the serve error into the state directory, if
// there is one that OwnGit may use: it opens the directory with
// state.OpenStateDirectory and writes through that handle with
// state.OpenOwnFile. A serve that was refused another account's folder, for
// example root's, or a folder on a share, writes nothing there, and no link
// leads the error elsewhere.
func recordServeError(stateDir string, err error) {
	directory, openErr := state.OpenStateDirectory(stateDir)
	if openErr != nil {
		return
	}
	defer directory.Close()
	file, openErr := state.OpenOwnFile(directory, serveErrorFile, os.O_WRONLY|os.O_CREATE)
	if openErr != nil {
		return
	}
	defer file.Close()
	if file.Truncate(0) == nil {
		_, _ = file.WriteString(err.Error() + "\n")
	}
}

// clearServeError removes the serve error from the state directory that
// serve resolved and opened.
func clearServeError(stateDir string) {
	_ = os.Remove(filepath.Join(stateDir, serveErrorFile))
}

// maxServeError bounds how much of serveErrorFile is read.
const maxServeError = 4 << 10

// serveErrorSince returns the recorded serve error when it was written at or
// after since. The file belongs to the account that runs the service, which
// may not be the one reading it (root after switching accounts), so only a
// regular file is read, at most maxServeError bytes, and control characters
// are replaced before the text reaches a terminal.
func serveErrorSince(stateDir string, since time.Time) (string, bool) {
	path := filepath.Join(stateDir, serveErrorFile)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.ModTime().Before(since) {
		return "", false
	}
	file, err := os.OpenFile(path, os.O_RDONLY|serveErrorOpenFlags, 0)
	if err != nil {
		return "", false
	}
	defer file.Close()
	if opened, err := file.Stat(); err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return "", false
	}
	content, err := io.ReadAll(io.LimitReader(file, maxServeError))
	if err != nil {
		return "", false
	}
	return printable(strings.TrimSpace(string(content))), true
}

// errServeFailed is a service that ended with an error while starting.
type errServeFailed struct{ message string }

func (failed errServeFailed) Error() string { return failed.message }

// waitHealthy waits until the server of stateDir has published its address
// and answers there, and returns that address. It stops early with an
// errServeFailed when a serve started during the wait ended with an error.
func waitHealthy(stateDir string, timeout time.Duration) (string, error) {
	// File times can be a little coarser than the clock.
	since := time.Now().Add(-time.Second)
	deadline := time.Now().Add(timeout)
	for {
		// A serve that met a retryable state error is started again by
		// systemd, so only other errors end the wait.
		if message, failed := serveErrorSince(stateDir, since); failed && !strings.Contains(message, state.ErrInspectionUnstable.Error()) {
			return "", errServeFailed{message}
		}
		target, running, err := healthAddress(stateDir)
		if err == nil && !running {
			err = errors.New("no running server has published its address yet")
		}
		if err == nil {
			if err = checkHealth(target); err == nil {
				return target, nil
			}
		}
		if time.Now().After(deadline) {
			if errors.Is(err, errOlderSchema) {
				// Only the starting server upgrades, after its backup.
				err = errors.New("it is still backing up the state before it upgrades it, which takes a while with large repositories; the log says when it is done")
			}
			return "", err
		}
		time.Sleep(500 * time.Millisecond)
	}
}
