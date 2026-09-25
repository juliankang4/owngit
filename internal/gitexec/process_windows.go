//go:build windows

package gitexec

import (
	"errors"
	"fmt"
	"os/exec"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32ProcessAPI   = windows.NewLazySystemDLL("kernel32.dll")
	getProcessIDOfThread = kernel32ProcessAPI.NewProc("GetProcessIdOfThread")
	isProcessInJob       = kernel32ProcessAPI.NewProc("IsProcessInJob")
)

type jobProcessIDListHeader struct {
	numberAssigned uint32
	numberInList   uint32
}

type windowsJobAccounting struct {
	totalUserTime             int64
	totalKernelTime           int64
	thisPeriodTotalUserTime   int64
	thisPeriodTotalKernelTime int64
	totalPageFaultCount       uint32
	totalProcesses            uint32
	activeProcesses           uint32
	totalTerminatedProcesses  uint32
}

type windowsTrackedProcess struct {
	processID uint32
	handle    windows.Handle
}

type windowsJobCapture struct {
	processes   []windowsTrackedProcess
	stableTotal uint32
	stable      bool
}

type ProcessOwner struct {
	job windows.Handle
}

// Job queries are variables so a focused test can replay what a job reports
// while its processes exit. Production uses the real queries.
var (
	jobAccountingQuery = queryWindowsJobAccounting
	jobProcessIDsQuery = windowsJobProcessIDs
)

// Windows can keep counting, and briefly listing, a process in its job for a
// few milliseconds after the process has exited and been waited. Cleanup polls
// at this interval while it waits for such departures to settle.
const windowsJobSettleInterval = time.Millisecond

func ConfigureOwnedProcess(cmd *exec.Cmd) {
	// CREATE_SUSPENDED keeps the primary thread from running until Attach has
	// assigned the process to its kill-on-close job.
	cmd.SysProcAttr = &windows.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_SUSPENDED,
	}
}

func AttachOwnedProcess(cmd *exec.Cmd) (*ProcessOwner, error) {
	return AttachOwnedProcessObserved(cmd, nil)
}

// AttachOwnedProcessObserved exposes the process-start boundary to a focused
// regression fixture. Ordinary callers pass no observer.
func AttachOwnedProcessObserved(cmd *exec.Cmd, observeStarted func() error) (*ProcessOwner, error) {
	owner, err := newWindowsProcessOwner()
	if err != nil {
		return nil, err
	}
	if cmd.Process == nil {
		return nil, abortWindowsProcessOwner(owner, false, errors.New("attach process owner: process has not started"))
	}

	assigned := false
	var attachErr error
	handleErr := cmd.Process.WithHandle(func(rawHandle uintptr) {
		process := windows.Handle(rawHandle)
		processID, err := windows.GetProcessId(process)
		if err != nil {
			attachErr = fmt.Errorf("read started process identity: %w", err)
			return
		}
		if processID != uint32(cmd.Process.Pid) {
			attachErr = fmt.Errorf("started process identity mismatch: handle=%d pid=%d", processID, cmd.Process.Pid)
			return
		}
		assigned, attachErr = assignAndResumeWindowsProcess(owner.job, process, processID)
	})
	if handleErr != nil {
		return nil, abortWindowsProcessOwner(owner, assigned, fmt.Errorf("access started process handle: %w", handleErr))
	}
	if attachErr != nil {
		return nil, abortWindowsProcessOwner(owner, assigned, attachErr)
	}
	if observeStarted != nil {
		if err := observeStarted(); err != nil {
			return nil, abortWindowsProcessOwner(owner, true, fmt.Errorf("observe started process: %w", err))
		}
	}
	return owner, nil
}

func newWindowsProcessOwner() (*ProcessOwner, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create process job object: %w", err)
	}
	owner := &ProcessOwner{job: job}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		return nil, errors.Join(
			fmt.Errorf("configure process job object: %w", err),
			CloseOwnedProcess(owner),
		)
	}
	return owner, nil
}

func assignAndResumeWindowsProcess(job, process windows.Handle, processID uint32) (assigned bool, err error) {
	thread, err := openSuspendedPrimaryThread(processID)
	if err != nil {
		return false, err
	}
	defer func() {
		if closeErr := windows.CloseHandle(thread); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close primary thread handle: %w", closeErr))
		}
	}()
	if err := requireActiveWindowsProcess(process); err != nil {
		return false, err
	}

	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		return false, fmt.Errorf("assign suspended process to job object: %w", err)
	}
	assigned = true
	previousSuspendCount, err := windows.ResumeThread(thread)
	if err != nil {
		return true, fmt.Errorf("resume owned primary thread: %w", err)
	}
	if previousSuspendCount != 1 {
		return true, fmt.Errorf("resume owned primary thread: unexpected suspend count %d", previousSuspendCount)
	}
	return true, nil
}

