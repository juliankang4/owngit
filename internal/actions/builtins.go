package actions

import (
	"fmt"
	"strings"
)

// Builtin describes a locally handled action. No code or tools are downloaded.
type Builtin struct {
	Name    string
	Status  string
	Outputs map[string]string
}

type builtinDefinition struct {
	inputs string
	tool   string
	code   string
}

// Input names follow each actions/<name>/main/action.yml, including deprecated
// names. Only checkout's five workspace inputs and setup *-version are evaluated.
var builtinDefinitions = map[string]builtinDefinition{
	"actions/checkout":        {inputs: "repository ref token ssh-key ssh-known-hosts ssh-strict ssh-user persist-credentials path clean filter sparse-checkout sparse-checkout-cone-mode fetch-depth fetch-tags show-progress lfs submodules set-safe-directory github-server-url allow-unsafe-pr-checkout", code: "note.checkout"},
	"actions/setup-go":        {inputs: "go-version go-version-file check-latest token cache cache-dependency-path architecture go-download-base-url", tool: "Go", code: "note.setup"},
	"actions/setup-node":      {inputs: "always-auth node-version node-version-file architecture check-latest registry-url scope token cache package-manager-cache cache-dependency-path mirror mirror-token", tool: "Node.js", code: "note.setup"},
	"actions/setup-python":    {inputs: "python-version python-version-file cache architecture check-latest token mirror mirror-token cache-dependency-path update-environment allow-prereleases freethreaded pip-version", tool: "Python", code: "note.setup"},
	"actions/setup-java":      {inputs: "java-version java-version-file distribution java-package architecture jdk-file jdkFile check-latest force-download set-default verify-signature verify-signature-public-key server-id server-username-env-var server-username server-password-env-var server-password mvn-server-credentials mvn-server-repository-origins mvn-repositories mvn-repositories-include-central mvn-repositories-prioritize-central settings-path overwrite-settings gpg-private-key gpg-passphrase-env-var gpg-passphrase cache cache-jdk cache-dependency-path cache-path cache-read-only job-status token mvn-toolchain-id mvn-toolchain-vendor show-download-progress problem-matcher", tool: "Java", code: "note.setup"},
	"actions/cache":           {inputs: "path key restore-keys upload-chunk-size enableCrossOsArchive fail-on-cache-miss lookup-only save-always", code: "note.cache"},
	"actions/cache/restore":   {inputs: "path key restore-keys enableCrossOsArchive fail-on-cache-miss lookup-only", code: "note.cache"},
	"actions/cache/save":      {inputs: "path key upload-chunk-size enableCrossOsArchive", code: "note.cache"},
	"actions/upload-artifact": {inputs: "name path if-no-files-found retention-days compression-level overwrite include-hidden-files archive", code: "note.artifact"},
}

func LookupBuiltin(uses string) (Builtin, error) {
	name, version, found := strings.Cut(uses, "@")
	definition, known := builtinDefinitions[name]
	if !found || version == "" || !known {
		code := "workflow.action"
		detail := fmt.Sprintf("OwnGit does not download actions, so %s does not run. Replace the step with a run step that does the same work, and install the tool in the administrator's container image or on the runner.", uses)
		switch {
		case strings.HasPrefix(uses, "./"):
			code, detail = "workflow.local_action", fmt.Sprintf("Local actions such as %s do not run. Call the action's script from a run step.", uses)
		case strings.HasPrefix(uses, "docker://"):
			code, detail = "workflow.docker_action", fmt.Sprintf("Container actions such as %s do not run, because the administrator's policy chooses the only container. Run the tool in a run step.", uses)
		case name == "actions/download-artifact":
			code, detail = "workflow.artifact_download", "actions/download-artifact does not run, because OwnGit keeps no artifacts and later steps would miss the files. Build the files in the same job."
		}
		return Builtin{}, refuse(code, "step.uses", 0, detail)
	}
	builtin := Builtin{Name: name, Status: StatusNotRun}
	if definition.code == "note.checkout" {
		builtin.Status = StatusPassed
	}
	if definition.code == "note.cache" {
		builtin.Outputs = map[string]string{"cache-hit": "false"}
	}
	return builtin, nil
}

// EvaluateBuiltin validates known inputs and returns the admission-time notes.
// Inert inputs are intentionally not evaluated or scanned for secret references.
func EvaluateBuiltin(step Step, ctx EvalContext) (Builtin, []Message, error) {
	builtin, err := LookupBuiltin(step.Uses)
	if err != nil {
		return builtin, nil, err
	}
	definition := builtinDefinitions[builtin.Name]
	var versions []string
	for _, input := range sortedKeys(step.With) {
		if !containsWord(definition.inputs, input) {
			return builtin, nil, refuse("workflow.unknown_key", "step.with."+input, 0, fmt.Sprintf("%s is not an input of %s. Check the spelling against the action's documented inputs, or remove it.", input, builtin.Name))
		}
		if builtin.Name == "actions/checkout" && containsWord("ref repository path submodules lfs", input) || definition.code == "note.setup" && strings.HasSuffix(input, "-version") {
			value, err := EvalTemplate(step.With[input], "builtin.with", ctx)
			if err != nil {
				return builtin, nil, err
			}
			text, err := ScalarString(value)
			if err != nil {
				return builtin, nil, err
			}
			if definition.code == "note.setup" {
				versions = append(versions, text)
				continue
			}
			github, _ := ctx.Values["github"].(map[string]any)
			accepted := text == ""
			switch input {
			case "ref":
				accepted = accepted || text == github["sha"] || text == github["ref"] || text == github["ref_name"]
			case "repository":
				accepted = accepted || text == github["repository"]
			case "path":
				accepted = accepted || text == "." || text == "./"
			case "submodules", "lfs":
				accepted = accepted || strings.EqualFold(text, "false")
			}
			if !accepted {
				return builtin, nil, refuse("workflow.checkout_input", "step.with."+input, 0, fmt.Sprintf("actions/checkout input %s is not supported. OwnGit prepares only the files of commit %v: no other ref or repository, no submodules and no Git LFS content. Remove the input, or fetch what you need in a run step.", input, github["sha"]))
			}
		}
	}
	note := Message{Code: definition.code}
	switch definition.code {
	case "note.checkout":
		github, _ := ctx.Values["github"].(map[string]any)
		note.Detail = fmt.Sprintf("Built in: commit %v is in the workspace without a .git folder, so Git commands do not work. Git LFS files are pointer files.", github["sha"])
	case "note.setup":
		note.Detail = fmt.Sprintf("Not run: OwnGit does not install, check or select %s %s. This job uses the %s already on the computer or in the image; a version matrix runs that same tool each time.", definition.tool, strings.Join(versions, ", "), definition.tool)
	case "note.cache":
		note.Detail = "Not run: OwnGit keeps no cache, so later steps start without restored files."
	case "note.artifact":
		note.Detail = "Not run: OwnGit keeps no artifacts. The files stay only in this job's workspace."
	}
	return builtin, []Message{note}, nil
}
