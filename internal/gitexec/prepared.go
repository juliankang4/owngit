package gitexec

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// RunPreparedUpdateContext runs one explicit update-ref transaction. Git holds
// every update and verify lock while prepared runs. The callback must honor the
// derived context. A callback or context error sends abort; protocol failure
// closes stdin so Git aborts on EOF.
//
// Lifetime boundary: cancellation, deadline and termination handling proceed
// independently of the callback, so Git cleanup stays bounded and a late
// callback result can never commit. The protocol goroutine admits the callback
// through a synchronized gate before starting it; after Git is aborted or
// reaped the runner closes that gate first, so a delayed protocol goroutine can
// no longer start a callback after the caller's guards are released. An
// admitted callback is joined for one grace interval. Only an admitted callback
// that is still running then is reported through ErrPreparedCallbackDetached so
// the caller can complete its own barrier instead of releasing state the
// callback still uses. A callback that was never admitted is not reported as
// detached, so the return carries only the context or lifecycle error.
// ErrPreparedCallbackDetached reports that an admitted prepared callback was
// still running when the runner returned. The transaction is already aborted.
var ErrPreparedCallbackDetached = errors.New("prepared callback is still running after the transaction ended")

// ErrPreparedProcessNotReaped reports that the update-ref process did not exit
// within the grace intervals after termination. It may still be running in the
// repository directory, so the caller must not treat the directory as settled.
var ErrPreparedProcessNotReaped = errors.New("prepared update process was not reaped after termination")

// preparedTerminateOwnedProcess is the termination seam for prepared updates.
// Tests replace it to apply Linux zombie semantics on other platforms.
var preparedTerminateOwnedProcess = TerminateOwnedProcess

// preparedAdmission is the start-versus-stop boundary between the protocol
// goroutine and the runner return decision. Admission and closure are
// serialized so exactly one outcome holds: the callback was admitted before
// the decision and the runner joins it, or the gate closed first and a delayed
// protocol goroutine must not run the callback at all.
type preparedAdmission struct {
	mu    sync.Mutex
	state admissionState
	done  chan struct{}
}

type admissionState uint8

const (
	admissionPending admissionState = iota
	admissionStarted
	admissionFinished
	admissionClosed
)

func newPreparedAdmission() *preparedAdmission {
	return &preparedAdmission{done: make(chan struct{})}
}

// admit starts the callback unless the runner already closed the gate.
func (a *preparedAdmission) admit() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state != admissionPending {
		return false
	}
	a.state = admissionStarted
	return true
}

// finish records that the admitted callback returned and releases the join.
func (a *preparedAdmission) finish() {
	a.mu.Lock()
	a.state = admissionFinished
	close(a.done)
	a.mu.Unlock()
}

// close refuses later admission and reports whether a callback was admitted.
// The completion channel closes only after an admitted callback returned.
func (a *preparedAdmission) close() (bool, <-chan struct{}) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state == admissionPending {
		a.state = admissionClosed
	}
	return a.state == admissionStarted || a.state == admissionFinished, a.done
}

// preparedProtocolSeam is an optional per-call protocol scheduling seam for
// tests. Both hooks run on the protocol goroutine and either may be nil.
type preparedProtocolSeam struct {
	beforeAdmission func()
	afterProtocol   func()
}

func (r *Runner) RunPreparedUpdateContext(ctx context.Context, dir string, commands []string, limits CommandLimits, prepared func(context.Context) error) (Result, error) {
	return r.runPreparedUpdateContext(ctx, dir, commands, limits, prepared, nil)
}

