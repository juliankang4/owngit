package importsync

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"

	"owngit/internal/importfetch"
)

// Stable problem codes. A later HTTP or CLI binding maps them to user-facing
// responses without parsing messages.
const (
	CodeInvalidSource      = "invalid_source"
	CodeNotConfigured      = "not_configured"
	CodeBusy               = "busy"
	CodeRepositoryMissing  = "repository_missing"
	CodeRepositoryTaken    = "repository_taken"
	CodeUnsupportedFormat  = "unsupported_object_format"
	CodeUnsupportedRefs    = "unsupported_refs"
	CodeNetwork            = "network"
	CodeProtocol           = "protocol"
	CodeTooLarge           = "too_large"
	CodeIndexFailed        = "index_failed"
	CodeVerifyFailed       = "verify_failed"
	CodePublishFailed      = "publish_failed"
	CodeLFSRequired        = "git_lfs_required"
	CodeDestinationChanged = "destination_changed"
	CodeUnresolved         = "publication_unresolved"
	CodeCancelled          = "cancelled"
	CodeSuperseded         = "superseded"
	CodeInterrupted        = "interrupted"
	CodeStateUnavailable   = "state_unavailable"
	CodeRuntimeUnavailable = "runtime_unavailable"
	CodeRuntimeUnsafe      = "runtime_unsafe"
	CodeUnsupported        = "unsupported"
	CodeLimit              = "limit"
	CodeStagingUnsafe      = "staging_unsafe"
	// CodeNothingToResolve reports an owner resolution with no unresolved
	// publication intent to accept.
	CodeNothingToResolve = "nothing_to_resolve"
	// CodeInvalidSchedule reports a schedule interval that is not a supported
	// duration. The source is not involved.
	CodeInvalidSchedule = "invalid_schedule"
)

var (
	// ErrNotConfigured reports a repository without an import source.
	ErrNotConfigured = errors.New("import source is not configured")
	// ErrBusy reports an import already running for the repository.
	ErrBusy = errors.New("an import is already running for the repository")
	// ErrCancelled reports a run cancelled before publication completed.
	ErrCancelled = errors.New("import cancelled")
	// ErrSuperseded reports a run whose source changed while it was running.
	ErrSuperseded = errors.New("import source changed during the run")
)

// Problem is one classified import failure. Cause never contains credentials
// because the transport and the state layer are already secret-free.
type Problem struct {
	Code    string
	Message string
	Cause   error

	// runRead marks a state_unavailable failure of a state read made during a
	// run. Only such a read can have been cut by the run's stop.
	runRead bool
}

func (p *Problem) Error() string {
	if p.Cause == nil {
		return fmt.Sprintf("import %s: %s", p.Code, p.Message)
	}
	return fmt.Sprintf("import %s: %s: %v", p.Code, p.Message, p.Cause)
}

func (p *Problem) Unwrap() error { return p.Cause }

func newProblem(code, message string, cause error) *Problem {
	return &Problem{Code: code, Message: message, Cause: cause}
}

// runStateReadProblem reports a failed state read made during a run. Use it
// only for reads, never for writes or bookkeeping, so a genuine state failure
// is never taken for a stop. A read on an uncancelled context cannot return
// the run's context error, so it is never reclassified either.
func runStateReadProblem(message string, cause error) *Problem {
	return &Problem{Code: CodeStateUnavailable, Message: message, Cause: cause, runRead: true}
}

func cancellationProblem(ctx context.Context, message string, cause error) *Problem {
	if errors.Is(context.Cause(ctx), ErrSuperseded) {
		return newProblem(CodeSuperseded, "import authority changed during the run", joinDistinct(cause, ErrSuperseded))
	}
	return newProblem(CodeCancelled, message, joinDistinct(cause, context.Cause(ctx)))
}

// joinDistinct adds reason to cause unless cause already carries it, so a
// stored message does not repeat the same context error.
func joinDistinct(cause, reason error) error {
	if cause == nil || reason == nil || errors.Is(cause, reason) {
		if cause == nil {
			return reason
		}
		return cause
	}
	return errors.Join(cause, reason)
}

// stoppedProblem classifies a failure that happened after the run's own
// context ended: cancellation stays cancelled or superseded, and an expired
// run deadline is a limit. stage names what the run was doing.
func stoppedProblem(ctx context.Context, stage string, cause error) *Problem {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return newProblem(CodeLimit, "import run deadline expired "+stage, cause)
	}
	return cancellationProblem(ctx, "import stopped "+stage, cause)
}

// stoppedStageFailure reports a local Git failure that the run's own
// cancellation or deadline caused as that stop, the same way the transfer
// stage does, instead of as bad content. A state database failure is
// reclassified only when it is a run-context read (runStateReadProblem) whose
// own cause is the run's context error; any other state failure, including a
// bookkeeping write joined with the stop's error, stays state_unavailable.
// A failure that does not carry the context error is unchanged, and
// publication outcomes such as unresolved are never reclassified.
func stoppedStageFailure(ctx context.Context, stage string, err error) error {
	if err == nil || ctx.Err() == nil || !(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return err
	}
	problem := &Problem{Code: CodeUnsupported}
	errors.As(err, &problem)
	switch problem.Code {
	case CodeIndexFailed, CodeVerifyFailed, CodePublishFailed, CodeRepositoryMissing, CodeNetwork, CodeProtocol, CodeUnsupported:
		return stoppedProblem(ctx, "while "+stage, err)
	case CodeStateUnavailable:
		if problem.runRead && errors.Is(problem.Cause, ctx.Err()) {
			return stoppedProblem(ctx, "while "+stage, err)
		}
	}
	return err
}

