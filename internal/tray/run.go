package tray

import (
	"context"
	"errors"
)

// Options configure Run.
type Options struct {
	// StateDir is the state directory of the server the icon reports on.
	StateDir string
	// Diagnose runs the checkup when the server does not answer.
	Diagnose func(ctx context.Context, lang string) (Diagnosis, error)
	// Stop ends the icon when it is closed.
	Stop <-chan struct{}
}

// ErrAlreadyRunning means the icon of the state directory already runs in
// this sign-in session.
var ErrAlreadyRunning = errors.New("the OwnGit icon already runs for this state directory")
