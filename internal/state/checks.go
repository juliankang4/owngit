package state

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Task statuses. A task keeps one identity while its revisions change.
const (
	TaskActive    = "active"
	TaskResolved  = "resolved"
	TaskExhausted = "exhausted"
)

// Check attempt and per-check result statuses. They describe what actually
// happened, never what the user hoped happened. Pending means the attempt was
// registered before execution and has not reported a completion yet.
const (
	AttemptPending     = "pending"
	AttemptPassed      = "passed"
	AttemptFailed      = "failed"
	AttemptError       = "error"
	AttemptCancelled   = "cancelled"
	AttemptIncomplete  = "incomplete"
	AttemptUnavailable = "unavailable"
)

// Worktree states recorded with an attempt. A dirty or unknown worktree means
// the tested commit is not proven to be the bytes that ran.
const (
	WorktreeClean   = "clean"
	WorktreeDirty   = "dirty"
	WorktreeUnknown = "unknown"
)

// Execution context recorded with every attempt. The helper runs in the user's
// own environment, so the scope is inherited and protection stays unknown
// unless it is actually established. A server-linked automatic job supplies
// the executor-derived scope and the job's protection, so an ordinary helper
// can never assert either one.
const (
	ExecutionScopeInherited      = "inherited"
	ExecutionScopeContainer      = "container"
	ExecutionScopeExternalRunner = "external_runner"

	ProtectionUnknown        = "unknown"
	ProtectionHost           = "host"
	ProtectionContainer      = "container"
	ProtectionRunnerReported = "runner_reported"
)

func validExecutionScope(value string) bool {
	switch value {
	case ExecutionScopeInherited, ExecutionScopeContainer, ExecutionScopeExternalRunner:
		return true
	default:
		return false
	}
}

func validProtection(value string) bool {
	switch value {
	case ProtectionUnknown, ProtectionHost, ProtectionContainer, ProtectionRunnerReported:
		return true
	default:
		return false
	}
}

// Raw log availability. A durable attempt record outlives its disposable log.
const (
	CheckLogFound   = "found"
	CheckLogExpired = "expired"
	CheckLogMissing = "missing"
)

const (
	// CorrectionCycleLimit is the number of automatic correction cycles for
	// one task. The budget belongs to the task, so moving the revision does
	// not reset it.
	CorrectionCycleLimit = 3
	// DefaultCheckLogRetentionDays bounds disposable raw logs.
	DefaultCheckLogRetentionDays = 30
	// MaximumCheckDefinitions bounds one versioned configuration.
	MaximumCheckDefinitions = 50
	// MaximumCheckNameBytes and MaximumCheckCommandBytes bound one check
	// definition. A shell command line can carry many arguments.
	MaximumCheckNameBytes    = 100
	MaximumCheckCommandBytes = 24000
	// MaximumCheckLogBytes bounds one uploaded raw log.
	MaximumCheckLogBytes = 256 << 10
	// MaximumCheckExcerptBytes bounds one durable per-check excerpt.
	MaximumCheckExcerptBytes = 8 << 10
	// MaximumCleanupErrorBytes bounds one recorded cleanup failure.
	MaximumCleanupErrorBytes = 500
	// checkLogPruneBatch keeps one expiry transaction within 4 MiB of
	// maximum-size logical log content while draining every due row.
	checkLogPruneBatch = 16
)