func openSuspendedPrimaryThread(processID uint32) (windows.Handle, error) {
	// os/exec retains the process handle but not CreateProcess's primary-thread
	// handle. Tool Help finds the sole thread of the still-suspended process;
	// GetProcessIdOfThread revalidates its owner after OpenThread.
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return 0, fmt.Errorf("snapshot threads for suspended process %d: %w", processID, err)
	}
	threadID, findErr := findOnlyProcessThread(snapshot, processID)
	closeErr := windows.CloseHandle(snapshot)
	if findErr != nil || closeErr != nil {
		return 0, errors.Join(findErr, wrapWindowsCloseError("thread snapshot", closeErr))
	}

	thread, err := windows.OpenThread(
		windows.THREAD_SUSPEND_RESUME|windows.THREAD_QUERY_LIMITED_INFORMATION,
		false,
		threadID,
	)
	if err != nil {
		return 0, fmt.Errorf("open suspended primary thread %d: %w", threadID, err)
	}
	ownerProcessID, err := processIDOfThread(thread)
	if err != nil {
		return 0, errors.Join(
			fmt.Errorf("verify suspended primary thread %d: %w", threadID, err),
			wrapWindowsCloseError("primary thread handle", windows.CloseHandle(thread)),
		)
	}
	if ownerProcessID != processID {
		return 0, errors.Join(
			fmt.Errorf("suspended primary thread identity mismatch: thread=%d owner=%d process=%d", threadID, ownerProcessID, processID),
			wrapWindowsCloseError("primary thread handle", windows.CloseHandle(thread)),
		)
	}
	return thread, nil
}

func findOnlyProcessThread(snapshot windows.Handle, processID uint32) (uint32, error) {
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	if err := windows.Thread32First(snapshot, &entry); err != nil {
		return 0, fmt.Errorf("enumerate threads for suspended process %d: %w", processID, err)
	}

	var threadID uint32
	for {
		if entry.OwnerProcessID == processID {
			if threadID != 0 {
				return 0, fmt.Errorf("identify suspended primary thread for process %d: found multiple threads", processID)
			}
			threadID = entry.ThreadID
		}
		entry.Size = uint32(unsafe.Sizeof(windows.ThreadEntry32{}))
		err := windows.Thread32Next(snapshot, &entry)
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("enumerate threads for suspended process %d: %w", processID, err)
		}
	}
	if threadID == 0 {
		return 0, fmt.Errorf("identify suspended primary thread for process %d: no thread found", processID)
	}
	return threadID, nil
}

func requireActiveWindowsProcess(process windows.Handle) error {
	state, err := windows.WaitForSingleObject(process, 0)
	if err != nil {
		return fmt.Errorf("inspect suspended process state: %w", err)
	}
	if state != uint32(windows.WAIT_TIMEOUT) {
		return fmt.Errorf("suspended process exited before job assignment: wait state 0x%x", state)
	}
	return nil
}

func processIDOfThread(thread windows.Handle) (uint32, error) {
	result, _, callErr := getProcessIDOfThread.Call(uintptr(thread))
	if result != 0 {
		return uint32(result), nil
	}
	if errors.Is(callErr, windows.ERROR_SUCCESS) {
		return 0, errors.New("GetProcessIdOfThread returned zero")
	}
	return 0, callErr
}

func abortWindowsProcessOwner(owner *ProcessOwner, assigned bool, cause error) error {
	var cleanupErr error
	if assigned {
		cleanupErr = errors.Join(cleanupErr, TerminateOwnedProcess(owner, 0))
	}
	cleanupErr = errors.Join(cleanupErr, CloseOwnedProcess(owner))
	return errors.Join(cause, cleanupErr)
}

func wrapWindowsCloseError(name string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("close %s: %w", name, err)
}

// TerminateOwnedProcess captures exact process handles before requesting job
// termination, then confirms every captured process exit within one deadline.
func TerminateOwnedProcess(owner *ProcessOwner, grace time.Duration) error {
	if owner == nil || owner.job == 0 {
		return nil
	}
	if grace <= 0 {
		grace = 100 * time.Millisecond
	}
	return terminateWindowsOwnedProcess(owner, time.Now().Add(grace))
}

