package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	testIdentity = "Developer ID Application: Example Maintainer (ABCDE12345)"
	testTeam     = "ABCDE12345"
	testProfile  = "owngit-notary"
	testSubmit   = "2efe2717-52ef-43a5-96dc-0797e4ca1041"
)

// fakeApple stands in for codesign, ditto, notarytool, stapler and spctl.
// It records every command and answers from the outcome fields, which
// default to a successful signing and an accepted notarization.
type fakeApple struct {
	calls []string
	// signError is returned by codesign --sign.
	signError error
	// waitOutput and waitError are what notarytool wait prints and returns.
	waitOutput string
	waitError  error
	// logOutput and logError are what notarytool log prints and returns.
	logOutput string
	logError  error
}

func (fake *fakeApple) run(name string, arguments []string, _ []string) (string, error) {
	fake.calls = append(fake.calls, name+" "+strings.Join(arguments, " "))
	switch {
	case name == "codesign" && slices.Contains(arguments, "--sign"):
		return "", fake.signError
	case name == "xcrun" && arguments[0] == "notarytool":
		switch arguments[1] {
		case "submit":
			return `{"id":"` + testSubmit + `","message":"Successfully uploaded file","path":"` + arguments[2] + `"}`, nil
		case "wait":
			if fake.waitOutput == "" && fake.waitError == nil {
				return `{"id":"` + testSubmit + `","message":"Processing complete","status":"Accepted"}`, nil
			}
			return fake.waitOutput, fake.waitError
		case "log":
			return fake.logOutput, fake.logError
		}
	}
	return "", nil
}

func (fake *fakeApple) signer() *appleSigner {
	return &appleSigner{identity: testIdentity, team: testTeam, profile: testProfile, xcrun: "xcrun", run: fake.run}
}

// called reports whether a recorded command starts with prefix.
func (fake *fakeApple) called(prefix string) bool {
	return slices.ContainsFunc(fake.calls, func(call string) bool { return strings.HasPrefix(call, prefix) })
}

func writeTestBinary(t *testing.T, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "owngit")
	noErr(t, os.WriteFile(path, []byte(data), 0o755))
	return path
}

func TestNewAppleSignerOptions(t *testing.T) {
	if signer, err := newAppleSigner("", "", "xcrun", runNativeCommand); signer != nil || err != nil {
		t.Fatalf("no options gave %v, %v; want an unsigned build", signer, err)
	}
	for _, test := range []struct {
		name, identity, profile, want string
	}{
		{"identity alone", testIdentity, "", "used together"},
		{"profile alone", "", testProfile, "used together"},
		{"ad hoc identity", "-", testProfile, "Developer ID Application certificate name"},
		{"development identity", "Apple Development: Example (ABCDE12345)", testProfile, "Developer ID Application certificate name"},
		{"identity hash", "F11CBC7FEAF273BF335CC496BDA48F183118298D", testProfile, "Developer ID Application certificate name"},
		{"short team", "Developer ID Application: Example (ABC)", testProfile, "Developer ID Application certificate name"},
		{"control character", "Developer ID Application: Ex\nample (ABCDE12345)", testProfile, "Developer ID Application certificate name"},
		{"profile with a space", testIdentity, "owngit notary", "keychain profile"},
		{"profile option", testIdentity, "--apple-id", "keychain profile"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := newAppleSigner(test.identity, test.profile, "xcrun", runNativeCommand)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error %v, want %q", err, test.want)
			}
		})
	}
	signer, err := newAppleSigner(testIdentity, testProfile, "xcrun", runNativeCommand)
	if runtime.GOOS != "darwin" {
		if err == nil || !strings.Contains(err.Error(), "macOS host") {
			t.Fatalf("error %v, want the macOS host requirement", err)
		}
		return
	}
	noErr(t, err)
	if signer.team != testTeam || signer.identity != testIdentity || signer.profile != testProfile {
		t.Fatalf("signer = %#v", signer)
	}
}

