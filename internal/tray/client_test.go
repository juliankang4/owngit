package tray

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"owngit/internal/server"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// fakeServer answers the tray status for the token in the access file of
// a state directory.
type fakeServer struct {
	*httptest.Server
	stateDir string
	token    string
	// proof is the secret the server proves its answers with; sign
	// computes the proof header of an answer from it.
	proof  string
	sign   func(secret, nonce string, body []byte) string
	answer func(http.ResponseWriter)
	// events, when set, answers the event feed; answer answers the rest.
	events   func(http.ResponseWriter)
	requests []string
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	fake := &fakeServer{stateDir: t.TempDir(), token: "first", proof: "first-proof", sign: state.TrayProof}
	fake.Server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fake.requests = append(fake.requests, request.URL.RequestURI()+" "+request.Header.Get("Authorization"))
		if request.Header.Get("Authorization") != "Bearer "+fake.token {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusUnauthorized)
			writer.Write([]byte(`{"ok":false,"error":{"code":"unauthorized"}}`))
			return
		}
		recorder := httptest.NewRecorder()
		if fake.events != nil && request.URL.Path == server.TrayEventsPath {
			fake.events(recorder)
		} else {
			fake.answer(recorder)
		}
		for name, values := range recorder.Header() {
			writer.Header()[name] = values
		}
		writer.Header().Set(state.TrayProofHeader, fake.sign(fake.proof, request.Header.Get(state.TrayNonceHeader), recorder.Body.Bytes()))
		writer.WriteHeader(recorder.Code)
		writer.Write(recorder.Body.Bytes())
	}))
	t.Cleanup(fake.Close)
	fake.answer = answerStatus(server.TrayStatus{OK: true, State: "running", Version: "1.1.3", Shown: true})
	fake.publish(t, fake.URL, fake.token)
	return fake
}

func (fake *fakeServer) publish(t *testing.T, url, token string) {
	t.Helper()
	content, err := json.Marshal(state.TrayAccess{URL: url, Token: token, Proof: fake.proof})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fake.stateDir, state.TrayAccessFile), content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func answerStatus(status server.TrayStatus) func(http.ResponseWriter) {
	return func(writer http.ResponseWriter) {
		writer.Header().Set("Content-Type", "application/json")
		json.NewEncoder(writer).Encode(status)
	}
}

func answer(code int, contentType, body string) func(http.ResponseWriter) {
	return func(writer http.ResponseWriter) {
		writer.Header().Set("Content-Type", contentType)
		writer.WriteHeader(code)
		writer.Write([]byte(body))
	}
}

// diagnosis records whether the checkup ran and answers with result.
type diagnosis struct {
	ran    int
	result Diagnosis
	err    error
}

func (d *diagnosis) run(context.Context, string) (Diagnosis, error) {
	d.ran++
	return d.result, d.err
}

func TestReadNamesWhatTheServerSays(t *testing.T) {
	fake := newFakeServer(t)
	checkup := &diagnosis{}
	client := NewClient(fake.stateDir, checkup.run)
	if report := client.Read(context.Background(), "ko"); report.Condition != Running || report.Status == nil || report.Status.Version != "1.1.3" || report.Dashboard != fake.URL {
		t.Fatalf("running: %+v", report)
	}
	if want := "/tray/status?lang=ko Bearer first"; len(fake.requests) != 1 || fake.requests[0] != want {
		t.Errorf("requests %q, want %q", fake.requests, want)
	}
	fake.answer = answerStatus(server.TrayStatus{OK: true, State: "attention", SetupRequired: true})
	if report := client.Read(context.Background(), "en"); report.Condition != Attention {
		t.Fatalf("attention: %+v", report)
	}
	if checkup.ran != 0 {
		t.Errorf("the checkup ran while the server answered")
	}
}

// A new start of the server writes a new token: a refused token makes the
// client read the file again once, and a second refusal is not taken for a
// stopped server.
func TestReadTakesTheTokenOfTheCurrentStart(t *testing.T) {
	fake := newFakeServer(t)
	checkup := &diagnosis{}
	client := NewClient(fake.stateDir, checkup.run)
	if report := client.Read(context.Background(), "en"); report.Condition != Running {
		t.Fatalf("first start: %+v", report)
	}
	fake.token = "second"
	fake.publish(t, fake.URL, "second")
	fake.requests = nil
	if report := client.Read(context.Background(), "en"); report.Condition != Running || len(fake.requests) != 2 {
		t.Fatalf("after a new start: %+v, requests %q", report, fake.requests)
	}
	fake.token = "third"
	if report := client.Read(context.Background(), "en"); report.Condition != Unavailable || checkup.ran != 0 {
		t.Fatalf("token refused again: %+v, checkup ran %d times", report, checkup.ran)
	}
}

