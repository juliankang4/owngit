package gitexec

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// A descendant that starts a new session leaves the owned process group. On
// Linux every process of an owned run carries an environment variable that
// names the run, so ending the run can find such a descendant by reading
// /proc. macOS gives no such view of system binaries, so there only the
// process group contains a run.

// ownedRunVariable starts the name of the environment variable that marks one
// owned run. The name is unique per run, so a run nested in another keeps the
// mark of the outer one.
const ownedRunVariable = "OWNGIT_OWNED_RUN_"

// ownedRunMark returns the environment entry for token.
func ownedRunMark(token string) string { return ownedRunVariable + token + "=1" }

// markOwnedRun puts a new mark last in the environment of cmd.
func markOwnedRun(cmd *exec.Cmd) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return // Without a mark the process group alone contains the run.
	}
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, ownedRunMark(hex.EncodeToString(random)))
}

// ownedRunToken returns the mark that markOwnedRun put last in the
// environment, or an empty string when there is none.
func ownedRunToken(cmd *exec.Cmd) string {
	if n := len(cmd.Env); n > 0 {
		if token, ok := strings.CutPrefix(cmd.Env[n-1], ownedRunVariable); ok {
			return strings.TrimSuffix(token, "=1")
		}
	}
	return ""
}

// markedProcessBound bounds the whole cleanup of the marked processes of one
// run, from its start: a killed process stays visible until the kernel
// finishes its exit, and a descendant may keep starting new ones. A variable
// only so a test can shorten it.
var markedProcessBound = 5 * time.Second

const (
	// unknownProcessBound bounds the wait for a process of this account whose
	// environment cannot be read yet, for example one in the middle of exec.
	// Past it the process is skipped, like a non-dumpable one.
	unknownProcessBound = time.Second
)

// procDirectory is where processes are listed. A variable only so a test can
// make the list unreadable.
var procDirectory = "/proc"

// listMarkedProcesses is markedProcesses; a variable only so a test can start a
// marked process before each scan, as a descendant that never stops would.
var listMarkedProcesses = markedProcesses

var warnProcessListOnce sync.Once

// killMarkedProcesses ends every process of this account whose environment
// carries the mark of one owned run and waits until none is left. It signals
// each process once and scans again until no marked process remains, so a
// process that is still exiting is waited for, not counted again, and one
// started during the scan is found. It fails when signalling fails, or when
// marked processes are still there when markedProcessBound, counted from the
// start, has passed, however many new ones keep appearing. When the process
// list cannot be read, the run keeps what the process group gave it: a warning
// is logged once and no error returned.
func killMarkedProcesses(token string, since uint64) error {
	if token == "" {
		return nil
	}
	var failures error
	signalled := map[int]bool{}
	start := time.Now()
	signalNew := func(pids []int) {
		for _, pid := range pids {
			if signalled[pid] {
				continue
			}
			if err := killMarkedProcess(pid, token); err == nil {
				signalled[pid] = true
			} else if !errors.Is(err, errNotMarked) {
				signalled[pid] = true
				failures = errors.Join(failures, fmt.Errorf("kill process %d of the owned run: %w", pid, err))
			}
		}
	}
	for {
		pids, unknown, err := listMarkedProcesses(token, since)
		if err != nil {
			warnProcessListOnce.Do(func() {
				log.Printf("OwnGit could not read %s (%v); only the process group is stopped, so descendants that left it are not checked", procDirectory, err)
			})
			return failures
		}
		signalNew(pids)
		elapsed := time.Since(start)
		if elapsed > markedProcessBound {
			// The last sweep: signal what is new once, then count exactly what
			// is still there. A failed listing is a failed cleanup.
			fresh, _, err := listMarkedProcesses(token, since)
			if err != nil {
				return errors.Join(failures, fmt.Errorf("list the processes of the owned run: %w", err))
			}
			signalNew(fresh)
			survivors, _, err := listMarkedProcesses(token, since)
			if err != nil {
				return errors.Join(failures, fmt.Errorf("list the processes of the owned run: %w", err))
			}
			if len(survivors) > 0 {
				failures = errors.Join(failures, fmt.Errorf("%d processes of the owned run are still running", len(survivors)))
			}
			return failures
		}
		if len(pids) == 0 && !(unknown > 0 && elapsed < unknownProcessBound) {
			return failures
		}
		time.Sleep(time.Millisecond)
	}
}