// Signing and notarizing a bare binary runs the documented steps in order and
// records the Go linker digest next to the notarization.
func TestSignToolSignsChecksAndNotarizes(t *testing.T) {
	fake := &fakeApple{}
	binary := writeTestBinary(t, "linker output")
	signature, err := fake.signer().signTool(binary)
	noErr(t, err)
	want := appleSignature{TeamID: testTeam, Identifier: appleToolIdentifier, NotarySubmission: testSubmit, UnsignedSHA256: sha256Bytes([]byte("linker output"))}
	if *signature != want {
		t.Fatalf("signature = %#v, want %#v", *signature, want)
	}
	if len(fake.calls) != 5 {
		t.Fatalf("ran %d commands: %q", len(fake.calls), fake.calls)
	}
	requirement := "-R=" + developerIDRequirement(testTeam, appleToolIdentifier)
	for index, want := range []string{
		"codesign --force --sign " + testIdentity + " --timestamp --options runtime --identifier app.owngit.cli " + binary,
		"codesign --verify --strict --verbose=2 " + requirement + " " + binary,
		"ditto -c -k --keepParent " + binary + " ",
		"xcrun notarytool submit ",
		"xcrun notarytool wait " + testSubmit + " --keychain-profile owngit-notary --output-format json",
	} {
		if !strings.HasPrefix(fake.calls[index], want) {
			t.Errorf("command %d = %q, want prefix %q", index, fake.calls[index], want)
		}
	}
	if !strings.HasSuffix(fake.calls[3], "owngit.zip --no-wait --keychain-profile owngit-notary --output-format json") {
		t.Errorf("submit command = %q", fake.calls[3])
	}
	for _, part := range []string{`identifier "app.owngit.cli"`, `certificate leaf[subject.OU] = "ABCDE12345"`, "1.2.840.113635.100.6.2.6", "1.2.840.113635.100.6.1.13"} {
		if !strings.Contains(requirement, part) {
			t.Errorf("requirement %q lacks %q", requirement, part)
		}
	}
}

// A missing or unusable identity stops at codesign with its own message,
// before anything is submitted to Apple.
func TestSignToolMissingIdentity(t *testing.T) {
	fake := &fakeApple{signError: &nativeCommandError{command: "codesign --force --sign", message: "error: The specified item could not be found in the keychain."}}
	_, err := fake.signer().signTool(writeTestBinary(t, "linker output"))
	if err == nil || !strings.Contains(err.Error(), "could not be found in the keychain") || !strings.Contains(err.Error(), "sign owngit") {
		t.Fatalf("error %v, want codesign's message", err)
	}
	if fake.called("xcrun notarytool") || fake.called("ditto") {
		t.Fatalf("continued after the signing failure: %q", fake.calls)
	}
}

// A rejected submission stops with the id, Apple's status and message, and
// the issues from the notary log, whatever notarytool's exit status was.
func TestNotarizeRejected(t *testing.T) {
	log := `{"jobId":"` + testSubmit + `","status":"Invalid","statusSummary":"Archive contains critical validation errors","issues":[{"severity":"error","path":"owngit.zip/owngit","message":"The executable does not have the hardened runtime enabled."}]}`
	invalid := `{"id":"` + testSubmit + `","message":"Processing complete","status":"Invalid"}`
	for _, test := range []struct {
		name    string
		waitErr error
	}{
		{"exit zero", nil},
		{"exit one", &nativeCommandError{command: "xcrun notarytool wait", message: "exit status 1"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeApple{waitOutput: invalid, waitError: test.waitErr, logOutput: log}
			_, err := fake.signer().notarize("/tmp/owngit.zip")
			if err == nil {
				t.Fatal("a rejected submission was accepted")
			}
			for _, want := range []string{"Apple did not accept owngit.zip", "submission " + testSubmit, "Invalid: Processing complete", "Archive contains critical validation errors", "error: owngit.zip/owngit: The executable does not have the hardened runtime enabled."} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q lacks %q", err, want)
				}
			}
			if !fake.called("xcrun notarytool log " + testSubmit + " --keychain-profile owngit-notary") {
				t.Errorf("did not read the notary log: %q", fake.calls)
			}
		})
	}
}

// When the log cannot be read, the rejection still names the id and says how
// to read the log.
func TestNotarizeRejectedWithoutLog(t *testing.T) {
	fake := &fakeApple{
		waitOutput: `{"id":"` + testSubmit + `","message":"Processing complete","status":"Rejected"}`,
		logError:   errors.New("network is down"),
	}
	_, err := fake.signer().notarize("/tmp/owngit.dmg")
	for _, want := range []string{"Rejected", testSubmit, "network is down", "xcrun notarytool log " + testSubmit + " --keychain-profile owngit-notary"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error %v lacks %q", err, want)
		}
	}
}

