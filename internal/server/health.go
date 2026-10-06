package server

import (
	"net/http"

	"owngit/internal/state"
)

// HealthPath is the liveness check. It answers 200 with no body whenever
// this process serves HTTP, before and after setup, and reads no state, so
// it says nothing about the installation.
//
// It stays behind the Host check like every other path. The callers that
// matter, "owngit health", "owngit service status" and a container health
// check, run on this computer and use a loopback name, which the Host check
// always accepts. A monitor on another device uses an accepted name. A page
// that reaches this server through DNS rebinding is refused as before, and
// the setup exception for unknown Hosts does not include this path.
const HealthPath = "/healthz"

func (app *App) handleHealth(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	// A checker that sends a nonce gets the proof that this server holds the
	// key of state.HealthRunFile.
	if nonce := request.Header.Get(state.HealthNonceHeader); app.HealthKey != "" && state.ValidTrayNonce(nonce) {
		writer.Header().Set(state.HealthProofHeader, state.HealthProof(app.HealthKey, nonce))
	}
	writer.WriteHeader(http.StatusOK)
}
