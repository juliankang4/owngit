package main

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"owngit/internal/apiclient"
	"owngit/internal/state"
)

// A password or credential file that other accounts can read is refused with
// what is wrong and a one-line fix, and the message never holds the secret.
func TestSecretFileRefusalSaysWhatIsWrongAndHowToFixIt(t *testing.T) {
	const secret = "valid-password-7351"
	path := filepath.Join(t.TempDir(), "secret")
	noErr(t, os.WriteFile(path, []byte(secret+"\n"), 0o600))
	noErr(t, state.ProtectPrivatePath(path, false))
	makePasswordFileBroad(t, path)

	var problem, run, fix string
	switch runtime.GOOS {
	case "windows":
		problem, run, fix = "can also access it", ". In PowerShell, run: icacls '", ` /remove '*S-1-1-0'`
	default:
		problem, run, fix = "its mode 0644 gives access to its group and all other users", ". To fix it, run: ", "chmod 600 '"+path+"'"
	}
	check := func(source, message, what string) {
		t.Helper()
		for _, want := range []string{what + " is not private: ", problem, run, fix, `(see "Password and token files" in docs/OPERATIONS.md).`} {
			if !strings.Contains(message, want) {
				t.Errorf("%s: message %q lacks %q", source, message, want)
			}
		}
		if strings.Contains(message, secret) {
			t.Errorf("%s: message holds the secret: %q", source, message)
		}
	}

	server, err := url.Parse("http://127.0.0.1:7654")
	noErr(t, err)
	_, err = readServerPassword(path, server, false, "The shared password file")
	var refusal *apiclient.Error
	if !errors.As(err, &refusal) || refusal.Code != "invalid_password_file" {
		t.Fatalf("shared password refusal %v", err)
	}
	check("shared password", refusal.Message, "The shared password file")

	_, err = readPrivatePassword(path)
	if err == nil {
		t.Fatal("reset-admin accepted the file")
	}
	check("reset-admin", err.Error(), "The password file")

	_, err = readTokenFile(path)
	if !errors.As(err, &refusal) || refusal.Code != "invalid_credential_file" {
		t.Fatalf("token refusal %v", err)
	}
	check("helper credential", refusal.Message, "The helper credential file")

	_, err = readPrivateImportSecret(path)
	if !errors.As(err, &refusal) || refusal.Code != "invalid_credential_file" {
		t.Fatalf("import credential refusal %v", err)
	}
	check("import credential", refusal.Message, "The credential file")

	// A file that cannot be inspected keeps the general sentence.
	_, err = readServerPassword(filepath.Join(t.TempDir(), "missing"), server, false, "The shared password file")
	if !errors.As(err, &refusal) || refusal.Message != "The shared password file is unavailable or is not private." {
		t.Fatalf("missing file refusal %v", err)
	}
}