// An interrupted or failed wait has no verdict. The error keeps the id and
// the command that shows the result, because Apple keeps processing.
func TestNotarizeInterrupted(t *testing.T) {
	for _, test := range []struct {
		name   string
		output string
		err    error
		want   string
	}{
		{"signal", "", &nativeCommandError{command: "xcrun notarytool wait", message: "signal: interrupt"}, "signal: interrupt"},
		{"timeout", `{"id":"` + testSubmit + `","message":"Timeout","status":"In Progress"}`, &nativeCommandError{command: "xcrun notarytool wait", message: "exit status 1"}, "exit status 1"},
		{"unparsable success", "Processing complete", nil, "Processing complete"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeApple{waitOutput: test.output, waitError: test.err}
			_, err := fake.signer().notarize("/tmp/owngit.zip")
			for _, want := range []string{"did not finish", testSubmit, test.want, "Apple may still be processing it", "xcrun notarytool info " + testSubmit + " --keychain-profile owngit-notary"} {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Errorf("error %v lacks %q", err, want)
				}
			}
			if fake.called("xcrun notarytool log") {
				t.Errorf("read a log for an unfinished submission: %q", fake.calls)
			}
		})
	}
}

// An Accepted verdict for another submission is not a verdict for this
// file, so it stops like any unexpected answer.
func TestNotarizeRejectsVerdictForAnotherSubmission(t *testing.T) {
	other := "784642f5-0f55-4dfd-9bd1-0527e5395107"
	fake := &fakeApple{waitOutput: `{"id":"` + other + `","message":"Processing complete","status":"Accepted"}`}
	submission, err := fake.signer().notarize("/tmp/owngit.dmg")
	for _, want := range []string{testSubmit, other, "Accepted", "xcrun notarytool info " + testSubmit + " --keychain-profile owngit-notary"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("notarize returned %q, %v; want an error naming %q", submission, err, want)
		}
	}
}

// A submit that fails or prints no id stops before any wait.
func TestNotarizeSubmitFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		output string
		err    error
		want   string
	}{
		{"failed", "", &nativeCommandError{command: "xcrun notarytool submit", message: "Error: No Keychain password item found for profile: owngit-notary"}, "No Keychain password item found"},
		{"no id", "{}", nil, "printed no submission id"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			signer := &appleSigner{identity: testIdentity, team: testTeam, profile: testProfile, xcrun: "xcrun", run: func(name string, arguments []string, _ []string) (string, error) {
				calls = append(calls, strings.Join(arguments, " "))
				return test.output, test.err
			}}
			_, err := signer.notarize("/tmp/owngit.zip")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error %v, want %q", err, test.want)
			}
			if len(calls) != 1 {
				t.Fatalf("continued after submit: %q", calls)
			}
		})
	}
}

// release build signs and notarizes the darwin binary before running,
// archiving or recording it, and records the signature in the manifest.
func TestBuildTargetSignsDarwinBinary(t *testing.T) {
	requireGoToolchain(t)
	root := repoRoot(t)
	darwin, err := targetFor("darwin/arm64")
	noErr(t, err)
	readme := filepath.Join(root, "packaging", "archive", "README.txt.tmpl")
	version, err := versionFromSource(root)
	noErr(t, err)

	fake := &fakeApple{}
	out := t.TempDir()
	built, err := buildTarget("go", root, out, readme, darwin, version, fake.signer())
	noErr(t, err)
	// The output folder receives the archive only; the binary is staged
	// outside it.
	assertFolderHolds(t, out, darwin.archiveName(version))
	signature := built.AppleSignature
	if signature == nil || signature.TeamID != testTeam || signature.Identifier != appleToolIdentifier || signature.NotarySubmission != testSubmit {
		t.Fatalf("recorded signature = %#v", signature)
	}
	// The fake leaves the bytes alone, so the shipped digest is the linker
	// digest here; the ad hoc test covers a real signature changing them.
	for _, file := range built.Files {
		if file.Path == darwin.binary && file.SHA256 != signature.UnsignedSHA256 {
			t.Fatalf("unsigned digest %s, binary %s", signature.UnsignedSHA256, file.SHA256)
		}
	}
	if !fake.called("xcrun notarytool wait") {
		t.Fatalf("did not notarize: %q", fake.calls)
	}

	// A rejected notarization leaves nothing in the output folder, not even
	// the signed binary that Apple refused.
	rejected := &fakeApple{waitOutput: `{"id":"` + testSubmit + `","status":"Invalid","message":"Processing complete"}`, logOutput: "{}"}
	rejectedOut := t.TempDir()
	if _, err := buildTarget("go", root, rejectedOut, readme, darwin, version, rejected.signer()); err == nil || !strings.Contains(err.Error(), testSubmit) {
		t.Fatalf("rejected build returned %v", err)
	}
	assertFolderHolds(t, rejectedOut)

	// Only darwin binaries are signed.
	linux, err := targetFor("linux/amd64")
	noErr(t, err)
	other := &fakeApple{}
	unsigned, err := buildTarget("go", root, t.TempDir(), readme, linux, version, other.signer())
	noErr(t, err)
	if unsigned.AppleSignature != nil || len(other.calls) != 0 {
		t.Fatalf("signed a linux binary: %#v %q", unsigned.AppleSignature, other.calls)
	}
}

