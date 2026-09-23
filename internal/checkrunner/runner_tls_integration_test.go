package checkrunner_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"owngit/internal/state"
)

func TestExternalRunnerTLSAndLifecycleBoundaries(t *testing.T) {
	t.Run("private CA succeeds and untrusted CA stops before HTTP or command", func(t *testing.T) {
		t.Run("trusted", func(t *testing.T) {
			fixture := newRunnerIntegrationFixture(t, "echo private-ca-ok")
			tlsServer, origin, caPEM := fixture.startPrivateTLSServer(nil)
			defer tlsServer.Close()
			client := fixture.client(origin)
			if err := client.AddCertificateAuthorities(caPEM); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := fixture.runner(client).Run(ctx); err != nil {
				t.Fatal(err)
			}
			if job := fixture.readJob(); job.Status != state.CheckJobPassed {
				t.Fatalf("trusted private-CA job status=%s summary=%s", job.Status, job.Summary)
			}
		})

		t.Run("untrusted", func(t *testing.T) {
			sentinel := filepath.Join(t.TempDir(), "command-ran")
			fixture := newRunnerIntegrationFixture(t, writeRunnerSentinelCommand(sentinel))
			var requests atomic.Int64
			tlsServer, origin, _ := fixture.startPrivateTLSServer(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					requests.Add(1)
					next.ServeHTTP(writer, request)
				})
			})
			defer tlsServer.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := fixture.runner(fixture.client(origin)).Run(ctx); err == nil {
				t.Fatal("untrusted private CA was accepted")
			}
			if requests.Load() != 0 {
				t.Fatalf("untrusted TLS reached HTTP handler %d times", requests.Load())
			}
			assertRunnerSentinelAbsent(t, sentinel)
			if job := fixture.readJob(); job.Status != state.CheckJobPending {
				t.Fatalf("untrusted TLS changed job status to %s", job.Status)
			}
		})
	})

	t.Run("redirect is refused without second-origin token request", func(t *testing.T) {
		sentinel := filepath.Join(t.TempDir(), "command-ran")
		fixture := newRunnerIntegrationFixture(t, writeRunnerSentinelCommand(sentinel))
		var secondRequests, secondAuthorization atomic.Int64
		second := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			secondRequests.Add(1)
			if request.Header.Get("Authorization") != "" {
				secondAuthorization.Add(1)
			}
			writer.WriteHeader(http.StatusNoContent)
		}))
		defer second.Close()
		first, origin := fixture.startHTTPServer(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if strings.HasSuffix(request.URL.Path, "/runner/claim") {
					http.Redirect(writer, request, second.URL+request.URL.Path, http.StatusTemporaryRedirect)
					return
				}
				next.ServeHTTP(writer, request)
			})
		})
		defer first.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := fixture.runner(fixture.client(origin)).Run(ctx); err == nil {
			t.Fatal("runner followed a cross-origin redirect")
		}
		if secondRequests.Load() != 0 || secondAuthorization.Load() != 0 {
			t.Fatalf("second origin requests=%d authorization=%d", secondRequests.Load(), secondAuthorization.Load())
		}
		assertRunnerSentinelAbsent(t, sentinel)
		if job := fixture.readJob(); job.Status != state.CheckJobPending {
			t.Fatalf("redirect changed job status to %s", job.Status)
		}
	})

	t.Run("revoked credential cannot claim", func(t *testing.T) {
		sentinel := filepath.Join(t.TempDir(), "command-ran")
		fixture := newRunnerIntegrationFixture(t, writeRunnerSentinelCommand(sentinel))
		if err := fixture.store.RevokeCheckRunnerToken(fixture.ctx, fixture.repository.ID, fixture.credential.ID, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		httpServer, origin := fixture.startHTTPServer(nil)
		defer httpServer.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := fixture.runner(fixture.client(origin)).Run(ctx); err == nil {
			t.Fatal("revoked runner credential claimed a job")
		}
		assertRunnerSentinelAbsent(t, sentinel)
		if job := fixture.readJob(); job.Status != state.CheckJobPending {
			t.Fatalf("revoked credential changed job status to %s", job.Status)
		}
	})

	t.Run("consent revoked between claim and start denies command", func(t *testing.T) {
		sentinel := filepath.Join(t.TempDir(), "command-ran")
		fixture := newRunnerIntegrationFixture(t, writeRunnerSentinelCommand(sentinel))
		revoked := make(chan error, 1)
		var once sync.Once
		httpServer, origin := fixture.startHTTPServer(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if strings.HasSuffix(request.URL.Path, "/start") {
					once.Do(func() {
						_, err := fixture.store.RevokeCheckConsent(request.Context(), fixture.repository.ID, time.Now().UTC())
						revoked <- err
					})
				}
				next.ServeHTTP(writer, request)
			})
		})
		defer httpServer.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := fixture.runner(fixture.client(origin)).Run(ctx); err == nil {
			t.Fatal("runner command started after consent revocation")
		}
		select {
		case err := <-revoked:
			if err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatal("runner never reached the start boundary")
		}
		assertRunnerSentinelAbsent(t, sentinel)
		job := fixture.readJob()
		if job.Status != state.CheckJobInterrupted || job.AttemptID != "" || job.StartedAt != nil {
			t.Fatalf("denied start status=%s attempt_registered=%t started=%t", job.Status, job.AttemptID != "", job.StartedAt != nil)
		}
	})

	t.Run("running cancellation is observed by lease renewal", func(t *testing.T) {
		ready := filepath.Join(t.TempDir(), "ready")
		fixture := newRunnerIntegrationFixture(t, holdRunnerCommand(ready))
		var renewals atomic.Int64
		httpServer, origin := fixture.startHTTPServer(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if strings.HasSuffix(request.URL.Path, "/renew") {
					renewals.Add(1)
				}
				next.ServeHTTP(writer, request)
			})
		})
		defer httpServer.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		result := make(chan error, 1)
		go func() { result <- fixture.runner(fixture.client(origin)).Run(ctx) }()
		if readyErr := waitForRunnerFile(ctx, ready, fixture); readyErr != nil {
			cancel()
			runnerFinished := false
			select {
			case <-result:
				runnerFinished = true
			case <-time.After(5 * time.Second):
			}
			job := fixture.readJob()
			t.Fatalf("runner command did not create its ready marker: wait_error=%v runner_finished=%v diagnostic=%s", readyErr, runnerFinished, runnerStoredAttemptDiagnostic(fixture, job))
		}
		waitForRunnerJobStatus(t, ctx, fixture, state.CheckJobStarted)
		if _, err := fixture.store.CancelCheckJob(ctx, fixture.repository.ID, fixture.job.ID, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-result:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("runner did not finish after cancellation")
		}
		job := fixture.readJob()
		if job.Status != state.CheckJobCancelled || job.AttemptID == "" {
			t.Fatalf("cancelled runner status=%s attempt_registered=%t", job.Status, job.AttemptID != "")
		}
		if renewals.Load() == 0 {
			t.Fatal("running cancellation completed without an observed lease renewal")
		}
	})
}

