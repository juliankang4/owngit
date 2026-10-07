package importsync

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"owngit/internal/checksource"
	"owngit/internal/gitexec"
	"owngit/internal/importgit"
	"owngit/internal/repository"
)

// lfsInspection records what one bounded Git LFS pointer scan actually
// covered. Complete is false when any bound cut the reachable object set, the
// type list, or the candidate set.
type lfsInspection struct {
	Objects             int64
	ObjectListTruncated bool
	Candidates          int64
	CandidatesTruncated bool
	BytesRead           int64
	BytesTruncated      bool
	Pointers            int64
	Complete            bool
	Examples            []string
}

// createStagingRepository prepares the fresh bare repository that receives the
// pack. The object format must match the advertisement exactly.
func (s *Service) createStagingRepository(ctx context.Context, path, objectFormat string, limits Limits) error {
	arguments := []string{"init", "--bare", "--initial-branch=main"}
	if objectFormat == importgit.FormatSHA256 {
		arguments = append(arguments, "--object-format=sha256")
	}
	arguments = append(arguments, ".")
	if _, err := s.Repositories.Git.RunWithLimits(ctx, path, nil, limits.commandLimits(limits.IndexTimeout), arguments...); err != nil {
		return newProblem(CodeIndexFailed, "staging repository could not be created", err)
	}
	format, err := s.Repositories.ObjectFormat(ctx, path)
	if err != nil {
		return newProblem(CodeIndexFailed, "staging repository format could not be read", err)
	}
	if format != objectFormat {
		return newProblem(CodeUnsupportedFormat, fmt.Sprintf("source is %s but the staging repository is %s", objectFormat, format), nil)
	}
	return nil
}

// historicFormatWarnings are the fsck message IDs that strict indexing
// reports as warnings instead of refusing the pack. Each one is a spelling
// fault that old versions of Git wrote into popular public repositories. It
// changes neither a path, a file type, a link nor a submodule, so accepting it
// cannot place a file or reach outside the repository. Every other check,
// including the .git, .gitmodules and symbolic link checks, still refuses the
// pack. A date these two let through that Git cannot render at all is refused
// later by verifyCommitDates, before publication.
//
//   - badTimezone: an author or committer offset such as +051800 (rails/rails
//     commit 4cf94979, psf/requests commit 5e6ecdad).
//   - missingSpaceBeforeDate: a tagger line without a date (30 tags in
//     coreutils/coreutils, such as v4.5.1).
//   - zeroPaddedFilemode: a directory mode written 040000 instead of 40000,
//     which still means a directory (141 trees in rails/rails). Git itself
//     only warns about it; --strict alone would refuse it.
var historicFormatWarnings = []string{"badTimezone", "missingSpaceBeforeDate", "zeroPaddedFilemode"}

// strictIndexPackArguments is shared by staging and destination indexing so a
// pack cannot pass through either object store without Git's strict checks,
// apart from historicFormatWarnings. The keep message is written into the
// pack's .keep file, which protects the new pack from a concurrent repack
// until refs use it. A .keep file left by a crash therefore names the OwnGit
// import that created it.
func strictIndexPackArguments(keepMessage string) []string {
	strict := "--strict=" + strings.Join(historicFormatWarnings, "=warn,") + "=warn"
	return []string{"--git-dir", ".", "index-pack", "--stdin", strict, "--keep=" + keepMessage}
}

// createdPackKeep returns the pack hash when index-pack reported that it
// created the pack's .keep file ("keep\t<hash>"). Git reports "pack\t<hash>"
// instead when the .keep file already existed, and that file belongs to
// someone else, so it is never returned here.
func createdPackKeep(stdout []byte) (string, bool) {
	line, _, _ := bytes.Cut(stdout, []byte{'\n'})
	hash, found := bytes.CutPrefix(line, []byte("keep\t"))
	if !found || (len(hash) != 40 && len(hash) != 64) || !isLowerHexString(string(hash)) {
		return "", false
	}
	return string(hash), true
}

// indexStagingPack streams the pack into the staging object store. Strict
// indexing validates object contents, pack structure, and delta resolution
// before anything is verified or copied to the destination.
func (s *Service) indexStagingPack(ctx context.Context, stagingPath string, reader io.Reader, limits Limits) error {
	// The staging directory is discarded whole, so its .keep file needs no
	// separate cleanup.
	if _, err := s.Repositories.Git.RunWithLimits(ctx, stagingPath, reader, limits.commandLimits(limits.IndexTimeout), strictIndexPackArguments("owngit import staging")...); err != nil {
		return newProblem(CodeIndexFailed, "pack indexing failed", err)
	}
	return nil
}

