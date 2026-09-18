package pullrequest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"owngit/internal/gitexec"
	"owngit/internal/state"
)

type branchHead struct {
	Branch string
	Ref    string
	OID    string
	Status string
}

func RevisionRefNames(number int64, sourceOID, targetOID string) (string, string) {
	base := "refs/owngit/pull-requests/" + strconv.FormatInt(number, 10) + "/revisions/" + mergeRevisionKey(sourceOID, targetOID)
	return base + "/source", base + "/target"
}

func MergeTreeRef(number int64, sourceOID, targetOID string) string {
	return "refs/owngit/pull-requests/" + strconv.FormatInt(number, 10) + "/merges/" + mergeRevisionKey(sourceOID, targetOID) + "/tree"
}

func MergeResultRef(number int64, sourceOID, targetOID string) string {
	return "refs/owngit/pull-requests/" + strconv.FormatInt(number, 10) + "/merges/" + mergeRevisionKey(sourceOID, targetOID) + "/result"
}

func mergeRevisionKey(sourceOID, targetOID string) string {
	digest := sha256.Sum256([]byte(sourceOID + "\x00" + targetOID))
	return hex.EncodeToString(digest[:16])
}

func MergeReceiptRef(number int64) string {
	return "refs/owngit/pull-requests/" + strconv.FormatInt(number, 10) + "/merge-receipt"
}

func (service *Service) resolveBranch(ctx context.Context, repositoryPath, branch string) (branchHead, error) {
	ref := "refs/heads/" + branch
	oid, exists, err := service.readRef(ctx, repositoryPath, ref)
	if err != nil {
		return branchHead{}, err
	}
	if !exists {
		return branchHead{Branch: branch, Ref: ref, Status: "missing"}, nil
	}
	result, err := service.Repositories.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "cat-file", "-t", oid)
	if err != nil {
		return branchHead{}, &Problem{Code: "repository_unavailable", Message: "The branch object could not be inspected.", Cause: err}
	}
	if strings.TrimSpace(string(result.Stdout)) != "commit" {
		return branchHead{Branch: branch, Ref: ref, OID: oid, Status: "not_commit"}, nil
	}
	return branchHead{Branch: branch, Ref: ref, OID: oid, Status: "commit"}, nil
}

func (service *Service) readRef(ctx context.Context, repositoryPath, ref string) (string, bool, error) {
	result, err := service.Repositories.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "rev-parse", "--verify", "--quiet", "--end-of-options", ref)
	if err != nil {
		if code, ok := gitexec.ExitCode(err); ok && code == 1 {
			return "", false, nil
		}
		return "", false, &Problem{Code: "repository_unavailable", Message: "A Git ref could not be read.", Cause: err}
	}
	oid := strings.TrimSpace(string(result.Stdout))
	if !validOID(oid) {
		return "", false, NewProblem("repository_integrity_error", "Git returned an invalid object ID for a repository ref.")
	}
	return oid, true, nil
}

func (service *Service) ensureRevisionRefs(ctx context.Context, repositoryPath string, record state.PullRequest, sourceOID, targetOID string) error {
	sourceRef, targetRef := RevisionRefNames(record.Number, sourceOID, targetOID)
	commands := []string{"start"}
	for _, binding := range []struct {
		name string
		oid  string
	}{{sourceRef, sourceOID}, {targetRef, targetOID}} {
		actual, exists, err := service.readRef(ctx, repositoryPath, binding.name)
		if err != nil {
			return err
		}
		if exists {
			if actual != binding.oid {
				return NewProblem("repository_integrity_error", "A protected pull request revision ref has an unexpected value.")
			}
			continue
		}
		commands = append(commands, "create "+binding.name+" "+binding.oid)
	}
	commands = append(commands,
		"verify refs/heads/"+record.SourceBranch+" "+sourceOID,
		"verify refs/heads/"+record.TargetBranch+" "+targetOID,
		"prepare", "commit",
	)
	input := strings.NewReader(strings.Join(commands, "\n") + "\n")
	if _, err := service.Repositories.Git.Run(ctx, "", input, "--git-dir", repositoryPath, "update-ref", "--stdin"); err != nil {
		source, sourceErr := service.resolveBranch(ctx, repositoryPath, record.SourceBranch)
		target, targetErr := service.resolveBranch(ctx, repositoryPath, record.TargetBranch)
		if sourceErr == nil && targetErr == nil && (source.OID != sourceOID || target.OID != targetOID) {
			return NewProblem("stale_revision", "The source or target branch changed while its pull request revision was retained.")
		}
		return &Problem{Code: "repository_unavailable", Message: "The pull request revision could not be retained.", Cause: err}
	}
	return nil
}

