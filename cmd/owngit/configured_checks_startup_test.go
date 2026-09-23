package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
)

func TestUnavailableCheckWorkspacePreservesDataAndDoesNotBlockServe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	store, err := state.Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(stateDir, "runtime", "check-jobs", strings.Repeat("a", 32))
	if err := os.MkdirAll(unknown, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(unknown, "unrelated.txt")
	if err := os.WriteFile(sentinel, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	listening := make(chan string, 1)
	result := make(chan error, 1)
	degraded := make(chan string, 1)
	go func() {
		result <- serveWithContext(ctx, []string{"--state-dir", stateDir, "--listen", "127.0.0.1:0", "--no-open"},
			func(string) error { return errors.New("opener must not run") }, func(format string, arguments ...any) {
				message := fmt.Sprintf(format, arguments...)
				if strings.Contains(message, "configured check runtime unavailable") {
					select {
					case degraded <- message:
					default:
					}
				}
				const prefix = "OwnGit listening on "
				if strings.HasPrefix(message, prefix) {
					select {
					case listening <- strings.TrimPrefix(message, prefix):
					default:
					}
				}
			})
	}()
	var address string
	select {
	case address = <-listening:
	case err := <-result:
		t.Fatalf("advisory runtime blocked serve: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not listen with an unavailable check workspace")
	}
	select {
	case <-degraded:
	default:
		t.Fatal("check runtime unavailability was not logged")
	}
	response, err := (&http.Client{Timeout: 2 * time.Second}).Get("http://" + address + "/")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop")
	}
	if content, err := os.ReadFile(sentinel); err != nil || string(content) != "preserve" {
		t.Fatalf("sentinel content=%q err=%v", content, err)
	}
}

func TestServeStartsAndStopsWithConfiguredCheckCoordinator(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listening := make(chan string, 1)
	result := make(chan error, 1)
	stateDir := filepath.Join(t.TempDir(), "state")
	go func() {
		result <- serveWithContext(ctx, []string{
			"--state-dir", stateDir,
			"--listen", "127.0.0.1:0",
			"--no-open",
		}, func(string) error { return errors.New("opener must not run") }, func(format string, arguments ...any) {
			message := fmt.Sprintf(format, arguments...)
			const prefix = "OwnGit listening on "
			if strings.HasPrefix(message, prefix) {
				select {
				case listening <- strings.TrimPrefix(message, prefix):
				default:
				}
			}
		})
	}()

	var address string
	select {
	case address = <-listening:
	case err := <-result:
		t.Fatalf("serve exited before listening: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not reach its listening boundary")
	}
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get("http://" + address + "/")
	if err != nil {
		t.Fatalf("request started serve: %v", err)
	}
	response.Body.Close()

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("serve shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop after cancellation")
	}
}