// problemCode returns the stable code of the first classified problem in the
// error chain, or CodeUnsupported for an unclassified failure.
func problemCode(err error) string {
	var problem *Problem
	if errors.As(err, &problem) {
		return problem.Code
	}
	var fetchError *importfetch.Error
	if errors.As(err, &fetchError) {
		return classifyFetchError(err).Code
	}
	switch {
	case errors.Is(err, context.Canceled):
		return CodeCancelled
	case errors.Is(err, context.DeadlineExceeded):
		return CodeLimit
	}
	return CodeUnsupported
}

// classifyFetchError maps one transport failure to a stable code and a safe
// summary. Messages never include remote response bytes or credentials.
func classifyFetchError(err error) *Problem {
	if err == nil {
		return nil
	}
	var fetchError *importfetch.Error
	if !errors.As(err, &fetchError) {
		switch {
		case errors.Is(err, context.Canceled):
			return newProblem(CodeCancelled, "import was cancelled", err)
		case errors.Is(err, context.DeadlineExceeded):
			return newProblem(CodeLimit, "import deadline expired", err)
		default:
			return newProblem(CodeNetwork, "source request failed", err)
		}
	}
	switch {
	case errors.Is(fetchError, importfetch.ErrInvalidRequest):
		return newProblem(CodeInvalidSource, "source request is invalid (check the URL and credential sizes)", err)
	case errors.Is(fetchError, importfetch.ErrNameResolution):
		return newProblem(CodeNetwork, "source host name could not be resolved", err)
	case errors.Is(fetchError, importfetch.ErrAddressPolicy):
		return newProblem(CodeNetwork, "a resolved source address is forbidden; private-network consent may be required", err)
	case errors.Is(fetchError, importfetch.ErrConnection):
		if message := tlsFailureMessage(err); message != "" {
			return newProblem(CodeNetwork, message, err)
		}
		return newProblem(CodeNetwork, "source connection or response body failed", err)
	case errors.Is(fetchError, importfetch.ErrRedirect):
		return newProblem(CodeProtocol, "source redirect was refused", err)
	case errors.Is(fetchError, importfetch.ErrHTTPStatus):
		return newProblem(CodeNetwork, "source returned an unexpected HTTP status", err)
	case errors.Is(fetchError, importfetch.ErrMediaType), errors.Is(fetchError, importfetch.ErrContentEncoding), errors.Is(fetchError, importfetch.ErrResponseHeaders):
		return newProblem(CodeProtocol, "source returned an unsupported HTTP response", err)
	case errors.Is(fetchError, importfetch.ErrRequestTooLarge), errors.Is(fetchError, importfetch.ErrResponseTooLarge):
		return newProblem(CodeTooLarge, "source exceeds a configured transfer bound", err)
	case errors.Is(fetchError, importfetch.ErrAdvertisement):
		return newProblem(CodeProtocol, "source returned an unsupported Git advertisement", err)
	case errors.Is(fetchError, importfetch.ErrUploadPackProtocol):
		return newProblem(CodeProtocol, "source returned an unsupported upload-pack response", err)
	case errors.Is(fetchError, importfetch.ErrConsumerStoppedEarly):
		return newProblem(CodeIndexFailed, "staging pack indexing did not consume the complete pack", err)
	case errors.Is(fetchError, importfetch.ErrConsumer):
		if cause := localFetchCause(fetchError); cause != nil {
			var problem *Problem
			if errors.As(cause, &problem) {
				return problem
			}
			switch {
			case errors.Is(cause, context.Canceled):
				return newProblem(CodeCancelled, "import was cancelled while staging the pack", err)
			case errors.Is(cause, context.DeadlineExceeded):
				return newProblem(CodeLimit, "staging pack indexing exceeded its deadline", err)
			}
		}
		return newProblem(CodeIndexFailed, "staging pack indexing failed", err)
	default:
		return newProblem(CodeNetwork, "source request failed", err)
	}
}

// tlsFailureMessage names a TLS certificate or handshake failure, which is
// otherwise indistinguishable from a network outage. It returns "" for other
// errors.
func tlsFailureMessage(err error) string {
	var unknownAuthority x509.UnknownAuthorityError
	var verification *tls.CertificateVerificationError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var alert tls.AlertError
	var record tls.RecordHeaderError
	switch {
	case errors.As(err, &hostname):
		return "source TLS certificate does not match the source host name"
	case errors.As(err, &unknownAuthority), errors.As(err, &invalid), errors.As(err, &verification):
		return "source TLS certificate could not be verified; if the source uses a private CA, store that CA with the credentials"
	case errors.As(err, &alert), errors.As(err, &record):
		return "source TLS handshake failed"
	}
	return ""
}

// localFetchCause returns the local cause of a transport error, if one was
// attached. The transport unwraps to the stable kind first and any local cause
// last.
func localFetchCause(fetchError *importfetch.Error) error {
	causes := fetchError.Unwrap()
	if len(causes) < 2 {
		return nil
	}
	return causes[len(causes)-1]
}