// A failed final verification of build, which runs after the checksums and
// manifest are written, says that they exist and how to check them again.
func TestFinalVerifyFailureNamesTheWrittenRecords(t *testing.T) {
	dir := sharedDist(t)
	err := verifyBuilt(dir, "false")
	for _, want := range []string{"the checksums and manifest in " + dir + " are written", "check them again with: release verify -dir " + dir} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error %v lacks %q", err, want)
		}
	}
}

func TestSigningOptionsNeedSomethingToSign(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("signing options are accepted only on macOS")
	}
	err := buildCommand([]string{"-source", repoRoot(t), "-out", t.TempDir(), "-targets", "linux/amd64", "-sign-identity", testIdentity, "-notary-profile", testProfile})
	if err == nil || !strings.Contains(err.Error(), "no darwin target") {
		t.Fatalf("build error %v", err)
	}
	for _, formats := range []string{"deb", "all"} {
		err := nativeCommand([]string{"-source", repoRoot(t), "-out", filepath.Join(t.TempDir(), "native"), "-formats", formats, "-baseline", testNativeBaseline, "-sign-identity", testIdentity, "-notary-profile", testProfile})
		if err == nil || !strings.Contains(err.Error(), "only to -formats macos") {
			t.Fatalf("native -formats %s error %v", formats, err)
		}
	}
}

func TestVerifyToolSignature(t *testing.T) {
	darwin, err := targetFor("darwin/arm64")
	noErr(t, err)
	linux, err := targetFor("linux/amd64")
	noErr(t, err)
	valid := appleSignature{TeamID: testTeam, Identifier: appleToolIdentifier, NotarySubmission: testSubmit, UnsignedSHA256: sha256Bytes(nil)}
	var checked []string
	run := func(name string, arguments []string, _ []string) (string, error) {
		checked = append(checked, name+" "+strings.Join(arguments, " "))
		return "", nil
	}

	noErr(t, verifyToolSignature(run, "darwin", darwin, nil, "/tmp/owngit"))
	noErr(t, verifyToolSignature(run, "linux", darwin, &valid, "/tmp/owngit"))
	if len(checked) != 0 {
		t.Fatalf("checked a signature it cannot or need not check: %q", checked)
	}
	noErr(t, verifyToolSignature(run, "darwin", darwin, &valid, "/tmp/owngit"))
	if want := "codesign --verify --strict --verbose=2 -R=" + developerIDRequirement(testTeam, appleToolIdentifier) + " /tmp/owngit"; len(checked) != 1 || checked[0] != want {
		t.Fatalf("checked %q, want %q", checked, want)
	}

	for _, test := range []struct {
		name   string
		target target
		mutate func(*appleSignature)
		want   string
	}{
		{"linux binary", linux, func(*appleSignature) {}, "for a linux binary"},
		{"team", darwin, func(s *appleSignature) { s.TeamID = "abc" }, "Team ID"},
		{"identifier", darwin, func(s *appleSignature) { s.Identifier = appleBundleID }, "signing identifier"},
		{"submission", darwin, func(s *appleSignature) { s.NotarySubmission = " " }, "notarization submission"},
		{"unsigned digest", darwin, func(s *appleSignature) { s.UnsignedSHA256 = "" }, "unsigned build"},
	} {
		t.Run(test.name, func(t *testing.T) {
			signature := valid
			test.mutate(&signature)
			err := verifyToolSignature(run, "darwin", test.target, &signature, "/tmp/owngit")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error %v, want %q", err, test.want)
			}
		})
	}
}

