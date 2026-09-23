package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

func readDirectReviewRecovery(ctx context.Context, tx *sql.Tx, snapshot *RecoveryState) error {
	rows, err := tx.QueryContext(ctx, directReviewSettingsSelect+` ORDER BY repository_id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		settings, err := scanDirectReviewSettings(rows)
		if err != nil {
			rows.Close()
			return err
		}
		// Credential attachment, probe authority, and automatic consent are
		// machine-local authority. The nonsecret connection generation remains
		// monotonic so pre-backup consent cannot revive after reconnection.
		settings.CredentialID = ""
		settings.CredentialAttached = false
		settings.AuthorityEpoch = ""
		// Preserve the nonsecret generation so a credential attached after restore
		// cannot revive pre-backup confirmations or automatic consent.
		clearDirectReviewProbeAuthority(&settings)
		disableAutomaticDirectReview(&settings)
		snapshot.DirectReviewSettings = append(snapshot.DirectReviewSettings, settings)
	}
	if err := closeRows(rows); err != nil {
		return err
	}

	rows, err = tx.QueryContext(ctx, `SELECT context_id,repository_id,task_id,attempt_id,credential_id,base_oid,head_oid,
		pull_request_number,observed_source_oid,observed_target_oid,registration_digest,created_at
		FROM direct_review_task_contexts ORDER BY repository_id,task_id,attempt_id,context_id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		contextRecord, err := scanDirectReviewTaskContext(rows)
		if err != nil {
			rows.Close()
			return err
		}
		snapshot.DirectReviewTaskContexts = append(snapshot.DirectReviewTaskContexts, contextRecord)
	}
	if err := closeRows(rows); err != nil {
		return err
	}

	rows, err = tx.QueryContext(ctx, directReviewRequestSelect+` ORDER BY sequence`)
	if err != nil {
		return err
	}
	for rows.Next() {
		request, err := scanDirectReviewRequest(rows)
		if err != nil {
			rows.Close()
			return err
		}
		if request.Phase != DirectReviewPhaseTerminal {
			rows.Close()
			return errors.New("nonterminal direct review request remains after reconciliation")
		}
		snapshot.DirectReviewRequests = append(snapshot.DirectReviewRequests, request)
	}
	return closeRows(rows)
}

