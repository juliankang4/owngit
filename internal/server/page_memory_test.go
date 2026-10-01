package server

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"owngit/internal/state"
)

// This opt-in integration test runs the real executable. Resource probes are
// kept out of ordinary developer runs and must use disposable Linux state.
func TestStandaloneCodePagesStayBounded(t *testing.T) {
	binary := os.Getenv("OWNGIT_PAGE_TEST_BINARY")
	if binary == "" || runtime.GOOS != "linux" {
		t.Skip("requires a built executable in a disposable Linux environment")
	}
	root := t.TempDir()
	store, err := state.Open(context.Background(), filepath.Join(root, "state"))
	noErr(t, err)
	repos := filepath.Join(root, "repos")
	noErr(t, os.Mkdir(repos, 0o700))
	noErr(t, store.CompleteSetup(context.Background(), repos, "open", "", fixturePasswordHash(t, "admin-password"), true))
	manager := newRepositoryManager(t, store, filepath.Join(root, "runtime"))
	manager.SetRoot(repos)
	for _, id := range []string{"lines", "wide"} {
		_, err := manager.Create(context.Background(), id, "Synthetic page fixture")
		noErr(t, err)
		remote, err := manager.Path(id)
		noErr(t, err)
		var input strings.Builder
		if id == "lines" {
			content := strings.Repeat("a\n", 1000000)
			fmt.Fprintf(&input, "blob\nmark :1\ndata %d\n%s\n", len(content), content)
		} else {
			input.WriteString("blob\nmark :1\ndata 2\nx\n\n")
		}
		input.WriteString("commit refs/heads/main\ncommitter Synthetic <synthetic@example.invalid> 1756684800 +0000\ndata 8\nfixture\n")
		if id == "lines" {
			input.WriteString("M 100644 :1 lines.txt\n")
		} else {
			for i := 0; i < 100000; i++ {
				fmt.Fprintf(&input, "M 100644 :1 f%06d.txt\n", i)
			}
			input.WriteString("M 100644 :1 readme.md\n")
		}
		input.WriteString("\ndone\n")
		_, err = manager.Git.Run(context.Background(), remote, strings.NewReader(input.String()), "--git-dir", ".", "fast-import", "--quiet")
		noErr(t, err)
	}
	noErr(t, store.Close())
	available, err := net.Listen("tcp", "127.0.0.1:0")
	noErr(t, err)
	listen := available.Addr().String()
	noErr(t, available.Close())
	command := exec.Command(binary, "serve", "--state-dir", filepath.Join(root, "state"), "--listen", listen,
		"--tailscale", filepath.Join(root, "missing-tailscale"), "--no-open", "--no-update-check", "--headless=false")
	command.Stdout, command.Stderr = io.Discard, io.Discard
	noErr(t, command.Start())
	t.Cleanup(func() {
		_ = command.Process.Signal(os.Interrupt)
		ended := make(chan error, 1)
		go func() { ended <- command.Wait() }()
		select {
		case <-ended:
		case <-time.After(10 * time.Second):
			_ = command.Process.Kill()
			<-ended
		}
	})
	client := &http.Client{Timeout: 60 * time.Second}
	base := "http://" + listen
	ready := false
	for start := time.Now(); time.Since(start) < 10*time.Second; {
		response, err := client.Get(base + "/healthz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !ready {
		t.Fatal("synthetic executable did not become ready")
	}
	before := processRSS(command.Process.Pid)
	var mu sync.Mutex
	peak := before
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			mu.Lock()
			peak = max(peak, processRSS(command.Process.Pid))
			mu.Unlock()
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	defer func() { close(stop); <-done }()
	for _, target := range []string{"/repositories/lines/code?path=lines.txt", "/repositories/wide/code", "/repositories/wide/code?path=f099999.txt"} {
		body, status := dashboardGET(t, client, base+target)
		if status != http.StatusOK || len(body) > 4<<20 || !strings.Contains(body, "data-page-continuation") || (target == "/repositories/wide/code" && !strings.Contains(body, "readme.md")) {
			t.Fatalf("%s: status=%d bytes=%d", target, status, len(body))
		}
		t.Logf("%s: status=%d bytes=%d", target, status, len(body))
	}
	var wait sync.WaitGroup
	for i := 0; i < 3; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			body, status := dashboardGET(t, client, base+"/repositories/wide/code")
			if status != http.StatusOK || strings.Count(body, `class="flist__row"`) != 1000 {
				t.Errorf("parallel folder: status=%d bytes=%d rows=%d", status, len(body), strings.Count(body, `class="flist__row"`))
			}
		}()
	}
	wait.Wait()
	created := createShare(t, base, "lines", map[string]any{"label": "Synthetic browse"})
	shared, home := openShare(t, created)
	body, status := dashboardGET(t, shared, home+"/code?path=lines.txt")
	if status != http.StatusOK || strings.Count(body, `class="codetable__t"`) != 10000 || len(body) > 4<<20 {
		t.Fatalf("shared million-line file: status=%d bytes=%d", status, len(body))
	}
	body, status = dashboardGET(t, shared, home+"/code?path=lines.txt&line=12345")
	if status != http.StatusOK || !strings.Contains(body, `id="L12345"`) {
		t.Fatalf("shared line address: status=%d", status)
	}
	lineRemote, _ := manager.Path("lines")
	oid := apiGitOutput(t, lineRemote, "rev-parse", "refs/heads/main")
	body, status = dashboardGET(t, client, base+"/repositories/lines/commits/"+oid+"?path=lines.txt")
	if status != http.StatusOK || strings.Count(body, `class="difftable__r is-add"`) != 10000 || len(body) > 4<<20 {
		t.Fatalf("million-line selected diff: status=%d bytes=%d", status, len(body))
	}
	t.Logf("selected diff: status=%d bytes=%d", status, len(body))
	mu.Lock()
	observed := peak
	mu.Unlock()
	t.Logf("server RSS KiB: before=%d peak=%d after=%d", before, observed, processRSS(command.Process.Pid))
	if observed == 0 || observed > 256*1024 {
		t.Fatalf("bounded views exceeded process headroom: %d KiB", observed)
	}
	if readyFile := os.Getenv("OWNGIT_PAGE_BROWSER_READY"); readyFile != "" {
		noErr(t, os.WriteFile(readyFile, []byte(base), 0o600))
		for until := time.Now().Add(5 * time.Minute); time.Now().Before(until); {
			if _, err := os.Stat(readyFile + ".stop"); err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
}

func processRSS(pid int) int {
	content, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	for line := range strings.SplitSeq(string(content), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			value, _ := strconv.Atoi(fields[1])
			return value
		}
	}
	return 0
}
