package server

import (
	"encoding/base64"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"owngit/internal/checkapi"
	"owngit/internal/checksource"
	"owngit/internal/repository"
	"owngit/internal/state"
)

const runnerLeaseHeader = "X-OwnGit-Runner-Lease"

// handleConfiguredCheckOwnerAPI serves owner-only policy, job and runner-token
// operations. Every mutation requires the current administrator password.
func (app *App) handleConfiguredCheckOwnerAPI(writer http.ResponseWriter, request *http.Request, repositoryID, resource, remainder string) {
	if !app.authorizeAdminAPI(writer, request, request.Method != http.MethodGet) {
		return
	}
	if _, exists, err := app.Store.Repository(request.Context(), repositoryID); err != nil {
		writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "OwnGit state is unavailable.", nil)
		return
	} else if !exists {
		writeAPIError(writer, http.StatusNotFound, "repository_not_found", "The repository does not exist.", nil)
		return
	}
	// Runner credentials are credential management, not repository use, so
	// they stay manageable (and revocable) while the repository is locked.
	if resource != "runner-credentials" && app.refusePreparingAPI(writer, repositoryID) {
		return
	}
	switch resource {
	case "check-policy":
		app.handleCheckPolicy(writer, request, repositoryID, remainder)
	case "check-jobs":
		app.handleCheckJobs(writer, request, repositoryID, remainder)
	case "runner-credentials":
		app.handleRunnerCredentials(writer, request, repositoryID, remainder)
	default:
		writeAPIError(writer, http.StatusNotFound, "not_found", "The API endpoint does not exist.", nil)
	}
}

func (app *App) handleCheckPolicy(writer http.ResponseWriter, request *http.Request, repositoryID, remainder string) {
	if remainder == "" && request.Method == http.MethodGet {
		policy, exists, err := app.Store.CheckPolicy(request.Context(), repositoryID)
		if err != nil {
			writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "The configured-check policy could not be read.", nil)
			return
		}
		if !exists {
			writeAPIError(writer, http.StatusNotFound, "check_policy_not_found", "The repository has no configured-check policy.", nil)
			return
		}
		writeAPIJSON(writer, http.StatusOK, app.policyResponse(policy))
		return
	}
	if remainder == "" && request.Method == http.MethodPut {
		var input checkapi.PolicyInput
		if !decodeAPIJSON(writer, request, &input) {
			return
		}
		policy, err := app.Store.SetCheckPolicy(request.Context(), state.CheckPolicyInput{
			RepositoryID: repositoryID, Executor: input.Executor, AllowedEvents: input.AllowedEvents,
			MaxTimeoutMS: input.MaxTimeoutMS, MaxOutputLimitBytes: input.MaxOutputLimitBytes,
			QueueLimit: input.QueueLimit, MaxActiveJobs: input.MaxActiveJobs, MaxLeaseMS: input.MaxLeaseMS,
			Execution: input.Execution,
		}, app.now())
		if err != nil {
			writeAPIError(writer, http.StatusUnprocessableEntity, "invalid_check_policy", err.Error(), nil)
			return
		}
		app.wakeChecks(repositoryID)
		writeAPIJSON(writer, http.StatusOK, app.policyResponse(policy))
		return
	}
	if (remainder == "enable" || remainder == "disable") && request.Method == http.MethodPost {
		var (
			policy state.CheckPolicy
			err    error
		)
		if remainder == "enable" {
			policy, err = app.Store.GrantCheckConsent(request.Context(), repositoryID, app.now())
		} else {
			policy, err = app.Store.RevokeCheckConsent(request.Context(), repositoryID, app.now())
		}
		if err != nil {
			status := http.StatusServiceUnavailable
			code := "state_unavailable"
			if errors.Is(err, state.ErrCheckPolicyMissing) {
				status, code = http.StatusNotFound, "check_policy_not_found"
			} else if errors.Is(err, state.ErrInvalidCheckPolicy) {
				status, code = http.StatusConflict, "check_policy_incomplete"
			}
			writeAPIError(writer, status, code, err.Error(), nil)
			return
		}
		app.wakeChecks(repositoryID)
		writeAPIJSON(writer, http.StatusOK, app.policyResponse(policy))
		return
	}
	if remainder == "" {
		writeAPIMethodError(writer, http.MethodGet+", "+http.MethodPut)
		return
	}
	writeAPIError(writer, http.StatusNotFound, "not_found", "The API endpoint does not exist.", nil)
}