func terminateWindowsOwnedProcess(owner *ProcessOwner, deadline time.Time) (result error) {
	capture, captureErr := captureWindowsJobProcesses(owner.job, deadline)
	defer func() {
		result = errors.Join(result, closeWindowsTrackedProcesses(capture.processes))
	}()

	var terminationErr error
	if err := windows.TerminateJobObject(owner.job, 1); err != nil {
		terminationErr = fmt.Errorf("terminate owned job object: %w", err)
	}
	waitErr := waitForWindowsTrackedProcesses(capture.processes, deadline)

	accounting, accountingErr := awaitWindowsJobEmpty(owner.job, deadline)
	if accountingErr != nil {
		accountingErr = fmt.Errorf("query owned job after termination: %w", accountingErr)
	} else {
		if accounting.activeProcesses != 0 {
			accountingErr = errors.Join(accountingErr, fmt.Errorf(
				"owned job still reports %d active processes after termination",
				accounting.activeProcesses,
			))
		}
		if capture.stable && accounting.totalProcesses != capture.stableTotal {
			accountingErr = errors.Join(accountingErr, fmt.Errorf(
				"owned job process lifetime changed during termination: before=%d after=%d",
				capture.stableTotal,
				accounting.totalProcesses,
			))
		}
	}
	return errors.Join(captureErr, terminationErr, waitErr, accountingErr)
}

// awaitWindowsJobEmpty returns the job accounting once no process is active,
// or the last accounting at the deadline. A terminated process can remain
// counted for a moment after its handle is signaled.
func awaitWindowsJobEmpty(job windows.Handle, deadline time.Time) (windowsJobAccounting, error) {
	for {
		accounting, err := jobAccountingQuery(job)
		if err != nil || accounting.activeProcesses == 0 || !time.Now().Before(deadline) {
			return accounting, err
		}
		time.Sleep(windowsJobSettleInterval)
	}
}

// captureWindowsJobProcesses retains a handle to every process in the job.
// An inconsistent capture in which the job only lost members, such as a main
// process that exited just before cleanup, is retried until it is consistent.
// Retries use at most half of the remaining cleanup time, so termination can
// still be confirmed. Growth, failed queries and inconsistencies that persist
// are reported.
func captureWindowsJobProcesses(job windows.Handle, deadline time.Time) (windowsJobCapture, error) {
	settleBy := time.Now().Add(time.Until(deadline) / 2)
	for {
		capture, onlyDeparted, err := captureWindowsJobProcessesOnce(job, deadline)
		if err == nil || !onlyDeparted || !time.Now().Before(settleBy) {
			return capture, err
		}
		if closeErr := closeWindowsTrackedProcesses(capture.processes); closeErr != nil {
			return windowsJobCapture{}, errors.Join(err, closeErr)
		}
		time.Sleep(windowsJobSettleInterval)
	}
}