// The real codesign applies the Developer ID requirement: a validly signed
// Apple platform binary fails it with codesign's requirement message, which
// also shows that the requirement text parses.
func TestDeveloperIDRequirementRejectsOtherSigners(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("codesign is a macOS tool")
	}
	data, err := os.ReadFile("/usr/bin/true")
	noErr(t, err)
	path := writeTestBinary(t, string(data))
	err = checkDeveloperIDSignature(runNativeCommand, path, testTeam, appleToolIdentifier, false)
	if err == nil || !strings.Contains(err.Error(), "failed to satisfy specified code requirement") {
		t.Fatalf("error %v, want an unsatisfied requirement", err)
	}
}

// adHocRunner runs codesign, ditto, hdiutil, plutil and swiftc for real, so
// an ad hoc signature (codesign -s -) is made and verified with the
// production arguments. Ad hoc code cannot satisfy the Developer ID
// requirement, so verification drops only that requirement. notarytool,
// stapler and spctl are faked, and each spctl call hands its path to assess
// while the staged app still exists.
func adHocRunner(calls *[]string, assess func(kind, path string)) commandRunner {
	return func(name string, arguments []string, environment []string) (string, error) {
		*calls = append(*calls, name+" "+strings.Join(arguments, " "))
		switch {
		case name == "codesign" && arguments[0] == "--verify":
			plain := slices.DeleteFunc(slices.Clone(arguments), func(argument string) bool { return strings.HasPrefix(argument, "-R=") })
			return runNativeCommand(name, plain, environment)
		case name == "xcrun" && arguments[0] == "notarytool" && arguments[1] == "submit":
			return `{"id":"` + testSubmit + `"}`, nil
		case name == "xcrun" && arguments[0] == "notarytool" && arguments[1] == "wait":
			return `{"id":"` + testSubmit + `","status":"Accepted","message":"Processing complete"}`, nil
		case name == "xcrun" && arguments[0] == "stapler":
			return "", nil
		case name == "spctl":
			assess(arguments[2], arguments[len(arguments)-1])
			return "", nil
		}
		return runNativeCommand(name, arguments, environment)
	}
}

// assertFolderHolds fails unless dir holds exactly the named entries.
func assertFolderHolds(t *testing.T, dir string, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	noErr(t, err)
	var found []string
	for _, entry := range entries {
		found = append(found, entry.Name())
	}
	if strings.Join(found, ",") != strings.Join(names, ",") {
		t.Fatalf("%s holds %q, want %q", dir, found, names)
	}
}

func codesignDisplay(t *testing.T, path string) string {
	t.Helper()
	output, err := exec.Command("codesign", "--display", "--verbose=2", path).CombinedOutput()
	noErrf(t, err, "codesign --display %s: %s", path, output)
	return string(output)
}