// Task is the derived presentation of one task. Only identity, title, and
// server-observed timestamps are stored; everything else follows from the
// attempts and the reserved cycles.
type Task struct {
	ID           string
	RepositoryID string
	Title        string
	Status       string
	// CorrectionCyclesUsed is the number of explicitly reserved rounds.
	CorrectionCyclesUsed int
	// InitialCheckDone reports whether a real verdict has been applied. The
	// first real verdict is the initial check and does not consume a cycle.
	InitialCheckDone bool
	// LastRegisteredSequence is the highest repository-wide sequence issued to
	// this task. It can contain gaps, so it is not an attempt count.
	LastRegisteredSequence int64
	// LastAppliedSequence is the sequence of the newest completed attempt that
	// decides the task state.
	LastAppliedSequence int64
	// PendingAttemptID is the newest pending attempt after the applied one.
	// Older unresolved registrations stay historical.
	PendingAttemptID string
	// LastAppliedAttemptID and LastAppliedFinishedAt identify the newest
	// applied attempt. The finish time is client evidence, not authority.
	LastAppliedAttemptID  string
	LastAppliedFinishedAt time.Time
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// CorrectionCyclesRemaining reports how many automatic correction cycles the
// task still has. It never goes below zero.
func (task Task) CorrectionCyclesRemaining() int {
	remaining := CorrectionCycleLimit - task.CorrectionCyclesUsed
	if remaining < 0 {
		return 0
	}
	return remaining
}

type CheckDefinition struct {
	Name    string `json:"name"`
	Command string `json:"command"`
}

type CheckConfiguration struct {
	RepositoryID string
	Version      int64
	ConfigHash   string
	Checks       []CheckDefinition
	CreatedAt    time.Time
}

type CheckResult struct {
	Position      int
	Name          string
	Command       string
	Status        string
	ExitCode      *int
	DurationMS    int64
	OutputExcerpt string
	Truncated     bool
	// CleanupError reports that the owned process group could not be confirmed
	// released. It makes the result an error while ExitCode stays visible.
	CleanupError string
}

// CheckCycle is one reserved automatic correction round. It is reserved before
// an agent is asked to correct, counted once whether the following check
// succeeds or fails, and reused by retries inside the round.
type CheckCycle struct {
	ID           string
	TaskID       string
	RepositoryID string
	Sequence     int64
	ReservedAt   time.Time
	// ReservedAfterSequence is the repository attempt counter observed inside
	// the reservation transaction. It is the immutable boundary that decides
	// whether a later completion can certify the round.
	ReservedAfterSequence int64
	// AttemptID is the newest attempt registered against this round. It is
	// derived from the attempts that refer to the cycle.
	AttemptID string
}

type CheckAttempt struct {
	ID           string
	TaskID       string
	RepositoryID string
	RevisionOID  string
	// WorktreeState is the immutable observation taken at registration.
	WorktreeState string
	// SubmittedWorktreeState is the observation reported with the completion.
	SubmittedWorktreeState string
	ConfigurationVersion   int64
	Status                 string
	ExitCode               *int
	StartedAt              time.Time
	FinishedAt             time.Time
	DurationMS             int64
	Summary                string
	// Protection and ExecutionScope describe what was actually established.
	Protection     string
	ExecutionScope string
	// CredentialID is the server-authenticated helper credential that
	// submitted the attempt, not a caller-supplied identity.
	CredentialID string
	// JobID links an automatic job. Empty keeps the exact historical helper
	// behavior; a nonempty value means the server derived the execution scope
	// and protection from the job instead of the helper payload.
	JobID string
	// TimeoutMS and OutputLimitBytes are the execution limits that applied.
	TimeoutMS        int64
	OutputLimitBytes int64
	LogID            string
	LogExpiresAt     *time.Time
	LogTruncated     bool
	LogError         string
	CreatedAt        time.Time
	// Sequence is the repository-wide server-issued order. It is the authority
	// for task progression, not the client finish time or the attempt identity.
	Sequence int64
	// CycleID binds an automatic correction round. Empty means the initial
	// check or a manual rerun, which consume no cycle.
	CycleID string
	// RegistrationDigest and CompletionDigest make a retransmit idempotent
	// without touching an accepted log. SubmittedLogDigest, SubmittedTruncated,
	// and SubmittedCancelled describe what the helper submitted, independently
	// of whether publication succeeded.
	RegistrationDigest string
	CompletionDigest   string
	SubmittedLogDigest string
	SubmittedTruncated bool
	SubmittedCancelled bool
	// LogDigest is the digest of the stored raw row, which is the proof of
	// what a reader actually gets.
	LogDigest string
	Checks    []CheckDefinition
	Results   []CheckResult
}

// EffectiveWorktreeState keeps the most pessimistic observation, so a check
// that dirties the tree is never certified as the revision observed before it
// ran.
func (attempt CheckAttempt) EffectiveWorktreeState() string {
	return worseWorktree(attempt.WorktreeState, attempt.SubmittedWorktreeState)
}

// CleanupFailed reports whether any result recorded a cleanup failure.
func (attempt CheckAttempt) CleanupFailed() bool {
	return hasCleanupFailure(attempt.Results)
}

func hasCleanupFailure(results []CheckResult) bool {
	for _, result := range results {
		if result.CleanupError != "" {
			return true
		}
	}
	return false
}

// CheckCompletion is the second half of the two-phase attempt protocol. The
// registration already fixed the identity, revision, and declared checks.
type CheckCompletion struct {
	AttemptID    string
	RepositoryID string
	// TaskID is the task named by the request path. It is validated before any
	// durable effect.
	TaskID        string
	Results       []CheckResult
	Cancelled     bool
	FinishedAt    time.Time
	WorktreeState string
	Log           string
	LogTruncated  bool
}

var (
	// ErrTaskNotFound reports an attempt for a task that does not exist.
	ErrTaskNotFound = errors.New("task does not exist")
	// ErrAttemptNotFound reports a completion for an unregistered attempt, or
	// a completion posted to the wrong task.
	ErrAttemptNotFound = errors.New("check attempt does not exist")
	// ErrAttemptConflict reports a reused attempt identity with different
	// content, or a completion that disagrees with an accepted one.
	ErrAttemptConflict = errors.New("check attempt identity was reused with different content")
	// ErrCycleConflict reports a reused correction cycle identity that belongs
	// to another task or repository.
	ErrCycleConflict = errors.New("correction cycle identity was reused with different content")
	// ErrCycleNotFound reports an attempt bound to an unreserved round.
	ErrCycleNotFound = errors.New("correction cycle does not exist")
	// ErrCorrectionBudgetExhausted reports a reservation after the task used
	// all of its automatic correction cycles.
	ErrCorrectionBudgetExhausted = errors.New("the task has no correction cycle left")
	// ErrResultMismatch reports a completion whose results do not describe the
	// registered checks.
	ErrResultMismatch = errors.New("check results do not match the declared configuration")
)

// querier is satisfied by the pool and by an open transaction, so a read inside
// a transaction never needs a second connection.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (s *Store) CreateTask(ctx context.Context, repositoryID, title string, now time.Time) (Task, error) {
	if repositoryID == "" || now.IsZero() {
		return Task{}, errors.New("invalid task")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Check task"
	}
	if len(title) > 200 || !validText(title, 200) {
		return Task{}, errors.New("invalid task title")
	}
	id, err := RandomID()
	if err != nil {
		return Task{}, err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO tasks(id,repository_id,title,created_at,updated_at) VALUES(?,?,?,?,?)`,
		id, repositoryID, title, now.Unix(), now.Unix()); err != nil {
		return Task{}, err
	}
	return s.task(ctx, s.db, repositoryID, id)
}

func (s *Store) Task(ctx context.Context, repositoryID, id string) (Task, bool, error) {
	task, err := s.task(ctx, s.db, repositoryID, id)
	if errors.Is(err, ErrTaskNotFound) {
		return Task{}, false, nil
	}
	return task, err == nil, err
}

func (s *Store) Tasks(ctx context.Context, repositoryID string) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx, taskProjection+` WHERE t.repository_id=? ORDER BY t.created_at,t.id`, repositoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []Task
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (s *Store) task(ctx context.Context, queryer querier, repositoryID, id string) (Task, error) {
	task, err := scanTask(queryer.QueryRowContext(ctx, taskProjection+` WHERE t.repository_id=? AND t.id=?`, repositoryID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrTaskNotFound
	}
	return task, err
}

// taskProjection derives the whole task presentation in one indexed query. The
// applied attempt is the newest completed attempt, the pending attempt is the
// newest pending attempt after it, and the reservation boundary is the newest
// reserved round.
const taskProjection = `SELECT t.id,t.repository_id,t.title,t.created_at,t.updated_at,
	COALESCE(a.id,''),COALESCE(a.sequence,0),COALESCE(a.finished_at,0),COALESCE(a.status,''),
	COALESCE(p.id,''),
	COALESCE(c.used,0),COALESCE(c.boundary,0),
	COALESCE(r.registered,0),
	COALESCE(i.initial,0)
FROM tasks t
LEFT JOIN check_attempts a ON a.id=(SELECT id FROM check_attempts WHERE task_id=t.id AND status!='pending' ORDER BY sequence DESC LIMIT 1)
LEFT JOIN check_attempts p ON p.id=(SELECT id FROM check_attempts WHERE task_id=t.id AND status='pending' AND sequence>COALESCE(a.sequence,0) ORDER BY sequence DESC LIMIT 1)
LEFT JOIN (SELECT task_id,COUNT(*) AS used,MAX(reserved_after_sequence) AS boundary FROM check_cycles GROUP BY task_id) c ON c.task_id=t.id
LEFT JOIN (SELECT task_id,MAX(sequence) AS registered FROM check_attempts GROUP BY task_id) r ON r.task_id=t.id
LEFT JOIN (SELECT task_id,1 AS initial FROM check_attempts WHERE status IN ('passed','failed','error') GROUP BY task_id) i ON i.task_id=t.id`

func scanTask(scanner rowScanner) (Task, error) {
	var task Task
	var appliedFinishedAt, createdAt, updatedAt int64
	var appliedStatus string
	var boundary int64
	var initial int
	if err := scanner.Scan(&task.ID, &task.RepositoryID, &task.Title, &createdAt, &updatedAt,
		&task.LastAppliedAttemptID, &task.LastAppliedSequence, &appliedFinishedAt, &appliedStatus,
		&task.PendingAttemptID, &task.CorrectionCyclesUsed, &boundary, &task.LastRegisteredSequence, &initial); err != nil {
		return Task{}, err
	}
	task.CreatedAt = unixTime(createdAt)
	task.UpdatedAt = unixTime(updatedAt)
	if appliedFinishedAt != 0 {
		task.LastAppliedFinishedAt = unixNanoTime(appliedFinishedAt)
	}
	task.InitialCheckDone = initial != 0
	task.Status = deriveTaskStatus(task, appliedStatus, boundary)
	return task, nil
}

// deriveTaskStatus follows one deterministic projection. A completed attempt
// after the newest reservation boundary decides the state, a newer pending
// attempt keeps the task active, and otherwise the applied verdict and the used
// budget decide. A pre-reservation attempt finishing later cannot certify a
// reserved round.
func deriveTaskStatus(task Task, appliedStatus string, boundary int64) string {
	if task.LastAppliedAttemptID == "" || task.LastAppliedSequence <= boundary {
		return TaskActive
	}
	if task.PendingAttemptID != "" {
		return TaskActive
	}
	return advanceTask(task, appliedStatus)
}

// advanceTask derives the task status from the newest applied verdict. The
// correction budget is consumed by an explicit reservation, not by a verdict,
// so a successful correction still counts.
func advanceTask(task Task, status string) string {
	switch status {
	case AttemptPassed:
		return TaskResolved
	case AttemptCancelled, AttemptIncomplete, AttemptUnavailable:
		if task.CorrectionCyclesUsed >= CorrectionCycleLimit {
			return TaskExhausted
		}
		return TaskActive
	}
	if task.CorrectionCyclesUsed >= CorrectionCycleLimit {
		return TaskExhausted
	}
	return TaskActive
}

// worseWorktree keeps the most pessimistic observation.
func worseWorktree(before, after string) string {
	if before == WorktreeDirty || after == WorktreeDirty {
		return WorktreeDirty
	}
	if before == WorktreeUnknown || after == WorktreeUnknown {
		return WorktreeUnknown
	}
	return WorktreeClean
}

// RegisterCheckAttempt reserves an immutable attempt identity and a
// repository-wide sequence before execution. A retransmit with the same content
// returns the same registration; different content is rejected.
func (s *Store) RegisterCheckAttempt(ctx context.Context, attempt CheckAttempt) (Task, CheckAttempt, error) {
	if attempt.Protection == "" {
		attempt.Protection = ProtectionUnknown
	}
	if attempt.ExecutionScope == "" {
		attempt.ExecutionScope = ExecutionScopeInherited
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, CheckAttempt{}, err
	}
	defer tx.Rollback()
	existing, exists, err := readAttemptTx(ctx, tx, attempt.ID)
	if err != nil {
		return Task{}, CheckAttempt{}, err
	}
	// A server-linked job owns the tested revision, task, configuration, limits,
	// and execution origin. Terminal state permits only replay of the attempt
	// already bound to that job.
	if attempt.JobID != "" {
		job, jobExists, err := readCheckJobTx(ctx, tx, attempt.RepositoryID, attempt.JobID)
		if err != nil {
			return Task{}, CheckAttempt{}, err
		}
		if !jobExists {
			return Task{}, CheckAttempt{}, ErrCheckJobNotFound
		}
		configuration, configurationExists, err := readCheckConfigurationTx(ctx, tx, job.RepositoryID, job.ConfigurationVersion)
		if err != nil {
			return Task{}, CheckAttempt{}, err
		}
		if !configurationExists {
			return Task{}, CheckAttempt{}, errors.New("the job configuration is missing")
		}
		if err := applyJobOrigin(&attempt, job, configuration, exists); err != nil {
			return Task{}, CheckAttempt{}, err
		}
	}
	if err := validateRegistration(attempt); err != nil {
		return Task{}, CheckAttempt{}, err
	}
	// CreatedAt is the server's observation, not helper payload. An exact
	// retransmit can arrive under a later server clock, so compare it using the
	// immutable time already stored for this identity. Do this before resolving
	// configurations or cycles so a retry cannot create any new durable facts.
	if exists {
		candidate := attempt
		candidate.CreatedAt = existing.CreatedAt
		if existing.RegistrationDigest != registrationDigest(candidate) {
			return Task{}, CheckAttempt{}, ErrAttemptConflict
		}
		task, err := s.task(ctx, tx, existing.RepositoryID, existing.TaskID)
		if err != nil {
			return Task{}, CheckAttempt{}, err
		}
		if err := tx.Commit(); err != nil {
			return Task{}, CheckAttempt{}, err
		}
		return task, existing, nil
	}
	task, err := s.task(ctx, tx, attempt.RepositoryID, attempt.TaskID)
	if err != nil {
		return Task{}, CheckAttempt{}, err
	}
	configurationVersion, _, err := ensureCheckConfiguration(ctx, tx, attempt.RepositoryID, attempt.Checks, attempt.CreatedAt)
	if err != nil {
		return Task{}, CheckAttempt{}, err
	}
	attempt.ConfigurationVersion = configurationVersion
	if attempt.CycleID != "" {
		cycle, exists, err := readCycleTx(ctx, tx, attempt.CycleID)
		if err != nil {
			return Task{}, CheckAttempt{}, err
		}
		if !exists {
			return Task{}, CheckAttempt{}, ErrCycleNotFound
		}
		if cycle.TaskID != attempt.TaskID || cycle.RepositoryID != attempt.RepositoryID {
			return Task{}, CheckAttempt{}, ErrCycleConflict
		}
	}
	attempt.RegistrationDigest = registrationDigest(attempt)
	// The repository counter advances only with a new accepted registration.
	if err := tx.QueryRowContext(ctx, `INSERT INTO repository_attempt_counters(repository_id,attempt_sequence) VALUES(?,1)
		ON CONFLICT(repository_id) DO UPDATE SET attempt_sequence=attempt_sequence+1 RETURNING attempt_sequence`,
		attempt.RepositoryID).Scan(&attempt.Sequence); err != nil {
		return Task{}, CheckAttempt{}, err
	}
	attempt.Status = AttemptPending
	// The finish time is not known before execution, so a registration carries
	// none. The completion supplies it as evidence.
	attempt.FinishedAt = time.Time{}
	attempt.DurationMS = 0
	attempt.Summary = ""
	if _, err := tx.ExecContext(ctx, `INSERT INTO check_attempts(
		id,task_id,repository_id,revision_oid,registration_worktree_state,submitted_worktree_state,configuration_version,status,started_at,finished_at,duration_ms,summary,protection,execution_scope,credential_id,timeout_ms,output_limit_bytes,created_at,sequence,cycle_id,registration_digest,job_id
	) VALUES(?,?,?,?,?,'',?,?,?,0,0,'',?,?,?,?,?,?,?,?,?,?)`,
		attempt.ID, attempt.TaskID, attempt.RepositoryID, attempt.RevisionOID, attempt.WorktreeState, attempt.ConfigurationVersion,
		attempt.Status, attempt.StartedAt.UnixNano(), attempt.Protection, attempt.ExecutionScope, attempt.CredentialID,
		attempt.TimeoutMS, attempt.OutputLimitBytes, attempt.CreatedAt.Unix(), attempt.Sequence, attempt.CycleID, attempt.RegistrationDigest, attempt.JobID); err != nil {
		return Task{}, CheckAttempt{}, err
	}
	// One job binds one attempt. A concurrent different attempt is rejected
	// instead of silently overwriting the linkage.
	if attempt.JobID != "" {
		result, err := tx.ExecContext(ctx, `UPDATE check_jobs SET attempt_id=? WHERE id=? AND repository_id=? AND (attempt_id='' OR attempt_id=?)`,
			attempt.ID, attempt.JobID, attempt.RepositoryID, attempt.ID)
		if err != nil {
			return Task{}, CheckAttempt{}, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return Task{}, CheckAttempt{}, err
		}
		if affected != 1 {
			return Task{}, CheckAttempt{}, ErrCheckJobAttemptBound
		}
	}
	if err := tx.Commit(); err != nil {
		return Task{}, CheckAttempt{}, err
	}
	task, err = s.task(ctx, s.db, attempt.RepositoryID, attempt.TaskID)
	if err != nil {
		return Task{}, CheckAttempt{}, err
	}
	return task, attempt, nil
}

// checkCompletionOps isolates the two SQLite boundaries whose failure handling
// needs deterministic tests. Production uses the direct transaction methods.
type checkCompletionOps struct {
	insertRawLog func(context.Context, *sql.Tx, string, []byte, int64) error
	commit       func(*sql.Tx) error
}

func defaultCheckCompletionOps() checkCompletionOps {
	return checkCompletionOps{
		insertRawLog: func(ctx context.Context, tx *sql.Tx, attemptID string, content []byte, expiresAt int64) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO check_raw_logs(attempt_id,content,expires_at) VALUES(?,?,?)`, attemptID, content, expiresAt)
			return err
		},
		commit: func(tx *sql.Tx) error { return tx.Commit() },
	}
}