// captureWindowsJobProcessesOnce makes one capture. onlyDeparted reports that
// both accounting queries succeeded and no process joined the job between
// them, so any inconsistency can come from processes leaving it.
func captureWindowsJobProcessesOnce(job windows.Handle, deadline time.Time) (capture windowsJobCapture, onlyDeparted bool, captureErr error) {
	if err := windowsCaptureDeadlineError(deadline); err != nil {
		return capture, false, err
	}
	before, err := jobAccountingQuery(job)
	if err != nil {
		return capture, false, errors.Join(
			fmt.Errorf("query owned job before process capture: %w", err),
			windowsCaptureDeadlineError(deadline),
		)
	}
	if err := windowsCaptureDeadlineError(deadline); err != nil {
		return capture, false, err
	}

	processIDs, listErr := jobProcessIDsQuery(job, before.activeProcesses)
	captureErr = listErr
	uniqueProcessIDs := make(map[uint32]struct{}, len(processIDs))
	for _, processID := range processIDs {
		if windowsCaptureDeadlineError(deadline) != nil {
			break
		}
		if _, exists := uniqueProcessIDs[processID]; exists {
			captureErr = errors.Join(captureErr, fmt.Errorf("owned job listed duplicate process identifier %d", processID))
			continue
		}
		uniqueProcessIDs[processID] = struct{}{}
		process, err := retainWindowsJobProcess(job, processID)
		if err != nil {
			captureErr = errors.Join(captureErr, err)
		} else {
			capture.processes = append(capture.processes, windowsTrackedProcess{processID: processID, handle: process})
		}
		if windowsCaptureDeadlineError(deadline) != nil {
			break
		}
	}
	if err := windowsCaptureDeadlineError(deadline); err != nil {
		return capture, false, errors.Join(captureErr, err)
	}

	after, err := jobAccountingQuery(job)
	if err != nil {
		captureErr = errors.Join(captureErr, fmt.Errorf("query owned job after process capture: %w", err))
		return capture, false, errors.Join(captureErr, windowsCaptureDeadlineError(deadline))
	}
	if err := windowsCaptureDeadlineError(deadline); err != nil {
		return capture, false, errors.Join(captureErr, err)
	}
	// The total counts every process ever assigned, so it grows whenever a
	// process joins; the active count can then only have fallen by departures.
	onlyDeparted = after.totalProcesses == before.totalProcesses
	if before.totalProcesses != after.totalProcesses || before.activeProcesses != after.activeProcesses {
		captureErr = errors.Join(captureErr, fmt.Errorf(
			"owned job membership changed during process capture: total=%d/%d active=%d/%d",
			before.totalProcesses,
			after.totalProcesses,
			before.activeProcesses,
			after.activeProcesses,
		))
	}
	if uint64(len(uniqueProcessIDs)) != uint64(after.activeProcesses) {
		captureErr = errors.Join(captureErr, fmt.Errorf(
			"owned job process list is incomplete: unique=%d active=%d",
			len(uniqueProcessIDs),
			after.activeProcesses,
		))
	}
	if uint64(len(capture.processes)) != uint64(after.activeProcesses) {
		captureErr = errors.Join(captureErr, fmt.Errorf(
			"owned job process handle capture is incomplete: handles=%d active=%d",
			len(capture.processes),
			after.activeProcesses,
		))
	}
	if captureErr == nil {
		capture.stable = true
		capture.stableTotal = after.totalProcesses
	}
	return capture, onlyDeparted, captureErr
}

func windowsCaptureDeadlineError(deadline time.Time) error {
	if time.Now().Before(deadline) {
		return nil
	}
	return errors.New("owned job process capture exceeded the cleanup deadline")
}

func retainWindowsJobProcess(job windows.Handle, processID uint32) (windows.Handle, error) {
	process, err := windows.OpenProcess(
		windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false,
		processID,
	)
	if err != nil {
		return 0, fmt.Errorf("open owned process %d before termination: %w", processID, err)
	}
	actualProcessID, err := windows.GetProcessId(process)
	if err != nil {
		return 0, errors.Join(
			fmt.Errorf("read owned process %d identity: %w", processID, err),
			wrapWindowsCloseError("owned process handle", windows.CloseHandle(process)),
		)
	}
	if actualProcessID != processID {
		return 0, errors.Join(
			fmt.Errorf("owned process identity mismatch: listed=%d handle=%d", processID, actualProcessID),
			wrapWindowsCloseError("owned process handle", windows.CloseHandle(process)),
		)
	}
	inJob, err := windowsProcessInJob(process, job)
	if err != nil {
		return 0, errors.Join(
			fmt.Errorf("verify owned process %d membership: %w", processID, err),
			wrapWindowsCloseError("owned process handle", windows.CloseHandle(process)),
		)
	}
	if !inJob {
		return 0, errors.Join(
			fmt.Errorf("owned process %d left the job before termination", processID),
			wrapWindowsCloseError("owned process handle", windows.CloseHandle(process)),
		)
	}
	return process, nil
}

func waitForWindowsTrackedProcesses(processes []windowsTrackedProcess, deadline time.Time) error {
	var waitErr error
	for _, process := range processes {
		if err := waitForWindowsTrackedProcess(process, deadline); err != nil {
			waitErr = errors.Join(waitErr, err)
		}
	}
	return waitErr
}

func waitForWindowsTrackedProcess(process windowsTrackedProcess, deadline time.Time) error {
	for {
		remaining := time.Until(deadline)
		waitMilliseconds := uint32(0)
		if remaining > 0 {
			waitDuration := (remaining + time.Millisecond - 1) / time.Millisecond
			if waitDuration >= time.Duration(windows.INFINITE) {
				waitDuration = time.Duration(windows.INFINITE - 1)
			}
			waitMilliseconds = uint32(waitDuration)
		}
		state, err := windows.WaitForSingleObject(process.handle, waitMilliseconds)
		if err != nil {
			return fmt.Errorf("wait for owned process %d exit: %w", process.processID, err)
		}
		switch state {
		case uint32(windows.WAIT_OBJECT_0):
			return nil
		case uint32(windows.WAIT_TIMEOUT):
			if time.Now().Before(deadline) && waitMilliseconds == windows.INFINITE-1 {
				continue
			}
			return fmt.Errorf("owned process %d exit was not confirmed before the cleanup deadline", process.processID)
		default:
			return fmt.Errorf("wait for owned process %d exit returned state 0x%x", process.processID, state)
		}
	}
}