// The production signing steps produce signatures that the real codesign
// accepts on a built Go binary, an app and a DMG.
func TestAdHocSigningWithRealCodesign(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("ad hoc signing needs codesign and the native Apple silicon toolchain")
	}
	root := repoRoot(t)
	dist := sharedDist(t)
	var calls []string
	assessed := map[string]bool{}
	run := adHocRunner(&calls, func(kind, path string) {
		assessed[kind] = true
		display := codesignDisplay(t, path)
		switch kind {
		case "execute":
			if !strings.Contains(display, "Identifier="+appleBundleID) || !strings.Contains(display, "runtime)") {
				t.Errorf("app signature:\n%s", display)
			}
			output, err := exec.Command("codesign", "--verify", "--deep", "--strict", "--verbose=2", path).CombinedOutput()
			noErrf(t, err, "codesign --verify --deep %s: %s", path, output)
			if !strings.Contains(string(output), "--validated:"+filepath.Join(path, appHelperPath)) {
				t.Errorf("the helper was not validated as nested code:\n%s", output)
			}
		case "open":
			if !strings.Contains(display, "Identifier="+appleDiskImageIdentifier) {
				t.Errorf("DMG signature:\n%s", display)
			}
		}
	})
	signer := &appleSigner{identity: "-", team: "", profile: testProfile, xcrun: "xcrun", run: run}

	t.Run("binary", func(t *testing.T) {
		document, err := readManifest(filepath.Join(dist, "manifest.json"))
		noErr(t, err)
		payload, err := loadPortablePayload(dist, document, "darwin/arm64")
		noErr(t, err)
		binary := writeTestBinary(t, string(payload.binary.data))
		signature, err := signer.signTool(binary)
		noErr(t, err)
		signed, err := sha256File(binary)
		noErr(t, err)
		if signature.UnsignedSHA256 != payload.binary.sha || signed == payload.binary.sha {
			t.Fatalf("unsigned %s, linker %s, signed %s", signature.UnsignedSHA256, payload.binary.sha, signed)
		}
		display := codesignDisplay(t, binary)
		if !strings.Contains(display, "Identifier="+appleToolIdentifier) || !strings.Contains(display, "runtime)") || strings.Contains(display, "linker-signed") {
			t.Fatalf("binary signature:\n%s", display)
		}
		output, err := exec.Command(binary, "version").Output()
		noErrf(t, err, "run the signed binary")
		if want := "owngit " + document.Version + "\n"; string(output) != want {
			t.Fatalf("signed binary printed %q, want %q", output, want)
		}
		_, metadata, err := buildInfo("go", binary)
		noErr(t, err)
		if metadata != payload.built.BuildInfo {
			t.Fatal("signing changed the embedded build metadata")
		}
	})

	t.Run("app and DMG", func(t *testing.T) {
		inputs, err := loadNativeInputs(root, filepath.Join(dist, "manifest.json"), testNativeBaseline, "go", "xcrun", "hdiutil", map[string]bool{"macos": true})
		noErr(t, err)
		payload := inputs.payloads["darwin/arm64"]
		payload.built.AppleSignature = &appleSignature{TeamID: signer.team}
		inputs.payloads["darwin/arm64"] = payload
		inputs.run, inputs.signer = run, signer
		calls = nil
		built, err := buildMacPrototype(inputs, t.TempDir())
		noErr(t, err)

		if !built.PublisherSigned || !built.Notarized || built.PublicReady || built.NativeInstallVerified || !built.Prototype {
			t.Fatalf("signed DMG claims: %#v", built)
		}
		if want := (appleSignature{Identifier: appleDiskImageIdentifier, NotarySubmission: testSubmit}); built.AppleSignature == nil || *built.AppleSignature != want {
			t.Fatalf("DMG signature record = %#v", built.AppleSignature)
		}
		provenance, err := provenanceBytes(inputs, payload, "macos-dmg", true)
		noErr(t, err)
		if built.PackagedProvenanceSHA256 != sha256Bytes(provenance) {
			t.Fatal("the packaged provenance does not state the signed state")
		}
		var record packageProvenance
		noErr(t, json.Unmarshal(provenance, &record))
		if !record.PublisherSigned || !record.Notarized || record.Status != nativeSignedStatus {
			t.Fatalf("provenance = %#v", record)
		}
		files := map[string]fileEntry{}
		for _, file := range built.Files {
			files[file.Path] = file
		}
		if _, ok := files["OwnGit.app/Contents/_CodeSignature/CodeResources"]; !ok {
			t.Fatal("the manifest does not list the app's signature")
		}
		if files["OwnGit.app/"+appHelperPath].SHA256 != payload.binary.sha {
			t.Fatal("signing the app changed the helper binary")
		}
		if !assessed["execute"] || !assessed["open"] {
			t.Fatalf("Gatekeeper assessments = %v", assessed)
		}

		// The steps run in the documented order.
		order := []string{
			"codesign --force --sign - --timestamp --options runtime ",
			"codesign --verify --strict --deep",
			"hdiutil create",
			"codesign --force --sign - --timestamp --identifier " + appleDiskImageIdentifier,
			"codesign --verify --strict --verbose=2",
			"hdiutil verify",
			"xcrun notarytool submit",
			"xcrun notarytool wait",
			"xcrun stapler staple",
			"xcrun stapler validate",
			"spctl --assess --type open --context context:primary-signature",
			"spctl --assess --type execute",
		}
		next := 0
		for _, call := range calls {
			if next < len(order) && strings.HasPrefix(call, order[next]) {
				next++
			}
		}
		if next != len(order) {
			t.Fatalf("stopped matching the order at %q; commands:\n%s", order[next], strings.Join(calls, "\n"))
		}
	})
}

