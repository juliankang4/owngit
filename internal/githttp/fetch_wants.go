package githttp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"owngit/internal/gitexec"
)

// A fetch serves only what the advertised refs reach. Git's own check cannot
// say that: version 0 applies it to commits only, and version 2 applies none.
// So OwnGit reads the want lines of every upload-pack request (fetchWantsGate)
// before Git sees them and refuses a want that is not an advertised ref's
// tip and not a commit an advertised ref reaches.

// maximumFetchRequest bounds the part of a fetch request OwnGit holds in
// memory: everything up to the first flush packet. In version 0 that ends the
// want list (the haves follow it); in version 2 it ends the whole command.
// It is the size git-http-backend accepts for a request too, and the
// whole request is also bound by Limits.MaximumRequest.
var maximumFetchRequest = 10 << 20

// fetchCheckHook, when set by a test, runs before the wants are checked.
var fetchCheckHook func()

var (
	// errFetchRequest reports a fetch request OwnGit cannot read as Git's
	// request format, such as a bad packet length or want line.
	errFetchRequest = errors.New("invalid Git fetch request")
	// errFetchTooLarge reports a want and have list over maximumFetchRequest.
	errFetchTooLarge = errors.New("request exceeded the Git backend request buffer")
	// errFetchCheck reports a failure to check the wants, not a refusal.
	errFetchCheck = errors.New("could not check the Git fetch request")
)

// errNotOurRef reports a want that no advertised ref reaches. It names the
// object, as Git's own refusal does.
type errNotOurRef struct{ oid string }

func (e errNotOurRef) Error() string { return "not our ref " + e.oid }

// fetchScope says which refs a fetch address advertises.
type fetchScope struct {
	// patterns limit the refs read; empty reads every ref.
	patterns []string
	// hidden is a ref name prefix that is not advertised, or empty.
	hidden string
	// reaching are the rev-list arguments that select the same refs.
	reaching []string
}

var (
	// mainScope is the owner's address: every ref except kept history.
	mainScope = &fetchScope{hidden: "refs/owngit/", reaching: []string{"--exclude=refs/owngit/*", "--all"}}
	// branchesAndTags is a share link's address (see readOnlyConfig).
	branchesAndTags = &fetchScope{patterns: []string{"refs/heads/", "refs/tags/"}, reaching: []string{"--branches", "--tags"}}
)

// usesProtocolV2 reports whether Git reads the request in protocol version 2.
// Git takes the highest "version=" of the Git-Protocol header.
func usesProtocolV2(header string) bool {
	for _, field := range strings.Split(header, ":") {
		if field == "version=2" {
			return true
		}
	}
	return false
}

// fetchWantsGate passes an upload-pack request to Git. It holds the request
// back until the first flush packet, and gives the wants to check first.
// Wants come before that flush in version 0, and in version 2 within the
// arguments of the one command the request carries, which must be the first
// packet. When the request is not one OwnGit reads, ends before its flush, or
// check fails, Read returns the error and Git sees nothing.
type fetchWantsGate struct {
	io.ReadCloser
	v2    bool
	check func(wants []string) error
	// passed, when set, runs once the check has succeeded.
	passed func()

	head  bytes.Buffer // the packets read so far
	ready []byte       // bytes checked, not yet returned
	done  bool         // the wants were checked; pass the rest through
}