func (app *App) handleCheckJobs(writer http.ResponseWriter, request *http.Request, repositoryID, remainder string) {
	if remainder == "" {
		if request.Method != http.MethodGet {
			writeAPIMethodError(writer, http.MethodGet)
			return
		}
		jobs, err := app.Store.LatestCheckJobs(request.Context(), repositoryID, 100)
		if err != nil {
			writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "Configured-check jobs could not be read.", nil)
			return
		}
		response := checkapi.JobListResponse{OK: true, Jobs: make([]*checkapi.Job, 0, len(jobs))}
		for _, job := range jobs {
			response.Jobs = append(response.Jobs, app.jobJSON(request, job, false))
		}
		writeAPIJSON(writer, http.StatusOK, response)
		return
	}
	parts := strings.Split(remainder, "/")
	jobID := parts[0]
	if !validAttemptID(jobID) {
		writeAPIError(writer, http.StatusUnprocessableEntity, "invalid_job_id", "A valid configured-check job identifier is required.", nil)
		return
	}
	if len(parts) == 1 && request.Method == http.MethodGet {
		job, exists, err := app.Store.CheckJob(request.Context(), repositoryID, jobID)
		if err != nil {
			writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "The configured-check job could not be read.", nil)
			return
		}
		if !exists {
			writeAPIError(writer, http.StatusNotFound, "check_job_not_found", "The configured-check job does not exist.", nil)
			return
		}
		response := checkapi.JobResponse{OK: true, Job: app.jobJSON(request, job, true)}
		if job.AttemptID != "" {
			if attempt, exists, err := app.Store.CheckAttemptByID(request.Context(), repositoryID, job.AttemptID); err == nil && exists {
				response.Attempt = attemptJSON(attempt)
			}
		}
		writeAPIJSON(writer, http.StatusOK, response)
		return
	}
	if len(parts) == 2 && parts[1] == "log" && request.Method == http.MethodGet {
		job, exists, err := app.Store.CheckJob(request.Context(), repositoryID, jobID)
		if err != nil {
			writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "The configured-check job could not be read.", nil)
			return
		}
		if !exists {
			writeAPIError(writer, http.StatusNotFound, "check_job_not_found", "The configured-check job does not exist.", nil)
			return
		}
		if job.AttemptID == "" {
			writeAPIError(writer, http.StatusNotFound, "attempt_not_found", "The configured-check job has no attempt.", nil)
			return
		}
		attempt, exists, err := app.Store.CheckAttemptByID(request.Context(), repositoryID, job.AttemptID)
		if err != nil {
			writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "The configured-check attempt could not be read.", nil)
			return
		}
		if !exists {
			writeAPIError(writer, http.StatusNotFound, "attempt_not_found", "The configured-check attempt does not exist.", nil)
			return
		}
		app.writeCheckAttemptLog(writer, attempt)
		return
	}
	if len(parts) == 2 && request.Method == http.MethodPost {
		var (
			job state.CheckJob
			err error
		)
		switch parts[1] {
		case "cancel":
			job, err = app.Store.CancelCheckJob(request.Context(), repositoryID, jobID, app.now())
		case "rerun":
			var deduped bool
			job, deduped, err = app.Store.RerunCheckJob(request.Context(), repositoryID, jobID, app.now())
			_ = deduped
		default:
			writeAPIError(writer, http.StatusNotFound, "not_found", "The API endpoint does not exist.", nil)
			return
		}
		if err != nil {
			writeConfiguredJobError(writer, err)
			return
		}
		app.wakeChecks(repositoryID)
		writeAPIJSON(writer, http.StatusOK, checkapi.JobResponse{OK: true, Job: app.jobJSON(request, job, true)})
		return
	}
	writeAPIError(writer, http.StatusNotFound, "not_found", "The API endpoint does not exist.", nil)
}