// boundedCommand runs one bounded command and reports whether an output limit
// truncated the captured prefix.
func (s *Service) boundedCommand(ctx context.Context, dir string, stdin io.Reader, limits gitexec.CommandLimits, args ...string) ([]byte, bool, error) {
	result, err := s.Repositories.Git.RunWithLimits(ctx, dir, stdin, limits, args...)
	if err == nil {
		return result.Stdout, false, nil
	}
	var limitError *gitexec.LimitError
	if errors.As(err, &limitError) {
		return result.Stdout, true, nil
	}
	return result.Stdout, false, err
}

// verifyStaging proves the staged snapshot is exactly what the advertisement
// promised before the destination is touched.
func (s *Service) verifyStaging(ctx context.Context, run *runState) error {
	if err := s.runtimeCurrentForRun(run); err != nil {
		return err
	}
	if err := s.setStagingRefs(ctx, run); err != nil {
		return err
	}
	if err := s.verifyStagingRefs(ctx, run); err != nil {
		return err
	}
	wanted := wantedOIDs(run.advertisement)
	if err := s.verifyObjects(ctx, run, wanted); err != nil {
		return err
	}
	if err := s.verifyPeelFacts(ctx, run); err != nil {
		return err
	}
	if err := s.runtimeCurrentForRun(run); err != nil {
		return err
	}
	if _, err := s.Repositories.Git.RunWithLimits(ctx, run.stagingPath, nil, run.limits.commandLimits(run.limits.VerifyTimeout),
		"--git-dir", ".", "fsck", "--connectivity-only", "--no-progress", "--no-dangling"); err != nil {
		return newProblem(CodeVerifyFailed, "staged object graph failed a connectivity check", err)
	}
	return s.verifyCommitDates(ctx, run)
}

// commitDateBatch is how many commits one git log process reads for
// verifyCommitDates, which bounds that process's output and the memory of the
// check however many commits the pack holds.
const commitDateBatch = 10_000

// verifyCommitDates refuses a pack holding a commit whose author or committer
// date OwnGit cannot show. Strict indexing lets old date spellings through
// (historicFormatWarnings), so this decides with the reader the history and
// commit pages use: a date Git still renders, such as a +051800 offset, is
// kept, and an unreadable one refuses the import before publication. It
// covers every commit in the staged pack, including commits no ref reaches,
// because the whole pack is published. Tags are not checked: no page reads a
// tagger date, and old tags without one are accepted.
func (s *Service) verifyCommitDates(ctx context.Context, run *runState) error {
	if err := s.runtimeCurrentForRun(run); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, run.limits.VerifyTimeout)
	defer cancel()
	batch := make([]string, 0, commitDateBatch)
	list := func(stdout io.Reader) error {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			kind, oid, _ := strings.Cut(scanner.Text(), " ")
			if kind != "commit" {
				continue
			}
			batch = append(batch, oid)
			if len(batch) == commitDateBatch {
				if err := s.checkCommitDates(ctx, run, batch); err != nil {
					return err
				}
				batch = batch[:0]
			}
		}
		return scanner.Err()
	}
	if _, err := s.Repositories.Git.StreamGit(ctx, run.stagingPath, list,
		"--git-dir", ".", "cat-file", "--batch-all-objects", "--unordered", "--batch-check=%(objecttype) %(objectname)"); err != nil {
		var problem *Problem
		if errors.As(err, &problem) {
			return problem
		}
		return newProblem(CodeVerifyFailed, "staged commits could not be listed", err)
	}
	return s.checkCommitDates(ctx, run, batch)
}