type sqliteCodeError interface {
	Code() int
}

type checkLogStorageError struct {
	cause error
}

func (err *checkLogStorageError) Error() string {
	return fmt.Sprintf("store raw check log: %v", err.cause)
}
func (err *checkLogStorageError) Unwrap() error { return err.cause }

func isSQLiteStorageFailure(err error) bool {
	var sqliteErr sqliteCodeError
	if !errors.As(err, &sqliteErr) {
		return false
	}
	switch sqliteErr.Code() & 0xff {
	case 10, 13: // SQLITE_IOERR and SQLITE_FULL, including extended IOERR codes.
		return true
	default:
		return false
	}
}

func rawLogTransactionError(err error) error {
	if isSQLiteStorageFailure(err) {
		return &checkLogStorageError{cause: err}
	}
	return err
}

// CompleteCheckAttempt stores one completion and its disposable raw log. The
// normal path commits the BLOB and durable facts together. If SQLite cannot
// store that transaction, one fresh transaction revalidates the request and
// records the durable facts without resubmitting the BLOB.
func (s *Store) CompleteCheckAttempt(ctx context.Context, completion CheckCompletion, now time.Time) (Task, CheckAttempt, error) {
	return s.completeCheckAttempt(ctx, completion, nil, now, defaultCheckCompletionOps())
}

// CompleteCheckJobAttempt completes an automatic attempt under the exact claim
// authority that started it. This boundary is intentionally separate from the
// ordinary helper completion method.
func (s *Store) CompleteCheckJobAttempt(ctx context.Context, completion CheckCompletion, authority CheckJobCompletionAuthority, now time.Time) (Task, CheckAttempt, error) {
	return s.completeCheckAttempt(ctx, completion, &authority, now, defaultCheckCompletionOps())
}

func (s *Store) completeCheckAttempt(ctx context.Context, completion CheckCompletion, authority *CheckJobCompletionAuthority, now time.Time, ops checkCompletionOps) (Task, CheckAttempt, error) {
	task, attempt, err := s.completeCheckAttemptTx(ctx, completion, authority, now, true, "", ops)
	var storageErr *checkLogStorageError
	if !errors.As(err, &storageErr) {
		return task, attempt, err
	}

	// The failed transaction has returned and its deferred Rollback has retired
	// it. Do not issue another statement through that Tx: SQLITE_FULL and
	// SQLITE_IOERR may already have rolled it back automatically.
	task, attempt, fallbackErr := s.completeCheckAttemptTx(ctx, completion, authority, now, false, checkLogFailureMessage(), ops)
	if fallbackErr == nil || errors.Is(fallbackErr, ErrAttemptConflict) || errors.Is(fallbackErr, ErrAttemptNotFound) {
		return task, attempt, fallbackErr
	}
	return Task{}, CheckAttempt{}, errors.Join(storageErr, fmt.Errorf("store completion metadata without raw log: %w", fallbackErr))
}

