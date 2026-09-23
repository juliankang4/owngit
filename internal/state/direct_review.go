package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Stored direct review records.
//
// Built-in provider review was removed after it had written durable rows, so
// the schema, the record shapes, and the export rules stay even though nothing
// calls a provider any more. What remains here is the SQL layer:
//
//   - the select statements and row scanners for the legacy record shapes,
//   - SQL interruption reconciliation, which turns work a dead process owned
//     into an honest terminal record at startup and before a backup,
//   - the small helpers those two need.
//
// Runtime operations (register, mark, complete, cancel, probe, and credential
// writes) live in the service that was removed, so their replay and lifecycle
// rules are not repeated here. Row validation belongs to
// direct_review_types.go and portability belongs to
// direct_review_recovery.go.

const directReviewSettingsSelect = `SELECT repository_id,configuration_version,protocol,endpoint,model,authentication_mode,
	provider_limits_json,repository_limits_json,instruction_version,credential_id,connection_version,authority_epoch,
	probe_fingerprint,probe_request_id,probe_updated_at,automatic_pr_enabled,automatic_task_enabled,
	automatic_consent_version,automatic_consent_digest,automatic_consent_active,created_at,updated_at
	FROM direct_review_repository_settings`

const directReviewProbeSelect = `SELECT request_id,repository_id,registration_digest,capability_fingerprint,
	configuration_version,connection_version,connection_fingerprint,protocol,endpoint,model,authentication_mode,
	provider_limits_json,disclosure_version,disclosure_digest,phase,cancel_requested_at,created_at,running_at,terminal_at,result_json
	FROM direct_review_probes`

const directReviewRequestSelect = `SELECT sequence,request_id,repository_id,trigger_kind,source_event_key,pull_request_number,task_id,attempt_id,
	observed_source_oid,observed_target_oid,effective_base_oid,effective_head_oid,diff_mode,configuration_version,connection_version,
	connection_fingerprint,protocol,endpoint,model,authentication_mode,provider_limits_json,repository_limits_json,instruction_version,
	consent_version,consent_digest,disclosure_version,disclosure_digest,registration_digest,phase,cancel_requested_at,created_at,
	preparing_at,running_at,terminal_at,initial_context_bytes,initial_context_truncated,result_json
	FROM direct_review_requests`