// checkCommitDates reads the author and committer dates of oids as the pages
// do and refuses the first one repository.ParseGitDate cannot read.
func (s *Service) checkCommitDates(ctx context.Context, run *runState, oids []string) error {
	if len(oids) == 0 {
		return nil
	}
	input := strings.NewReader(strings.Join(oids, "\n") + "\n")
	// An OID, two raw dates and separators take well under 192 bytes.
	stdout, truncated, err := s.runStagingGit(ctx, run, input, int64(len(oids))*192+(1<<20),
		"--git-dir", ".", "log", "--no-walk=unsorted", "--stdin", "-z", "--no-decorate", repository.GitDateOption, "--format=%H%x00%ad%x00%cd")
	if err != nil || truncated {
		return newProblem(CodeVerifyFailed, "staged commit dates could not be read", err)
	}
	fields := bytes.Split(bytes.TrimSuffix(stdout, []byte{0}), []byte{0})
	if len(fields) != 3*len(oids) {
		return newProblem(CodeVerifyFailed, "Git did not report the date of every staged commit", nil)
	}
	for index, oid := range oids {
		record := fields[3*index : 3*index+3]
		if string(record[0]) != oid {
			return newProblem(CodeVerifyFailed, "Git reported staged commit dates out of order", nil)
		}
		for _, date := range record[1:] {
			if _, err := repository.ParseGitDate(date); err != nil {
				return newProblem(CodeVerifyFailed, fmt.Sprintf("commit %s has an author or committer date that OwnGit cannot read, so its history could not be shown", oid), nil)
			}
		}
	}
	return nil
}

func (s *Service) runStagingGit(ctx context.Context, run *runState, stdin io.Reader, output int64, args ...string) ([]byte, bool, error) {
	if err := s.runtimeCurrentForRun(run); err != nil {
		return nil, false, err
	}
	return s.boundedCommand(ctx, run.stagingPath, stdin, run.limits.commandLimitsFor(run.limits.VerifyTimeout, output), args...)
}

func (s *Service) setStagingRefs(ctx context.Context, run *runState) error {
	if err := s.runtimeCurrentForRun(run); err != nil {
		return err
	}
	headTarget := headSymrefTarget(run.advertisement)
	commands := []string{"start"}
	refs := advertisedRefs(run.advertisement)
	for _, ref := range refs {
		commands = append(commands, "create "+ref.Name+" "+ref.OID)
	}
	commands = append(commands, "prepare", "commit")
	input := strings.NewReader(strings.Join(commands, "\n") + "\n")
	if len(refs) > 0 {
		if _, err := s.Repositories.Git.RunWithLimits(ctx, run.stagingPath, input, run.limits.commandLimits(run.limits.VerifyTimeout),
			"--git-dir", ".", "update-ref", "--stdin"); err != nil {
			return newProblem(CodeVerifyFailed, "staged refs could not be created", err)
		}
	}
	if headTarget != "" {
		if _, err := s.Repositories.Git.RunWithLimits(ctx, run.stagingPath, nil, run.limits.commandLimits(run.limits.VerifyTimeout),
			"--git-dir", ".", "symbolic-ref", "HEAD", headTarget); err != nil {
			return newProblem(CodeVerifyFailed, "staged HEAD could not be set", err)
		}
	} else if run.advertisement.Head.Advertised && run.advertisement.Head.OID != "" {
		if _, err := s.Repositories.Git.RunWithLimits(ctx, run.stagingPath, nil, run.limits.commandLimits(run.limits.VerifyTimeout),
			"--git-dir", ".", "update-ref", "--no-deref", "HEAD", run.advertisement.Head.OID); err != nil {
			return newProblem(CodeVerifyFailed, "staged HEAD could not be detached", err)
		}
	}
	return nil
}

// verifyStagingRefs requires the unpublished staging ref list to equal every
// advertised named ref exactly, including skipped namespaces and case.
func (s *Service) verifyStagingRefs(ctx context.Context, run *runState) error {
	records, _, err := s.Repositories.ReadRefs(ctx, run.stagingPath, 0, "refs")
	if err != nil {
		return newProblem(CodeVerifyFailed, "staged refs could not be read", err)
	}
	refs := advertisedRefs(run.advertisement)
	if len(records) != len(refs) {
		return newProblem(CodeVerifyFailed, fmt.Sprintf("staged ref count is %d, advertised named ref count is %d", len(records), len(refs)), nil)
	}
	for i, ref := range refs {
		if records[i].Name != ref.Name {
			return newProblem(CodeVerifyFailed, fmt.Sprintf("staged ref inventory has %q where advertised inventory has %q", records[i].Name, ref.Name), nil)
		}
		if records[i].OID != ref.OID {
			return newProblem(CodeVerifyFailed, fmt.Sprintf("staged ref %q does not match its advertised value", ref.Name), nil)
		}
	}
	return nil
}