func (s *Store) completeCheckAttemptTx(ctx context.Context, completion CheckCompletion, authority *CheckJobCompletionAuthority, now time.Time, storeRawLog bool, logFailure string, ops checkCompletionOps) (Task, CheckAttempt, error) {
	if err := validateCompletion(completion); err != nil {
		return Task{}, CheckAttempt{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, CheckAttempt{}, err
	}
	defer tx.Rollback()

	// Identity, configuration, result ordering, digests, and replay are checked
	// inside every transaction, including the metadata-only fallback.
	registered, exists, err := readAttemptTx(ctx, tx, completion.AttemptID)
	if err != nil {
		return Task{}, CheckAttempt{}, err
	}
	if !exists || registered.RepositoryID != completion.RepositoryID || registered.TaskID != completion.TaskID {
		return Task{}, CheckAttempt{}, ErrAttemptNotFound
	}
	if registered.JobID != "" {
		if authority == nil {
			return Task{}, CheckAttempt{}, ErrCheckJobCompletionRequired
		}
		if err := authorizeCheckJobCompletionTx(ctx, tx, registered, *authority); err != nil {
			return Task{}, CheckAttempt{}, err
		}
	} else if authority != nil {
		return Task{}, CheckAttempt{}, ErrCheckJobNotFound
	}
	configuration, hasConfiguration, err := readCheckConfigurationTx(ctx, tx, registered.RepositoryID, registered.ConfigurationVersion)
	if err != nil {
		return Task{}, CheckAttempt{}, err
	}
	if !hasConfiguration {
		return Task{}, CheckAttempt{}, errors.New("the registered configuration is missing")
	}
	registered.Checks = configuration.Checks
	if err := matchResults(registered.Checks, completion.Results); err != nil {
		return Task{}, CheckAttempt{}, err
	}
	submitted := registered
	submitted.SubmittedWorktreeState = completion.WorktreeState
	submitted.SubmittedCancelled = completion.Cancelled
	submitted.SubmittedTruncated = completion.LogTruncated
	submitted.SubmittedLogDigest = digestFields(completion.Log)
	submitted.FinishedAt = completion.FinishedAt.UTC()
	digest := completionDigest(submitted, completion.Results)
	if registered.Status != AttemptPending {
		if registered.CompletionDigest != digest {
			return Task{}, registered, ErrAttemptConflict
		}
		task, err := s.task(ctx, tx, registered.RepositoryID, registered.TaskID)
		if err != nil {
			return Task{}, registered, err
		}
		if commitErr := ops.commit(tx); commitErr != nil {
			_ = tx.Rollback()
			reconciledTask, reconciled, resolved, reconcileErr := s.reconcileCheckCompletion(ctx, registered.RepositoryID, registered.ID, digest)
			if resolved {
				return reconciledTask, reconciled, reconcileErr
			}
			if reconcileErr != nil {
				return Task{}, CheckAttempt{}, errors.Join(commitErr, fmt.Errorf("reconcile completion commit: %w", reconcileErr))
			}
			return Task{}, CheckAttempt{}, commitErr
		}
		return task, registered, nil
	}

	logID, logDigest, logError := "", "", logFailure
	var logExpiresAt *time.Time
	logTruncated := false
	rawStored := false
	if storeRawLog {
		retentionDays, err := readCheckLogRetentionTx(ctx, tx)
		if err != nil {
			return Task{}, CheckAttempt{}, err
		}
		expiresAt := now.UTC().Add(retention(retentionDays))
		if err := ops.insertRawLog(ctx, tx, registered.ID, []byte(completion.Log), expiresAt.Unix()); err != nil {
			return Task{}, CheckAttempt{}, rawLogTransactionError(err)
		}
		rawStored = true
		logID = registered.ID
		logDigest = submitted.SubmittedLogDigest
		logExpiresAt = &expiresAt
		logTruncated = completion.LogTruncated
		logError = ""
	}

	status := AggregateAttemptStatus(completion.Results, completion.Cancelled)
	worktree := worseWorktree(registered.WorktreeState, completion.WorktreeState)
	finished := completion.FinishedAt.UTC()
	duration := finished.Sub(registered.StartedAt).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	summary := AttemptSummary(completion.Results, worktree, status)
	if _, err := tx.ExecContext(ctx, `UPDATE check_attempts SET status=?,exit_code=?,finished_at=?,duration_ms=?,summary=?,submitted_worktree_state=?,log_id=?,log_expires_at=?,log_truncated=?,log_error=?,completion_digest=?,submitted_log_digest=?,submitted_truncated=?,submitted_cancelled=?,log_digest=? WHERE id=?`,
		status, nullableInt(aggregateExitCode(completion.Results)), finished.UnixNano(), duration, summary, completion.WorktreeState,
		logID, nullableTime(logExpiresAt), boolInt(logTruncated), logError, digest, submitted.SubmittedLogDigest,
		boolInt(completion.LogTruncated), boolInt(completion.Cancelled), logDigest, registered.ID); err != nil {
		if rawStored {
			return Task{}, CheckAttempt{}, rawLogTransactionError(err)
		}
		return Task{}, CheckAttempt{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM check_results WHERE attempt_id=?`, registered.ID); err != nil {
		if rawStored {
			return Task{}, CheckAttempt{}, rawLogTransactionError(err)
		}
		return Task{}, CheckAttempt{}, err
	}
	for position, result := range completion.Results {
		if _, err := tx.ExecContext(ctx, `INSERT INTO check_results(attempt_id,position,name,command,status,exit_code,duration_ms,output_excerpt,truncated,cleanup_error) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			registered.ID, position, result.Name, result.Command, result.Status, nullableInt(result.ExitCode), result.DurationMS, result.OutputExcerpt, boolInt(result.Truncated), result.CleanupError); err != nil {
			if rawStored {
				return Task{}, CheckAttempt{}, rawLogTransactionError(err)
			}
			return Task{}, CheckAttempt{}, err
		}
	}
	// A completion of a server-linked automatic attempt finalizes its job in
	// the same transaction, so the job can never advance without the evidence.
	if registered.JobID != "" {
		if err := finalizeCheckJobTx(ctx, tx, registered, status, finished, summary, completion.Cancelled); err != nil {
			if rawStored {
				return Task{}, CheckAttempt{}, rawLogTransactionError(err)
			}
			return Task{}, CheckAttempt{}, err
		}
	}
	if commitErr := ops.commit(tx); commitErr != nil {
		_ = tx.Rollback()
		reconciledTask, reconciled, resolved, reconcileErr := s.reconcileCheckCompletion(ctx, registered.RepositoryID, registered.ID, digest)
		if resolved {
			return reconciledTask, reconciled, reconcileErr
		}
		if reconcileErr != nil {
			commitErr = errors.Join(commitErr, fmt.Errorf("reconcile completion commit: %w", reconcileErr))
		}
		if rawStored {
			return Task{}, CheckAttempt{}, rawLogTransactionError(commitErr)
		}
		return Task{}, CheckAttempt{}, commitErr
	}
	stored, _, err := s.CheckAttemptByID(ctx, registered.RepositoryID, registered.ID)
	if err != nil {
		return Task{}, CheckAttempt{}, err
	}
	task, err := s.task(ctx, s.db, registered.RepositoryID, registered.TaskID)
	if err != nil {
		return Task{}, CheckAttempt{}, err
	}
	return task, stored, nil
}

func (s *Store) reconcileCheckCompletion(ctx context.Context, repositoryID, attemptID, digest string) (Task, CheckAttempt, bool, error) {
	attempt, exists, err := s.CheckAttemptByID(ctx, repositoryID, attemptID)
	if err != nil {
		return Task{}, CheckAttempt{}, false, err
	}
	if !exists || attempt.Status == AttemptPending {
		return Task{}, CheckAttempt{}, false, nil
	}
	if attempt.CompletionDigest != digest {
		return Task{}, attempt, true, ErrAttemptConflict
	}
	task, err := s.task(ctx, s.db, repositoryID, attempt.TaskID)
	if err != nil {
		return Task{}, attempt, true, err
	}
	return task, attempt, true, nil
}

// ReserveCorrectionCycle reserves one automatic correction round before an
// agent is asked to correct. The round is counted once, whether the following
// check succeeds or fails, and a retransmit with the same identity returns the
// same round.
func (s *Store) ReserveCorrectionCycle(ctx context.Context, repositoryID, taskID, cycleID string, now time.Time) (Task, CheckCycle, error) {
	if !validAttemptID(cycleID) || repositoryID == "" || taskID == "" || now.IsZero() {
		return Task{}, CheckCycle{}, errors.New("invalid correction cycle")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, CheckCycle{}, err
	}
	defer tx.Rollback()
	task, err := s.task(ctx, tx, repositoryID, taskID)
	if err != nil {
		return Task{}, CheckCycle{}, err
	}
	existing, exists, err := readCycleTx(ctx, tx, cycleID)
	if err != nil {
		return Task{}, CheckCycle{}, err
	}
	if exists {
		if existing.TaskID != taskID || existing.RepositoryID != repositoryID {
			return Task{}, CheckCycle{}, ErrCycleConflict
		}
		if err := tx.Commit(); err != nil {
			return Task{}, CheckCycle{}, err
		}
		return task, existing, nil
	}
	if task.CorrectionCyclesUsed >= CorrectionCycleLimit {
		return Task{}, CheckCycle{}, ErrCorrectionBudgetExhausted
	}
	// The boundary is the repository counter observed here. It does not
	// advance the counter, so a later registration still gets a higher
	// sequence than every pre-reservation attempt.
	var boundary int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT attempt_sequence FROM repository_attempt_counters WHERE repository_id=?),0)`, repositoryID).Scan(&boundary); err != nil {
		return Task{}, CheckCycle{}, err
	}
	cycle := CheckCycle{
		ID: cycleID, TaskID: taskID, RepositoryID: repositoryID,
		Sequence: int64(task.CorrectionCyclesUsed) + 1, ReservedAt: now, ReservedAfterSequence: boundary,
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO check_cycles(id,task_id,repository_id,sequence,reserved_at,reserved_after_sequence) VALUES(?,?,?,?,?,?)`,
		cycle.ID, cycle.TaskID, cycle.RepositoryID, cycle.Sequence, cycle.ReservedAt.Unix(), cycle.ReservedAfterSequence); err != nil {
		return Task{}, CheckCycle{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET updated_at=? WHERE repository_id=? AND id=?`, now.Unix(), repositoryID, taskID); err != nil {
		return Task{}, CheckCycle{}, err
	}
	if err := tx.Commit(); err != nil {
		return Task{}, CheckCycle{}, err
	}
	task, err = s.task(ctx, s.db, repositoryID, taskID)
	if err != nil {
		return Task{}, CheckCycle{}, err
	}
	return task, cycle, nil
}

func (s *Store) CheckCycles(ctx context.Context, repositoryID, taskID string) ([]CheckCycle, error) {
	rows, err := s.db.QueryContext(ctx, cycleSelect+` WHERE c.repository_id=? AND c.task_id=? ORDER BY c.sequence`, repositoryID, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cycles []CheckCycle
	for rows.Next() {
		cycle, err := scanCycle(rows)
		if err != nil {
			return nil, err
		}
		cycles = append(cycles, cycle)
	}
	return cycles, rows.Err()
}

// cycleSelect derives the linked attempt from the attempts that refer to the
// cycle, so no separate pointer can drift.
const cycleSelect = `SELECT c.id,c.task_id,c.repository_id,c.sequence,c.reserved_at,c.reserved_after_sequence,
	COALESCE((SELECT id FROM check_attempts WHERE cycle_id=c.id ORDER BY sequence DESC LIMIT 1),'') FROM check_cycles c`

func readCycleTx(ctx context.Context, tx *sql.Tx, id string) (CheckCycle, bool, error) {
	cycle, err := scanCycle(tx.QueryRowContext(ctx, cycleSelect+` WHERE c.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return CheckCycle{}, false, nil
	}
	return cycle, err == nil, err
}

func scanCycle(scanner rowScanner) (CheckCycle, error) {
	var cycle CheckCycle
	var reservedAt int64
	if err := scanner.Scan(&cycle.ID, &cycle.TaskID, &cycle.RepositoryID, &cycle.Sequence, &reservedAt, &cycle.ReservedAfterSequence, &cycle.AttemptID); err != nil {
		return CheckCycle{}, err
	}
	cycle.ReservedAt = unixTime(reservedAt)
	return cycle, nil
}

func ensureCheckConfiguration(ctx context.Context, tx *sql.Tx, repositoryID string, checks []CheckDefinition, now time.Time) (int64, string, error) {
	encoded, err := json.Marshal(checks)
	if err != nil {
		return 0, "", err
	}
	hash := sha256.Sum256(encoded)
	configHash := hex.EncodeToString(hash[:])
	var version int64
	err = tx.QueryRowContext(ctx, `SELECT version FROM check_configurations WHERE repository_id=? AND config_hash=?`, repositoryID, configHash).Scan(&version)
	if err == nil {
		return version, configHash, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, "", err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0)+1 FROM check_configurations WHERE repository_id=?`, repositoryID).Scan(&version); err != nil {
		return 0, "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO check_configurations(repository_id,version,config_hash,checks_json,created_at) VALUES(?,?,?,?,?)`,
		repositoryID, version, configHash, string(encoded), now.Unix()); err != nil {
		return 0, "", err
	}
	return version, configHash, nil
}

func (s *Store) LatestCheckConfiguration(ctx context.Context, repositoryID string) (CheckConfiguration, bool, error) {
	return s.checkConfigurationQuery(ctx, s.db, ` WHERE repository_id=? ORDER BY version DESC LIMIT 1`, repositoryID)
}

// CheckConfiguration reads one exact recorded configuration version.
func (s *Store) CheckConfiguration(ctx context.Context, repositoryID string, version int64) (CheckConfiguration, bool, error) {
	return s.checkConfiguration(ctx, repositoryID, version)
}

func (s *Store) checkConfiguration(ctx context.Context, repositoryID string, version int64) (CheckConfiguration, bool, error) {
	return s.checkConfigurationQuery(ctx, s.db, ` WHERE repository_id=? AND version=?`, repositoryID, version)
}

func readCheckConfigurationTx(ctx context.Context, tx *sql.Tx, repositoryID string, version int64) (CheckConfiguration, bool, error) {
	configuration, err := scanCheckConfiguration(tx.QueryRowContext(ctx, checkConfigurationSelect+` WHERE repository_id=? AND version=?`, repositoryID, version))
	if errors.Is(err, sql.ErrNoRows) {
		return CheckConfiguration{}, false, nil
	}
	return configuration, err == nil, err
}

func (s *Store) checkConfigurationQuery(ctx context.Context, queryer querier, suffix string, arguments ...any) (CheckConfiguration, bool, error) {
	configuration, err := scanCheckConfiguration(queryer.QueryRowContext(ctx, checkConfigurationSelect+suffix, arguments...))
	if errors.Is(err, sql.ErrNoRows) {
		return CheckConfiguration{}, false, nil
	}
	return configuration, err == nil, err
}

const checkConfigurationSelect = `SELECT repository_id,version,config_hash,checks_json,created_at FROM check_configurations`

func scanCheckConfiguration(scanner rowScanner) (CheckConfiguration, error) {
	var configuration CheckConfiguration
	var encoded string
	var createdAt int64
	if err := scanner.Scan(&configuration.RepositoryID, &configuration.Version, &configuration.ConfigHash, &encoded, &createdAt); err != nil {
		return CheckConfiguration{}, err
	}
	if err := json.Unmarshal([]byte(encoded), &configuration.Checks); err != nil {
		return CheckConfiguration{}, err
	}
	configuration.CreatedAt = unixTime(createdAt)
	return configuration, nil
}

func (s *Store) LatestCheckAttempt(ctx context.Context, repositoryID string) (CheckAttempt, bool, error) {
	return s.latestCheckAttempt(ctx, attemptSelect+` WHERE repository_id=? ORDER BY sequence DESC LIMIT 1`, repositoryID)
}

func (s *Store) LatestCheckAttemptForRevision(ctx context.Context, repositoryID, revisionOID string) (CheckAttempt, bool, error) {
	return s.latestCheckAttempt(ctx, attemptSelect+` WHERE repository_id=? AND revision_oid=? ORDER BY sequence DESC LIMIT 1`, repositoryID, revisionOID)
}