func (app *App) handleRunnerCredentials(writer http.ResponseWriter, request *http.Request, repositoryID, remainder string) {
	parts := strings.Split(remainder, "/")
	switch {
	case remainder == "" && request.Method == http.MethodGet:
		credentials, err := app.Store.CheckRunnerCredentials(request.Context(), repositoryID)
		if err != nil {
			writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "Runner credentials could not be read.", nil)
			return
		}
		response := checkapi.RunnerCredentialListResponse{OK: true, Credentials: make([]*checkapi.RunnerCredential, 0, len(credentials))}
		for _, credential := range credentials {
			response.Credentials = append(response.Credentials, runnerCredentialJSON(credential))
		}
		writeAPIJSON(writer, http.StatusOK, response)
	case remainder == "" && request.Method == http.MethodPost:
		var input checkapi.CreateCredentialInput
		if !decodeAPIJSON(writer, request, &input) {
			return
		}
		credential, token, created, err := app.Store.IssueCheckRunnerToken(request.Context(), repositoryID, input.Label, input.CreationID, app.now())
		if err != nil {
			if errors.Is(err, state.ErrCheckRunnerCreationConflict) {
				writeAPIError(writer, http.StatusConflict, "creation_conflict", "The creation identity already belongs to different runner credential content.", nil)
			} else {
				writeAPIError(writer, http.StatusUnprocessableEntity, "invalid_runner_credential", err.Error(), nil)
			}
			return
		}
		response := checkapi.RunnerCredentialResponse{OK: true, Credential: runnerCredentialJSON(credential)}
		if created {
			response.Token = token
		}
		writeAPIJSON(writer, http.StatusOK, response)
	case len(parts) == 1 && parts[0] != "" && request.Method == http.MethodDelete:
		if err := app.Store.RevokeCheckRunnerToken(request.Context(), repositoryID, parts[0], app.now()); err != nil {
			writeAPIError(writer, http.StatusConflict, "runner_credential_not_found", "The runner credential was not found or was already revoked.", nil)
			return
		}
		app.wakeChecks(repositoryID)
		writeAPIJSON(writer, http.StatusOK, checkapi.OKResponse{OK: true})
	case len(parts) == 2 && parts[0] == "by-creation" && parts[1] != "" && request.Method == http.MethodDelete:
		if err := app.Store.RevokeCheckRunnerTokenByCreation(request.Context(), repositoryID, parts[1], app.now()); err != nil {
			writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "The runner credential could not be revoked.", nil)
			return
		}
		writeAPIJSON(writer, http.StatusOK, checkapi.OKResponse{OK: true})
	default:
		writeAPIError(writer, http.StatusNotFound, "not_found", "The API endpoint does not exist.", nil)
	}
}

