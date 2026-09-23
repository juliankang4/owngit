package repository

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"owngit/internal/gitexec"
)

// Supported object formats. An empty format in CreateOptions means the Git
// default, which is reported as sha1.
const (
	ObjectFormatSHA1   = "sha1"
	ObjectFormatSHA256 = "sha256"
)

// ObjectFormat reports the repository's hash algorithm from its local
// configuration. A missing extensions.objectFormat means the SHA-1 default.
func (m *Manager) ObjectFormat(ctx context.Context, repositoryPath string) (string, error) {
	result, err := m.Git.Run(ctx, repositoryPath, nil, "--git-dir", ".", "config", "--local", "--get", "extensions.objectFormat")
	if err != nil {
		if code, ok := gitexec.ExitCode(err); ok && code == 1 {
			return ObjectFormatSHA1, nil
		}
		return "", fmt.Errorf("read repository object format: %w", err)
	}
	format := strings.ToLower(strings.TrimSpace(string(result.Stdout)))
	if format == "" {
		return ObjectFormatSHA1, nil
	}
	if format != ObjectFormatSHA1 && format != ObjectFormatSHA256 {
		return "", fmt.Errorf("repository uses unsupported object format %q", format)
	}
	return format, nil
}

// RetainedRefName is the durable retention ref the Git update hook creates for
// a replaced branch or tag tip. A direct update-ref publication does not run
// the hook, so the importer creates the same ref name.
func RetainedRefName(kind, oid string) string {
	return "refs/owngit/retained/" + kind + "/" + oid
}

// ProvenanceRefName is the per-branch retention record the update hook keeps
// beside RetainedRefName. Import creates it for the same reason.
func ProvenanceRefName(kind, short, oid string) string {
	return "refs/owngit/provenance/" + kind + "/" + short + "/" + oid
}

// RefRecord is one fully-resolved reference.
type RefRecord struct {
	Name         string
	OID          string
	SymrefTarget string
}

// ReadRefs returns refs under the given prefixes in stable name order. A
// nonpositive limit is unbounded.
func (m *Manager) ReadRefs(ctx context.Context, repositoryPath string, limit int, prefixes ...string) ([]RefRecord, bool, error) {
	arguments := []string{"--git-dir", ".", "for-each-ref", "--format=%(refname)%00%(objectname)%00%(symref)"}
	arguments = append(arguments, prefixes...)
	result, err := m.Git.RunWithLimits(ctx, repositoryPath, nil, gitexec.CommandLimits{OutputLimit: 64 << 20}, arguments...)
	if err != nil {
		return nil, false, fmt.Errorf("read repository refs: %w", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(result.Stdout), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil, false, nil
	}
	records := make([]RefRecord, 0, len(lines))
	for _, line := range lines {
		parts := strings.SplitN(line, "\x00", 3)
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
			return nil, false, errors.New("Git returned an unexpected ref record")
		}
		records = append(records, RefRecord{Name: parts[0], OID: parts[1], SymrefTarget: parts[2]})
	}
	sort.Slice(records, func(left, right int) bool { return records[left].Name < records[right].Name })
	if limit > 0 && len(records) > limit {
		return records[:limit], true, nil
	}
	return records, false, nil
}

// ReadHead returns HEAD's immediate symbolic target and its resolved object ID.
// An empty symbolic value with an empty OID means HEAD does not resolve (an unborn
// or detached-but-missing HEAD). A detached HEAD reports its object ID.
func (m *Manager) ReadHead(ctx context.Context, repositoryPath string) (string, string, error) {
	symbolic, err := m.Git.Run(ctx, repositoryPath, nil, "--git-dir", ".", "symbolic-ref", "--quiet", "--no-recurse", "HEAD")
	if err == nil {
		target := strings.TrimSpace(string(symbolic.Stdout))
		resolved, resolveErr := m.Git.Run(ctx, repositoryPath, nil, "--git-dir", ".", "rev-parse", "--verify", "--quiet", "HEAD")
		if resolveErr != nil {
			if code, ok := gitexec.ExitCode(resolveErr); ok && code == 1 {
				return target, "", nil
			}
			return "", "", fmt.Errorf("resolve HEAD: %w", resolveErr)
		}
		return target, strings.TrimSpace(string(resolved.Stdout)), nil
	}
	if code, ok := gitexec.ExitCode(err); !ok || code != 1 {
		return "", "", fmt.Errorf("read HEAD: %w", err)
	}
	resolved, resolveErr := m.Git.Run(ctx, repositoryPath, nil, "--git-dir", ".", "rev-parse", "--verify", "--quiet", "HEAD")
	if resolveErr != nil {
		if code, ok := gitexec.ExitCode(resolveErr); ok && code == 1 {
			return "", "", nil
		}
		return "", "", fmt.Errorf("resolve HEAD: %w", resolveErr)
	}
	return "", strings.TrimSpace(string(resolved.Stdout)), nil
}

// ReadSymbolicRefTarget resolves a symbolic ref chain to its final ref target.
// The boolean is false when name is not symbolic. Traversal is bounded and
// rejects cycles instead of relying on recursive Git output.
func (m *Manager) ReadSymbolicRefTarget(ctx context.Context, repositoryPath, name string) (string, bool, error) {
	const maxSymbolicRefDepth = 32
	current := name
	seen := map[string]bool{name: true}
	for depth := 0; depth < maxSymbolicRefDepth; depth++ {
		result, err := m.Git.Run(ctx, repositoryPath, nil, "--git-dir", ".", "symbolic-ref", "--quiet", "--no-recurse", current)
		if err != nil {
			if code, ok := gitexec.ExitCode(err); ok && code == 1 {
				if current == name {
					return "", false, nil
				}
				return current, true, nil
			}
			return "", false, fmt.Errorf("resolve symbolic ref %q: %w", name, err)
		}
		next := strings.TrimSpace(string(result.Stdout))
		if next == "" || seen[next] {
			return "", false, fmt.Errorf("symbolic ref %q has an empty or cyclic target", name)
		}
		seen[next] = true
		current = next
	}
	return "", false, fmt.Errorf("symbolic ref %q exceeds depth %d", name, maxSymbolicRefDepth)
}

// IsAncestor proves whether ancestor is reachable from descendant.
func (m *Manager) IsAncestor(ctx context.Context, repositoryPath, ancestor, descendant string) (bool, error) {
	if ancestor == "" || descendant == "" {
		return false, nil
	}
	if _, err := m.Git.Run(ctx, repositoryPath, nil, "--git-dir", ".", "merge-base", "--is-ancestor", "--end-of-options", ancestor, descendant); err != nil {
		if code, ok := gitexec.ExitCode(err); ok {
			if code == 0 {
				return true, nil
			}
			if code == 1 {
				return false, nil
			}
		}
		return false, fmt.Errorf("compare Git history: %w", err)
	}
	return true, nil
}