func (s *Store) LatestCheckAttemptForTask(ctx context.Context, repositoryID, taskID string) (CheckAttempt, bool, error) {
	return s.latestCheckAttempt(ctx, attemptSelect+` WHERE repository_id=? AND task_id=? ORDER BY sequence DESC LIMIT 1`, repositoryID, taskID)
}

func (s *Store) latestCheckAttempt(ctx context.Context, query string, arguments ...any) (CheckAttempt, bool, error) {
	attempt, err := scanCheckAttempt(s.db.QueryRowContext(ctx, query, arguments...))
	if errors.Is(err, sql.ErrNoRows) {
		return CheckAttempt{}, false, nil
	}
	if err != nil {
		return CheckAttempt{}, false, err
	}
	if err := loadCheckResults(ctx, s.db, &attempt); err != nil {
		return CheckAttempt{}, false, err
	}
	return attempt, true, nil
}

func (s *Store) CheckAttemptByID(ctx context.Context, repositoryID, id string) (CheckAttempt, bool, error) {
	attempt, exists, err := s.checkAttemptByID(ctx, id)
	if err != nil || !exists {
		return CheckAttempt{}, exists, err
	}
	if attempt.RepositoryID != repositoryID {
		return CheckAttempt{}, false, nil
	}
	return attempt, true, nil
}

