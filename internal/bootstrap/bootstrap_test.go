package bootstrap

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"owngit/internal/state"
)

func TestIssueWritesSecretOnlyToOwnerFile(t *testing.T) {
	store, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "state"))
	noErr(t, err)
	defer store.Close()
	issuer := Issuer{Store: store, BaseURL: "http://127.0.0.1:7654"}
	path, err := issuer.Issue(context.Background())
	noErr(t, err)
	if filepath.Base(path) != ownerFileName {
		t.Fatalf("unexpected owner file: %s", path)
	}
	assertOwnerFilePrivate(t, path)
	content, err := os.ReadFile(path)
	noErr(t, err)
	if !strings.Contains(string(content), `"http://127.0.0.1:7654/setup"`) || !strings.Contains(string(content), "location.replace") {
		t.Fatal("owner file does not use a fragment redirect to the setup page")
	}
}

func TestPublicationFailurePreservesPreviousUsableCapability(t *testing.T) {
	store, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "state"))
	noErr(t, err)
	defer store.Close()
	issuer := Issuer{Store: store, BaseURL: "http://127.0.0.1:7654"}
	path, err := issuer.Issue(context.Background())
	noErr(t, err)
	oldToken, err := readOwnerToken(path)
	noErr(t, err)
	issuer.Publish = func(string, string) error { return errors.New("synthetic publication failure") }
	if _, err := issuer.Issue(context.Background()); err == nil {
		t.Fatal("Issue succeeded despite publication failure")
	}
	preserved, err := readOwnerToken(path)
	if err != nil || preserved != oldToken {
		t.Fatalf("owner file changed after failed publication: token preserved=%v err=%v", preserved == oldToken, err)
	}
	redeemed, err := store.RedeemBootstrap(context.Background(), oldToken, "setup-session", "csrf", time.Now(), time.Now().Add(time.Minute))
	if err != nil || !redeemed {
		t.Fatalf("previous setup capability was not usable after rollback: redeemed=%v err=%v", redeemed, err)
	}
}

func TestInterruptedIssueRecoversPreviousCapability(t *testing.T) {
	store, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "state"))
	noErr(t, err)
	defer store.Close()
	issuer := Issuer{Store: store, BaseURL: "http://127.0.0.1:7654"}
	path, err := issuer.Issue(context.Background())
	noErr(t, err)
	oldToken, err := readOwnerToken(path)
	noErr(t, err)
	oldSnapshot, err := store.BootstrapSnapshot(context.Background())
	noErr(t, err)
	newToken := strings.Repeat("n", 43)
	newHash := sha256.Sum256([]byte(newToken))
	newSnapshot := state.BootstrapSnapshot{Present: true, TokenHash: newHash[:], ExpiresAt: time.Now().Add(time.Minute).Unix()}
	noErr(t, writeJournal(store.Dir(), issueJournal{Old: oldSnapshot, New: newSnapshot}))
	noErr(t, store.PutBootstrap(context.Background(), newToken, time.Unix(newSnapshot.ExpiresAt, 0)))
	noErr(t, issuer.recoverInterruptedIssue(context.Background()))
	redeemed, err := store.RedeemBootstrap(context.Background(), oldToken, "setup-session", "csrf", time.Now(), time.Now().Add(time.Minute))
	if err != nil || !redeemed {
		t.Fatalf("recovery did not restore old capability: redeemed=%v err=%v", redeemed, err)
	}
}

func TestCompletionBarrierPreventsLateCapabilityPublication(t *testing.T) {
	store, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "state"))
	noErr(t, err)
	defer store.Close()
	issuer := &Issuer{Store: store, BaseURL: "http://127.0.0.1:7654"}
	path, err := issuer.Issue(context.Background())
	noErr(t, err)
	unlock, err := AcquireSetupLock(context.Background(), store.Dir())
	noErr(t, err)
	issueDone := make(chan error, 1)
	go func() {
		_, issueErr := issuer.Issue(context.Background())
		issueDone <- issueErr
	}()
	if err := store.CompleteSetup(context.Background(), t.TempDir(), "open", "", "admin-hash", true); err != nil {
		unlock()
		t.Fatal(err)
	}
	if err := RemoveOwnerSetupFiles(store.Dir()); err != nil {
		unlock()
		t.Fatal(err)
	}
	unlock()
	select {
	case err := <-issueDone:
		if !errors.Is(err, state.ErrSetupComplete) {
			t.Fatalf("late Issue error=%v, want setup complete", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("late Issue did not cross the completion barrier")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("owner setup file reappeared after completion: %v", err)
	}
	snapshot, err := store.BootstrapSnapshot(context.Background())
	if err != nil || snapshot.Present {
		t.Fatalf("bootstrap capability survived completion: snapshot=%+v err=%v", snapshot, err)
	}
}

func TestConcurrentIssuersPublishMatchingCapability(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	firstStore, err := state.Open(context.Background(), directory)
	noErr(t, err)
	defer firstStore.Close()
	secondStore, err := state.Open(context.Background(), directory)
	noErr(t, err)
	defer secondStore.Close()
	issuers := []*Issuer{
		{Store: firstStore, BaseURL: "http://127.0.0.1:7654"},
		{Store: secondStore, BaseURL: "http://127.0.0.1:7654"},
	}
	start := make(chan struct{})
	errorsCh := make(chan error, len(issuers))
	var group sync.WaitGroup
	for _, issuer := range issuers {
		group.Add(1)
		go func(issuer *Issuer) {
			defer group.Done()
			<-start
			_, err := issuer.Issue(context.Background())
			errorsCh <- err
		}(issuer)
	}
	close(start)
	group.Wait()
	close(errorsCh)
	for err := range errorsCh {
		noErr(t, err, "concurrent Issue")
	}
	token, err := readOwnerToken(filepath.Join(directory, ownerFileName))
	noErr(t, err)
	redeemed, err := firstStore.RedeemBootstrap(context.Background(), token, "setup-session", "csrf", time.Now(), time.Now().Add(time.Minute))
	if err != nil || !redeemed {
		t.Fatalf("published capability did not match durable state: redeemed=%v err=%v", redeemed, err)
	}
}