func (fixture *runnerIntegrationFixture) startPrivateTLSServer(wrap func(http.Handler) http.Handler) (*httptest.Server, *url.URL, []byte) {
	fixture.t.Helper()
	certificate, caPEM := privateRunnerCertificate(fixture.t)
	handler := fixture.app.Handler()
	if wrap != nil {
		handler = wrap(handler)
	}
	tlsServer := httptest.NewUnstartedServer(handler)
	tlsServer.Config.ErrorLog = log.New(io.Discard, "", 0)
	tlsServer.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
	tlsServer.StartTLS()
	origin := fixture.addHost(tlsServer.URL)
	return tlsServer, origin, caPEM
}

func privateRunnerCertificate(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	now := time.Now().UTC()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "OwnGit synthetic runner CA"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, ca, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	serverKeyDER, err := x509.MarshalPKCS8PrivateKey(serverKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: serverKeyDER}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
}

func writeRunnerSentinelCommand(path string) string {
	if runtime.GOOS == "windows" {
		return `echo invoked>"` + strings.ReplaceAll(path, `"`, `""`) + `"`
	}
	return "printf invoked > " + shellQuoteRunnerTest(path)
}

func holdRunnerCommand(ready string) string {
	if runtime.GOOS == "windows" {
		return `echo ready>"` + strings.ReplaceAll(ready, `"`, `""`) + `" && ping -t 127.0.0.1 >NUL`
	}
	return "printf ready > " + shellQuoteRunnerTest(ready) + "; while :; do sleep 1; done"
}

func shellQuoteRunnerTest(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func assertRunnerSentinelAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runner command sentinel exists or cannot be checked: %v", err)
	}
}

func waitForRunnerFile(ctx context.Context, path string, fixture *runnerIntegrationFixture) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		job := fixture.readJob()
		if job.Status != state.CheckJobPending && job.Status != state.CheckJobClaimed && job.Status != state.CheckJobStarted {
			return fmt.Errorf("job reached terminal status %s before the ready marker", job.Status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func runnerStoredAttemptDiagnostic(fixture *runnerIntegrationFixture, job state.CheckJob) string {
	if job.AttemptID == "" {
		return fmt.Sprintf("job_status=%s attempt_registered=false started=%t", job.Status, job.StartedAt != nil)
	}
	attempt, exists, err := fixture.store.CheckAttemptByID(fixture.ctx, fixture.repository.ID, job.AttemptID)
	if err != nil || !exists {
		return fmt.Sprintf("job_status=%s attempt_registered=true attempt_available=%t lookup_error=%t", job.Status, exists, err != nil)
	}
	parts := make([]string, 0, len(attempt.Results))
	for _, result := range attempt.Results {
		exitCode := "none"
		if result.ExitCode != nil {
			exitCode = fmt.Sprint(*result.ExitCode)
		}
		parts = append(parts, fmt.Sprintf("name=%q status=%s exit=%s output=%q cleanup=%q",
			result.Name, result.Status, exitCode, boundedRunnerTestDiagnostic(result.OutputExcerpt), boundedRunnerTestDiagnostic(result.CleanupError)))
	}
	return fmt.Sprintf("job_status=%s attempt_registered=true attempt_status=%s result_count=%d results=[%s]",
		job.Status, attempt.Status, len(attempt.Results), strings.Join(parts, "; "))
}

func boundedRunnerTestDiagnostic(value string) string {
	const limit = 4096
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "...[truncated]"
}

func waitForRunnerJobStatus(t *testing.T, ctx context.Context, fixture *runnerIntegrationFixture, expected string) {
	t.Helper()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		job := fixture.readJob()
		if job.Status == expected {
			return
		}
		if job.Status != state.CheckJobPending && job.Status != state.CheckJobClaimed {
			t.Fatalf("runner job reached %s before %s", job.Status, expected)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("runner job did not reach %s: %v", expected, ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestRunnerCAPEMOmitsPrivateKeyMaterial(t *testing.T) {
	certificate, caPEM := privateRunnerCertificate(t)
	if len(certificate.Certificate) == 0 || len(caPEM) == 0 || bytes.Contains(caPEM, []byte("PRIVATE KEY")) {
		t.Fatal("synthetic CA output did not contain only public certificate material")
	}
}