// A signed DMG reaches the output folder only after Apple accepted it and
// the ticket and Gatekeeper checks passed. Every earlier failure leaves the
// output folder and its parent as they were, because the DMG seals the claim
// that it is notarized. Failures after acceptance name the submission.
func TestSignedDiskImageFailuresLeaveNoOutput(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("building the DMG needs the native Apple silicon toolchain")
	}
	root := repoRoot(t)
	dist := sharedDist(t)
	failure := &nativeCommandError{command: "fake", message: "fake failure"}
	for _, test := range []struct {
		name string
		fail func(name string, arguments []string) (string, error, bool)
		want []string
	}{
		{"rejected", func(name string, arguments []string) (string, error, bool) {
			switch {
			case name == "xcrun" && arguments[0] == "notarytool" && arguments[1] == "wait":
				return `{"id":"` + testSubmit + `","status":"Invalid","message":"Processing complete"}`, &nativeCommandError{command: "xcrun notarytool wait", message: "exit status 1"}, true
			case name == "xcrun" && arguments[0] == "notarytool" && arguments[1] == "log":
				return `{"statusSummary":"Archive contains critical validation errors","issues":[]}`, nil, true
			}
			return "", nil, false
		}, []string{"Apple did not accept", testSubmit}},
		{"staple", func(name string, arguments []string) (string, error, bool) {
			if name == "xcrun" && arguments[0] == "stapler" && arguments[1] == "staple" {
				return "", failure, true
			}
			return "", nil, false
		}, []string{"Apple accepted", "submission " + testSubmit, "stapler staple", "fake failure"}},
		{"gatekeeper", func(name string, arguments []string) (string, error, bool) {
			if name == "spctl" && arguments[2] == "execute" {
				return "", failure, true
			}
			return "", nil, false
		}, []string{"Apple accepted", "submission " + testSubmit, "Gatekeeper assessment of OwnGit.app", "fake failure"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			base := adHocRunner(&calls, func(string, string) {})
			run := func(name string, arguments []string, environment []string) (string, error) {
				if output, err, handled := test.fail(name, arguments); handled {
					return output, err
				}
				return base(name, arguments, environment)
			}
			inputs, err := loadNativeInputs(root, filepath.Join(dist, "manifest.json"), testNativeBaseline, "go", "xcrun", "hdiutil", map[string]bool{"macos": true})
			noErr(t, err)
			payload := inputs.payloads["darwin/arm64"]
			payload.built.AppleSignature = &appleSignature{}
			inputs.payloads["darwin/arm64"] = payload
			inputs.run, inputs.signer = run, &appleSigner{identity: "-", profile: testProfile, xcrun: "xcrun", run: run}
			parent := t.TempDir()
			out := filepath.Join(parent, "native")
			noErr(t, os.Mkdir(out, 0o755))

			_, err = buildMacPrototype(inputs, out)
			for _, want := range test.want {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Errorf("error %v lacks %q", err, want)
				}
			}
			assertFolderHolds(t, out)
			assertFolderHolds(t, parent, "native")
		})
	}
}

// fakeAppleTools writes shell stand-ins for codesign, spctl and xcrun into a
// new folder for PATH. codesign and spctl succeed. xcrun answers notarytool
// submit with testSubmit, and notarytool wait as $FAKE_WAIT says: "accept"
// prints an Accepted verdict, "hang" records $FAKE_WAIT_MARK and sleeps.
// stapler succeeds, and every other xcrun call, such as swiftc, runs the real
// xcrun.
func fakeAppleTools(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	xcrun := `#!/bin/sh
if [ "$1" = notarytool ]; then
  case "$2" in
  submit) echo '{"id":"` + testSubmit + `","message":"Successfully uploaded file"}' ;;
  wait)
    if [ "$FAKE_WAIT" = hang ]; then echo waiting > "$FAKE_WAIT_MARK"; exec sleep 120; fi
    echo '{"id":"` + testSubmit + `","message":"Processing complete","status":"Accepted"}' ;;
  esac
  exit 0
fi
[ "$1" = stapler ] && exit 0
exec /usr/bin/xcrun "$@"
`
	for name, body := range map[string]string{"codesign": "#!/bin/sh\nexit 0\n", "spctl": "#!/bin/sh\nexit 0\n", "xcrun": xcrun} {
		noErr(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755))
	}
	return dir
}