func (app *App) handleRunnerAPI(writer http.ResponseWriter, request *http.Request, repositoryID, remainder string) {
	credential, ok := app.authorizeRunner(writer, request, repositoryID)
	if !ok {
		return
	}
	if app.refusePreparingAPI(writer, repositoryID) {
		return
	}
	status := app.checkRuntimeStatus()
	if !status.Available {
		writeAPIError(writer, http.StatusServiceUnavailable, "check_runtime_unavailable", status.UnavailableReason, map[string]string{"reason": status.UnavailableCode})
		return
	}
	if remainder == "claim" {
		if request.Method != http.MethodPost {
			writeAPIMethodError(writer, http.MethodPost)
			return
		}
		job, claimed, err := app.Store.ClaimCheckJob(request.Context(), repositoryID, credential.ID, app.now())
		if err != nil {
			writeRunnerError(writer, err)
			return
		}
		if !claimed {
			writeAPIJSON(writer, http.StatusOK, checkapi.JobResponse{OK: true})
			return
		}
		writeAPIJSON(writer, http.StatusOK, checkapi.JobResponse{OK: true, Job: app.jobJSON(request, job, true)})
		return
	}
	parts := strings.Split(remainder, "/")
	if len(parts) < 2 || parts[0] != "jobs" || !validAttemptID(parts[1]) {
		writeAPIError(writer, http.StatusNotFound, "not_found", "The runner endpoint does not exist.", nil)
		return
	}
	jobID := parts[1]
	leaseID := request.Header.Get(runnerLeaseHeader)
	if len(parts) == 3 && parts[2] == "source" && request.Method == http.MethodGet {
		app.runnerSourceManifest(writer, request, repositoryID, jobID, leaseID, credential)
		return
	}
	if len(parts) == 5 && parts[2] == "files" && request.Method == http.MethodGet {
		app.runnerSourceBlob(writer, request, repositoryID, jobID, leaseID, parts[3], parts[4], credential)
		return
	}
	if len(parts) != 3 || request.Method != http.MethodPost {
		writeAPIError(writer, http.StatusNotFound, "not_found", "The runner endpoint does not exist.", nil)
		return
	}
	authority := state.CheckJobCompletionAuthority{JobID: jobID, LeaseID: leaseID, CredentialID: credential.ID, CredentialGeneration: credential.Generation}
	switch parts[2] {
	case "renew":
		job, err := app.Store.RenewCheckJobLease(request.Context(), authority, app.now())
		if err != nil {
			writeRunnerError(writer, err)
			return
		}
		writeAPIJSON(writer, http.StatusOK, checkapi.JobResponse{OK: true, Job: app.jobJSON(request, job, false)})
	case "start":
		var input checkapi.RunnerStartInput
		if !decodeAPIJSON(writer, request, &input) {
			return
		}
		if input.LeaseID != leaseID || !validAttemptID(input.AttemptID) {
			writeAPIError(writer, http.StatusUnprocessableEntity, "invalid_runner_start", "The runner start identity is invalid.", nil)
			return
		}
		started, err := app.Store.StartCheckJob(request.Context(), state.CheckJobStart{
			RepositoryID: repositoryID, JobID: jobID, LeaseID: leaseID,
			CredentialID: credential.ID, CredentialGeneration: credential.Generation,
			Protection: state.ProtectionRunnerReported,
		}, app.now())
		if err != nil {
			writeRunnerError(writer, err)
			return
		}
		configuration, exists, err := app.Store.CheckConfiguration(request.Context(), repositoryID, started.ConfigurationVersion)
		if err != nil || !exists {
			writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "The job configuration is unavailable.", nil)
			return
		}
		attempt := state.CheckAttempt{
			ID: input.AttemptID, TaskID: started.TaskID, RepositoryID: repositoryID, RevisionOID: started.SourceOID,
			WorktreeState: state.WorktreeClean, JobID: started.ID, Checks: configuration.Checks,
			StartedAt: *started.StartedAt, CreatedAt: app.now(), CredentialID: credential.ID,
		}
		_, stored, err := app.Store.RegisterCheckAttempt(request.Context(), attempt)
		if err != nil {
			writeRunnerError(writer, err)
			return
		}
		writeAPIJSON(writer, http.StatusOK, checkapi.JobResponse{OK: true, Job: app.jobJSON(request, started, true), Attempt: attemptJSON(stored)})
	case "complete":
		var input checkapi.RunnerCompletionInput
		if !decodeAPIJSONLimit(writer, request, &input, maximumCheckUpload) {
			return
		}
		if input.LeaseID != leaseID {
			writeAPIError(writer, http.StatusConflict, "check_job_lease", "The job lease does not match.", nil)
			return
		}
		job, exists, err := app.Store.CheckJob(request.Context(), repositoryID, jobID)
		if err != nil || !exists || job.AttemptID == "" {
			writeRunnerError(writer, errors.Join(err, state.ErrCheckJobNotFound))
			return
		}
		completion, problem := completionFromUpload(input.Completion, repositoryID, job.TaskID, job.AttemptID)
		if problem != nil {
			writeAPIError(writer, apiStatus(problem.Code), problem.Code, problem.Message, nil)
			return
		}
		task, attempt, err := app.Store.CompleteCheckJobAttempt(request.Context(), completion, authority, app.now())
		if err != nil {
			writeRunnerError(writer, err)
			return
		}
		writeAPIJSON(writer, http.StatusOK, checkapi.TaskResponse{OK: true, Task: taskJSON(task), Attempt: attemptJSON(attempt)})
	case "unavailable":
		var input checkapi.RunnerUnavailableInput
		if !decodeAPIJSON(writer, request, &input) {
			return
		}
		if input.LeaseID != leaseID {
			writeAPIError(writer, http.StatusConflict, "check_job_lease", "The job lease does not match.", nil)
			return
		}
		job, err := app.Store.FailCheckJobBeforeStart(request.Context(), authority, input.Status, input.Summary, app.now())
		if err != nil {
			writeRunnerError(writer, err)
			return
		}
		writeAPIJSON(writer, http.StatusOK, checkapi.JobResponse{OK: true, Job: app.jobJSON(request, job, false)})
	default:
		writeAPIError(writer, http.StatusNotFound, "not_found", "The runner endpoint does not exist.", nil)
	}
}