func restoreDirectReviewRecovery(ctx context.Context, tx *sql.Tx, snapshot RecoveryState) error {
	for _, settings := range snapshot.DirectReviewSettings {
		providerJSON, repositoryJSON, err := marshalDirectReviewLimits(settings.ProviderLimits, settings.RepositoryLimits)
		if err != nil {
			return err
		}
		authorityEpoch, err := RandomID()
		if err != nil {
			return fmt.Errorf("create restored direct review authority for %q: %w", settings.RepositoryID, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO direct_review_repository_settings(
			repository_id,configuration_version,protocol,endpoint,model,authentication_mode,provider_limits_json,repository_limits_json,
			instruction_version,credential_id,connection_version,authority_epoch,probe_fingerprint,probe_request_id,probe_updated_at,
			automatic_pr_enabled,automatic_task_enabled,automatic_consent_version,automatic_consent_digest,automatic_consent_active,created_at,updated_at
		) VALUES(?,?,?,?,?,?,?,?,?,'',?,?,'','',0,0,0,0,'',0,?,?)`, settings.RepositoryID, settings.ConfigurationVersion,
			settings.Protocol, settings.Endpoint, settings.Model, settings.AuthenticationMode, providerJSON, repositoryJSON,
			settings.InstructionVersion, settings.ConnectionVersion, authorityEpoch, settings.CreatedAt.Unix(), settings.UpdatedAt.Unix()); err != nil {
			return fmt.Errorf("restore direct review settings for %q: %w", settings.RepositoryID, err)
		}
	}
	for _, contextRecord := range snapshot.DirectReviewTaskContexts {
		if _, err := tx.ExecContext(ctx, `INSERT INTO direct_review_task_contexts(
			context_id,repository_id,task_id,attempt_id,credential_id,base_oid,head_oid,pull_request_number,
			observed_source_oid,observed_target_oid,registration_digest,created_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, contextRecord.ContextID, contextRecord.RepositoryID, contextRecord.TaskID,
			contextRecord.AttemptID, contextRecord.CredentialID, contextRecord.BaseOID, contextRecord.HeadOID,
			contextRecord.PullRequestNumber, contextRecord.ObservedSourceOID, contextRecord.ObservedTargetOID,
			contextRecord.RegistrationDigest, contextRecord.CreatedAt.Unix()); err != nil {
			return fmt.Errorf("restore direct review task context %q: %w", contextRecord.ContextID, err)
		}
	}
	for _, request := range snapshot.DirectReviewRequests {
		providerJSON, repositoryJSON, err := marshalDirectReviewLimits(request.ProviderLimits, request.RepositoryLimits)
		if err != nil {
			return err
		}
		resultJSON, err := marshalDirectReviewResult(*request.Result)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO direct_review_requests(
			sequence,request_id,repository_id,trigger_kind,source_event_key,pull_request_number,task_id,attempt_id,
			observed_source_oid,observed_target_oid,effective_base_oid,effective_head_oid,diff_mode,configuration_version,connection_version,
			connection_fingerprint,protocol,endpoint,model,authentication_mode,provider_limits_json,repository_limits_json,instruction_version,
			consent_version,consent_digest,disclosure_version,disclosure_digest,registration_digest,phase,cancel_requested_at,created_at,
			preparing_at,running_at,terminal_at,initial_context_bytes,initial_context_truncated,result_json
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, request.Sequence, request.RequestID,
			request.RepositoryID, request.TriggerKind, request.SourceEventKey, request.PullRequestNumber, request.TaskID, request.AttemptID,
			request.ObservedSourceOID, request.ObservedTargetOID, request.EffectiveBaseOID, request.EffectiveHeadOID, request.DiffMode,
			request.ConfigurationVersion, request.ConnectionVersion, request.ConnectionFingerprint, request.Protocol, request.Endpoint,
			request.Model, request.AuthenticationMode, providerJSON, repositoryJSON, request.InstructionVersion, request.ConsentVersion,
			request.ConsentDigest, request.DisclosureVersion, request.DisclosureDigest, request.RegistrationDigest, request.Phase,
			nullableUnix(request.CancelRequestedAt), request.CreatedAt.Unix(), nullableUnix(request.PreparingAt), nullableUnix(request.RunningAt),
			nullableUnix(request.TerminalAt), request.InitialContextBytes, boolInt(request.InitialContextTruncated), resultJSON); err != nil {
			return fmt.Errorf("restore direct review request %q: %w", request.RequestID, err)
		}
	}
	return nil
}

// ValidateDirectReviewRecovery checks portable relationships before restore
// writes. Current connection and consent authority must already be absent.
func ValidateDirectReviewRecovery(snapshot RecoveryState) error {
	repositories := make(map[string]struct{}, len(snapshot.Repositories))
	for _, repository := range snapshot.Repositories {
		repositories[repository.ID] = struct{}{}
	}
	settingsByRepository := make(map[string]DirectReviewSettings, len(snapshot.DirectReviewSettings))
	for _, settings := range snapshot.DirectReviewSettings {
		if _, ok := repositories[settings.RepositoryID]; !ok {
			return errors.New("direct review settings refer to an unknown repository")
		}
		if _, duplicate := settingsByRepository[settings.RepositoryID]; duplicate {
			return errors.New("duplicate direct review settings")
		}
		if settings.CredentialID != "" || settings.CredentialAttached || settings.AuthorityEpoch != "" || settings.ConnectionVersion < 0 || settings.ProbeFingerprint != "" || settings.ProbeRequestID != "" || settings.ProbeUpdatedAt != nil || settings.AutomaticPREnabled || settings.AutomaticTaskEnabled || settings.AutomaticConsent || settings.AutomaticConsentVer != 0 || settings.AutomaticConsentHash != "" {
			return errors.New("portable direct review settings contain local authority")
		}
		validated := settings
		validated.AuthorityEpoch = "00000000000000000000000000000000"
		if err := validateDirectReviewSettings(validated, true); err != nil {
			return err
		}
		settingsByRepository[settings.RepositoryID] = settings
	}

	tasks := make(map[string]RecoveryTask, len(snapshot.Tasks))
	for _, task := range snapshot.Tasks {
		tasks[task.ID] = task
	}
	attempts := make(map[string]CheckAttempt, len(snapshot.CheckAttempts))
	for _, attempt := range snapshot.CheckAttempts {
		attempts[attempt.ID] = attempt
	}
	pullRequests := make(map[string]struct{}, len(snapshot.PullRequests))
	for _, request := range snapshot.PullRequests {
		pullRequests[pullRequestKey(request.RepositoryID, request.Number)] = struct{}{}
	}
	reviewEvents := make(map[string]PullRequestReview)
	for _, review := range snapshot.PullRequestReviews {
		if review.ReviewEventID != "" {
			reviewEvents[review.RepositoryID+"/"+review.ReviewEventID] = review
		}
	}
	revisions := make(map[string]struct{}, len(snapshot.PullRequestRevisions))
	for _, revision := range snapshot.PullRequestRevisions {
		revisions[revisionKey(revision.RepositoryID, revision.PullRequestNumber, revision.SourceOID, revision.TargetOID)] = struct{}{}
	}

	contextIDs := make(map[string]struct{}, len(snapshot.DirectReviewTaskContexts))
	contextAttempts := make(map[string]DirectReviewTaskContext, len(snapshot.DirectReviewTaskContexts))
	for _, contextRecord := range snapshot.DirectReviewTaskContexts {
		if err := validateDirectReviewTaskContext(contextRecord); err != nil {
			return err
		}
		if _, ok := repositories[contextRecord.RepositoryID]; !ok {
			return errors.New("direct review task context refers to an unknown repository")
		}
		task, taskOK := tasks[contextRecord.TaskID]
		attempt, attemptOK := attempts[contextRecord.AttemptID]
		if !taskOK || !attemptOK || task.RepositoryID != contextRecord.RepositoryID || attempt.RepositoryID != contextRecord.RepositoryID ||
			attempt.TaskID != contextRecord.TaskID || attempt.RevisionOID != contextRecord.HeadOID || attempt.CredentialID != contextRecord.CredentialID {
			return errors.New("direct review task context does not match its attempt")
		}
		if _, duplicate := contextIDs[contextRecord.ContextID]; duplicate {
			return errors.New("duplicate direct review task context identity")
		}
		if _, duplicate := contextAttempts[contextRecord.AttemptID]; duplicate {
			return errors.New("duplicate direct review task context attempt")
		}
		contextIDs[contextRecord.ContextID] = struct{}{}
		contextAttempts[contextRecord.AttemptID] = contextRecord
		if contextRecord.PullRequestNumber > 0 {
			if _, ok := revisions[revisionKey(contextRecord.RepositoryID, contextRecord.PullRequestNumber, contextRecord.ObservedSourceOID, contextRecord.ObservedTargetOID)]; !ok {
				return errors.New("direct review task context refers to an unknown PR revision")
			}
		}
	}

	requestIDs := make(map[string]struct{}, len(snapshot.DirectReviewRequests))
	sequences := make(map[int64]struct{}, len(snapshot.DirectReviewRequests))
	sourceEvents := make(map[string]struct{})
	for _, request := range snapshot.DirectReviewRequests {
		if request.Phase != DirectReviewPhaseTerminal {
			return errors.New("portable direct review request is not terminal")
		}
		if err := validateDirectReviewRequest(request, true); err != nil {
			return err
		}
		if _, ok := repositories[request.RepositoryID]; !ok {
			return errors.New("direct review request refers to an unknown repository")
		}
		settings, ok := settingsByRepository[request.RepositoryID]
		if !ok {
			return errors.New("direct review request has no repository configuration")
		}
		if request.ConfigurationVersion > settings.ConfigurationVersion || request.ConnectionVersion > settings.ConnectionVersion {
			return errors.New("direct review request exceeds the portable configuration generation")
		}
		if _, duplicate := requestIDs[request.RequestID]; duplicate {
			return errors.New("duplicate direct review RequestID")
		}
		if _, duplicate := sequences[request.Sequence]; duplicate {
			return errors.New("duplicate direct review sequence")
		}
		requestIDs[request.RequestID] = struct{}{}
		sequences[request.Sequence] = struct{}{}
		if request.SourceEventKey != "" {
			key := request.RepositoryID + "/" + request.TriggerKind + "/" + request.SourceEventKey
			if _, duplicate := sourceEvents[key]; duplicate {
				return errors.New("duplicate direct review source event")
			}
			sourceEvents[key] = struct{}{}
		}
		if request.TriggerKind == DirectReviewTriggerAutomaticPR {
			review, exists := reviewEvents[request.RepositoryID+"/"+request.SourceEventKey]
			if !exists || review.Status != ReviewPending || review.Provenance != ReviewProvenanceRequest ||
				review.PullRequestNumber != request.PullRequestNumber || review.SourceOID != request.ObservedSourceOID || review.TargetOID != request.ObservedTargetOID {
				return errors.New("automatic pull request review source event does not match its pending review request")
			}
		}
		if request.PullRequestNumber > 0 {
			if _, ok := pullRequests[pullRequestKey(request.RepositoryID, request.PullRequestNumber)]; !ok {
				return errors.New("direct review request refers to an unknown pull request")
			}
			if request.ObservedSourceOID != "" {
				if _, ok := revisions[revisionKey(request.RepositoryID, request.PullRequestNumber, request.ObservedSourceOID, request.ObservedTargetOID)]; !ok {
					return errors.New("direct review request refers to an unknown PR revision")
				}
			}
		}
		if request.TaskID != "" {
			task, taskOK := tasks[request.TaskID]
			attempt, attemptOK := attempts[request.AttemptID]
			if !taskOK || !attemptOK || task.RepositoryID != request.RepositoryID || attempt.RepositoryID != request.RepositoryID || attempt.TaskID != request.TaskID {
				return errors.New("direct review request refers to an unknown task attempt")
			}
			if request.EffectiveHeadOID != "" {
				contextRecord, contextOK := contextAttempts[request.AttemptID]
				if !contextOK || request.EffectiveHeadOID != attempt.RevisionOID || request.EffectiveHeadOID != contextRecord.HeadOID || request.EffectiveBaseOID != contextRecord.BaseOID ||
					request.PullRequestNumber != contextRecord.PullRequestNumber || request.ObservedSourceOID != contextRecord.ObservedSourceOID || request.ObservedTargetOID != contextRecord.ObservedTargetOID {
					return errors.New("direct review task request does not match its registered context")
				}
			}
		}
	}
	return nil
}

func directReviewRecoveryRequestKey(request DirectReviewRequest) string {
	return request.RepositoryID + "/" + strconv.FormatInt(request.Sequence, 10)
}