// verifyObjects requires every advertised object to exist with a valid type.
// Branch tips and HEAD must be commits; other namespaces retain Git's broader
// object-type semantics.
func (s *Service) verifyObjects(ctx context.Context, run *runState, wanted []string) error {
	if len(wanted) == 0 {
		return nil
	}
	input := strings.NewReader(strings.Join(wanted, "\n") + "\n")
	outputLimit := int64(len(wanted))*192 + (1 << 20)
	stdout, truncated, err := s.runStagingGit(ctx, run, input, outputLimit,
		"--git-dir", ".", "cat-file", "--batch-check")
	if err != nil {
		return newProblem(CodeVerifyFailed, "wanted objects could not be inspected", err)
	}
	if truncated {
		return newProblem(CodeVerifyFailed, "wanted object inspection exceeded its output bound", nil)
	}
	lines := splitBoundedLines(stdout)
	if len(lines) != len(wanted) {
		return newProblem(CodeVerifyFailed, fmt.Sprintf("wanted object inspection returned %d records for %d objects", len(lines), len(wanted)), nil)
	}
	commitRequired := map[string]bool{}
	for _, ref := range run.advertisement.Refs {
		if ref.Name == "HEAD" || strings.HasPrefix(ref.Name, "refs/heads/") {
			commitRequired[ref.OID] = true
		}
	}
	if run.advertisement.Head.Advertised {
		commitRequired[run.advertisement.Head.OID] = true
	}
	for index, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != wanted[index] {
			return newProblem(CodeVerifyFailed, fmt.Sprintf("wanted object %s is missing from the staged pack", wanted[index]), nil)
		}
		switch fields[1] {
		case "commit", "tree", "blob", "tag":
		default:
			return newProblem(CodeVerifyFailed, fmt.Sprintf("wanted object %s has unexpected type %q", wanted[index], fields[1]), nil)
		}
		if commitRequired[wanted[index]] && fields[1] != "commit" {
			return newProblem(CodeVerifyFailed, fmt.Sprintf("advertised branch or HEAD object %s has type %q instead of commit", wanted[index], fields[1]), nil)
		}
		if size, err := strconv.ParseInt(fields[2], 10, 64); err != nil || size < 0 {
			return newProblem(CodeVerifyFailed, fmt.Sprintf("wanted object %s has an invalid size", wanted[index]), err)
		}
	}
	return nil
}

// verifyPeelFacts proves every advertised peel fact resolves to exactly the
// advertised peeled object.
func (s *Service) verifyPeelFacts(ctx context.Context, run *runState) error {
	type peelFact struct {
		name   string
		oid    string
		peeled string
	}
	var facts []peelFact
	seen := map[string]bool{}
	add := func(name, oid, peeled string) {
		key := oid + "\x00" + peeled
		if oid == "" || peeled == "" || seen[key] {
			return
		}
		seen[key] = true
		facts = append(facts, peelFact{name: name, oid: oid, peeled: peeled})
	}
	for _, ref := range run.advertisement.Refs {
		add(ref.Name, ref.OID, ref.PeeledOID)
	}
	if run.advertisement.Head.Advertised {
		add("HEAD", run.advertisement.Head.OID, run.advertisement.Head.PeeledOID)
	}
	for _, fact := range facts {
		result, err := s.Repositories.Git.RunWithLimits(ctx, run.stagingPath, nil, run.limits.commandLimits(run.limits.VerifyTimeout),
			"--git-dir", ".", "rev-parse", "--verify", "--quiet", "--end-of-options", fact.oid+"^{}")
		if err != nil {
			return newProblem(CodeVerifyFailed, fmt.Sprintf("advertised peel fact for %s did not resolve", fact.name), err)
		}
		if peeled := strings.TrimSpace(string(result.Stdout)); peeled != fact.peeled {
			return newProblem(CodeVerifyFailed, fmt.Sprintf("advertised peel fact for %s resolved to %s, advertised %s", fact.name, peeled, fact.peeled), nil)
		}
	}
	return nil
}