func (app *App) runnerSourceManifest(writer http.ResponseWriter, request *http.Request, repositoryID, jobID, leaseID string, credential state.RunnerCredential) {
	job, err := app.Store.AuthorizeCheckJobSource(request.Context(), repositoryID, jobID, leaseID, credential.ID, credential.Generation, app.now())
	if err != nil {
		writeRunnerError(writer, err)
		return
	}
	pinned, err := app.Repositories.PinRepository(request.Context(), repositoryID, job.SourceOID, job.SourceOID)
	if err != nil {
		writeAPIError(writer, http.StatusServiceUnavailable, "check_source_unavailable", "The exact configured-check source is unavailable.", nil)
		return
	}
	source, err := checksource.NewPinnedSource(pinned, repository.PinnedHead)
	if err != nil {
		writeAPIError(writer, http.StatusServiceUnavailable, "check_source_unavailable", err.Error(), nil)
		return
	}
	entries, err := source.ListTree(request.Context(), job.Execution.Source.MetadataLimit)
	if err != nil || checksource.ValidateEntries(entries, sourceLimits(job.Execution.Source)) != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, "check_source_refused", "The exact source does not satisfy the configured materialization bounds.", nil)
		return
	}
	response := checkapi.SourceManifest{OK: true, JobID: job.ID, CommitOID: job.SourceOID, ObjectFormat: source.ObjectFormat(), Limits: job.Execution.Source}
	for _, entry := range entries {
		response.Entries = append(response.Entries, checkapi.SourceEntry{Path: entry.Path, OID: entry.OID, Mode: entry.Mode, Type: entry.Type, Size: entry.Size})
	}
	writeAPIJSON(writer, http.StatusOK, response)
}

