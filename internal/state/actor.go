package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Actor records who made a durable change, as far as OwnGit knows it. The
// zero Actor means nobody was recorded, which every record written before
// schema 16 has. An actor is provenance, not authority: restoring one brings
// back no credential, and it is never inferred from a Git commit author.
type Actor struct {
	Kind string `json:"kind"`
	// ID names the credential (and later an account) for kinds that have one.
	ID string `json:"id,omitempty"`
	// Label is the credential's label at the time, kept because the
	// credential itself is machine-local and a restore does not bring it.
	Label string `json:"label,omitempty"`
}

const (
	// ActorAccess is general access: open mode or the shared password.
	ActorAccess = "access"
	// ActorAdministrator is the administrator password, typed or remembered.
	ActorAdministrator = "administrator"
	// ActorHelperCredential is a repository helper credential.
	ActorHelperCredential = "helper_credential"
)

// Validate accepts the zero Actor and every recorded kind with the fields it
// needs.
func (a Actor) Validate() error {
	switch a.Kind {
	case "":
		if a.ID != "" || a.Label != "" {
			return errors.New("an actor without a kind has details")
		}
	case ActorAccess, ActorAdministrator:
		if a.ID != "" || a.Label != "" {
			return fmt.Errorf("actor %s has a credential", a.Kind)
		}
	case ActorHelperCredential:
		if !validAttemptID(a.ID) || !validText(a.Label, 100) {
			return errors.New("helper credential actor is incomplete")
		}
	default:
		return fmt.Errorf("unknown actor kind %q", a.Kind)
	}
	return nil
}

// encodeActor returns the column text of a valid actor: "" for nobody.
func encodeActor(a Actor) (string, error) {
	if err := a.Validate(); err != nil {
		return "", err
	}
	if a.Kind == "" {
		return "", nil
	}
	encoded, err := json.Marshal(a)
	return string(encoded), err
}

// decodeActor reads column text that encodeActor wrote. Anything else is an
// error, not an unknown actor.
func decodeActor(text string) (Actor, error) {
	if text == "" {
		return Actor{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(text)))
	decoder.DisallowUnknownFields()
	var a Actor
	if err := decoder.Decode(&a); err != nil || decoder.More() {
		return Actor{}, fmt.Errorf("stored actor %q is not valid", text)
	}
	if a.Kind == "" {
		return Actor{}, fmt.Errorf("stored actor %q has no kind", text)
	}
	if err := a.Validate(); err != nil {
		return Actor{}, fmt.Errorf("stored actor: %w", err)
	}
	return a, nil
}
