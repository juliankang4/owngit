package gitexec

import "errors"

// InjectCleanupFaults makes runner's owned-process cleanup fail with the given
// errors after the real termination and owner release, from the command after
// the first skip on. The returned function reports any failure of the real
// operations. It exists for the consumer tests in package gitexec_test.
func InjectCleanupFaults(runner *Runner, skip int, terminateErr, closeErr error) func() error {
	faults := injectCleanupFaults(runner, terminateErr, closeErr)
	faults.skip = skip
	return func() error { return errors.Join(faults.terminateErr, faults.closeErr) }
}