func (gate *fetchWantsGate) Read(buffer []byte) (int, error) {
	if !gate.done {
		if err := gate.readWants(); err != nil {
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

// readWants reads packets up to the flush, then checks the wants.
func (gate *fetchWantsGate) readWants() error {
	var wants []string
	command := ""
	for packets := 0; ; packets++ {
		start := gate.head.Len()
		if _, err := io.CopyN(&gate.head, gate.ReadCloser, 4); err != nil {
			if err == io.EOF {
				// The request ended before its flush.
				return errFetchRequest
			}
			return err
		}
		length, err := strconv.ParseUint(string(gate.head.Bytes()[start:]), 16, 16)
		if err != nil || length == 2 || length == 3 {
			return errFetchRequest
		}
		if length == 0 {
			break
		}
		if length == 1 {
			continue
		}
		if gate.head.Len()+int(length) > maximumFetchRequest {
			return errFetchTooLarge
		}
		if _, err := io.CopyN(&gate.head, gate.ReadCloser, int64(length)-4); err != nil {
			if err == io.EOF {
				return errFetchRequest
			}
			return err
		}
		line := strings.TrimSuffix(string(gate.head.Bytes()[start+4:]), "\n")
		switch {
		case strings.HasPrefix(line, "command="):
			// Git takes the command from the first packet of version 2, and
			// a second one is not a command OwnGit knows how to read.
			if command != "" || packets > 0 {
				return errFetchRequest
			}
			command = strings.TrimPrefix(line, "command=")
		case strings.HasPrefix(line, "want-ref "):
			return errFetchRequest
		case strings.HasPrefix(line, "want "):
			// Version 0 follows the first want with capabilities.
			oid, _, _ := strings.Cut(strings.TrimPrefix(line, "want "), " ")
			oid, _, _ = strings.Cut(oid, "\x00")
			if !validObjectID(oid) {
				return errFetchRequest
			}
			wants = append(wants, strings.ToLower(oid))
		}
	}
	if gate.v2 && command != "fetch" && command != "ls-refs" {
		return errFetchRequest
	}
	if fetchCheckHook != nil {
		fetchCheckHook()
	}
	if err := gate.check(wants); err != nil {
		return err
	}
	if gate.passed != nil {
		gate.passed()
	}
	gate.ready, gate.done = gate.head.Bytes(), true
	return nil
}

func validObjectID(oid string) bool {
	if len(oid) != 40 && len(oid) != 64 {
		return false
	}
	for _, c := range []byte(oid) {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// checkWants returns errNotOurRef for the first want that is neither the tip
// of a ref scope advertises nor a commit those refs reach, and errFetchCheck
// when it cannot tell. Tags, trees and blobs are served only as ref tips.
// The caller holds the repository read lock.
func (h *Handler) checkWants(ctx context.Context, repositoryPath string, scope *fetchScope, wants []string) error {
	if len(wants) == 0 {
		return nil
	}
	limits := gitexec.CommandLimits{OutputLimit: 64 << 20}
	git := func(stdin io.Reader, args ...string) ([]byte, error) {
		result, err := h.Repositories.Git.RunWithLimits(ctx, repositoryPath, stdin, limits, append([]string{"--git-dir", "."}, args...)...)
		if err != nil {
			return nil, fmt.Errorf("%w: git %s: %w", errFetchCheck, args[0], err)
		}
		return result.Stdout, nil
	}
	refs, err := git(nil, append([]string{"for-each-ref", "--format=%(objectname) %(refname)"}, scope.patterns...)...)
	if err != nil {
		return err
	}
	tips := map[string]bool{}
	for _, line := range strings.Split(string(refs), "\n") {
		if oid, name, ok := strings.Cut(line, " "); ok && (scope.hidden == "" || !strings.HasPrefix(name, scope.hidden)) {
			tips[oid] = true
		}
	}
	// HEAD is advertised too, and is a branch except when detached.
	if head, err := h.Repositories.Git.RunWithLimits(ctx, repositoryPath, nil, limits, "--git-dir", ".", "rev-parse", "--verify", "--quiet", "HEAD"); err == nil {
		tips[strings.TrimSpace(string(head.Stdout))] = true
	}
	var others []string
	seen := map[string]bool{}
	for _, oid := range wants {
		if !tips[oid] && !seen[oid] {
			others = append(others, oid)
		}
		seen[oid] = true
	}
	if len(others) == 0 {
		return nil
	}
	kinds, err := git(strings.NewReader(strings.Join(others, "\n")+"\n"), "cat-file", "--batch-check=%(objecttype)")
	if err != nil {
		return err
	}
	types := strings.Split(strings.TrimSuffix(string(kinds), "\n"), "\n")
	if len(types) != len(others) {
		return fmt.Errorf("%w: unexpected cat-file answer", errFetchCheck)
	}
	for i, oid := range others {
		if types[i] != "commit" {
			return errNotOurRef{oid}
		}
	}
	// One walk for all of them, as Git's own check does: rev-list prints a
	// commit only when no advertised ref reaches it.
	unreached, err := git(strings.NewReader(strings.Join(others, "\n")+"\n"), append([]string{"rev-list", "--max-count=1", "--stdin", "--not"}, scope.reaching...)...)
	if err != nil {
		return err
	}
	if found := strings.TrimSpace(string(unreached)); found != "" {
		// The commit printed can be an ancestor of the want, whose ID is
		// not the requester's to learn.
		for _, oid := range others {
			if oid == found {
				return errNotOurRef{oid}
			}
		}
		return errNotOurRef{others[0]}
	}
	return nil
}

// answerNotOurRef sends Git's own answer to a want it does not serve: an
// ERR packet, which both protocol versions show as a remote error.
func answerNotOurRef(writer *responseState, oid string) {
	message := "ERR upload-pack: not our ref " + oid
	writer.Header().Set("Content-Type", "application/x-git-upload-pack-result")
	writer.Header().Set("Cache-Control", "no-cache, max-age=0, must-revalidate")
	writer.WriteHeader(200)
	_, _ = fmt.Fprintf(writer, "%04x%s", len(message)+4, message)
	_ = writer.finish()
}
