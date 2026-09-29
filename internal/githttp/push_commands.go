package githttp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"owngit/internal/gitexec"
	"owngit/internal/repository"
)

// A push request starts with its command list: optional "shallow" lines,
// then one pkt-line per ref update ("old new name", the first one followed
// by NUL and the capabilities), then a flush packet; the pack follows.
// receive-pack runs the update hook only after it has read the flush packet.

// maximumPushCommands bounds the command list OwnGit holds in memory. It
// holds tens of thousands of ref updates.
var maximumPushCommands = 16 << 20

// errPushRequest reports a push request that is not a plain command list.
var errPushRequest = errors.New("invalid Git push request")

// nameConflictGate passes a push request to Git. It holds back the flush
// packet that ends the command list until it has written, to the file the
// update hook reads (OWNGIT_NAME_CONFLICTS_FILE), every ref of the push
// whose name a file system can treat as the same as another ref's name in
// the repository or in the push (repository.RefNameConflicts). The hook
// refuses creating, changing or deleting those refs. When the command list
// is not one OwnGit reads, such as a signed push or one over
// maximumPushCommands, the gate removes the file, which makes the hook
// refuse every ref, and passes the request on unchanged.
type nameConflictGate struct {
	io.ReadCloser
	check     func([]string) error
	refuseAll func()

	commands bytes.Buffer // the command list read so far
	names    []string
	ready    []byte // bytes read and checked, not yet returned
	done     bool   // the command list has ended; pass the rest through
}

func (gate *nameConflictGate) Read(buffer []byte) (int, error) {
	for !gate.done && len(gate.ready) == 0 {
		start := gate.commands.Len()
		err := gate.readPacket()
		if errors.Is(err, errPushRequest) {
			gate.refuseAll()
			gate.ready, gate.done = gate.commands.Bytes()[start:], true
			break
		}
		if err == io.EOF {
			// Git reads the request as it ended, and refuses it.
			gate.ready, gate.done = gate.commands.Bytes()[start:], true
			break
		}
		if err != nil {
			return 0, err
		}
	}
	if len(gate.ready) > 0 {
		n := copy(buffer, gate.ready)
		gate.ready = gate.ready[n:]
		return n, nil
	}
	return gate.ReadCloser.Read(buffer)
}

// readPacket reads one pkt-line of the command list and makes it ready. At
// the flush packet it first checks the names.
func (gate *nameConflictGate) readPacket() error {
	start := gate.commands.Len()
	if _, err := io.CopyN(&gate.commands, gate.ReadCloser, 4); err != nil {
		return err
	}
	length, err := strconv.ParseUint(string(gate.commands.Bytes()[start:]), 16, 16)
	if err != nil {
		return errPushRequest
	}
	if length == 0 {
		if err := gate.check(gate.names); err != nil {
			return err
		}
		gate.ready, gate.done = gate.commands.Bytes()[start:], true
		return nil
	}
	if length < 5 || gate.commands.Len()+int(length) > maximumPushCommands {
		return errPushRequest
	}
	if _, err := io.CopyN(&gate.commands, gate.ReadCloser, int64(length)-4); err != nil {
		return err
	}
	packet := gate.commands.Bytes()[start:]
	line, _, _ := strings.Cut(strings.TrimSuffix(string(packet[4:]), "\n"), "\x00")
	if !strings.HasPrefix(line, "shallow ") {
		fields := strings.SplitN(line, " ", 3)
		if len(fields) != 3 || fields[2] == "" {
			return errPushRequest
		}
		gate.names = append(gate.names, fields[2])
	}
	gate.ready = packet
	return nil
}

// nameConflictFile creates the empty file a push's update hook reads and
// returns the variable that names it and a function that removes it.
func (h *Handler) nameConflictFile() (string, string, func(), error) {
	file, err := os.CreateTemp(h.Git.TempDir, "owngit-name-conflicts-*")
	if err != nil {
		return "", "", nil, err
	}
	path := file.Name()
	remove := func() { _ = os.Remove(path) }
	if err := file.Close(); err != nil {
		remove()
		return "", "", nil, err
	}
	return path, "OWNGIT_NAME_CONFLICTS_FILE=" + filepath.ToSlash(path), remove, nil
}

// writeNameConflicts writes to path, one per line, the names that share
// their repository.RefNameKey with another ref of the repository or
// with another of names. The caller holds the repository write lock.
func (h *Handler) writeNameConflicts(ctx context.Context, repositoryPath, path string, names []string) error {
	if len(names) == 0 {
		return nil
	}
	result, err := h.Repositories.Git.RunWithLimits(ctx, repositoryPath, nil, gitexec.CommandLimits{OutputLimit: 64 << 20},
		"--git-dir", ".", "for-each-ref", "--format=%(refname)")
	if err != nil {
		return fmt.Errorf("read repository refs: %w", err)
	}
	existing := strings.Split(strings.TrimSuffix(string(result.Stdout), "\n"), "\n")
	var refused strings.Builder
	for name := range repository.RefNameConflicts(existing, names) {
		refused.WriteString(name + "\n")
	}
	return os.WriteFile(path, []byte(refused.String()), 0o600)
}
