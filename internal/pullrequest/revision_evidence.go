package pullrequest

import (
	"context"
	"owngit/internal/actions"
	"owngit/internal/state"
)

func (service *Service) checksForRevision(ctx context.Context, repositoryPath string, record state.PullRequest, source branchHead) Checks {
	display := service.attemptChecksForRevision(ctx, repositoryPath, record, source, false)
	if source.Status != "commit" || display.ReadFailure != nil {
		return display
	}
	evidence, err := service.Store.PullRequestRevisionEvidence(ctx, record.RepositoryID, record.Number, source.OID)
	if err != nil {
		return Checks{Advisory: true, Configured: display.Configured, ReadFailure: &ReadFailure{Code: ReadFailureCheckEvidence}}
	}
	checks := display
	if len(evidence.Workflows) > 0 {
		checks = service.attemptChecksForRevision(ctx, repositoryPath, record, source, true)
		checks.DisplayChecks = &display
		jsonStale := checks.Stale
		if evidence.JSONStale {
			checks = Checks{Advisory: true, Configured: true, DisplayChecks: &display}
		}
		if evidence.Conclusion != "" {
			checks.Status = evidence.Conclusion
			checks.Configured = true
			checks.Stale = evidence.HasStaleEvidence()
			checks.RevisionOID = source.OID
			if evidence.JSONAttempt != nil && !evidence.JSONStale && checks.ConfigurationVersion != 0 {
				if jsonStale {
					checks.Status = actions.RevisionConclusion([]string{checks.Status, actions.StatusIncomplete})
				}
			}
			checks.Passed = checks.Status == actions.StatusPassed
			checks.TestedCommit = checks.Passed && !checks.Stale && (evidence.JSONAttempt == nil || evidence.JSONAttempt.EffectiveWorktreeState() == "clean" && !evidence.JSONAttempt.CleanupFailed())
			checks.Summary = "Workflow evidence: " + checks.Status
		} else {
			checks.Status = "stale"
			checks.Stale = true
			checks.TestedCommit = false
			checks.Passed = false
			checks.Summary = "Workflow evidence is from an earlier revision of this pull request."
		}
	}
	checks.Evidence = &evidence
	policy, found, err := service.Store.CheckPolicy(ctx, record.RepositoryID)
	if err != nil {
		checks.ReadFailure = &ReadFailure{Code: ReadFailureCheckEvidence}
		checks.Status, checks.Passed, checks.TestedCommit = "", false, false
		return checks
	}
	if found && policy.RunWorkflows {
		checks.AdmissionNote, err = service.Store.ActionsPullRequestAdmissionNote(ctx, record.RepositoryID, source.OID)
		if err != nil {
			checks.ReadFailure = &ReadFailure{Code: ReadFailureCheckEvidence}
			checks.Status, checks.Passed, checks.TestedCommit = "", false, false
		}
	}
	return checks
}