func (service *Service) ensureStoredRevisionRefs(ctx context.Context, repositoryPath string, revision state.PullRequestRevision) error {
	sourceRef, targetRef := RevisionRefNames(revision.PullRequestNumber, revision.SourceOID, revision.TargetOID)
	commands := []string{"start"}
	for _, binding := range []struct {
		name string
		oid  string
	}{{sourceRef, revision.SourceOID}, {targetRef, revision.TargetOID}} {
		actual, exists, err := service.readRef(ctx, repositoryPath, binding.name)
		if err != nil {
			return err
		}
		if exists {
			if actual != binding.oid {
				return NewProblem("repository_integrity_error", "A protected pull request revision ref has an unexpected value.")
			}
			continue
		}
		if _, err := service.Repositories.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "cat-file", "-e", binding.oid+"^{commit}"); err != nil {
			return &Problem{Code: "repository_integrity_error", Message: "A recorded pull request revision object is unavailable.", Cause: err}
		}
		commands = append(commands, "create "+binding.name+" "+binding.oid)
	}
	if len(commands) == 1 {
		return nil
	}
	commands = append(commands, "prepare", "commit")
	if _, err := service.Repositories.Git.Run(ctx, "", strings.NewReader(strings.Join(commands, "\n")+"\n"), "--git-dir", repositoryPath, "update-ref", "--stdin"); err != nil {
		return &Problem{Code: "repository_unavailable", Message: "Recorded pull request revision refs could not be repaired.", Cause: err}
	}
	return nil
}

func (service *Service) ensureProtectedMergeRef(ctx context.Context, repositoryPath, ref, oid, objectType string, record *state.PullRequest, intent state.PullRequestMergeIntent) error {
	result, err := service.Repositories.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "cat-file", "-t", oid)
	if err != nil {
		return &Problem{Code: "repository_integrity_error", Message: "A planned merge object is unavailable.", Cause: err}
	}
	if strings.TrimSpace(string(result.Stdout)) != objectType {
		return NewProblem("repository_integrity_error", "A planned merge ref points to an object of the wrong type.")
	}
	actual, exists, err := service.readRef(ctx, repositoryPath, ref)
	if err != nil {
		return err
	}
	if exists {
		if actual != oid {
			return NewProblem("repository_integrity_error", "A protected merge candidate ref has an unexpected value.")
		}
		return nil
	}
	commands := []string{"start"}
	if record != nil {
		commands = append(commands,
			"verify refs/heads/"+record.SourceBranch+" "+intent.SourceOID,
			"verify refs/heads/"+record.TargetBranch+" "+intent.TargetOID,
		)
	}
	commands = append(commands, "create "+ref+" "+oid, "prepare", "commit")
	if _, err := service.Repositories.Git.Run(ctx, "", strings.NewReader(strings.Join(commands, "\n")+"\n"), "--git-dir", repositoryPath, "update-ref", "--stdin"); err != nil {
		actual, exists, readErr := service.readRef(ctx, repositoryPath, ref)
		if readErr == nil && exists && actual == oid {
			return nil
		}
		if record != nil {
			source, sourceErr := service.resolveBranch(ctx, repositoryPath, record.SourceBranch)
			target, targetErr := service.resolveBranch(ctx, repositoryPath, record.TargetBranch)
			if sourceErr == nil && targetErr == nil && (source.OID != intent.SourceOID || target.OID != intent.TargetOID) {
				return staleRevisionProblem(source.OID, target.OID)
			}
		}
		return &Problem{Code: "repository_unavailable", Message: "The protected merge candidate could not be retained.", Cause: err}
	}
	return nil
}

func (service *Service) ensurePlannedMergeTree(ctx context.Context, repositoryPath string, record state.PullRequest, intent state.PullRequestMergeIntent, verifyHeads bool) error {
	if intent.Mode == "fast_forward" {
		if intent.TreeOID != "" {
			return NewProblem("repository_integrity_error", "A fast-forward merge intent unexpectedly contains a merge tree.")
		}
		return nil
	}
	if intent.Mode != "merge_commit" || !validOID(intent.TreeOID) {
		return NewProblem("repository_integrity_error", "The merge intent does not contain a valid merge tree.")
	}
	var verifiedRecord *state.PullRequest
	if verifyHeads {
		verifiedRecord = &record
	}
	return service.ensureProtectedMergeRef(ctx, repositoryPath, MergeTreeRef(record.Number, intent.SourceOID, intent.TargetOID), intent.TreeOID, "tree", verifiedRecord, intent)
}

