package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestBrowseKeepsComposedAndDecomposedPathIdentity(t *testing.T) {
	ctx := context.Background()
	manager, remote, work := newTestRepository(t)
	// Preserve the precomposing config an existing macOS repository can have.
	runGit(t, "", "--git-dir", remote, "config", "core.precomposeUnicode", "true")
	names := []string{"한글", "\u1112\u1161\u11ab\u1100\u1173\u11af", "café", "cafe\u0301"}
	paths := make([]string, 0, 6)
	for _, name := range names {
		paths = append(paths, name+".txt")
	}
	paths = append(paths, names[0]+"/"+names[2]+".md", names[1]+"/"+names[3]+".md")

	// Plumbing keeps both spellings in Git without requiring the host file
	// system to store two canonically equivalent working-tree filenames.
	parent := ""
	for version := 0; version < 2; version++ {
		var root strings.Builder
		for i, filePath := range paths {
			marker := fmt.Sprintf("version_%d_file_%d\n", version, i)
			blob := gitInputOutput(t, work, []byte(marker), "hash-object", "-w", "--stdin")
			if directory, base, nested := strings.Cut(filePath, "/"); nested {
				tree := gitInputOutput(t, work, []byte("100644 blob "+blob+"\t"+base+"\x00"), "mktree", "-z")
				fmt.Fprintf(&root, "040000 tree %s\t%s\x00", tree, directory)
			} else {
				fmt.Fprintf(&root, "100644 blob %s\t%s\x00", blob, filePath)
			}
		}
		tree := gitInputOutput(t, work, []byte(root.String()), "mktree", "-z")
		args := []string{"commit-tree", tree}
		if parent != "" {
			args = append(args, "-p", parent)
		}
		parent = gitInputOutput(t, work, []byte("exact names\n"), args...)
	}
	runGit(t, work, "push", "origin", parent+":refs/heads/main")
	_, entries, err := manager.Tree(ctx, "sample", "main", "")
	noErr(t, err)
	if len(entries) != 6 {
		t.Fatalf("root entries=%+v", entries)
	}
	for i, filePath := range paths {
		t.Run(fmt.Sprintf("file_%d", i), func(t *testing.T) {
			if directory, base, nested := strings.Cut(filePath, "/"); nested {
				_, entries, err := manager.Tree(ctx, "sample", "main", directory)
				if err != nil || len(entries) != 1 || entries[0].Name != base || entries[0].Path != filePath {
					t.Fatalf("nested listing=%+v err=%v", entries, err)
				}
			}
			_, blob, err := manager.ReadBlob(ctx, "sample", "main", filePath, 1024)
			want := fmt.Sprintf("version_1_file_%d\n", i)
			if err != nil || blob.Path != filePath || string(blob.Content) != want {
				t.Fatalf("blob=%+v err=%v, want path=%q content=%q", blob, err, filePath, want)
			}
			patch, truncated, err := manager.CommitPatch(ctx, "sample", parent, filePath, nil, 4096)
			if err != nil || truncated || !strings.Contains(patch, "+"+want) || !strings.Contains(patch, fmt.Sprintf("-version_0_file_%d\n", i)) {
				t.Fatalf("wrong file diff: truncated=%v patch=%q err=%v", truncated, patch, err)
			}
		})
	}
}

func TestBrowseServesLooseDecomposedRefsByExactName(t *testing.T) {
	ctx := context.Background()
	manager, remote, work := newTestRepository(t)
	commitFile(t, work, "exact ref content\n", "exact refs", "2024-01-01T00:00:00Z")
	oid := gitOutput(t, work, "rev-parse", "HEAD")
	branch, tag := "\u1112\u1161\u11ab\u1100\u1173\u11af-follow", "cafe\u0301-tag"
	runGit(t, "", "--git-dir", remote, "config", "core.precomposeUnicode", "true")
	runGit(t, work, "-c", "core.precomposeUnicode=false", "push", "origin", oid+":refs/heads/"+branch, oid+":refs/tags/"+tag)
	summary, err := manager.Summary(ctx, "sample")
	noErr(t, err)
	if len(summary.Branches) != 1 || summary.Branches[0].Name != branch || summary.Branches[0].OID != oid || len(summary.Tags) != 1 || summary.Tags[0].Name != tag || summary.Tags[0].OID != oid {
		t.Fatalf("ref identity changed: %+v", summary)
	}
	for _, ref := range []string{"refs/heads/" + branch, "refs/tags/" + tag} {
		resolved, got, err := manager.ResolveRef(ctx, "sample", ref)
		if err != nil || resolved != ref || got != oid {
			t.Fatalf("resolve %q: ref=%q oid=%q err=%v", ref, resolved, got, err)
		}
		_, blob, err := manager.ReadBlob(ctx, "sample", ref, "file.txt", 1024)
		if err != nil || string(blob.Content) != "exact ref content\n" {
			t.Fatalf("ref %q blob=%+v err=%v", ref, blob, err)
		}
	}
}
