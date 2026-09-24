package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// RefSnapshot summarizes a repository from one ref listing. A page that lists
// many repositories uses it instead of Summary plus a commit read, so it starts
// one Git process per repository rather than four, and none while the cached
// snapshot is current (see snapshot_cache.go).
type RefSnapshot struct {
	Summary Summary
	// Head holds the OID, author name, author date, and subject of the default
	// branch tip. HeadFound is false when the default branch is missing or
	// does not point to a commit.
	Head      Commit
	HeadFound bool
	// ActivityKey equals the Key of an Activity observation of the same refs.
	ActivityKey string
}

// snapshotFormat prints each ref, and for the ref HEAD points to, the tip
// commit fields the dashboard shows.
const snapshotFormat = "--format=%(refname)%00%(objectname)%00%(objecttype)%00%(HEAD)%(if)%(HEAD)%(then)%00%(authorname)%00%(authordate:iso-strict)%00%(subject)%(end)"

// readRefSnapshot lists the refs of the repository at repositoryPath. The
// caller holds the repository's read lock.
//
// complete is false when a follow-up read failed: the git log read of the
// default branch tip, or the symbolic-ref read of HEAD. The snapshot is then
// returned without those fields, as before, but must not be cached, since the
// failure may be transient (a canceled request, a deadline, a network share
// hiccup). symbolic-ref --quiet exiting with status 1 while ctx is still live
// is a definitive answer that HEAD is not a symbolic ref (a detached HEAD),
// so that result is complete. Any other symbolic-ref failure, including a
// canceled or expired ctx, another exit status, or a failure to start Git,
// makes the result incomplete.
func (m *Manager) readRefSnapshot(ctx context.Context, repositoryPath string) (snapshot RefSnapshot, complete bool, err error) {
	result, err := m.Git.Run(ctx, repositoryPath, nil, "--git-dir", ".", "for-each-ref", snapshotFormat,
		"refs/heads", "refs/tags", "refs/owngit/retained", "refs/owngit/provenance/heads")
	if err != nil {
		return RefSnapshot{}, false, err
	}
	complete = true
	summary := &snapshot.Summary
	hasRetained := false
	var keyed []activityKeyRef
	for _, line := range bytes.Split(bytes.TrimSpace(result.Stdout), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		parts := bytes.SplitN(line, []byte{0}, 7)
		if len(parts) != 4 && len(parts) != 7 {
			return RefSnapshot{}, false, errors.New("Git returned a malformed ref record")
		}
		name, oid, objectType := string(parts[0]), string(parts[1]), string(parts[2])
		if activityKeyRefName(name) {
			keyed = append(keyed, activityKeyRef{name: name, oid: oid})
		}
		switch {
		case strings.HasPrefix(name, "refs/heads/"):
			branch := Ref{Name: strings.TrimPrefix(name, "refs/heads/"), OID: oid, Type: objectType}
			summary.Branches = append(summary.Branches, branch)
			if string(parts[3]) == "*" && len(parts) == 7 {
				summary.DefaultBranch, summary.DefaultOID = branch.Name, oid
				if authored, err := time.Parse(time.RFC3339, string(parts[5])); err == nil && objectType == "commit" {
					snapshot.Head = Commit{OID: oid, AuthorName: string(parts[4]), AuthoredAt: authored, Subject: string(parts[6])}
					snapshot.HeadFound = true
				}
			}
		case strings.HasPrefix(name, "refs/tags/"):
			summary.Tags = append(summary.Tags, Ref{Name: strings.TrimPrefix(name, "refs/tags/"), OID: oid, Type: objectType})
		case strings.HasPrefix(name, "refs/owngit/retained/"):
			hasRetained = true
		}
	}
	summary.Empty = len(summary.Branches) == 0 && len(summary.Tags) == 0 && !hasRetained
	snapshot.ActivityKey = activityKey(keyed)
	if snapshot.HeadFound && !sameAsLog(snapshot.Head) {
		metadata, err := commitMetadataByOID(ctx, m.Git, repositoryPath, []string{snapshot.Head.OID})
		snapshot.Head, snapshot.HeadFound = metadata[snapshot.Head.OID], err == nil
		complete = err == nil
	}
	if summary.DefaultBranch == "" {
		// HEAD names no existing branch. Read it only in this case, so a
		// missing default branch is still reported by name.
		head, err := m.Git.Run(ctx, repositoryPath, nil, "--git-dir", ".", "symbolic-ref", "--quiet", "HEAD")
		if full := strings.TrimSpace(string(head.Stdout)); err == nil && strings.HasPrefix(full, "refs/heads/") {
			summary.DefaultBranch = strings.TrimPrefix(full, "refs/heads/")
		}
		if err != nil && !gitAnsweredNo(ctx, err) {
			complete = false
		}
	}
	return snapshot, complete, nil
}

type activityKeyRef struct {
	name string
	oid  string
}

// activityKeyRefName reports whether a ref is an input to Activity: current
// branches, retained branch history, and its provenance.
func activityKeyRefName(name string) bool {
	return strings.HasPrefix(name, "refs/heads/") || strings.HasPrefix(name, "refs/owngit/retained/heads/") || strings.HasPrefix(name, "refs/owngit/provenance/heads/")
}

// activityKey digests the Activity input refs. Commits are immutable, so equal
// keys mean an equal observation for the same limit.
func activityKey(refs []activityKeyRef) string {
	sorted := make([]activityKeyRef, 0, len(refs))
	for _, ref := range refs {
		if activityKeyRefName(ref.name) {
			sorted = append(sorted, ref)
		}
	}
	sort.Slice(sorted, func(left, right int) bool { return sorted[left].name < sorted[right].name })
	digest := sha256.New()
	for _, ref := range sorted {
		digest.Write([]byte(ref.name))
		digest.Write([]byte{0})
		digest.Write([]byte(ref.oid))
		digest.Write([]byte{'\n'})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// sameAsLog reports whether for-each-ref's head fields certainly equal git
// log's. for-each-ref prints raw bytes, while git log converts a commit with
// an encoding header to UTF-8. When folding a multi-line subject, git log
// also trims whitespace at the end of each line, which for-each-ref keeps, so
// whitespace before a joining space or at the end may differ. Such a commit is
// read again with git log, as Commits reads it.
func sameAsLog(commit Commit) bool {
	subject := commit.Subject
	return utf8.ValidString(commit.AuthorName) && utf8.ValidString(subject) &&
		!strings.Contains(subject, "  ") && !strings.Contains(subject, "\t ") &&
		!strings.HasSuffix(subject, " ") && !strings.HasSuffix(subject, "\t")
}