// errNotMarked says the process no longer carries the mark, so it is not a
// process of the run (its PID was reused, or it is in the middle of exec).
var errNotMarked = errors.New("process does not carry the mark of the run")

// killMarkedProcess kills one process after checking again that it carries the
// mark, so a reused PID is not signalled. It opens a pidfd first, so the check
// and the signal address the same process. Without pidfd support (older
// kernels) the check comes right before a plain kill, which leaves a window
// that only a PID reused within microseconds could use.
func killMarkedProcess(pid int, token string) error {
	needle := []byte("\x00" + ownedRunMark(token) + "\x00")
	fd, err := unix.PidfdOpen(pid, 0)
	switch {
	case err == nil:
		defer unix.Close(fd)
	case err == syscall.ESRCH:
		return nil
	case err != syscall.ENOSYS && err != syscall.EINVAL && err != syscall.EPERM:
		return err
	default:
		fd = -1
	}
	environment, err := os.ReadFile(procDirectory + "/" + strconv.Itoa(pid) + "/environ")
	if err != nil || !bytes.Contains(append(append([]byte{0}, environment...), 0), needle) {
		return errNotMarked
	}
	if fd >= 0 {
		err = unix.PidfdSendSignal(fd, syscall.SIGKILL, nil, 0)
	} else {
		err = syscall.Kill(pid, syscall.SIGKILL)
	}
	if err == syscall.ESRCH {
		return nil
	}
	return err
}

// markedProcesses lists the processes whose environment carries the mark of one
// owned run, and counts the processes of this account that could not be
// decided yet: the environment of a live process is empty or unreadable while
// it executes a program. Only a process that started at or after the run's
// first process (since) can be one. A zombie, a kernel thread, an older
// process, a process of another account and a process that is not dumpable,
// whose environment the kernel hides, are skipped, so the run cannot end the
// last of these.
func markedProcesses(token string, since uint64) (pids []int, unknown int, err error) {
	needle := []byte("\x00" + ownedRunMark(token) + "\x00")
	entries, err := os.ReadDir(procDirectory)
	if err != nil {
		return nil, 0, err
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		directory := procDirectory + "/" + entry.Name()
		environment, err := os.ReadFile(directory + "/environ")
		if err == nil && bytes.Contains(append(append([]byte{0}, environment...), 0), needle) {
			pids = append(pids, pid)
			continue
		}
		if (err != nil || len(environment) == 0) && couldBelongToRun(directory, since) {
			unknown++
		}
	}
	return pids, unknown, nil
}

// statFields returns the fields of /proc/PID/stat that follow the command
// name, starting with the state (field 3 of proc(5)).
func statFields(directory string) []string {
	stat, err := os.ReadFile(directory + "/stat")
	if err != nil {
		return nil
	}
	end := bytes.LastIndexByte(stat, ')')
	if end < 0 {
		return nil
	}
	return strings.Fields(string(stat[end+1:]))
}

// processStartTime returns the start time of a process in clock ticks since
// boot (field 22), or zero when it cannot be read.
func processStartTime(pid int) uint64 {
	fields := statFields(procDirectory + "/" + strconv.Itoa(pid))
	if len(fields) < 20 {
		return 0
	}
	started, _ := strconv.ParseUint(fields[19], 10, 64)
	return started
}

// pfKthread is the PF_KTHREAD bit of the process flags.
const pfKthread = 0x00200000

// couldBelongToRun reports whether the process directory is a live process of
// this account that started at or after the run's first process and is not a
// kernel thread. Only such a process can be a descendant whose environment is
// not readable yet; an unrelated process with an empty environment is not.
func couldBelongToRun(directory string, since uint64) bool {
	info, err := os.Stat(directory)
	if err != nil {
		return false
	}
	if owner, ok := info.Sys().(*syscall.Stat_t); !ok || int(owner.Uid) != os.Getuid() {
		return false
	}
	fields := statFields(directory)
	if len(fields) < 20 || strings.ContainsAny(fields[0], "ZXx") {
		return false
	}
	flags, _ := strconv.ParseUint(fields[6], 10, 64)
	started, _ := strconv.ParseUint(fields[19], 10, 64)
	return flags&pfKthread == 0 && started >= since
}