// Every answer that is not the status is "Status unavailable", never
// "stopped", and runs no checkup.
func TestReadTreatsOtherAnswersAsUnavailable(t *testing.T) {
	for name, reply := range map[string]func(http.ResponseWriter){
		"status unavailable": answer(http.StatusServiceUnavailable, "application/json", `{"ok":false,"error":{"code":"status_unavailable"}}`),
		"plain text 503":     answer(http.StatusServiceUnavailable, "text/plain", "Service unavailable"),
		"not local":          answer(http.StatusForbidden, "application/json", `{"ok":false,"error":{"code":"not_local"}}`),
		"not found":          answer(http.StatusNotFound, "application/json", `{"ok":false,"error":{"code":"not_found"}}`),
		"another program":    answer(http.StatusOK, "text/html", "<html></html>"),
		"other JSON":         answer(http.StatusOK, "application/json", `{"ok":true,"state":"sleeping"}`),
		"not JSON":           answer(http.StatusOK, "application/json", `{"ok":tr`),
		"redirect":           answer(http.StatusFound, "text/plain", ""),
	} {
		t.Run(name, func(t *testing.T) {
			fake := newFakeServer(t)
			fake.answer = reply
			checkup := &diagnosis{result: Diagnosis{Stopped: true}}
			if report := NewClient(fake.stateDir, checkup.run).Read(context.Background(), "en"); report.Condition != Unavailable || report.Status != nil || checkup.ran != 0 {
				t.Errorf("%+v, checkup ran %d times", report, checkup.ran)
			}
		})
	}
}

// Without a connection the checkup decides: only its finding that the
// server does not run means stopped.
func TestReadAsksTheCheckupWithoutAConnection(t *testing.T) {
	fake := newFakeServer(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := "http://" + listener.Addr().String()
	listener.Close()
	fake.publish(t, closed, fake.token)
	for _, test := range []struct {
		name  string
		setup func()
		check diagnosis
		want  Report
	}{
		{"stopped", nil, diagnosis{result: Diagnosis{Stopped: true, Message: "OwnGit is not running.", Repair: "owngit service start"}},
			Report{Condition: Stopped, Message: "OwnGit is not running.", Repair: "owngit service start"}},
		{"silent", nil, diagnosis{result: Diagnosis{Message: "OwnGit runs but does not answer."}},
			Report{Condition: Unavailable, Message: "OwnGit runs but does not answer."}},
		{"checkup failed", nil, diagnosis{err: errors.New("cannot read the state")}, Report{Condition: Unavailable}},
		{"no access file", func() { os.Remove(filepath.Join(fake.stateDir, state.TrayAccessFile)) },
			diagnosis{result: Diagnosis{Stopped: true, Message: "OwnGit is not running."}}, Report{Condition: Stopped, Message: "OwnGit is not running."}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.setup != nil {
				test.setup()
			}
			checkup := test.check
			report := NewClient(fake.stateDir, checkup.run).Read(context.Background(), "en")
			if report != test.want || checkup.ran != 1 {
				t.Errorf("%+v, checkup ran %d times, want %+v", report, checkup.ran, test.want)
			}
		})
	}
}

// The token goes only to a plain loopback address.
func TestReadSendsTheTokenOnlyToThisComputer(t *testing.T) {
	fake := newFakeServer(t)
	for _, address := range []string{"http://192.0.2.1:7654", "https://127.0.0.1:7654", "http://user@127.0.0.1:7654", "http://127.0.0.1:7654/elsewhere", "http://owngit.example:7654"} {
		fake.publish(t, address, fake.token)
		checkup := &diagnosis{result: Diagnosis{Stopped: true}}
		if report := NewClient(fake.stateDir, checkup.run).Read(context.Background(), "en"); report.Condition != Unavailable || checkup.ran != 0 {
			t.Errorf("%s: %+v, checkup ran %d times", address, report, checkup.ran)
		}
	}
	if len(fake.requests) != 0 {
		t.Errorf("requests reached the server: %q", fake.requests)
	}
}