func (service *Service) ensureMergeResult(ctx context.Context, repositoryPath string, record state.PullRequest, intent state.PullRequestMergeIntent, verifyHeads bool) (string, error) {
	if err := service.ensurePlannedMergeTree(ctx, repositoryPath, record, intent, verifyHeads); err != nil {
		return "", err
	}
	resultOID := intent.SourceOID
	if intent.Mode == "merge_commit" {
		var err error
		resultOID, err = service.createMergeCommit(ctx, repositoryPath, record, intent)
		if err != nil {
			return "", err
		}
	}
	if intent.ResultOID != "" && intent.ResultOID != resultOID {
		return "", NewProblem("repository_integrity_error", "The retained merge result does not match the exact merge intent.")
	}
	var verifiedRecord *state.PullRequest
	if verifyHeads {
		verifiedRecord = &record
	}
	if err := service.ensureProtectedMergeRef(ctx, repositoryPath, MergeResultRef(record.Number, intent.SourceOID, intent.TargetOID), resultOID, "commit", verifiedRecord, intent); err != nil {
		return "", err
	}
	return resultOID, nil
}

func (service *Service) requireMergeCapability(ctx context.Context) error {
	result, err := service.Repositories.Git.Run(ctx, "", nil, "--version")
	if err != nil {
		return &Problem{Code: "repository_unavailable", Message: "The Git version could not be read.", Cause: err}
	}
	if !supportsMergeVersion(string(result.Stdout)) {
		return NewProblem("unsupported_git", "Pull request merge requires Git 2.38 or newer.")
	}
	return nil
}

func (service *Service) planMerge(ctx context.Context, repositoryPath string, intent state.PullRequestMergeIntent) (state.PullRequestMergeIntent, error) {
	fastForward, err := service.isAncestor(ctx, repositoryPath, intent.TargetOID, intent.SourceOID)
	if err != nil {
		return state.PullRequestMergeIntent{}, err
	}
	intent.UpdatedAt = service.now()
	intent.Status = state.MergeIntentPlanned
	if fastForward {
		intent.Mode = "fast_forward"
		intent.ResultOID = intent.SourceOID
		return intent, nil
	}
	hasBase, err := service.hasMergeBase(ctx, repositoryPath, intent.TargetOID, intent.SourceOID)
	if err != nil {
		return state.PullRequestMergeIntent{}, err
	}
	if !hasBase {
		return state.PullRequestMergeIntent{}, NewProblem("merge_conflict", "The source and target branches do not share mergeable history.")
	}
	treeOID, err := service.calculateMergeTree(ctx, repositoryPath, intent.TargetOID, intent.SourceOID)
	if err != nil {
		return state.PullRequestMergeIntent{}, err
	}
	intent.Mode = "merge_commit"
	intent.TreeOID = treeOID
	return intent, nil
}

func (service *Service) calculateMergeTree(ctx context.Context, repositoryPath, targetOID, sourceOID string) (string, error) {
	result, err := service.Repositories.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "merge-tree", "--write-tree", targetOID, sourceOID)
	if err != nil {
		if code, ok := gitexec.ExitCode(err); ok && code == 1 {
			return "", NewProblem("merge_conflict", "The source and target branches have merge conflicts.")
		}
		return "", &Problem{Code: "repository_unavailable", Message: "Git could not calculate the merge tree.", Cause: err}
	}
	firstLine, _, _ := strings.Cut(strings.TrimSpace(string(result.Stdout)), "\n")
	fields := strings.Fields(firstLine)
	if len(fields) != 1 || !validOID(fields[0]) {
		return "", NewProblem("repository_integrity_error", "Git returned an invalid merge tree.")
	}
	return fields[0], nil
}