func (r *Runner) runPreparedUpdateContext(ctx context.Context, dir string, commands []string, limits CommandLimits, prepared func(context.Context) error, seam *preparedProtocolSeam) (Result, error) {
	outputLimit := r.OutputLimit
	if limits.OutputLimit > 0 {
		outputLimit = limits.OutputLimit
	}
	if outputLimit <= 0 {
		outputLimit = defaultOutputLimit
	}
	timeout := r.Timeout
	if limits.Timeout > 0 {
		timeout = limits.Timeout
	}
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var stdout, stderr limitedBuffer
	stdout.limit = outputLimit
	stderr.limit = outputLimit
	cmd := exec.Command(r.GitPath, "--git-dir", ".", "update-ref", "--no-deref", "--stdin")
	cmd.Dir = dir
	cmd.Env = r.Environment(limits.Environment...)
	cmd.Stderr = &stderr
	input, err := cmd.StdinPipe()
	if err != nil {
		return Result{}, fmt.Errorf("open prepared update input: %w", err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		_ = input.Close()
		return Result{}, fmt.Errorf("open prepared update output: %w", err)
	}
	ConfigureOwnedProcess(cmd)
	if err := cmd.Start(); err != nil {
		_ = input.Close()
		_ = output.Close()
		return Result{}, fmt.Errorf("start prepared update: %w", err)
	}
	owner, err := r.processSeam.attach(cmd)
	if err != nil {
		_ = input.Close()
		_ = output.Close()
		_, cleanupErr := cleanupUnattachedStartedProcess(cmd, r.TerminationGrace, err, r.processSeam)
		return Result{}, fmt.Errorf("contain prepared update: %w", cleanupErr)
	}
	type interactionResult struct {
		callbackErr error
		err         error
	}
	interactionCh := make(chan interactionResult, 1)
	admission := newPreparedAdmission()
	go func() {
		if seam != nil && seam.afterProtocol != nil {
			defer seam.afterProtocol()
		}
		defer input.Close()
		reader := bufio.NewReaderSize(output, 4096)
		expect := func(action string) error {
			line, readErr := reader.ReadSlice('\n')
			if errors.Is(readErr, bufio.ErrBufferFull) {
				return &LimitError{Stream: "stdout acknowledgement", Limit: 4096}
			}
			if _, writeErr := stdout.Write(line); writeErr != nil && readErr == nil {
				readErr = writeErr
			}
			if stdout.exceeded {
				return &LimitError{Stream: "stdout", Limit: outputLimit}
			}
			if readErr != nil {
				return fmt.Errorf("read %s acknowledgement: %w", action, readErr)
			}
			if string(line) != action+": ok\n" {
				return fmt.Errorf("unexpected %s acknowledgement %q", action, line)
			}
			return nil
		}
		body := "start\n" + strings.Join(commands, "\n") + "\nprepare\n"
		if _, err := io.WriteString(input, body); err != nil {
			interactionCh <- interactionResult{err: fmt.Errorf("write prepared update: %w", err)}
			return
		}
		if err := expect("start"); err != nil {
			interactionCh <- interactionResult{err: err}
			return
		}
		if err := expect("prepare"); err != nil {
			interactionCh <- interactionResult{err: err}
			return
		}
		if seam != nil && seam.beforeAdmission != nil {
			seam.beforeAdmission()
		}
		callbackCh := make(chan error, 1)
		var callbackErr error
		if admission.admit() {
			go func() {
				defer admission.finish()
				callbackCh <- prepared(runCtx)
			}()
			select {
			case callbackErr = <-callbackCh:
				if contextErr := runCtx.Err(); contextErr != nil {
					callbackErr = errors.Join(callbackErr, contextErr)
				}
			case <-runCtx.Done():
				callbackErr = runCtx.Err()
			}
		} else {
			callbackErr = runCtx.Err()
		}
		action := "commit"
		if callbackErr != nil {
			action = "abort"
		}
		if action == "commit" {
			if contextErr := runCtx.Err(); contextErr != nil {
				callbackErr = errors.Join(callbackErr, contextErr)
				action = "abort"
			}
		}
		if _, err := io.WriteString(input, action+"\n"); err != nil {
			interactionCh <- interactionResult{callbackErr: callbackErr, err: fmt.Errorf("write prepared update %s: %w", action, err)}
			return
		}
		if err := expect(action); err != nil {
			interactionCh <- interactionResult{callbackErr: callbackErr, err: err}
			return
		}
		if err := input.Close(); err != nil {
			interactionCh <- interactionResult{callbackErr: callbackErr, err: fmt.Errorf("close prepared update input: %w", err)}
			return
		}
		acknowledgementBytes := len(stdout.Bytes())
		if _, err := io.Copy(&stdout, reader); err != nil {
			interactionCh <- interactionResult{callbackErr: callbackErr, err: fmt.Errorf("drain prepared update output: %w", err)}
			return
		}
		if stdout.exceeded {
			interactionCh <- interactionResult{callbackErr: callbackErr, err: &LimitError{Stream: "stdout", Limit: outputLimit}}
			return
		}
		if trailing := stdout.Bytes()[acknowledgementBytes:]; len(trailing) != 0 {
			interactionCh <- interactionResult{callbackErr: callbackErr, err: fmt.Errorf("unexpected output after %s acknowledgement %q", action, trailing)}
			return
		}
		interactionCh <- interactionResult{callbackErr: callbackErr}
	}()

	grace := r.TerminationGrace
	if grace <= 0 {
		grace = 2 * time.Second
	}
	var interaction interactionResult
	var lifecycleErr error
	// Wait starts exactly once. As in Stream, it starts only after stdout is
	// complete or closed, so it never truncates a protocol read, and before
	// the group is signaled, so the leader is reaped while termination polls.
	// An unreaped leader is a zombie that Linux still reports as a live group
	// member, which would make termination wait out both grace intervals and
	// report a false SIGKILL survival.
	waitCh := make(chan error, 1)
	waitStarted := false
	startWait := func() {
		if !waitStarted {
			waitStarted = true
			go func() { waitCh <- cmd.Wait() }()
		}
	}
	terminated := false
	terminate := func() {
		if terminated {
			return
		}
		terminated = true
		_ = input.Close()
		_ = output.Close()
		startWait()
		lifecycleErr = errors.Join(lifecycleErr, preparedTerminateOwnedProcess(owner, grace))
	}
	select {
	case interaction = <-interactionCh:
	case <-runCtx.Done():
		// The callback shares runCtx and normally rejects with an explicit abort.
		// Give that protocol path one grace interval before forcing the group.
		timer := time.NewTimer(grace)
		select {
		case interaction = <-interactionCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
			terminate()
			second := time.NewTimer(grace)
			select {
			case interaction = <-interactionCh:
				if !second.Stop() {
					select {
					case <-second.C:
					default:
					}
				}
			case <-second.C:
				lifecycleErr = errors.Join(lifecycleErr, errors.New("prepared update interaction did not stop after termination"))
			}
		}
		interaction.err = errors.Join(runCtx.Err(), interaction.err)
	}

	startWait()
	var waitErr error
	waited := false
	if runCtx.Err() == nil {
		select {
		case waitErr = <-waitCh:
			waited = true
		case <-runCtx.Done():
		}
	}
	if !waited {
		timer := time.NewTimer(grace)
		select {
		case waitErr = <-waitCh:
			waited = true
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
			terminate()
		}
	}
	if !waited {
		timer := time.NewTimer(grace)
		select {
		case waitErr = <-waitCh:
			waited = true
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
			lifecycleErr = errors.Join(lifecycleErr, ErrPreparedProcessNotReaped)
		}
	}
	if contextErr := runCtx.Err(); contextErr != nil {
		interaction.err = errors.Join(interaction.err, contextErr)
	}
	interaction.err = errors.Join(interaction.err, lifecycleErr)
	_ = output.Close()
	// Git is already aborted, terminated or reaped here, so the callback can no
	// longer influence the transaction. Closing admission first serializes this
	// return with a protocol goroutine that has not admitted the callback yet:
	// after this point the callback must not start, because the caller releases
	// its guards when this call returns. Only an admitted callback is joined for
	// one grace interval.
	admitted, callbackDone := admission.close()
	if admitted {
		joinTimer := time.NewTimer(grace)
		select {
		case <-callbackDone:
			if !joinTimer.Stop() {
				select {
				case <-joinTimer.C:
				default:
				}
			}
		case <-joinTimer.C:
			interaction.err = errors.Join(interaction.err, ErrPreparedCallbackDetached)
		}
	}
	result := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err := CloseOwnedProcess(owner); err != nil {
		interaction.err = errors.Join(interaction.err, err)
	}
	if stdout.exceeded {
		interaction.err = errors.Join(interaction.err, &LimitError{Stream: "stdout", Limit: outputLimit})
	}
	if stderr.exceeded {
		interaction.err = errors.Join(interaction.err, &LimitError{Stream: "stderr", Limit: outputLimit})
	}
	if waitErr != nil {
		message := strings.TrimSpace(string(result.Stderr))
		if message != "" {
			waitErr = fmt.Errorf("git update-ref: %w: %s", waitErr, message)
		} else {
			waitErr = fmt.Errorf("git update-ref: %w", waitErr)
		}
		interaction.err = errors.Join(interaction.err, waitErr)
	}
	if combinedErr := errors.Join(interaction.callbackErr, interaction.err); combinedErr != nil {
		return result, combinedErr
	}
	return result, nil
}