func (app *App) runnerSourceBlob(writer http.ResponseWriter, request *http.Request, repositoryID, jobID, leaseID, encodedPath, oid string, credential state.RunnerCredential) {
	job, err := app.Store.AuthorizeCheckJobSource(request.Context(), repositoryID, jobID, leaseID, credential.ID, credential.Generation, app.now())
	if err != nil {
		writeRunnerError(writer, err)
		return
	}
	pinned, err := app.Repositories.PinRepository(request.Context(), repositoryID, job.SourceOID, job.SourceOID)
	if err != nil {
		writeAPIError(writer, http.StatusServiceUnavailable, "check_source_unavailable", "The exact configured-check source is unavailable.", nil)
		return
	}
	decodedPath, err := base64.RawURLEncoding.DecodeString(encodedPath)
	if err != nil || len(decodedPath) == 0 || len(decodedPath) > job.Execution.Source.MaxPathBytes {
		writeAPIError(writer, http.StatusUnprocessableEntity, "invalid_check_source_path", "The authorized source path is invalid.", nil)
		return
	}
	blob, err := pinned.ReadBlob(request.Context(), repository.PinnedHead, string(decodedPath), 0,
		job.Execution.Source.MetadataLimit, job.Execution.Source.MaxFileBytes+1, job.Execution.Source.MaxFileBytes+1)
	if err != nil || blob.HasMore || blob.OID != oid || blob.Size != int64(len(blob.Content)) ||
		blob.Symlink || (blob.Mode != "100644" && blob.Mode != "100755") {
		writeAPIError(writer, http.StatusServiceUnavailable, "check_source_unavailable", "The authorized exact source blob is unavailable.", nil)
		return
	}
	writer.Header().Set("Content-Type", "application/octet-stream")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("X-OwnGit-Blob-OID", oid)
	writer.Header().Set("Content-Length", strconv.FormatInt(int64(len(blob.Content)), 10))
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(blob.Content)
}

func (app *App) authorizeRunner(writer http.ResponseWriter, request *http.Request, repositoryID string) (state.RunnerCredential, bool) {
	header := request.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		writer.Header().Set("WWW-Authenticate", `Bearer realm="OwnGit runner"`)
		writeAPIError(writer, http.StatusUnauthorized, "runner_authentication_required", "A repository-scoped runner token is required.", nil)
		return state.RunnerCredential{}, false
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	credential, ok, err := app.Store.RunnerCredentialByToken(request.Context(), repositoryID, token, app.now())
	if err != nil {
		writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "The runner token could not be verified.", nil)
		return state.RunnerCredential{}, false
	}
	if !ok || credential.Role != state.RunnerRoleExternal {
		writeAPIError(writer, http.StatusUnauthorized, "invalid_runner_credential", "The runner token is unknown or revoked.", nil)
		return state.RunnerCredential{}, false
	}
	return credential, true
}

func (app *App) jobJSON(request *http.Request, job state.CheckJob, includeChecks bool) *checkapi.Job {
	result := &checkapi.Job{
		ID: job.ID, RepositoryID: job.RepositoryID, TaskID: job.TaskID, Trigger: job.Trigger, EventKey: job.EventKey,
		SourceOID: job.SourceOID, BaseOID: job.BaseOID, PullRequestNumber: job.PullRequestNumber, TriggerRef: job.TriggerRef,
		WorkflowPath: job.WorkflowPath, WorkflowOID: job.WorkflowOID, WorkflowDigest: job.WorkflowDigest,
		ConfigurationVersion: job.ConfigurationVersion, Executor: job.Executor, PolicyVersion: job.PolicyVersion,
		ConsentVersion: job.ConsentVersion, Limits: job.Limits, Execution: job.Execution,
		Status: job.Status, AttemptID: job.AttemptID, LeaseID: job.LeaseID, LeaseExpiresAt: job.LeaseExpiresAt,
		Protection: job.Protection, CancelRequested: job.CancelRequestedAt != nil, Summary: job.Summary,
		AdmittedAt: job.AdmittedAt, StartedAt: job.StartedAt, FinishedAt: job.FinishedAt,
	}
	if includeChecks {
		if configuration, exists, err := app.Store.CheckConfiguration(request.Context(), job.RepositoryID, job.ConfigurationVersion); err == nil && exists {
			for _, check := range configuration.Checks {
				result.Checks = append(result.Checks, checkapi.CheckDefinition{Name: check.Name, Command: check.Command})
			}
		}
	}
	return result
}

func (app *App) policyResponse(policy state.CheckPolicy) checkapi.PolicyResponse {
	return checkapi.PolicyResponse{OK: true, Policy: policyJSON(policy), Runtime: app.checkRuntimeStatus()}
}

