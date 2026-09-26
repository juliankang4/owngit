package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
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
	address, running := server.DefaultListenAddress, false
	if state.RequireExisting(stateDir) == nil {
		ctx := context.Background()
		store, err := openLiveState(ctx, stateDir)
		if err != nil {
			return "", false, err
		}
		defer store.Close()
		observed, err := store.ObserveRunningNetwork(ctx)
		if err != nil {
			return "", false, err
		}
		if observed.Server == state.ServerRunning && observed.Record != nil && observed.Record.Address != "" {
			address, running = observed.Record.Address, true
		} else if saved, err := store.NetworkSettings(ctx); err == nil && saved.Listen != "" {
			address = saved.Listen
		}
	}
	target, err := localTarget(address)
	return target, running, err
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

// waitHealthy waits until the server of stateDir has published its address
// and answers there, and returns that address.
func waitHealthy(stateDir string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
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
			return "", err
		}
		time.Sleep(500 * time.Millisecond)
	}
}