// inspectLFS scans reachable content for Git LFS pointers. No LFS object is
// downloaded or hosted, so a pointer means the source content is incomplete
// unless the owner consented to a Git-only import.
func (s *Service) inspectLFS(ctx context.Context, run *runState) error {
	if err := s.runtimeCurrentForRun(run); err != nil {
		return err
	}
	inspection := lfsInspection{Complete: true}
	run.inspection = inspection
	objects, listTruncated, err := s.listReachableObjects(ctx, run)
	if err != nil {
		return err
	}
	if listTruncated {
		inspection.ObjectListTruncated = true
		inspection.Complete = false
	}
	if len(objects) == 0 {
		run.inspection = inspection
		return nil
	}
	inspection.Objects = int64(len(objects))
	if inspection.Objects > int64(run.limits.LFS.MaxObjects) {
		objects = objects[:run.limits.LFS.MaxObjects]
		inspection.Objects = int64(run.limits.LFS.MaxObjects)
		inspection.ObjectListTruncated = true
		inspection.Complete = false
	}
	types, truncated, err := s.inspectObjectTypes(ctx, run, objects)
	if err != nil {
		return err
	}
	if truncated {
		inspection.Complete = false
	}
	pointerBound := run.limits.LFS.MaxPointerBytes
	if pointerBound > checksource.MaxLFSPointerBytes {
		pointerBound = checksource.MaxLFSPointerBytes
	}
	candidateCap := int64(run.limits.LFS.MaxCandidateBlobs)
	if maximum := run.limits.LFS.MaxCandidateBytes / pointerBound; maximum < candidateCap {
		candidateCap = maximum
	}
	if candidateCap < 1 {
		candidateCap = 1
	}
	var candidates []string
	for _, oid := range objects {
		entry, exists := types[oid]
		if !exists || entry.objectType != "blob" {
			continue
		}
		if entry.size > pointerBound {
			if entry.size <= checksource.MaxLFSPointerBytes {
				inspection.CandidatesTruncated = true
				inspection.Complete = false
			}
			continue
		}
		if int64(len(candidates)) >= candidateCap {
			inspection.CandidatesTruncated = true
			inspection.Complete = false
			break
		}
		candidates = append(candidates, oid)
	}
	if len(candidates) == 0 {
		run.inspection = inspection
		return nil
	}
	pointers, scanned, bytesRead, bytesTruncated, examples, err := s.inspectPointerCandidates(ctx, run, candidates)
	if err != nil {
		return err
	}
	inspection.Pointers = pointers
	inspection.Candidates = scanned
	inspection.BytesRead = bytesRead
	inspection.BytesTruncated = bytesTruncated
	inspection.Examples = examples
	if scanned != int64(len(candidates)) {
		inspection.CandidatesTruncated = true
	}
	if bytesTruncated || inspection.ObjectListTruncated || inspection.CandidatesTruncated {
		inspection.Complete = false
	}
	if err := s.runtimeCurrentForRun(run); err != nil {
		return err
	}
	run.inspection = inspection
	return nil
}

// listReachableObjects walks every object reachable from every advertised tip,
// including skipped namespaces and HEAD. A truncated result covers only the
// complete records in the captured prefix.
func (s *Service) listReachableObjects(ctx context.Context, run *runState) ([]string, bool, error) {
	roots := advertisedTipOIDs(run.advertisement)
	if len(roots) == 0 {
		return nil, false, nil
	}
	input := strings.NewReader(strings.Join(roots, "\n") + "\n")
	stdout, truncated, err := s.boundedCommand(ctx, run.stagingPath, input, gitexec.CommandLimits{
		Timeout:     run.limits.LFS.Timeout,
		OutputLimit: run.limits.LFS.MaxObjectListBytes,
		Environment: run.limits.commandLimits(run.limits.LFS.Timeout).Environment,
	}, "--git-dir", ".", "rev-list", "--objects", "--stdin")
	if err != nil {
		return nil, false, newProblem(CodeVerifyFailed, "reachable objects could not be listed for LFS inspection", err)
	}
	if truncated && len(stdout) > 0 && stdout[len(stdout)-1] != '\n' {
		if index := bytes.LastIndexByte(stdout, '\n'); index >= 0 {
			stdout = stdout[:index+1]
		} else {
			stdout = nil
		}
	}
	lines := splitBoundedLines(stdout)
	objects := make([]string, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		objects = append(objects, fields[0])
	}
	return objects, truncated, nil
}

type objectTypeEntry struct {
	objectType string
	size       int64
}