func (app *App) checkRuntimeStatus() checkapi.RuntimeStatus {
	if app.CheckRuntimeUnavailableCode == "" {
		return checkapi.RuntimeStatus{Available: true}
	}
	reason := app.CheckRuntimeUnavailableReason
	if reason == "" {
		reason = "Configured checks are unavailable. Repair the check runtime and restart OwnGit."
	}
	return checkapi.RuntimeStatus{
		Available: false, UnavailableCode: app.CheckRuntimeUnavailableCode,
		UnavailableReason: reason,
	}
}

func policyJSON(policy state.CheckPolicy) *checkapi.Policy {
	return &checkapi.Policy{
		RepositoryID: policy.RepositoryID, Version: policy.Version, Digest: policy.Digest, Executor: policy.Executor,
		AllowedEvents: policy.AllowedEvents, MaxTimeoutMS: policy.MaxTimeoutMS, MaxOutputLimitBytes: policy.MaxOutputLimitBytes,
		QueueLimit: policy.QueueLimit, MaxActiveJobs: policy.MaxActiveJobs, MaxLeaseMS: policy.MaxLeaseMS,
		Execution: policy.Execution, ConsentVersion: policy.ConsentVersion, ConsentActive: policy.ConsentActive,
		RunnerGeneration: policy.RunnerGeneration, CreatedAt: policy.CreatedAt, UpdatedAt: policy.UpdatedAt,
	}
}

func runnerCredentialJSON(credential state.RunnerCredential) *checkapi.RunnerCredential {
	return &checkapi.RunnerCredential{
		ID: credential.ID, RepositoryID: credential.RepositoryID, Label: credential.Label, CreationID: credential.CreationID,
		Generation: credential.Generation, CreatedAt: credential.CreatedAt, RevokedAt: credential.RevokedAt, LastUsedAt: credential.LastUsedAt,
	}
}

func sourceLimits(limits state.CheckSourceLimits) checksource.Limits {
	return checksource.Limits{
		MaxEntries: limits.MaxEntries, MaxFileBytes: limits.MaxFileBytes, MaxTotalBytes: limits.MaxTotalBytes,
		MaxPathDepth: limits.MaxPathDepth, MaxPathBytes: limits.MaxPathBytes, MaxNameBytes: limits.MaxNameBytes,
		MetadataLimit: limits.MetadataLimit,
	}
}

func writeConfiguredJobError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, state.ErrCheckJobNotFound):
		writeAPIError(writer, http.StatusNotFound, "check_job_not_found", "The configured-check job does not exist.", nil)
	case errors.Is(err, state.ErrCheckJobState), errors.Is(err, state.ErrCheckConsentRequired):
		writeAPIError(writer, http.StatusConflict, "check_job_state", err.Error(), nil)
	default:
		writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "The configured-check job could not be changed.", nil)
	}
}

func writeRunnerError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, state.ErrCheckJobNotFound):
		writeAPIError(writer, http.StatusNotFound, "check_job_not_found", "The configured-check job does not exist.", nil)
	case errors.Is(err, state.ErrCheckJobLease):
		writeAPIError(writer, http.StatusConflict, "check_job_lease", "The configured-check job lease is stale or foreign.", nil)
	case errors.Is(err, state.ErrCheckJobState), errors.Is(err, state.ErrCheckJobStartReplay):
		writeAPIError(writer, http.StatusConflict, "check_job_state", err.Error(), nil)
	case errors.Is(err, state.ErrCheckRunnerCredential):
		writeAPIError(writer, http.StatusUnauthorized, "invalid_runner_credential", "The runner token is unknown or revoked.", nil)
	case errors.Is(err, state.ErrCheckConsentRequired):
		writeAPIError(writer, http.StatusConflict, "check_consent_required", "Configured-check authority changed.", nil)
	default:
		// The cause can name internal state or host paths, so it stays in the
		// server log and the runner receives a fixed message.
		log.Printf("configured-check runner operation failed: %v", err)
		writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "The runner operation failed.", nil)
	}
}