// ReconcileDirectReviewInterruptions records honest terminal outcomes for work
// owned by a process that is no longer running. It never invokes a provider.
func (s *Store) ReconcileDirectReviewInterruptions(ctx context.Context, now time.Time) error {
	if now.IsZero() {
		return errors.New("direct review reconciliation time is required")
	}
	now = directReviewServerTime(now)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	type interruptedRequest struct {
		requestID                           string
		phase                               string
		createdAt                           int64
		preparingAt, runningAt, cancelledAt sql.NullInt64
		initialBytes                        int64
		initialTruncated                    bool
	}
	rows, err := tx.QueryContext(ctx, `SELECT request_id,phase,created_at,preparing_at,running_at,cancel_requested_at,initial_context_bytes,initial_context_truncated
		FROM direct_review_requests WHERE phase!='terminal'`)
	if err != nil {
		return err
	}
	var requests []interruptedRequest
	for rows.Next() {
		var item interruptedRequest
		var truncated int
		if err := rows.Scan(&item.requestID, &item.phase, &item.createdAt, &item.preparingAt, &item.runningAt, &item.cancelledAt, &item.initialBytes, &truncated); err != nil {
			rows.Close()
			return err
		}
		item.initialTruncated = truncated != 0
		requests = append(requests, item)
	}
	if err := closeRows(rows); err != nil {
		return err
	}
	for _, item := range requests {
		outcome := DirectReviewOutcomeInterruptedBeforeSubmission
		if item.phase == DirectReviewPhaseRunning {
			outcome = DirectReviewOutcomeInterruptedMayHaveSubmitted
		} else if item.cancelledAt.Valid {
			outcome = DirectReviewOutcomeCancelledBeforeSubmission
		}
		resultJSON, err := marshalDirectReviewResult(DirectReviewResult{
			Outcome: outcome, InitialContextBytes: item.initialBytes, InitialContextTruncated: item.initialTruncated,
		})
		if err != nil {
			return err
		}
		terminalAt := latestDirectReviewUnix(now.Unix(), item.createdAt, item.preparingAt, item.runningAt, item.cancelledAt)
		if _, err := tx.ExecContext(ctx, `UPDATE direct_review_requests SET phase='terminal',terminal_at=?,result_json=? WHERE request_id=? AND phase=?`,
			terminalAt, resultJSON, item.requestID, item.phase); err != nil {
			return err
		}
	}

	type interruptedProbe struct {
		requestID              string
		phase                  string
		createdAt              int64
		runningAt, cancelledAt sql.NullInt64
	}
	rows, err = tx.QueryContext(ctx, `SELECT request_id,phase,created_at,running_at,cancel_requested_at FROM direct_review_probes WHERE phase!='terminal'`)
	if err != nil {
		return err
	}
	var probes []interruptedProbe
	for rows.Next() {
		var item interruptedProbe
		if err := rows.Scan(&item.requestID, &item.phase, &item.createdAt, &item.runningAt, &item.cancelledAt); err != nil {
			rows.Close()
			return err
		}
		probes = append(probes, item)
	}
	if err := closeRows(rows); err != nil {
		return err
	}
	for _, item := range probes {
		outcome := DirectReviewOutcomeInterruptedBeforeSubmission
		if item.phase == DirectReviewPhaseRunning {
			outcome = DirectReviewOutcomeInterruptedMayHaveSubmitted
		} else if item.cancelledAt.Valid {
			outcome = DirectReviewOutcomeCancelledBeforeSubmission
		}
		resultJSON, err := marshalDirectReviewResult(DirectReviewResult{Outcome: outcome})
		if err != nil {
			return err
		}
		terminalAt := latestDirectReviewUnix(now.Unix(), item.createdAt, item.runningAt, item.cancelledAt)
		if _, err := tx.ExecContext(ctx, `UPDATE direct_review_probes SET phase='terminal',terminal_at=?,result_json=? WHERE request_id=? AND phase=?`,
			terminalAt, resultJSON, item.requestID, item.phase); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func scanDirectReviewSettings(scanner rowScanner) (DirectReviewSettings, error) {
	var settings DirectReviewSettings
	var providerJSON, repositoryJSON string
	var probeUpdated int64
	var autoPR, autoTask, autoActive int
	var createdAt, updatedAt int64
	if err := scanner.Scan(&settings.RepositoryID, &settings.ConfigurationVersion, &settings.Protocol, &settings.Endpoint, &settings.Model,
		&settings.AuthenticationMode, &providerJSON, &repositoryJSON, &settings.InstructionVersion, &settings.CredentialID,
		&settings.ConnectionVersion, &settings.AuthorityEpoch, &settings.ProbeFingerprint, &settings.ProbeRequestID, &probeUpdated, &autoPR, &autoTask,
		&settings.AutomaticConsentVer, &settings.AutomaticConsentHash, &autoActive, &createdAt, &updatedAt); err != nil {
		return DirectReviewSettings{}, err
	}
	provider, repository, err := unmarshalDirectReviewLimits(providerJSON, repositoryJSON)
	if err != nil {
		return DirectReviewSettings{}, err
	}
	settings.ProviderLimits = provider
	settings.RepositoryLimits = repository
	settings.CredentialAttached = settings.CredentialID != ""
	settings.AutomaticPREnabled = autoPR != 0
	settings.AutomaticTaskEnabled = autoTask != 0
	settings.AutomaticConsent = autoActive != 0
	settings.CreatedAt = unixTime(createdAt)
	settings.UpdatedAt = unixTime(updatedAt)
	if probeUpdated != 0 {
		value := unixTime(probeUpdated)
		settings.ProbeUpdatedAt = &value
	}
	if err := validateDirectReviewSettings(settings, true); err != nil {
		return DirectReviewSettings{}, err
	}
	return settings, nil
}

// scanDirectReviewProbe reads a machine-local probe row. Probes are excluded
// from portable state, so this scanner is the only reader the row shape still
// has.
func scanDirectReviewProbe(scanner rowScanner) (DirectReviewProbe, error) {
	var probe DirectReviewProbe
	var providerJSON, resultJSON string
	var cancelAt, runningAt, terminalAt sql.NullInt64
	var createdAt int64
	if err := scanner.Scan(&probe.RequestID, &probe.RepositoryID, &probe.RegistrationDigest, &probe.CapabilityFingerprint,
		&probe.ConfigurationVersion, &probe.ConnectionVersion, &probe.ConnectionFingerprint, &probe.Protocol, &probe.Endpoint,
		&probe.Model, &probe.AuthenticationMode, &providerJSON, &probe.DisclosureVersion, &probe.DisclosureDigest, &probe.Phase,
		&cancelAt, &createdAt, &runningAt, &terminalAt, &resultJSON); err != nil {
		return DirectReviewProbe{}, err
	}
	provider, _, err := unmarshalDirectReviewLimits(providerJSON, mustMarshalMinimumRepositoryLimits(providerJSON))
	if err != nil {
		return DirectReviewProbe{}, err
	}
	probe.ProviderLimits = provider
	probe.CreatedAt = unixTime(createdAt)
	probe.CancelRequestedAt = nullableTimePointer(cancelAt)
	probe.RunningAt = nullableTimePointer(runningAt)
	probe.TerminalAt = nullableTimePointer(terminalAt)
	probe.Result, err = unmarshalDirectReviewResult(resultJSON)
	if err != nil {
		return DirectReviewProbe{}, err
	}
	if err := validateDirectReviewProbe(probe, true); err != nil {
		return DirectReviewProbe{}, err
	}
	return probe, nil
}

func scanDirectReviewRequest(scanner rowScanner) (DirectReviewRequest, error) {
	var request DirectReviewRequest
	var providerJSON, repositoryJSON, resultJSON string
	var cancelAt, preparingAt, runningAt, terminalAt sql.NullInt64
	var createdAt int64
	var initialTruncated int
	if err := scanner.Scan(&request.Sequence, &request.RequestID, &request.RepositoryID, &request.TriggerKind, &request.SourceEventKey,
		&request.PullRequestNumber, &request.TaskID, &request.AttemptID, &request.ObservedSourceOID, &request.ObservedTargetOID,
		&request.EffectiveBaseOID, &request.EffectiveHeadOID, &request.DiffMode, &request.ConfigurationVersion, &request.ConnectionVersion,
		&request.ConnectionFingerprint, &request.Protocol, &request.Endpoint, &request.Model, &request.AuthenticationMode, &providerJSON,
		&repositoryJSON, &request.InstructionVersion, &request.ConsentVersion, &request.ConsentDigest, &request.DisclosureVersion,
		&request.DisclosureDigest, &request.RegistrationDigest, &request.Phase, &cancelAt, &createdAt, &preparingAt, &runningAt,
		&terminalAt, &request.InitialContextBytes, &initialTruncated, &resultJSON); err != nil {
		return DirectReviewRequest{}, err
	}
	provider, repository, err := unmarshalDirectReviewLimits(providerJSON, repositoryJSON)
	if err != nil {
		return DirectReviewRequest{}, err
	}
	request.ProviderLimits = provider
	request.RepositoryLimits = repository
	request.CreatedAt = unixTime(createdAt)
	request.CancelRequestedAt = nullableTimePointer(cancelAt)
	request.PreparingAt = nullableTimePointer(preparingAt)
	request.RunningAt = nullableTimePointer(runningAt)
	request.TerminalAt = nullableTimePointer(terminalAt)
	request.InitialContextTruncated = initialTruncated != 0
	request.Result, err = unmarshalDirectReviewResult(resultJSON)
	if err != nil {
		return DirectReviewRequest{}, err
	}
	if err := validateDirectReviewRequest(request, true); err != nil {
		return DirectReviewRequest{}, err
	}
	return request, nil
}

func scanDirectReviewTaskContext(scanner rowScanner) (DirectReviewTaskContext, error) {
	var contextRecord DirectReviewTaskContext
	var createdAt int64
	if err := scanner.Scan(&contextRecord.ContextID, &contextRecord.RepositoryID, &contextRecord.TaskID, &contextRecord.AttemptID,
		&contextRecord.CredentialID, &contextRecord.BaseOID, &contextRecord.HeadOID, &contextRecord.PullRequestNumber,
		&contextRecord.ObservedSourceOID, &contextRecord.ObservedTargetOID, &contextRecord.RegistrationDigest, &createdAt); err != nil {
		return DirectReviewTaskContext{}, err
	}
	contextRecord.CreatedAt = unixTime(createdAt)
	if err := validateDirectReviewTaskContext(contextRecord); err != nil {
		return DirectReviewTaskContext{}, err
	}
	return contextRecord, nil
}

func clearDirectReviewProbeAuthority(settings *DirectReviewSettings) {
	settings.ProbeFingerprint = ""
	settings.ProbeRequestID = ""
	settings.ProbeUpdatedAt = nil
}

func disableAutomaticDirectReview(settings *DirectReviewSettings) {
	settings.AutomaticPREnabled = false
	settings.AutomaticTaskEnabled = false
	settings.AutomaticConsent = false
	settings.AutomaticConsentVer = 0
	settings.AutomaticConsentHash = ""
}

func nullableUnix(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.Unix()
}

func nullableTimePointer(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	result := unixTime(value.Int64)
	return &result
}

func directReviewServerTime(value time.Time) time.Time {
	return time.Unix(value.Unix(), 0).UTC()
}

func latestDirectReviewUnix(value, required int64, optional ...sql.NullInt64) int64 {
	if value < required {
		value = required
	}
	for _, candidate := range optional {
		if candidate.Valid && value < candidate.Int64 {
			value = candidate.Int64
		}
	}
	return value
}

// Probe rows store only provider limits. The repository limit argument lets
// them reuse the same strict JSON decoder without creating a second format.
func minimumDirectReviewRepositoryLimits(provider DirectReviewProviderLimits) DirectReviewRepositoryLimits {
	return DirectReviewRepositoryLimits{
		MaxInitialContextBytes: 1,
		MaxDirectoryBytes:      1,
		MaxFileChunkBytes:      4,
		MaxFilePrefixBytes:     4,
		MaxToolOutputBytes:     minInt64(provider.MaxToolOutputBytes, 4),
		MaxOperationDurationMS: minInt64(provider.MaxDurationMS, 1),
	}
}

func mustMarshalMinimumRepositoryLimits(providerJSON string) string {
	var provider DirectReviewProviderLimits
	if decodeDirectReviewJSON([]byte(providerJSON), &provider) != nil {
		return "{}"
	}
	content, err := json.Marshal(minimumDirectReviewRepositoryLimits(provider))
	if err != nil {
		return "{}"
	}
	return string(content)
}

func minInt64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}