func (service *Service) createMergeCommit(ctx context.Context, repositoryPath string, record state.PullRequest, intent state.PullRequestMergeIntent) (string, error) {
	if intent.Mode != "merge_commit" || !validOID(intent.TreeOID) {
		return "", NewProblem("repository_integrity_error", "The merge intent does not contain a merge tree.")
	}
	identityDate := fmt.Sprintf("%d +0000", intent.CreatedAt.Unix())
	environment := []string{
		"GIT_AUTHOR_NAME=OwnGit",
		"GIT_AUTHOR_EMAIL=owngit@localhost",
		"GIT_AUTHOR_DATE=" + identityDate,
		"GIT_COMMITTER_NAME=OwnGit",
		"GIT_COMMITTER_EMAIL=owngit@localhost",
		"GIT_COMMITTER_DATE=" + identityDate,
	}
	message := fmt.Sprintf("Merge pull request #%d: %s\n", record.Number, record.Title)
	result, err := service.Repositories.Git.RunWithEnvironment(ctx, "", strings.NewReader(message), environment,
		"--git-dir", repositoryPath, "commit-tree", intent.TreeOID, "-p", intent.TargetOID, "-p", intent.SourceOID)
	if err != nil {
		return "", &Problem{Code: "repository_unavailable", Message: "Git could not create the merge commit.", Cause: err}
	}
	oid := strings.TrimSpace(string(result.Stdout))
	if !validOID(oid) {
		return "", NewProblem("repository_integrity_error", "Git returned an invalid merge commit object ID.")
	}
	return oid, nil
}

func (service *Service) publishMerge(ctx context.Context, repositoryPath string, record state.PullRequest, intent state.PullRequestMergeIntent) error {
	if err := service.validateMergeCandidate(ctx, repositoryPath, intent); err != nil {
		return err
	}
	commands := strings.Join([]string{
		"start",
		"verify refs/heads/" + record.SourceBranch + " " + intent.SourceOID,
		"update refs/heads/" + record.TargetBranch + " " + intent.ResultOID + " " + intent.TargetOID,
		"create " + intent.ReceiptRef + " " + intent.ResultOID,
		"prepare",
		"commit",
	}, "\n") + "\n"
	_, err := service.Repositories.Git.Run(ctx, "", strings.NewReader(commands), "--git-dir", repositoryPath, "update-ref", "--stdin")
	return err
}

func (service *Service) validateMergeCandidate(ctx context.Context, repositoryPath string, intent state.PullRequestMergeIntent) error {
	if err := service.validateProtectedMergeObjects(ctx, repositoryPath, intent); err != nil {
		return err
	}
	if intent.Mode != "merge_commit" {
		return nil
	}
	calculatedTree, err := service.calculateMergeTree(ctx, repositoryPath, intent.TargetOID, intent.SourceOID)
	if err != nil {
		return NewProblem("repository_integrity_error", "The planned merge tree cannot be reproduced from the exact source and target revision.")
	}
	if calculatedTree != intent.TreeOID {
		return NewProblem("repository_integrity_error", "The planned merge tree does not match the exact source and target revision.")
	}
	return nil
}

func (service *Service) validateProtectedMergeObjects(ctx context.Context, repositoryPath string, intent state.PullRequestMergeIntent) error {
	requireResult := intent.Status == state.MergeIntentReady || intent.Status == state.MergeIntentComplete
	switch intent.Mode {
	case "fast_forward":
		if intent.ResultOID != intent.SourceOID || intent.TreeOID != "" {
			return NewProblem("repository_integrity_error", "The fast-forward merge intent is invalid.")
		}
		ancestor, err := service.isAncestor(ctx, repositoryPath, intent.TargetOID, intent.ResultOID)
		if err != nil {
			return err
		}
		if !ancestor {
			return NewProblem("repository_integrity_error", "The fast-forward result does not descend from the target revision.")
		}
		return service.validateProtectedResultRef(ctx, repositoryPath, intent, intent.ResultOID, requireResult)
	case "merge_commit":
		if !validOID(intent.TreeOID) {
			return NewProblem("repository_integrity_error", "The merge intent does not contain a valid merge tree.")
		}
		if requireResult {
			if !validOID(intent.ResultOID) {
				return NewProblem("repository_integrity_error", "The merge intent does not contain a valid result commit.")
			}
		} else if intent.Status != state.MergeIntentPlanned || intent.ResultOID != "" {
			return NewProblem("repository_integrity_error", "The planned merge intent contains unexpected result metadata.")
		}
		if err := service.validateProtectedObjectRef(ctx, repositoryPath, MergeTreeRef(intent.PullRequestNumber, intent.SourceOID, intent.TargetOID), intent.TreeOID, "tree", "The protected merge tree ref does not match the durable merge intent."); err != nil {
			return err
		}
		resultOID, exists, err := service.readRef(ctx, repositoryPath, MergeResultRef(intent.PullRequestNumber, intent.SourceOID, intent.TargetOID))
		if err != nil {
			return err
		}
		if !exists {
			if requireResult {
				return NewProblem("repository_integrity_error", "The protected merge result ref is missing.")
			}
			return nil
		}
		if requireResult && resultOID != intent.ResultOID {
			return NewProblem("repository_integrity_error", "The protected merge result ref does not match the durable merge intent.")
		}
		return service.validateMergeCommitObject(ctx, repositoryPath, resultOID, intent)
	default:
		return NewProblem("repository_integrity_error", "The merge intent has an unsupported mode.")
	}
}

