// Package checkrunner implements the separately connected configured-check
// runner protocol. It has no direct access to OwnGit state or repositories.
package checkrunner

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"owngit/internal/apiclient"
	"owngit/internal/checkapi"
	"owngit/internal/checkexec"
	"owngit/internal/checksource"
	"owngit/internal/state"
)

const (
	leaseHeader       = "X-OwnGit-Runner-Lease"
	maximumRunnerLog  = 256 << 10
	minimumRenewDelay = 100 * time.Millisecond
)

// Runner polls one repository with one repository-scoped bearer token.
type Runner struct {
	Client        *apiclient.Client
	RepositoryID  string
	WorkspaceRoot string
	PollInterval  time.Duration
	Once          bool
	Logf          func(string, ...any)
}

func (runner *Runner) Run(ctx context.Context) error {
	if runner == nil || runner.Client == nil || runner.RepositoryID == "" || runner.WorkspaceRoot == "" {
		return errors.New("configured-check runner is incomplete")
	}
	workspaceRoot, err := checksource.AcquireWorkspaceRoot(runner.WorkspaceRoot)
	if err != nil {
		return fmt.Errorf("acquire runner workspace root: %w", err)
	}
	defer workspaceRoot.Close()
	removed, more, cleanupErr := workspaceRoot.Cleanup(1000)
	if cleanupErr != nil {
		return fmt.Errorf("clean interrupted runner workspaces: %w", cleanupErr)
	}
	if more {
		runner.log("runner startup workspace cleanup removed=%d; additional unrecognized or stale entries remain", removed)
	}
	interval := runner.PollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	for {
		worked, err := runner.runOne(ctx, workspaceRoot)
		if err != nil {
			return err
		}
		if runner.Once {
			return nil
		}
		if worked {
			continue
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (runner *Runner) runOne(ctx context.Context, workspaceRoot *checksource.WorkspaceRoot) (bool, error) {
	base := "/api/v1/repositories/" + url.PathEscape(runner.RepositoryID) + "/runner"
	content, err := runner.Client.Do(ctx, http.MethodPost, base+"/claim", nil)
	if err != nil {
		return false, err
	}
	var claimed checkapi.JobResponse
	if err := json.Unmarshal(content, &claimed); err != nil {
		return false, fmt.Errorf("decode claimed job: %w", err)
	}
	if claimed.Job == nil {
		return false, nil
	}
	job := claimed.Job
	if job.LeaseID == "" || job.Executor != state.CheckExecutorExternalRunner {
		return true, errors.New("server returned an invalid external-runner claim")
	}
	runner.log("claimed configured-check job %s", job.ID)
	leaseContext, cancelLease := context.WithCancel(ctx)
	defer cancelLease()
	leaseErrors := make(chan error, 1)
	go runner.renewLease(leaseContext, cancelLease, base, job, leaseErrors)

	_, workspace, materializeErr := workspaceRoot.PrepareJob(job.ID)
	remote := &remoteSource{client: runner.Client, base: base, job: job}
	if materializeErr == nil {
		materializeErr = remote.load(leaseContext)
	}
	var materialization *checksource.Result
	if materializeErr == nil {
		materialization, materializeErr = checksource.Materialize(leaseContext, remote, workspace, checksource.Options{
			Limits: checksource.Limits{
				MaxEntries: job.Execution.Source.MaxEntries, MaxFileBytes: job.Execution.Source.MaxFileBytes,
				MaxTotalBytes: job.Execution.Source.MaxTotalBytes, MaxPathDepth: job.Execution.Source.MaxPathDepth,
				MaxPathBytes: job.Execution.Source.MaxPathBytes, MaxNameBytes: job.Execution.Source.MaxNameBytes,
				MetadataLimit: job.Execution.Source.MetadataLimit,
			},
			Timeout: time.Duration(job.Limits.TimeoutMS) * time.Millisecond,
		})
	}
	if materializeErr != nil {
		cancelLease()
		<-leaseErrors
		_ = workspaceRoot.RemoveJob(job.ID)
		status := state.CheckJobUnavailable
		if ctx.Err() != nil {
			status = state.CheckJobInterrupted
		}
		input := checkapi.RunnerUnavailableInput{LeaseID: job.LeaseID, Status: status, Summary: bounded("Exact configured-check source is unavailable or workspace ownership is uncertain: " + materializeErr.Error())}
		_, reportErr := runner.Client.DoWithHeaders(context.WithoutCancel(ctx), http.MethodPost, base+"/jobs/"+job.ID+"/unavailable", input, leaseHeaders(job.LeaseID))
		if reportErr != nil {
			return true, errors.Join(materializeErr, reportErr)
		}
		runner.log("reported configured-check job %s unavailable: %v", job.ID, materializeErr)
		return true, nil
	}

	attemptID, err := state.RandomID()
	if err != nil {
		_ = workspaceRoot.RemoveJob(job.ID)
		return true, err
	}
	startInput := checkapi.RunnerStartInput{LeaseID: job.LeaseID, AttemptID: attemptID}
	if _, err := runner.Client.DoWithHeaders(leaseContext, http.MethodPost, base+"/jobs/"+job.ID+"/start", startInput, leaseHeaders(job.LeaseID)); err != nil {
		_ = workspaceRoot.RemoveJob(job.ID)
		cancelLease()
		<-leaseErrors
		return true, err
	}

	definitions := make([]checkexec.Definition, 0, len(job.Checks))
	for _, check := range job.Checks {
		definitions = append(definitions, checkexec.Definition{Name: check.Name, Command: check.Command})
	}
	results, cancelled := checkexec.Run(leaseContext, definitions, checkexec.Options{
		Dir: workspace, Timeout: time.Duration(job.Limits.TimeoutMS) * time.Millisecond,
		OutputLimit: job.Limits.OutputLimitBytes,
	})
	submittedWorktree := state.WorktreeClean
	verifyContext, cancelVerify := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	clean, verifyErr := checksource.VerifyResult(verifyContext, materialization)
	cancelVerify()
	if verifyErr != nil {
		submittedWorktree = state.WorktreeUnknown
		if len(results) != 0 {
			results[len(results)-1].Status = checkexec.StatusError
			results[len(results)-1].CleanupError = bounded("verify private runner workspace: " + verifyErr.Error())
		}
	} else if !clean {
		submittedWorktree = state.WorktreeDirty
		if len(results) != 0 && results[len(results)-1].Status == checkexec.StatusPassed {
			results[len(results)-1].Status = checkexec.StatusIncomplete
		}
	}
	if cleanupErr := workspaceRoot.RemoveJob(job.ID); cleanupErr != nil && len(results) != 0 {
		results[len(results)-1].Status = checkexec.StatusError
		results[len(results)-1].CleanupError = bounded("remove private runner workspace: " + cleanupErr.Error())
	}
	cancelLease()
	leaseErr := <-leaseErrors
	if leaseErr != nil && !errors.Is(leaseErr, context.Canceled) && len(results) != 0 {
		results[len(results)-1].Status = checkexec.StatusError
		results[len(results)-1].CleanupError = bounded("runner lease became uncertain: " + leaseErr.Error())
	}
	log, logTruncated := runnerLog(results)
	completion := checkapi.AttemptCompletion{
		Results: runnerResults(results), Cancelled: cancelled, FinishedAt: time.Now().UTC(),
		WorktreeState: submittedWorktree, Log: log, LogTruncated: logTruncated,
	}
	input := checkapi.RunnerCompletionInput{LeaseID: job.LeaseID, Completion: completion}
	_, err = runner.Client.DoWithHeaders(context.WithoutCancel(ctx), http.MethodPost, base+"/jobs/"+job.ID+"/complete", input, leaseHeaders(job.LeaseID))
	if err == nil {
		runner.log("completed configured-check job %s", job.ID)
	}
	return true, err
}

func (runner *Runner) renewLease(ctx context.Context, cancel context.CancelFunc, base string, job *checkapi.Job, done chan<- error) {
	delay := time.Second
	if job.LeaseExpiresAt != nil {
		if candidate := time.Until(*job.LeaseExpiresAt) / 3; candidate > minimumRenewDelay && candidate < delay {
			delay = candidate
		}
	}
	ticker := time.NewTicker(delay)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			done <- ctx.Err()
			return
		case <-ticker.C:
			content, err := runner.Client.DoWithHeaders(ctx, http.MethodPost, base+"/jobs/"+job.ID+"/renew", nil, leaseHeaders(job.LeaseID))
			if err != nil {
				cancel()
				done <- err
				return
			}
			var response checkapi.JobResponse
			if err := json.Unmarshal(content, &response); err != nil || response.Job == nil {
				cancel()
				if err == nil {
					err = errors.New("lease renewal returned no job")
				}
				done <- err
				return
			}
			if response.Job.CancelRequested {
				cancel()
				done <- nil
				return
			}
		}
	}
}

type remoteSource struct {
	client   *apiclient.Client
	base     string
	job      *checkapi.Job
	manifest *checkapi.SourceManifest
	paths    map[string]string
}

func (source *remoteSource) load(ctx context.Context) error {
	content, err := source.client.DoWithHeaders(ctx, http.MethodGet, source.base+"/jobs/"+source.job.ID+"/source", nil, leaseHeaders(source.job.LeaseID))
	if err != nil {
		return err
	}
	var manifest checkapi.SourceManifest
	if err := json.Unmarshal(content, &manifest); err != nil {
		return err
	}
	if manifest.JobID != source.job.ID || manifest.CommitOID != source.job.SourceOID || manifest.ObjectFormat == "" || manifest.Limits != source.job.Execution.Source {
		return errors.New("source manifest does not match the immutable job")
	}
	source.manifest = &manifest
	source.paths = make(map[string]string, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		if entry.Type == "blob" {
			if _, exists := source.paths[entry.OID]; !exists {
				source.paths[entry.OID] = entry.Path
			}
		}
	}
	return nil
}

func (source *remoteSource) ListTree(ctx context.Context, metadataLimit int64) ([]checksource.Entry, error) {
	if source.manifest == nil {
		return nil, errors.New("source manifest has not been loaded")
	}
	entries := make([]checksource.Entry, 0, len(source.manifest.Entries))
	for _, entry := range source.manifest.Entries {
		entries = append(entries, checksource.Entry{Path: entry.Path, OID: entry.OID, Mode: entry.Mode, Type: entry.Type, Size: entry.Size})
	}
	return entries, nil
}

func (source *remoteSource) ReadBlob(ctx context.Context, oid string, expectedSize int64) ([]byte, error) {
	if source.manifest == nil || expectedSize < 0 || expectedSize > source.manifest.Limits.MaxFileBytes {
		return nil, errors.New("source blob was requested without a valid manifest bound")
	}
	path, exists := source.paths[oid]
	if !exists {
		return nil, errors.New("source blob is not present in the authorized manifest")
	}
	encodedPath := base64.RawURLEncoding.EncodeToString([]byte(path))
	content, headers, err := source.client.GetBytes(ctx, source.base+"/jobs/"+source.job.ID+"/files/"+encodedPath+"/"+url.PathEscape(oid), leaseHeaders(source.job.LeaseID), expectedSize)
	if err != nil {
		return nil, err
	}
	if headers.Get("X-OwnGit-Blob-OID") != oid || int64(len(content)) != expectedSize {
		return nil, errors.New("source blob identity or size does not match the manifest")
	}
	header := []byte(fmt.Sprintf("blob %d%c", len(content), 0))
	var actual string
	switch source.manifest.ObjectFormat {
	case "sha1":
		hash := sha1.New()
		_, _ = hash.Write(header)
		_, _ = hash.Write(content)
		actual = fmt.Sprintf("%x", hash.Sum(nil))
	case "sha256":
		hash := sha256.New()
		_, _ = hash.Write(header)
		_, _ = hash.Write(content)
		actual = fmt.Sprintf("%x", hash.Sum(nil))
	default:
		return nil, errors.New("source manifest has an unsupported object format")
	}
	if actual != oid {
		return nil, errors.New("source blob hash does not match its Git object ID")
	}
	return content, nil
}

func (source *remoteSource) ObjectFormat() string {
	if source.manifest == nil {
		return ""
	}
	return source.manifest.ObjectFormat
}

func (source *remoteSource) CommitOID() string {
	if source.manifest == nil {
		return ""
	}
	return source.manifest.CommitOID
}

func leaseHeaders(leaseID string) map[string]string { return map[string]string{leaseHeader: leaseID} }

func runnerResults(results []checkexec.Result) []checkapi.Result {
	converted := make([]checkapi.Result, 0, len(results))
	for _, result := range results {
		excerpt, cut := checkapi.ClipText(result.Output, state.MaximumCheckExcerptBytes)
		truncated := result.Truncated || cut
		cleanupError := result.CleanupError
		if cleanupError != "" {
			cleanupError = bounded(cleanupError)
		}
		converted = append(converted, checkapi.Result{
			Name: result.Name, Command: result.Command, Status: result.Status, ExitCode: result.ExitCode,
			DurationMS: result.Duration.Milliseconds(), OutputExcerpt: excerpt, Truncated: truncated,
			CleanupError: cleanupError,
		})
	}
	return converted
}

func runnerLog(results []checkexec.Result) (string, bool) {
	log := checkapi.LogBuffer{Limit: maximumRunnerLog}
	for _, result := range results {
		if !log.Add("[" + result.Status + "] " + result.Command + "\n" + result.Output + "\n") {
			break
		}
	}
	return log.Result()
}

func bounded(value string) string {
	return checkapi.SummaryText(value, 500)
}

func (runner *Runner) log(format string, arguments ...any) {
	if runner.Logf != nil {
		runner.Logf(format, arguments...)
	}
}