func closeWindowsTrackedProcesses(processes []windowsTrackedProcess) error {
	var closeErr error
	for _, process := range processes {
		if err := windows.CloseHandle(process.handle); err != nil {
			closeErr = errors.Join(closeErr, fmt.Errorf(
				"close owned process %d confirmation handle: %w",
				process.processID,
				err,
			))
		}
	}
	return closeErr
}

func queryWindowsJobAccounting(job windows.Handle) (windowsJobAccounting, error) {
	var accounting windowsJobAccounting
	if err := windows.QueryInformationJobObject(
		job,
		windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&accounting)),
		uint32(unsafe.Sizeof(accounting)),
		nil,
	); err != nil {
		return windowsJobAccounting{}, err
	}
	return accounting, nil
}

func windowsJobProcessIDs(job windows.Handle, expected uint32) ([]uint32, error) {
	capacity := uint64(expected)
	if capacity == 0 {
		capacity = 1
	}
	headerSize := uint64(unsafe.Sizeof(jobProcessIDListHeader{}))
	processIDSize := uint64(unsafe.Sizeof(uintptr(0)))
	maximumInt := uint64(^uint(0) >> 1)
	bufferSize := headerSize + capacity*processIDSize
	if bufferSize < headerSize || bufferSize > uint64(^uint32(0)) || bufferSize > maximumInt {
		return nil, fmt.Errorf("owned job process list size overflows: active=%d", expected)
	}
	buffer := make([]byte, int(bufferSize))
	queryErr := windows.QueryInformationJobObject(
		job,
		windows.JobObjectBasicProcessIdList,
		uintptr(unsafe.Pointer(&buffer[0])),
		uint32(len(buffer)),
		nil,
	)
	if queryErr != nil && !errors.Is(queryErr, windows.ERROR_MORE_DATA) {
		return nil, fmt.Errorf("query owned job process list: %w", queryErr)
	}

	header := (*jobProcessIDListHeader)(unsafe.Pointer(&buffer[0]))
	listed := uint64(header.numberInList)
	var listErr error
	if queryErr != nil {
		listErr = errors.Join(listErr, fmt.Errorf("query owned job process list: %w", queryErr))
	}
	if listed > capacity {
		listErr = errors.Join(listErr, fmt.Errorf(
			"owned job process list exceeds its buffer: listed=%d capacity=%d",
			header.numberInList,
			capacity,
		))
		listed = capacity
	}
	if header.numberAssigned != header.numberInList {
		listErr = errors.Join(listErr, fmt.Errorf(
			"owned job process list changed during capture: assigned=%d listed=%d",
			header.numberAssigned,
			header.numberInList,
		))
	}

	rawIDs := unsafe.Slice(
		(*uintptr)(unsafe.Add(unsafe.Pointer(&buffer[0]), uintptr(headerSize))),
		int(listed),
	)
	processIDs := make([]uint32, 0, len(rawIDs))
	for _, rawID := range rawIDs {
		processID := uint32(rawID)
		if rawID == 0 || uintptr(processID) != rawID {
			listErr = errors.Join(listErr, fmt.Errorf("owned job returned invalid process identifier %d", rawID))
			continue
		}
		processIDs = append(processIDs, processID)
	}
	return processIDs, listErr
}

func windowsProcessInJob(process, job windows.Handle) (bool, error) {
	var result uint32
	callResult, _, callErr := isProcessInJob.Call(
		uintptr(process),
		uintptr(job),
		uintptr(unsafe.Pointer(&result)),
	)
	if callResult != 0 {
		return result != 0, nil
	}
	if errors.Is(callErr, windows.ERROR_SUCCESS) {
		return false, errors.New("IsProcessInJob returned failure without a Windows error")
	}
	return false, callErr
}

// CloseOwnedProcess releases the job handle and reports whether the handle was
// closed.
func CloseOwnedProcess(owner *ProcessOwner) error {
	if owner == nil || owner.job == 0 {
		return nil
	}
	handle := owner.job
	owner.job = 0
	if err := windows.CloseHandle(handle); err != nil {
		return fmt.Errorf("close owned job handle: %w", err)
	}
	return nil
}