func (s *Service) inspectObjectTypes(ctx context.Context, run *runState, objects []string) (map[string]objectTypeEntry, bool, error) {
	input := strings.NewReader(strings.Join(objects, "\n") + "\n")
	stdout, truncated, err := s.boundedCommand(ctx, run.stagingPath, input, gitexec.CommandLimits{
		Timeout:     run.limits.LFS.Timeout,
		OutputLimit: run.limits.LFS.MaxTypeListBytes,
		Environment: run.limits.commandLimits(run.limits.LFS.Timeout).Environment,
	}, "--git-dir", ".", "cat-file", "--batch-check")
	if err != nil {
		return nil, false, newProblem(CodeVerifyFailed, "object types could not be listed for LFS inspection", err)
	}
	if truncated && len(stdout) > 0 && stdout[len(stdout)-1] != '\n' {
		if index := bytes.LastIndexByte(stdout, '\n'); index >= 0 {
			stdout = stdout[:index+1]
		} else {
			stdout = nil
		}
	}
	lines := splitBoundedLines(stdout)
	if !truncated && len(lines) != len(objects) {
		return nil, false, newProblem(CodeVerifyFailed, "object type inspection returned an unexpected record count", nil)
	}
	entries := make(map[string]objectTypeEntry, len(lines))
	for index, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 3 || index >= len(objects) || fields[0] != objects[index] {
			if truncated {
				break
			}
			return nil, false, newProblem(CodeVerifyFailed, "object type inspection returned an unexpected object", nil)
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 {
			if truncated {
				break
			}
			return nil, false, newProblem(CodeVerifyFailed, "object type inspection returned an invalid size", err)
		}
		entries[fields[0]] = objectTypeEntry{objectType: fields[1], size: size}
	}
	return entries, truncated, nil
}

// inspectPointerCandidates reads candidate blob content and detects the Git LFS
// pointer signature. Blobs larger than the pointer bound were not inspected.
func (s *Service) inspectPointerCandidates(ctx context.Context, run *runState, candidates []string) (int64, int64, int64, bool, []string, error) {
	input := strings.NewReader(strings.Join(candidates, "\n") + "\n")
	outputLimit := run.limits.LFS.MaxCandidateBytes + int64(len(candidates))*run.limits.LFS.MaxPointerBytes + (1 << 20)
	stdout, truncated, err := s.boundedCommand(ctx, run.stagingPath, input, gitexec.CommandLimits{
		Timeout:     run.limits.LFS.Timeout,
		OutputLimit: outputLimit,
		Environment: run.limits.commandLimits(run.limits.LFS.Timeout).Environment,
	}, "--git-dir", ".", "cat-file", "--batch")
	if err != nil {
		return 0, 0, 0, false, nil, newProblem(CodeVerifyFailed, "candidate blobs could not be read for LFS inspection", err)
	}
	reader := bufio.NewReader(bytes.NewReader(stdout))
	var pointers, scanned, bytesRead int64
	var examples []string
	bytesTruncated := truncated
	for {
		header, err := reader.ReadString('\n')
		if err != nil && header == "" {
			break
		}
		fields := strings.Fields(header)
		if len(fields) != 3 {
			if truncated {
				bytesTruncated = true
				break
			}
			return 0, scanned, bytesRead, false, nil, newProblem(CodeVerifyFailed, "candidate blob inspection returned an invalid header", nil)
		}
		if scanned >= int64(len(candidates)) || fields[0] != candidates[scanned] || fields[1] != "blob" {
			return 0, scanned, bytesRead, false, nil, newProblem(CodeVerifyFailed, "candidate blob inspection returned an unexpected object", nil)
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 || size > run.limits.LFS.MaxPointerBytes {
			return 0, scanned, bytesRead, false, nil, newProblem(CodeVerifyFailed, "candidate blob inspection returned an invalid size", err)
		}
		content := make([]byte, size)
		if _, err := io.ReadFull(reader, content); err != nil {
			bytesTruncated = true
			break
		}
		if _, err := reader.ReadByte(); err != nil {
			bytesTruncated = true
			break
		}
		scanned++
		bytesRead += size
		if bytesRead > run.limits.LFS.MaxCandidateBytes {
			bytesTruncated = true
		}
		if checksource.ParseLFSPointer(content) != nil {
			pointers++
			if len(examples) < 3 {
				examples = append(examples, fields[0])
			}
		}
	}
	return pointers, scanned, bytesRead, bytesTruncated, examples, nil
}

// splitBoundedLines splits captured output into lines.
func splitBoundedLines(content []byte) []string {
	trimmed := strings.TrimSuffix(string(content), "\n")
	if trimmed == "" {
		return nil
	}
	lines := strings.Split(trimmed, "\n")
	return lines
}