// Only an answer that proves it comes from the server holding this
// start's secret is used: a program that took the address answers without
// the secret, or replays a proof made for another nonce, and is "Status
// unavailable", never its data and never "stopped".
func TestReadUsesOnlyProvenAnswers(t *testing.T) {
	fake := newFakeServer(t)
	fake.answer = answerStatus(server.TrayStatus{OK: true, State: "running", DashboardURL: "http://127.0.0.1:1/elsewhere", CloneAddress: "http://192.0.2.1/git/"})
	var lastNonce string
	for name, sign := range map[string]func(string, string, []byte) string{
		"no proof":       func(string, string, []byte) string { return "" },
		"another secret": func(_, nonce string, body []byte) string { return state.TrayProof("guessed", nonce, body) },
		"another nonce": func(secret, _ string, body []byte) string {
			return state.TrayProof(secret, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", body)
		},
		"a changed body": func(secret, nonce string, body []byte) string {
			return state.TrayProof(secret, nonce, append(body, ' '))
		},
		"the last nonce": func(secret, nonce string, body []byte) string {
			stale := lastNonce
			lastNonce = nonce
			return state.TrayProof(secret, stale, body)
		},
	} {
		t.Run(name, func(t *testing.T) {
			fake.sign = sign
			checkup := &diagnosis{result: Diagnosis{Stopped: true}}
			client := NewClient(fake.stateDir, checkup.run)
			for range 2 {
				if report := client.Read(context.Background(), "en"); report.Condition != Unavailable || report.Message != webui.Text(webui.LangEN, webui.MsgTrayUnproven) || report.Status != nil || report.Dashboard != "" || checkup.ran != 0 {
					t.Fatalf("%+v, checkup ran %d times", report, checkup.ran)
				}
			}
		})
	}
	fake.sign = state.TrayProof
	report := NewClient(fake.stateDir, nil).Read(context.Background(), "en")
	if report.Condition != Running || report.Dashboard != fake.URL {
		t.Fatalf("the real server: %+v", report)
	}
	// The icon opens the address it knows, not the one in the answer.
	if view := NewView(report, "en", time.Now()); view.DashboardURL != fake.URL {
		t.Errorf("the icon opens %q", view.DashboardURL)
	}
}

// Opening the dashboard asks again: a status read while OwnGit ran does
// not open the browser at a program that took the address since, and a
// genuine server takes one request.
func TestDashboardIsProvenAtTheClick(t *testing.T) {
	fake := newFakeServer(t)
	client := NewClient(fake.stateDir, nil)
	if report := client.Read(context.Background(), "en"); report.Condition != Running || report.Dashboard != fake.URL {
		t.Fatalf("while OwnGit runs: %+v", report)
	}
	fake.requests = nil
	if dashboard, err := client.Dashboard(context.Background(), "en"); err != nil || dashboard != fake.URL || len(fake.requests) != 1 {
		t.Fatalf("genuine server: %q, %v, requests %q", dashboard, err, fake.requests)
	}
	// OwnGit stops and another program answers at its address.
	fake.sign = func(string, string, []byte) string { return "" }
	if dashboard, err := client.Dashboard(context.Background(), "en"); err == nil || dashboard != "" {
		t.Fatalf("imposter: %q, %v", dashboard, err)
	}
	// Nothing answers at all.
	fake.Close()
	if dashboard, err := client.Dashboard(context.Background(), "en"); err == nil || dashboard != "" {
		t.Fatalf("stopped: %q, %v", dashboard, err)
	}
}

// The owner waits for the check at the click, so a server that does not
// answer in time opens nothing, soon.
func TestDashboardGivesUpSoon(t *testing.T) {
	previous := dashboardTimeout
	t.Cleanup(func() { dashboardTimeout = previous })
	dashboardTimeout = 200 * time.Millisecond
	fake := newFakeServer(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	answer := fake.answer
	fake.answer = func(writer http.ResponseWriter) {
		<-release
		answer(writer)
	}
	started := time.Now()
	dashboard, err := NewClient(fake.stateDir, nil).Dashboard(context.Background(), "en")
	if err == nil || dashboard != "" || time.Since(started) > 5*time.Second {
		t.Fatalf("%q, %v after %s", dashboard, err, time.Since(started))
	}
}