func (service *Service) validateProtectedResultRef(ctx context.Context, repositoryPath string, intent state.PullRequestMergeIntent, expectedOID string, required bool) error {
	ref := MergeResultRef(intent.PullRequestNumber, intent.SourceOID, intent.TargetOID)
	actual, exists, err := service.readRef(ctx, repositoryPath, ref)
	if err != nil {
		return err
	}
	if !exists && !required {
		return nil
	}
	if !exists || actual != expectedOID {
		return NewProblem("repository_integrity_error", "The protected merge result ref does not match the durable merge intent.")
	}
	return service.validateObjectType(ctx, repositoryPath, actual, "commit")
}

func (service *Service) validateProtectedObjectRef(ctx context.Context, repositoryPath, ref, expectedOID, objectType, mismatchMessage string) error {
	actual, exists, err := service.readRef(ctx, repositoryPath, ref)
	if err != nil {
		return err
	}
	if !exists || actual != expectedOID {
		return NewProblem("repository_integrity_error", mismatchMessage)
	}
	return service.validateObjectType(ctx, repositoryPath, actual, objectType)
}

func (service *Service) validateObjectType(ctx context.Context, repositoryPath, oid, expectedType string) error {
	result, err := service.Repositories.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "cat-file", "-t", oid)
	if err != nil {
		return &Problem{Code: "repository_integrity_error", Message: "A protected merge object is unavailable.", Cause: err}
	}
	if strings.TrimSpace(string(result.Stdout)) != expectedType {
		return NewProblem("repository_integrity_error", "A protected merge ref points to an object of the wrong type.")
	}
	return nil
}

func (service *Service) validateMergeCommitObject(ctx context.Context, repositoryPath, resultOID string, intent state.PullRequestMergeIntent) error {
	result, err := service.Repositories.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "cat-file", "-p", resultOID)
	if err != nil {
		return &Problem{Code: "repository_integrity_error", Message: "The planned merge commit is unavailable.", Cause: err}
	}
	var tree string
	var parents []string
	for _, line := range strings.Split(string(result.Stdout), "\n") {
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "tree ") {
			tree = strings.TrimPrefix(line, "tree ")
		}
		if strings.HasPrefix(line, "parent ") {
			parents = append(parents, strings.TrimPrefix(line, "parent "))
		}
	}
	if tree != intent.TreeOID || len(parents) != 2 || parents[0] != intent.TargetOID || parents[1] != intent.SourceOID {
		return NewProblem("repository_integrity_error", "The planned merge commit does not match the merge intent.")
	}
	return nil
}

func (service *Service) isAncestor(ctx context.Context, repositoryPath, ancestor, descendant string) (bool, error) {
	_, err := service.Repositories.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	if code, ok := gitexec.ExitCode(err); ok && code == 1 {
		return false, nil
	}
	return false, &Problem{Code: "repository_unavailable", Message: "Git could not compare commit ancestry.", Cause: err}
}

func (service *Service) hasMergeBase(ctx context.Context, repositoryPath, left, right string) (bool, error) {
	_, err := service.Repositories.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "merge-base", left, right)
	if err == nil {
		return true, nil
	}
	if code, ok := gitexec.ExitCode(err); ok && code == 1 {
		return false, nil
	}
	return false, &Problem{Code: "repository_unavailable", Message: "Git could not find the common merge history.", Cause: err}
}

func validOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || (character > '9' && character < 'a') || character > 'f' {
			return false
		}
	}
	return true
}

func supportsMergeVersion(output string) bool {
	fields := strings.Fields(strings.TrimSpace(output))
	if len(fields) < 3 {
		return false
	}
	parts := strings.Split(fields[2], ".")
	if len(parts) < 2 {
		return false
	}
	major, majorErr := strconv.Atoi(parts[0])
	minorDigits := parts[1]
	for index, character := range minorDigits {
		if character < '0' || character > '9' {
			minorDigits = minorDigits[:index]
			break
		}
	}
	minor, minorErr := strconv.Atoi(minorDigits)
	return majorErr == nil && minorErr == nil && (major > 2 || (major == 2 && minor >= 38))
}
