package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// The Apple code-signing identities of OwnGit's macOS artifacts. They are the
// same for signed and unsigned builds, so an app's identity never depends on
// how it was built.
const (
	// appleBundleID is the app's CFBundleIdentifier, which codesign also uses
	// as the app's signing identifier.
	appleBundleID = "app.owngit.OwnGit"
	// appleToolIdentifier is the signing identifier of the owngit binary,
	// which has no bundle to take one from.
	appleToolIdentifier = "app.owngit.cli"
	// appleDiskImageIdentifier is the signing identifier of the DMG.
	appleDiskImageIdentifier = "app.owngit.dmg"
	// appHelperPath is where OwnGit.app holds the owngit binary, relative to
	// the app. Apple reserves Contents/Helpers for helper tools; code placed
	// among resources is sealed as data instead of being validated as code.
	// Launcher.swift starts the binary at this same path.
	appHelperPath = "Contents/Helpers/owngit"
)

// developerIDIdentityPattern is the certificate name that security
// find-identity -v -p codesigning prints for a Developer ID Application
// identity. The Team ID in parentheses is the certificate's organizational
// unit.
var (
	developerIDIdentityPattern = regexp.MustCompile(`^Developer ID Application: [^\x00-\x1f\x7f]+ \(([A-Z0-9]{10})\)$`)
	teamIDPattern              = regexp.MustCompile(`^[A-Z0-9]{10}$`)
	notaryProfilePattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// appleSignature records a Developer ID signature and the notarization that
// Apple accepted for it.
type appleSignature struct {
	TeamID           string `json:"team_id"`
	Identifier       string `json:"identifier"`
	NotarySubmission string `json:"notary_submission"`
	// UnsignedSHA256 is the digest of the Go linker output before signing.
	// The Go build is reproducible and the signature is not, so this is the
	// digest someone rebuilding the source can compare. Containers that are
	// made on the release Mac, such as the DMG, have no reproducible unsigned
	// form and leave it empty.
	UnsignedSHA256 string `json:"unsigned_sha256,omitempty"`
}

// commandRunner runs a tool and returns its standard output. A failure
// carries the tool's error output. Tests replace it with fakes.
type commandRunner func(name string, arguments []string, extraEnvironment []string) (string, error)

// appleSigner signs code with a Developer ID Application identity and
// notarizes it with a notarytool keychain profile. Only names pass through
// it; codesign and notarytool read the key and the credential from the
// keychain themselves.
type appleSigner struct {
	identity string
	team     string
	profile  string
	xcrun    string
	run      commandRunner
}

// newAppleSigner returns nil when neither option is given, which means an
// unsigned build. Giving only one is an error: a signature without
// notarization fails Gatekeeper, and a profile alone has nothing to submit.
func newAppleSigner(identity, profile, xcrun string, run commandRunner) (*appleSigner, error) {
	if identity == "" && profile == "" {
		return nil, nil
	}
	if identity == "" || profile == "" {
		return nil, errors.New("-sign-identity and -notary-profile are used together: a signed release must also be notarized")
	}
	match := developerIDIdentityPattern.FindStringSubmatch(identity)
	if match == nil {
		return nil, fmt.Errorf("-sign-identity %q must be a Developer ID Application certificate name as security find-identity -v -p codesigning prints it, for example \"Developer ID Application: Name (TEAMID1234)\"", identity)
	}
	if !notaryProfilePattern.MatchString(profile) {
		return nil, fmt.Errorf("-notary-profile %q must be the name of a notarytool keychain profile (letters, digits, '.', '_' or '-')", profile)
	}
	if runtime.GOOS != "darwin" {
		return nil, errors.New("signing and notarization run Apple's tools and need a macOS host")
	}
	return &appleSigner{identity: identity, team: match[1], profile: profile, xcrun: xcrun, run: run}, nil
}

// sign replaces any existing signature, such as the Go linker's ad hoc one,
// with a timestamped Developer ID signature. Executables get the hardened
// runtime, which notarization requires; OwnGit needs no exception
// entitlements. An empty identifier keeps the bundle ID of a bundle.
func (signer *appleSigner) sign(path, identifier string, hardenedRuntime bool) error {
	arguments := []string{"--force", "--sign", signer.identity, "--timestamp"}
	if hardenedRuntime {
		arguments = append(arguments, "--options", "runtime")
	}
	if identifier != "" {
		arguments = append(arguments, "--identifier", identifier)
	}
	if _, err := signer.run("codesign", append(arguments, path), nil); err != nil {
		return fmt.Errorf("sign %s: %w", filepath.Base(path), err)
	}
	return nil
}

// signTool signs and notarizes a bare owngit binary in place, together
// with the icon app beside it when app is not "". The binary cannot hold a
// stapled ticket, so Gatekeeper finds its ticket online; the app gets its
// ticket stapled and must pass Gatekeeper's check.
func (signer *appleSigner) signTool(binary, app string) (*appleSignature, error) {
	unsigned, err := sha256File(binary)
	if err != nil {
		return nil, err
	}
	if err := signer.sign(binary, appleToolIdentifier, true); err != nil {
		return nil, err
	}
	if err := checkDeveloperIDSignature(signer.run, binary, signer.team, appleToolIdentifier, false); err != nil {
		return nil, err
	}
	if app != "" {
		if err := signer.sign(app, "", true); err != nil {
			return nil, err
		}
		if err := checkDeveloperIDSignature(signer.run, app, signer.team, appleBundleID, true); err != nil {
			return nil, err
		}
	}
	// notarytool accepts a zip, a disk image or an installer package, not a
	// bare Mach-O file. One zip holds the binary and the app, so one
	// submission covers both.
	scratch, err := os.MkdirTemp("", "owngit-notarize-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	archive := filepath.Join(scratch, filepath.Base(binary)+".zip")
	zip := []string{"-c", "-k", "--keepParent", binary, archive}
	if app != "" {
		payload := filepath.Join(scratch, "payload")
		for _, source := range []string{binary, app} {
			if _, err := signer.run("ditto", []string{source, filepath.Join(payload, filepath.Base(source))}, nil); err != nil {
				return nil, fmt.Errorf("stage %s for notarization: %w", filepath.Base(source), err)
			}
		}
		zip = []string{"-c", "-k", payload, archive}
	}
	if _, err := signer.run("ditto", zip, nil); err != nil {
		return nil, fmt.Errorf("zip %s for notarization: %w", filepath.Base(binary), err)
	}
	submission, err := signer.notarize(archive)
	if err != nil {
		return nil, err
	}
	if app != "" {
		if err := signer.staple(app); err != nil {
			return nil, fmt.Errorf("Apple accepted the binary and app (submission %s), but %w", submission, err)
		}
		if err := signer.assess(app, "execute"); err != nil {
			return nil, fmt.Errorf("Apple accepted the binary and app (submission %s), but %w", submission, err)
		}
	}
	return &appleSignature{
		TeamID: signer.team, Identifier: appleToolIdentifier,
		NotarySubmission: submission, UnsignedSHA256: unsigned,
	}, nil
}

// notaryStatus is the object notarytool prints with --output-format json.
type notaryStatus struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// notarize uploads a zip or disk image and waits for Apple's verdict.
//
// The submission is uploaded first and its id printed before the wait, so an
// interrupted or failed wait still leaves the id needed to follow the
// submission, which Apple keeps processing. The verdict is the status that
// notarytool reports, not its exit code, which is zero for a rejected
// submission in some notarytool versions.
func (signer *appleSigner) notarize(path string) (string, error) {
	name := filepath.Base(path)
	auth := []string{"--keychain-profile", signer.profile, "--output-format", "json"}
	output, err := signer.run(signer.xcrun, append([]string{"notarytool", "submit", path, "--no-wait"}, auth...), nil)
	if err != nil {
		return "", fmt.Errorf("submit %s for notarization: %w", name, err)
	}
	var submitted notaryStatus
	if json.Unmarshal([]byte(output), &submitted) != nil || submitted.ID == "" {
		return "", fmt.Errorf("notarytool submit printed no submission id for %s: %q; check xcrun notarytool history --keychain-profile %s", name, strings.TrimSpace(output), signer.profile)
	}
	id := submitted.ID
	fmt.Printf("submitted %s for notarization as %s; waiting for Apple\n", name, id)

	output, err = signer.run(signer.xcrun, append([]string{"notarytool", "wait", id}, auth...), nil)
	var result notaryStatus
	_ = json.Unmarshal([]byte(output), &result)
	switch {
	// A verdict belongs to the submission it names; one for another
	// submission says nothing about this file.
	case result.Status != "" && result.ID != id:
		return "", fmt.Errorf("notarytool wait for %s (submission %s) reported status %s for submission %q instead; check with: xcrun notarytool info %s --keychain-profile %s", name, id, result.Status, result.ID, id, signer.profile)
	case result.Status == "Accepted" && err == nil:
		fmt.Printf("notarized %s (submission %s)\n", name, id)
		return id, nil
	case result.Status == "Invalid" || result.Status == "Rejected":
		return "", fmt.Errorf("Apple did not accept %s (submission %s): %s: %s; %s", name, id, result.Status, result.Message, signer.notaryLog(id))
	}
	reason := strings.TrimSpace(output)
	if err != nil {
		reason = err.Error()
	}
	return "", fmt.Errorf("notarization of %s (submission %s) did not finish: %s; Apple may still be processing it. Check with: xcrun notarytool info %s --keychain-profile %s", name, id, reason, id, signer.profile)
}

// notaryLog summarizes the developer log of a finished submission: Apple's
// summary and every issue it found.
func (signer *appleSigner) notaryLog(id string) string {
	output, err := signer.run(signer.xcrun, []string{"notarytool", "log", id, "--keychain-profile", signer.profile}, nil)
	if err != nil {
		return fmt.Sprintf("the notary log could not be read (%v); read it with: xcrun notarytool log %s --keychain-profile %s", err, id, signer.profile)
	}
	var log struct {
		StatusSummary string `json:"statusSummary"`
		Issues        []struct {
			Severity string `json:"severity"`
			Path     string `json:"path"`
			Message  string `json:"message"`
		} `json:"issues"`
	}
	if json.Unmarshal([]byte(output), &log) != nil {
		return "notary log: " + strings.TrimSpace(output)
	}
	lines := []string{"notary log: " + log.StatusSummary}
	for _, issue := range log.Issues {
		lines = append(lines, fmt.Sprintf("%s: %s: %s", issue.Severity, issue.Path, issue.Message))
	}
	return strings.Join(lines, "\n  ")
}

// staple attaches the notarization ticket to a disk image or an app, so Gatekeeper
// can check it offline, and validates the attached ticket.
func (signer *appleSigner) staple(path string) error {
	for _, action := range []string{"staple", "validate"} {
		if _, err := signer.run(signer.xcrun, []string{"stapler", action, path}, nil); err != nil {
			return fmt.Errorf("stapler %s %s: %w", action, filepath.Base(path), err)
		}
	}
	return nil
}

// assess asks Gatekeeper whether it would open an app ("execute") or a disk
// image ("open"). Gatekeeper rejects Developer ID code that is not notarized,
// so acceptance shows the notarization as well as the signature.
func (signer *appleSigner) assess(path, kind string) error {
	arguments := []string{"--assess", "--type", kind}
	if kind == "open" {
		arguments = append(arguments, "--context", "context:primary-signature")
	}
	if _, err := signer.run("spctl", append(arguments, "--verbose", path), nil); err != nil {
		return fmt.Errorf("Gatekeeper assessment of %s: %w", filepath.Base(path), err)
	}
	return nil
}

// developerIDRequirement is the part of Apple's Developer ID designated
// requirement that identifies the signer: an Apple-issued Developer ID
// Application certificate of this team, on code with this identifier.
func developerIDRequirement(team, identifier string) string {
	return fmt.Sprintf(`anchor apple generic and identifier "%s" and certificate 1[field.1.2.840.113635.100.6.2.6] exists and certificate leaf[field.1.2.840.113635.100.6.1.13] exists and certificate leaf[subject.OU] = "%s"`, identifier, team)
}

// checkDeveloperIDSignature accepts a signature only if codesign finds it
// intact and satisfying the Developer ID requirement of the team and
// identifier. deep also validates nested code, such as the app's helper.
func checkDeveloperIDSignature(run commandRunner, path, team, identifier string, deep bool) error {
	arguments := []string{"--verify", "--strict"}
	if deep {
		arguments = append(arguments, "--deep")
	}
	arguments = append(arguments, "--verbose=2", "-R="+developerIDRequirement(team, identifier), path)
	if _, err := run("codesign", arguments, nil); err != nil {
		return fmt.Errorf("%s is not signed by Developer ID team %s as %s: %w", filepath.Base(path), team, identifier, err)
	}
	return nil
}

// validateToolSignature checks the shape of the signature recorded for an
// owngit binary before it is checked against the binary.
func validateToolSignature(signature *appleSignature) error {
	if !teamIDPattern.MatchString(signature.TeamID) {
		return fmt.Errorf("recorded Team ID %q is not 10 upper-case letters or digits", signature.TeamID)
	}
	if signature.Identifier != appleToolIdentifier {
		return fmt.Errorf("recorded signing identifier %q, want %q", signature.Identifier, appleToolIdentifier)
	}
	if strings.TrimSpace(signature.NotarySubmission) == "" {
		return errors.New("the recorded signature names no notarization submission")
	}
	if !sha256Pattern.MatchString(signature.UnsignedSHA256) {
		return errors.New("the recorded signature has no SHA-256 of the unsigned build")
	}
	return nil
}