// An interrupt or SIGTERM while waiting for Apple takes the normal error
// path in the real release command: the waiting tool is stopped, the
// temporary folders are removed, nothing reaches the output folder, and the
// error names the submission and how to follow it.
func TestInterruptWhileWaitingForNotarization(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("signing runs only on a macOS host; the macOS format needs Apple silicon")
	}
	requireGoToolchain(t)
	root := repoRoot(t)
	tool := filepath.Join(t.TempDir(), "release")
	output, err := exec.Command("go", "build", "-o", tool, ".").CombinedOutput()
	noErrf(t, err, "build the release tool: %s", output)
	fakes := fakeAppleTools(t)
	signing := []string{"-sign-identity", testIdentity, "-notary-profile", testProfile}

	// release runs the tool with the fakes first on PATH and a private
	// temporary folder. With hang it interrupts the tool with signal once
	// the fake is waiting, and returns the exit status and combined output.
	release := func(t *testing.T, tmp, wait string, signal os.Signal, arguments ...string) (int, string) {
		t.Helper()
		mark := filepath.Join(t.TempDir(), "waiting")
		command := exec.Command(tool, arguments...)
		command.Env = append(os.Environ(), "PATH="+fakes+string(os.PathListSeparator)+os.Getenv("PATH"), "TMPDIR="+tmp, "FAKE_WAIT="+wait, "FAKE_WAIT_MARK="+mark)
		var combined strings.Builder
		command.Stdout, command.Stderr = &combined, &combined
		noErr(t, command.Start())
		if signal != nil {
			for start := time.Now(); ; time.Sleep(100 * time.Millisecond) {
				if _, err := os.Stat(mark); err == nil {
					break
				}
				if time.Since(start) > 5*time.Minute {
					command.Process.Kill()
					t.Fatalf("the fake never started waiting:\n%s", combined.String())
				}
			}
			noErr(t, command.Process.Signal(signal))
		}
		done := make(chan error, 1)
		go func() { done <- command.Wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Minute):
			command.Process.Kill()
			t.Fatalf("the release tool did not stop:\n%s", combined.String())
		}
		return command.ProcessState.ExitCode(), combined.String()
	}
	assertInterrupted := func(t *testing.T, code int, output, tmp string) {
		t.Helper()
		for _, want := range []string{"did not finish", testSubmit, "interrupted", "xcrun notarytool info " + testSubmit + " --keychain-profile owngit-notary"} {
			if code != 1 || !strings.Contains(output, want) {
				t.Errorf("exit %d, output lacks %q:\n%s", code, want, output)
			}
		}
		entries, err := os.ReadDir(tmp)
		noErr(t, err)
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "owngit-") {
				t.Errorf("left the temporary folder %s", entry.Name())
			}
		}
	}

	for _, test := range []struct {
		name   string
		signal os.Signal
	}{{"interrupt", os.Interrupt}, {"SIGTERM", syscall.SIGTERM}} {
		t.Run("build "+test.name, func(t *testing.T) {
			tmp, out := t.TempDir(), filepath.Join(t.TempDir(), "portable")
			code, output := release(t, tmp, "hang", test.signal, append([]string{"build", "-source", root, "-out", out, "-targets", "darwin/arm64"}, signing...)...)
			assertInterrupted(t, code, output, tmp)
			assertFolderHolds(t, out)
		})
	}

	t.Run("native interrupt", func(t *testing.T) {
		portable := filepath.Join(t.TempDir(), "portable")
		code, output := release(t, t.TempDir(), "accept", nil, append([]string{"build", "-source", root, "-out", portable}, signing...)...)
		if code != 0 {
			t.Fatalf("signed build with accepting fakes failed (%d):\n%s", code, output)
		}
		tmp, parent := t.TempDir(), t.TempDir()
		out := filepath.Join(parent, "native")
		code, output = release(t, tmp, "hang", os.Interrupt, append([]string{"native", "-source", root, "-manifest", filepath.Join(portable, "manifest.json"), "-out", out, "-formats", "macos", "-baseline", testNativeBaseline}, signing...)...)
		assertInterrupted(t, code, output, tmp)
		if !strings.Contains(output, out+" holds no macOS output") {
			t.Errorf("output does not say the folder holds no macOS output:\n%s", output)
		}
		assertFolderHolds(t, out)
		assertFolderHolds(t, parent, "native")
	})
}