func (s *Store) checkAttemptByID(ctx context.Context, id string) (CheckAttempt, bool, error) {
	attempt, err := scanCheckAttempt(s.db.QueryRowContext(ctx, attemptSelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return CheckAttempt{}, false, nil
	}
	if err != nil {
		return CheckAttempt{}, false, err
	}
	if err := loadCheckResults(ctx, s.db, &attempt); err != nil {
		return CheckAttempt{}, false, err
	}
	return attempt, true, nil
}

func (s *Store) CheckAttempts(ctx context.Context, repositoryID string) ([]CheckAttempt, error) {
	rows, err := s.db.QueryContext(ctx, attemptSelect+` WHERE repository_id=? ORDER BY sequence`, repositoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var attempts []CheckAttempt
	for rows.Next() {
		attempt, err := scanCheckAttempt(rows)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range attempts {
		if err := loadCheckResults(ctx, s.db, &attempts[index]); err != nil {
			return nil, err
		}
	}
	return attempts, nil
}

const attemptSelect = `SELECT id,task_id,repository_id,revision_oid,registration_worktree_state,submitted_worktree_state,configuration_version,status,exit_code,started_at,finished_at,duration_ms,summary,protection,execution_scope,credential_id,timeout_ms,output_limit_bytes,log_id,log_expires_at,log_truncated,log_error,created_at,sequence,cycle_id,registration_digest,completion_digest,submitted_log_digest,submitted_truncated,submitted_cancelled,log_digest,job_id FROM check_attempts`

func scanCheckAttempt(scanner rowScanner) (CheckAttempt, error) {
	var attempt CheckAttempt
	var exitCode, logExpiresAt sql.NullInt64
	var startedAt, finishedAt, createdAt int64
	var logTruncated, submittedTruncated, submittedCancelled int
	if err := scanner.Scan(&attempt.ID, &attempt.TaskID, &attempt.RepositoryID, &attempt.RevisionOID, &attempt.WorktreeState,
		&attempt.SubmittedWorktreeState, &attempt.ConfigurationVersion, &attempt.Status, &exitCode, &startedAt, &finishedAt, &attempt.DurationMS,
		&attempt.Summary, &attempt.Protection, &attempt.ExecutionScope, &attempt.CredentialID, &attempt.TimeoutMS,
		&attempt.OutputLimitBytes, &attempt.LogID, &logExpiresAt, &logTruncated, &attempt.LogError, &createdAt,
		&attempt.Sequence, &attempt.CycleID, &attempt.RegistrationDigest, &attempt.CompletionDigest,
		&attempt.SubmittedLogDigest, &submittedTruncated, &submittedCancelled, &attempt.LogDigest, &attempt.JobID); err != nil {
		return CheckAttempt{}, err
	}
	if exitCode.Valid {
		value := int(exitCode.Int64)
		attempt.ExitCode = &value
	}
	if logExpiresAt.Valid {
		value := unixTime(logExpiresAt.Int64)
		attempt.LogExpiresAt = &value
	}
	attempt.StartedAt = unixNanoTime(startedAt)
	if finishedAt != 0 {
		attempt.FinishedAt = unixNanoTime(finishedAt)
	}
	attempt.LogTruncated = logTruncated != 0
	attempt.SubmittedTruncated = submittedTruncated != 0
	attempt.SubmittedCancelled = submittedCancelled != 0
	attempt.CreatedAt = unixTime(createdAt)
	return attempt, nil
}

func loadCheckResults(ctx context.Context, queryer querier, attempt *CheckAttempt) error {
	rows, err := queryer.QueryContext(ctx, `SELECT position,name,command,status,exit_code,duration_ms,output_excerpt,truncated,cleanup_error FROM check_results WHERE attempt_id=? ORDER BY position`, attempt.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var result CheckResult
		var exitCode sql.NullInt64
		var truncated int
		if err := rows.Scan(&result.Position, &result.Name, &result.Command, &result.Status, &exitCode, &result.DurationMS, &result.OutputExcerpt, &truncated, &result.CleanupError); err != nil {
			return err
		}
		if exitCode.Valid {
			value := int(exitCode.Int64)
			result.ExitCode = &value
		}
		result.Truncated = truncated != 0
		attempt.Results = append(attempt.Results, result)
	}
	return rows.Err()
}

func readAttemptTx(ctx context.Context, tx *sql.Tx, id string) (CheckAttempt, bool, error) {
	attempt, err := scanCheckAttempt(tx.QueryRowContext(ctx, attemptSelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return CheckAttempt{}, false, nil
	}
	if err != nil {
		return CheckAttempt{}, false, err
	}
	if err := loadCheckResults(ctx, tx, &attempt); err != nil {
		return CheckAttempt{}, false, err
	}
	return attempt, true, nil
}

func validateRegistration(attempt CheckAttempt) error {
	if !validAttemptID(attempt.ID) || attempt.TaskID == "" || attempt.RepositoryID == "" || !validObjectID(attempt.RevisionOID) {
		return errors.New("invalid check attempt identity")
	}
	switch attempt.WorktreeState {
	case WorktreeClean, WorktreeDirty, WorktreeUnknown:
	default:
		return errors.New("invalid check attempt worktree state")
	}
	if attempt.Status != "" && attempt.Status != AttemptPending && !validAttemptStatus(attempt.Status) {
		return errors.New("invalid check attempt status")
	}
	if attempt.StartedAt.IsZero() || attempt.CreatedAt.IsZero() {
		return errors.New("invalid check attempt time")
	}
	if len(attempt.CredentialID) > 100 || attempt.TimeoutMS < 0 || attempt.OutputLimitBytes < 0 {
		return errors.New("invalid check attempt execution limits")
	}
	if attempt.JobID != "" && !validAttemptID(attempt.JobID) {
		return errors.New("invalid check attempt job identity")
	}
	if !validExecutionScope(attempt.ExecutionScope) || !validProtection(attempt.Protection) {
		return errors.New("invalid check attempt execution context")
	}
	if attempt.CycleID != "" && !validAttemptID(attempt.CycleID) {
		return errors.New("invalid correction cycle identity")
	}
	if len(attempt.Checks) > MaximumCheckDefinitions {
		return errors.New("invalid check configuration count")
	}
	for _, check := range attempt.Checks {
		if !validText(check.Name, MaximumCheckNameBytes) || !validText(check.Command, MaximumCheckCommandBytes) {
			return errors.New("invalid check definition")
		}
	}
	return nil
}

func validateCompletion(completion CheckCompletion) error {
	if !validAttemptID(completion.AttemptID) || completion.RepositoryID == "" || completion.TaskID == "" {
		return errors.New("invalid check completion identity")
	}
	if completion.FinishedAt.IsZero() {
		return errors.New("invalid check completion time")
	}
	switch completion.WorktreeState {
	case WorktreeClean, WorktreeDirty, WorktreeUnknown:
	default:
		return errors.New("invalid check completion worktree state")
	}
	if len(completion.Log) > MaximumCheckLogBytes {
		return errors.New("check log is too large")
	}
	if len(completion.Results) > MaximumCheckDefinitions {
		return errors.New("invalid check result count")
	}
	for _, result := range completion.Results {
		if !validText(result.Name, MaximumCheckNameBytes) || !validText(result.Command, MaximumCheckCommandBytes) || !validAttemptStatus(result.Status) || result.DurationMS < 0 {
			return errors.New("invalid check result")
		}
		if len(result.OutputExcerpt) > MaximumCheckExcerptBytes {
			return errors.New("check result excerpt is too large")
		}
		if len(result.CleanupError) > MaximumCleanupErrorBytes {
			return errors.New("check result cleanup error is too large")
		}
	}
	return nil
}

// matchResults requires the results to describe the registered checks in
// order, so a result cannot be attributed to a check that never ran.
func matchResults(checks []CheckDefinition, results []CheckResult) error {
	if len(checks) != len(results) {
		return ErrResultMismatch
	}
	for index, check := range checks {
		if check.Name != results[index].Name || check.Command != results[index].Command {
			return ErrResultMismatch
		}
	}
	return nil
}

// validAttemptID accepts the 32 lowercase hex characters produced by RandomID.
func validAttemptID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func validAttemptStatus(status string) bool {
	switch status {
	case AttemptPassed, AttemptFailed, AttemptError, AttemptCancelled, AttemptIncomplete, AttemptUnavailable:
		return true
	default:
		return false
	}
}

// AggregateAttemptStatus derives the attempt status from its results. The
// server recomputes it instead of trusting a helper-supplied aggregate. A
// cleanup failure is an execution error, so it outranks cancellation instead of
// being masked by it.
func AggregateAttemptStatus(results []CheckResult, cancelled bool) string {
	if hasCleanupFailure(results) {
		return AttemptError
	}
	// An empty configured set is honest unavailable evidence, never passed.
	status := AttemptUnavailable
	if len(results) != 0 {
		status = AttemptPassed
	}
	rank := map[string]int{AttemptPassed: 5, AttemptIncomplete: 4, AttemptUnavailable: 3, AttemptFailed: 2, AttemptError: 1, AttemptCancelled: 0}
	worst := rank[status]
	for _, result := range results {
		if value, ok := rank[result.Status]; ok && value < worst {
			worst = value
			status = result.Status
		}
	}
	if status == AttemptError {
		return AttemptError
	}
	if cancelled {
		return AttemptCancelled
	}
	return status
}

func effectiveCheckStatus(result CheckResult) string {
	if result.CleanupError != "" {
		return AttemptError
	}
	return result.Status
}

// AttemptSummary describes the outcome in one durable line.
func AttemptSummary(results []CheckResult, worktree, status string) string {
	counts := make(map[string]int)
	for _, result := range results {
		counts[effectiveCheckStatus(result)]++
	}
	parts := make([]string, 0, 4)
	for _, candidate := range []string{AttemptFailed, AttemptError, AttemptUnavailable, AttemptIncomplete, AttemptCancelled, AttemptPassed} {
		if counts[candidate] != 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[candidate], candidate))
		}
	}
	summary := fmt.Sprintf("%d checks", len(results))
	if len(parts) != 0 {
		summary += ": " + strings.Join(parts, ", ")
	}
	if worktree == WorktreeDirty {
		summary += "; worktree dirty"
	} else if worktree == WorktreeUnknown {
		summary += "; worktree state unknown"
	}
	if status == AttemptCancelled {
		summary += "; cancelled"
	}
	if len(summary) > 500 {
		summary = summary[:500]
	}
	return summary
}

func aggregateExitCode(results []CheckResult) *int {
	for _, result := range results {
		if result.ExitCode != nil && result.Status != AttemptPassed {
			return result.ExitCode
		}
	}
	for _, result := range results {
		if result.ExitCode != nil {
			return result.ExitCode
		}
	}
	return nil
}

// registrationDigest covers the immutable identity of a registered attempt. It
// is recomputable from the persisted attempt row and its configuration.
func registrationDigest(attempt CheckAttempt) string {
	fields := []string{
		attempt.ID, attempt.TaskID, attempt.RepositoryID, attempt.RevisionOID, attempt.WorktreeState,
		attempt.CycleID, strconv.FormatInt(attempt.TimeoutMS, 10), strconv.FormatInt(attempt.OutputLimitBytes, 10),
		strconv.FormatInt(attempt.StartedAt.UnixNano(), 10), strconv.FormatInt(attempt.CreatedAt.Unix(), 10),
		attempt.Protection, attempt.ExecutionScope, attempt.CredentialID,
	}
	// The job link is tagged instead of always appended, so every historical
	// helper attempt keeps its exact recorded digest.
	if attempt.JobID != "" {
		fields = append(fields, "job:"+attempt.JobID)
	}
	for _, check := range attempt.Checks {
		fields = append(fields, check.Name, check.Command)
	}
	return digestFields(fields...)
}

// completionDigest covers the submitted completion facts and the complete
// ordered results, so a changed payload is detected without touching an
// accepted log.
func completionDigest(attempt CheckAttempt, results []CheckResult) string {
	fields := []string{
		attempt.ID, attempt.SubmittedWorktreeState, strconv.FormatBool(attempt.SubmittedCancelled),
		strconv.FormatInt(attempt.FinishedAt.UnixNano(), 10), strconv.FormatBool(attempt.SubmittedTruncated), attempt.SubmittedLogDigest,
	}
	for _, result := range results {
		fields = append(fields, result.Name, result.Command, result.Status, exitCodeField(result.ExitCode),
			strconv.FormatInt(result.DurationMS, 10), result.OutputExcerpt, strconv.FormatBool(result.Truncated), result.CleanupError)
	}
	return digestFields(fields...)
}

// RegistrationDigest and CompletionDigest are the canonical attempt digests.
// They are exported so a portable manifest can be validated against the same
// facts that produced the stored values.
func RegistrationDigest(attempt CheckAttempt) string { return registrationDigest(attempt) }

// CompletionDigest covers the submitted completion facts and ordered results.
func CompletionDigest(attempt CheckAttempt, results []CheckResult) string {
	return completionDigest(attempt, results)
}

func digestFields(fields ...string) string {
	hash := sha256.New()
	for _, field := range fields {
		fmt.Fprintf(hash, "%d:", len(field))
		hash.Write([]byte(field))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func exitCodeField(code *int) string {
	if code == nil {
		return "nil"
	}
	return strconv.Itoa(*code)
}

func checkLogFailureMessage() string {
	return "the raw log could not be stored"
}

func retention(days int) time.Duration {
	if days <= 0 {
		days = DefaultCheckLogRetentionDays
	}
	return time.Duration(days) * 24 * time.Hour
}

func readCheckLogRetentionTx(ctx context.Context, queryer querier) (int, error) {
	var value string
	err := queryer.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='check_log_retention_days'`).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultCheckLogRetentionDays, nil
	}
	if err != nil {
		return 0, err
	}
	days, err := strconv.Atoi(value)
	if err != nil || days <= 0 {
		return DefaultCheckLogRetentionDays, nil
	}
	return days, nil
}

var errCheckLogIntegrity = errors.New("raw check log does not match its durable metadata")

func (s *Store) readCheckRawLog(logID string, expiresAt *time.Time) ([]byte, bool, error) {
	if !validAttemptID(logID) || expiresAt == nil {
		return nil, false, nil
	}
	var content []byte
	var rawExpiry int64
	var digest string
	var attemptExpiry sql.NullInt64
	err := s.db.QueryRow(`SELECT r.content,r.expires_at,a.log_digest,a.log_expires_at
		FROM check_raw_logs r JOIN check_attempts a ON a.id=r.attempt_id
		WHERE r.attempt_id=? AND a.log_id=?`, logID, logID).Scan(&content, &rawExpiry, &digest, &attemptExpiry)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if len(content) > MaximumCheckLogBytes || digest == "" || digestFields(string(content)) != digest ||
		!attemptExpiry.Valid || rawExpiry != attemptExpiry.Int64 || rawExpiry != expiresAt.Unix() {
		return nil, false, errCheckLogIntegrity
	}
	return content, true, nil
}

// CheckLogState reports whether a raw log remains readable. The durable
// attempt record stays readable after the log expires or its local BLOB is
// absent.
func (s *Store) CheckLogState(logID string, expiresAt *time.Time, now time.Time) string {
	if !validAttemptID(logID) || expiresAt == nil {
		return CheckLogMissing
	}
	if !now.Before(*expiresAt) {
		return CheckLogExpired
	}
	_, found, err := s.readCheckRawLog(logID, expiresAt)
	if err != nil || !found {
		return CheckLogMissing
	}
	return CheckLogFound
}

// ReadCheckLog reads a raw log and enforces its expiry. The durable attempt
// record stays readable after the log expires.
func (s *Store) ReadCheckLog(logID string, expiresAt *time.Time, now time.Time) ([]byte, string, error) {
	if !validAttemptID(logID) || expiresAt == nil {
		return nil, CheckLogMissing, nil
	}
	if !now.Before(*expiresAt) {
		return nil, CheckLogExpired, nil
	}
	content, found, err := s.readCheckRawLog(logID, expiresAt)
	if err != nil {
		return nil, CheckLogMissing, err
	}
	if !found {
		return nil, CheckLogMissing, nil
	}
	return content, CheckLogFound, nil
}

// PruneCheckLogs deletes every raw-log row due at the fixed cutoff. Each
// transaction handles at most 4 MiB of maximum-size logical content, and the
// indexed loop stops only after that due set is empty or an error occurs.
func (s *Store) PruneCheckLogs(ctx context.Context, now time.Time) (int, error) {
	cutoff := now.Unix()
	removed := 0
	for {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return removed, err
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM check_raw_logs WHERE attempt_id IN (
			SELECT attempt_id FROM check_raw_logs WHERE expires_at<=? ORDER BY expires_at,attempt_id LIMIT ?
		)`, cutoff, checkLogPruneBatch)
		if err != nil {
			return removed, errors.Join(err, tx.Rollback())
		}
		count, err := result.RowsAffected()
		if err != nil {
			return removed, errors.Join(err, tx.Rollback())
		}
		if err := tx.Commit(); err != nil {
			return removed, err
		}
		removed += int(count)
		if count == 0 {
			return removed, nil
		}
	}
}

// RandomID returns a new opaque identifier for durable records.
func RandomID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.Unix()
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func configurationKey(repositoryID string, version int64) string {
	return repositoryID + "/" + fmt.Sprint(version)
}

// unixNanoOrZero keeps a pending attempt's zero finish time distinguishable
// from the zero instant during restore.
func unixNanoOrZero(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixNano()
}

// checkRecoveryResultVisited, when a test sets it, is called each time
// ValidateCheckRecovery reads a check result record. Validation runs while a
// backup holds the store's only connection, so tests use it to prove that
// every result is read once. Production leaves it nil.
var checkRecoveryResultVisited func()

// ValidateCheckRecovery checks portable task, configuration, cycle, attempt,
// and result records before a backup is written or a restore is published. It
// rejects tampered metadata instead of publishing a plausible but wrong state.
func ValidateCheckRecovery(snapshot RecoveryState) error {
	repositories := make(map[string]bool, len(snapshot.Repositories))
	counters := make(map[string]int64, len(snapshot.Repositories))
	for _, repository := range snapshot.Repositories {
		if repository.ID == "" || repository.AttemptSequence < 0 {
			return errors.New("invalid repository attempt counter")
		}
		if repositories[repository.ID] {
			return errors.New("duplicate repository attempt counter")
		}
		repositories[repository.ID] = true
		counters[repository.ID] = repository.AttemptSequence
	}
	tasks := make(map[string]RecoveryTask, len(snapshot.Tasks))
	for _, task := range snapshot.Tasks {
		if !repositories[task.RepositoryID] || task.ID == "" || !validText(task.Title, 200) || task.CreatedAt.IsZero() || task.UpdatedAt.IsZero() || task.UpdatedAt.Before(task.CreatedAt) {
			return errors.New("invalid task record")
		}
		if _, exists := tasks[task.ID]; exists {
			return errors.New("duplicate task record")
		}
		tasks[task.ID] = task
	}
	configurations := make(map[string]CheckConfiguration, len(snapshot.CheckConfigurations))
	for _, configuration := range snapshot.CheckConfigurations {
		if !repositories[configuration.RepositoryID] || configuration.Version <= 0 || configuration.ConfigHash == "" || configuration.CreatedAt.IsZero() {
			return errors.New("invalid check configuration record")
		}
		if len(configuration.Checks) > MaximumCheckDefinitions {
			return errors.New("invalid check configuration contents")
		}
		for _, check := range configuration.Checks {
			if !validText(check.Name, MaximumCheckNameBytes) || !validText(check.Command, MaximumCheckCommandBytes) {
				return errors.New("invalid check definition")
			}
		}
		// The stored hash must describe the stored definitions, so a tampered
		// configuration cannot silently change what the evidence means.
		encoded, err := json.Marshal(configuration.Checks)
		if err != nil {
			return errors.New("invalid check configuration contents")
		}
		hash := sha256.Sum256(encoded)
		if hex.EncodeToString(hash[:]) != configuration.ConfigHash {
			return errors.New("check configuration hash does not match its definitions")
		}
		key := configurationKey(configuration.RepositoryID, configuration.Version)
		if _, exists := configurations[key]; exists {
			return errors.New("duplicate check configuration record")
		}
		configurations[key] = configuration
	}
	// Operator policy and durable jobs are validated before attempts, so an
	// attempt can be checked against the exact job that owns its origin.
	jobs, err := validatePortableCheckJobs(snapshot, repositories, tasks, configurations)
	if err != nil {
		return err
	}
	cycles := make(map[string]RecoveryCheckCycle, len(snapshot.CheckCycles))
	cycleSequences := make(map[string]int64, len(snapshot.CheckCycles))
	cycleBoundaries := make(map[string]int64, len(snapshot.CheckCycles))
	for _, cycle := range snapshot.CheckCycles {
		if !validAttemptID(cycle.ID) || cycle.Sequence <= 0 || cycle.Sequence > CorrectionCycleLimit || cycle.ReservedAt.IsZero() || cycle.ReservedAfterSequence < 0 {
			return errors.New("invalid correction cycle record")
		}
		task, exists := tasks[cycle.TaskID]
		if !exists {
			return errors.New("correction cycle refers to an unknown task")
		}
		if task.RepositoryID != cycle.RepositoryID {
			return errors.New("correction cycle and task belong to different repositories")
		}
		if cycle.ReservedAfterSequence > counters[cycle.RepositoryID] {
			return errors.New("correction cycle boundary exceeds the repository attempt counter")
		}
		if _, exists := cycles[cycle.ID]; exists {
			return errors.New("duplicate correction cycle record")
		}
		// The budget numbering is dense from one, and the boundaries never
		// move backwards inside a task.
		if cycle.Sequence != cycleSequences[cycle.TaskID]+1 {
			return errors.New("correction cycle sequence is not a dense budget sequence")
		}
		if cycleSequences[cycle.TaskID] != 0 && cycle.ReservedAfterSequence < cycleBoundaries[cycle.TaskID] {
			return errors.New("correction cycle boundary is not increasing")
		}
		cycleSequences[cycle.TaskID] = cycle.Sequence
		cycleBoundaries[cycle.TaskID] = cycle.ReservedAfterSequence
		cycles[cycle.ID] = cycle
	}
	attempts := make(map[string]CheckAttempt, len(snapshot.CheckAttempts))
	attemptSequences := make(map[string]map[int64]bool, len(snapshot.Repositories))
	for _, attempt := range snapshot.CheckAttempts {
		if !repositories[attempt.RepositoryID] || !validAttemptID(attempt.ID) || !validObjectID(attempt.RevisionOID) || attempt.CreatedAt.IsZero() {
			return errors.New("invalid check attempt record")
		}
		task, exists := tasks[attempt.TaskID]
		if !exists {
			return errors.New("check attempt refers to an unknown task")
		}
		// A cross-repository attempt would bind evidence to the wrong project.
		if task.RepositoryID != attempt.RepositoryID {
			return errors.New("check attempt and task belong to different repositories")
		}
		if _, exists := configurations[configurationKey(attempt.RepositoryID, attempt.ConfigurationVersion)]; !exists {
			return errors.New("check attempt refers to an unknown configuration")
		}
		if attempt.Sequence <= 0 {
			return errors.New("invalid check attempt sequence")
		}
		// Array order is not authority. The issued sequence must be unique within
		// the repository and fall inside its durable counter.
		sequences := attemptSequences[attempt.RepositoryID]
		if sequences == nil {
			sequences = make(map[int64]bool)
			attemptSequences[attempt.RepositoryID] = sequences
		}
		if sequences[attempt.Sequence] {
			return errors.New("duplicate repository check attempt sequence")
		}
		sequences[attempt.Sequence] = true
		if attempt.Sequence > counters[attempt.RepositoryID] {
			return errors.New("check attempt sequence exceeds the repository attempt counter")
		}
		if attempt.CycleID != "" {
			cycle, exists := cycles[attempt.CycleID]
			if !exists || cycle.TaskID != attempt.TaskID || cycle.RepositoryID != attempt.RepositoryID {
				return errors.New("check attempt refers to an unknown correction cycle")
			}
			// A cycle-bound attempt must follow its reservation boundary, so a
			// pre-reservation attempt cannot certify the round.
			if attempt.Sequence <= cycle.ReservedAfterSequence {
				return errors.New("cycle-bound check attempt does not follow its reservation boundary")
			}
		}
		switch attempt.WorktreeState {
		case WorktreeClean, WorktreeDirty, WorktreeUnknown:
		default:
			return errors.New("invalid check attempt worktree state")
		}
		if !validExecutionScope(attempt.ExecutionScope) || !validProtection(attempt.Protection) {
			return errors.New("invalid check attempt execution context")
		}
		if attempt.JobID != "" {
			job, exists := jobs[attempt.JobID]
			if !exists {
				return errors.New("check attempt refers to an unknown automatic job")
			}
			if job.RepositoryID != attempt.RepositoryID {
				return errors.New("check attempt and automatic job belong to different repositories")
			}
			if job.AttemptID != attempt.ID {
				return errors.New("check attempt automatic job is not bound to this attempt")
			}
			if attempt.TaskID != job.TaskID || attempt.RevisionOID != job.SourceOID || attempt.ConfigurationVersion != job.ConfigurationVersion ||
				attempt.TimeoutMS != job.Limits.TimeoutMS || attempt.OutputLimitBytes != job.Limits.OutputLimitBytes ||
				attempt.CredentialID != job.CredentialID || attempt.CycleID != "" {
				return errors.New("check attempt facts do not match its automatic job")
			}
			if job.StartedAt == nil || !attempt.StartedAt.Equal(*job.StartedAt) ||
				attempt.ExecutionScope != executionScopeForExecutor(job.Executor) || attempt.Protection != job.Protection {
				return errors.New("check attempt execution context does not match its automatic job")
			}
		}
		if attempt.DurationMS < 0 || len(attempt.Summary) > 500 || attempt.TimeoutMS < 0 || attempt.OutputLimitBytes < 0 || len(attempt.LogError) > 200 {
			return errors.New("invalid check attempt summary")
		}
		if attempt.StartedAt.IsZero() {
			return errors.New("invalid check attempt contents")
		}
		if attempt.Status == AttemptPending {
			// A pending registration has no verdict, no finish time, and no
			// submitted facts.
			if !attempt.FinishedAt.IsZero() || len(attempt.Results) != 0 || attempt.CompletionDigest != "" ||
				attempt.SubmittedWorktreeState != "" || attempt.SubmittedLogDigest != "" || attempt.SubmittedTruncated || attempt.SubmittedCancelled {
				return errors.New("pending check attempt already has a result")
			}
		} else {
			if !validAttemptStatus(attempt.Status) || attempt.FinishedAt.IsZero() || attempt.FinishedAt.Before(attempt.StartedAt) {
				return errors.New("invalid check attempt contents")
			}
			switch attempt.SubmittedWorktreeState {
			case WorktreeClean, WorktreeDirty, WorktreeUnknown:
			default:
				return errors.New("invalid submitted check attempt worktree state")
			}
			if attempt.CompletionDigest == "" || attempt.SubmittedLogDigest == "" {
				return errors.New("completed check attempt has no canonical digest")
			}
		}
		if err := validateRecoveredCheckLog(attempt); err != nil {
			return err
		}
		if !validDigest(attempt.RegistrationDigest) {
			return errors.New("invalid check attempt registration digest")
		}
		if attempt.CompletionDigest != "" && !validDigest(attempt.CompletionDigest) {
			return errors.New("invalid check attempt completion digest")
		}
		if attempt.SubmittedLogDigest != "" && !validDigest(attempt.SubmittedLogDigest) {
			return errors.New("invalid check attempt submitted log digest")
		}
		if attempt.LogDigest != "" && !validDigest(attempt.LogDigest) {
			return errors.New("invalid check attempt log digest")
		}
		if attempts[attempt.ID].ID != "" {
			return errors.New("duplicate check attempt record")
		}
		attempts[attempt.ID] = attempt
	}
	// A counter records every issued sequence. Unique positive sequences no
	// greater than a counter are dense exactly when their count equals it.
	for repositoryID, counter := range counters {
		if int64(len(attemptSequences[repositoryID])) != counter {
			return errors.New("repository attempt counter does not match its issued sequences")
		}
	}
	// A job link must be symmetrical: a bound job names the attempt that names
	// the job, and one job never points at another repository's attempt.
	for _, job := range jobs {
		if job.AttemptID == "" {
			continue
		}
		attempt, exists := attempts[job.AttemptID]
		if !exists || attempt.JobID != job.ID {
			return errors.New("automatic job attempt linkage is not symmetrical")
		}
		if attempt.Status == AttemptPending {
			switch job.Status {
			case CheckJobStarted, CheckJobAmbiguous, CheckJobInterrupted:
			default:
				return errors.New("automatic job status does not match its pending attempt")
			}
		} else if job.Status != attempt.Status {
			return errors.New("automatic job status does not match its terminal attempt")
		}
	}
	// Results must be a dense ordered sequence per attempt, must describe the
	// referenced configuration by position, name, and command, and the
	// aggregate status must describe them. Results are grouped by attempt in
	// snapshot order once, so validation stays linear in the snapshot size.
	results := make(map[string][]CheckResult, len(attempts))
	for _, result := range snapshot.CheckResults {
		if checkRecoveryResultVisited != nil {
			checkRecoveryResultVisited()
		}
		attempt, exists := attempts[result.AttemptID]
		if !exists {
			return errors.New("check result refers to an unknown attempt")
		}
		if result.Position != len(results[result.AttemptID]) {
			return errors.New("check results are not a dense ordered sequence")
		}
		results[result.AttemptID] = append(results[result.AttemptID], result.CheckResult)
		if !validText(result.Name, MaximumCheckNameBytes) || !validText(result.Command, MaximumCheckCommandBytes) || !validAttemptStatus(result.Status) {
			return errors.New("invalid check result record")
		}
		if result.DurationMS < 0 || len(result.OutputExcerpt) > MaximumCheckExcerptBytes || len(result.CleanupError) > MaximumCleanupErrorBytes {
			return errors.New("invalid check result contents")
		}
		configuration := configurations[configurationKey(attempt.RepositoryID, attempt.ConfigurationVersion)]
		if result.Position >= len(configuration.Checks) {
			return errors.New("check result has no matching configuration entry")
		}
		check := configuration.Checks[result.Position]
		if check.Name != result.Name || check.Command != result.Command {
			return errors.New("check result does not match its configuration entry")
		}
	}
	for id, attempt := range attempts {
		configuration := configurations[configurationKey(attempt.RepositoryID, attempt.ConfigurationVersion)]
		// Registration facts are canonical for pending and completed attempts.
		recomputed := attempt
		recomputed.Checks = configuration.Checks
		if registrationDigest(recomputed) != attempt.RegistrationDigest {
			return errors.New("check attempt registration digest does not match its facts")
		}
		attemptResults := results[id]
		if attempt.Status == AttemptPending {
			if len(attemptResults) != 0 {
				return errors.New("pending check attempt has results")
			}
			continue
		}
		// Every configured check needs exactly one result, including an
		// unavailable or cancelled entry when execution could not occur.
		if len(attemptResults) != len(configuration.Checks) {
			return errors.New("check attempt does not have one result per configured check")
		}
		if AggregateAttemptStatus(attemptResults, attempt.SubmittedCancelled) != attempt.Status {
			return errors.New("check attempt status does not describe its results")
		}
		if AttemptSummary(attemptResults, attempt.EffectiveWorktreeState(), attempt.Status) != attempt.Summary {
			return errors.New("check attempt summary does not describe its results")
		}
		// The completion digest must be recomputable from the submitted facts.
		if completionDigest(attempt, attemptResults) != attempt.CompletionDigest {
			return errors.New("check attempt completion digest does not match its facts")
		}
	}
	return nil
}

func validateRecoveredCheckLog(attempt CheckAttempt) error {
	if attempt.Status == AttemptPending {
		if attempt.LogID != "" || attempt.LogDigest != "" || attempt.LogExpiresAt != nil || attempt.LogTruncated || attempt.LogError != "" {
			return errors.New("pending check attempt has log metadata")
		}
		return nil
	}
	if attempt.LogID == "" {
		// Storage failure keeps the submitted digest and truncation facts, but
		// has no accepted raw-log identity, digest, expiry, or truncation flag.
		if attempt.LogDigest != "" || attempt.LogExpiresAt != nil || attempt.LogTruncated || !validText(attempt.LogError, 200) {
			return errors.New("invalid check attempt log failure metadata")
		}
		return nil
	}
	if attempt.LogID != attempt.ID || attempt.LogExpiresAt == nil || attempt.LogExpiresAt.IsZero() || attempt.LogError != "" ||
		attempt.LogDigest != attempt.SubmittedLogDigest || attempt.LogTruncated != attempt.SubmittedTruncated {
		return errors.New("invalid published check log metadata")
	}
	return nil
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

// readCheckRecovery reads the portable check state inside the snapshot
// transaction, so a backup never needs a second connection.
func readCheckRecovery(ctx context.Context, tx *sql.Tx, snapshot *RecoveryState) error {
	if err := readCheckJobRecovery(ctx, tx, snapshot); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,repository_id,title,created_at,updated_at FROM tasks ORDER BY repository_id,created_at,id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var task RecoveryTask
		var createdAt, updatedAt int64
		if err := rows.Scan(&task.ID, &task.RepositoryID, &task.Title, &createdAt, &updatedAt); err != nil {
			rows.Close()
			return err
		}
		task.CreatedAt = unixTime(createdAt)
		task.UpdatedAt = unixTime(updatedAt)
		snapshot.Tasks = append(snapshot.Tasks, task)
	}
	if err := closeRows(rows); err != nil {
		return err
	}

	rows, err = tx.QueryContext(ctx, checkConfigurationSelect+` ORDER BY repository_id,version`)
	if err != nil {
		return err
	}
	for rows.Next() {
		configuration, err := scanCheckConfiguration(rows)
		if err != nil {
			rows.Close()
			return err
		}
		snapshot.CheckConfigurations = append(snapshot.CheckConfigurations, configuration)
	}
	if err := closeRows(rows); err != nil {
		return err
	}

	rows, err = tx.QueryContext(ctx, `SELECT id,task_id,repository_id,sequence,reserved_at,reserved_after_sequence FROM check_cycles ORDER BY repository_id,task_id,sequence`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cycle RecoveryCheckCycle
		var reservedAt int64
		if err := rows.Scan(&cycle.ID, &cycle.TaskID, &cycle.RepositoryID, &cycle.Sequence, &reservedAt, &cycle.ReservedAfterSequence); err != nil {
			rows.Close()
			return err
		}
		cycle.ReservedAt = unixTime(reservedAt)
		snapshot.CheckCycles = append(snapshot.CheckCycles, cycle)
	}
	if err := closeRows(rows); err != nil {
		return err
	}

	rows, err = tx.QueryContext(ctx, attemptSelect+` ORDER BY repository_id,sequence`)
	if err != nil {
		return err
	}
	for rows.Next() {
		attempt, err := scanCheckAttempt(rows)
		if err != nil {
			rows.Close()
			return err
		}
		snapshot.CheckAttempts = append(snapshot.CheckAttempts, attempt)
	}
	if err := closeRows(rows); err != nil {
		return err
	}

	rows, err = tx.QueryContext(ctx, `SELECT attempt_id,position,name,command,status,exit_code,duration_ms,output_excerpt,truncated,cleanup_error FROM check_results ORDER BY attempt_id,position`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var record CheckResultRecord
		var exitCode sql.NullInt64
		var truncated int
		if err := rows.Scan(&record.AttemptID, &record.Position, &record.Name, &record.Command, &record.Status, &exitCode, &record.DurationMS, &record.OutputExcerpt, &truncated, &record.CleanupError); err != nil {
			rows.Close()
			return err
		}
		if exitCode.Valid {
			value := int(exitCode.Int64)
			record.ExitCode = &value
		}
		record.Truncated = truncated != 0
		snapshot.CheckResults = append(snapshot.CheckResults, record)
	}
	return closeRows(rows)
}

// restoreCheckRecovery writes the portable check state into a new store inside
// the restore transaction. The derived task columns are recomputed on read, so
// only the authoritative task identity is written back.
func restoreCheckRecovery(ctx context.Context, tx *sql.Tx, snapshot RecoveryState) error {
	for _, task := range snapshot.Tasks {
		if _, err := tx.ExecContext(ctx, `INSERT INTO tasks(id,repository_id,title,created_at,updated_at) VALUES(?,?,?,?,?)`,
			task.ID, task.RepositoryID, task.Title, task.CreatedAt.Unix(), task.UpdatedAt.Unix()); err != nil {
			return fmt.Errorf("restore task %q: %w", task.ID, err)
		}
	}
	for _, configuration := range snapshot.CheckConfigurations {
		encoded, err := json.Marshal(configuration.Checks)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO check_configurations(repository_id,version,config_hash,checks_json,created_at) VALUES(?,?,?,?,?)`,
			configuration.RepositoryID, configuration.Version, configuration.ConfigHash, string(encoded), configuration.CreatedAt.Unix()); err != nil {
			return fmt.Errorf("restore check configuration %s/%d: %w", configuration.RepositoryID, configuration.Version, err)
		}
	}
	if err := restoreCheckJobRecovery(ctx, tx, snapshot); err != nil {
		return err
	}
	for _, cycle := range snapshot.CheckCycles {
		if _, err := tx.ExecContext(ctx, `INSERT INTO check_cycles(id,task_id,repository_id,sequence,reserved_at,reserved_after_sequence) VALUES(?,?,?,?,?,?)`,
			cycle.ID, cycle.TaskID, cycle.RepositoryID, cycle.Sequence, cycle.ReservedAt.Unix(), cycle.ReservedAfterSequence); err != nil {
			return fmt.Errorf("restore correction cycle %q: %w", cycle.ID, err)
		}
	}
	for _, attempt := range snapshot.CheckAttempts {
		if _, err := tx.ExecContext(ctx, `INSERT INTO check_attempts(
			id,task_id,repository_id,revision_oid,registration_worktree_state,submitted_worktree_state,configuration_version,status,exit_code,started_at,finished_at,duration_ms,summary,protection,execution_scope,credential_id,timeout_ms,output_limit_bytes,log_id,log_expires_at,log_truncated,log_error,created_at,sequence,cycle_id,registration_digest,completion_digest,submitted_log_digest,submitted_truncated,submitted_cancelled,log_digest,job_id
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			attempt.ID, attempt.TaskID, attempt.RepositoryID, attempt.RevisionOID, attempt.WorktreeState, attempt.SubmittedWorktreeState,
			attempt.ConfigurationVersion, attempt.Status, nullableInt(attempt.ExitCode), attempt.StartedAt.UnixNano(),
			unixNanoOrZero(attempt.FinishedAt), attempt.DurationMS, attempt.Summary, attempt.Protection, attempt.ExecutionScope,
			attempt.CredentialID, attempt.TimeoutMS, attempt.OutputLimitBytes, attempt.LogID, nullableTime(attempt.LogExpiresAt),
			boolInt(attempt.LogTruncated), attempt.LogError, attempt.CreatedAt.Unix(), attempt.Sequence, attempt.CycleID,
			attempt.RegistrationDigest, attempt.CompletionDigest, attempt.SubmittedLogDigest, boolInt(attempt.SubmittedTruncated),
			boolInt(attempt.SubmittedCancelled), attempt.LogDigest, attempt.JobID); err != nil {
			return fmt.Errorf("restore check attempt %q: %w", attempt.ID, err)
		}
	}
	for _, result := range snapshot.CheckResults {
		if _, err := tx.ExecContext(ctx, `INSERT INTO check_results(attempt_id,position,name,command,status,exit_code,duration_ms,output_excerpt,truncated,cleanup_error) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			result.AttemptID, result.Position, result.Name, result.Command, result.Status, nullableInt(result.ExitCode),
			result.DurationMS, result.OutputExcerpt, boolInt(result.Truncated), result.CleanupError); err != nil {
			return fmt.Errorf("restore check result %s/%d: %w", result.AttemptID, result.Position, err)
		}
	}
	return nil
}
