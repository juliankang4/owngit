package importsync

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/importgit"
)

func TestStagingRefInventory(t *testing.T) {
	f := newFixture(t)
	first := f.commit("one", "one")
	second := f.commit("two", "two")
	f.git(f.source, "tag", "-a", "v1", "-m", "annotation", first)
	tag := f.git(f.source, "rev-parse", "refs/tags/v1")
	f.git(f.source, "update-ref", "refs/notes/review", first)
	staging := filepath.Join(f.root, "staging.git")
	f.git("", "clone", "--bare", f.source, staging)
	f.git(staging, "update-ref", "refs/notes/review", first)
	f.git(staging, "symbolic-ref", "refs/heads/alias", "refs/heads/main")
	refs := []importgit.Ref{
		{Name: "refs/tags/v1", OID: tag}, {Name: "refs/notes/review", OID: first},
		{Name: "refs/heads/main", OID: second}, {Name: "refs/heads/alias", OID: second},
		{Name: "HEAD", OID: first},
	}
	for _, test := range []struct {
		name, detail string
		refs         []importgit.Ref
	}{
		{"equal", "", refs},
		{"count", "staged ref count", refs[:3]},
		{"shifted names", `inventory has "refs/heads/alias" where advertised inventory has "refs/heads/main"`, []importgit.Ref{refs[0], refs[1], refs[2], {Name: "refs/notes/z", OID: second}}},
		{"case", "inventory", []importgit.Ref{refs[0], refs[1], refs[2], {Name: "refs/heads/Alias", OID: second}}},
		{"oid", `staged ref "refs/heads/main" does not match its advertised value`, []importgit.Ref{refs[0], refs[1], {Name: "refs/heads/main", OID: first}, refs[3]}},
		{"tag object", `staged ref "refs/tags/v1" does not match`, []importgit.Ref{{Name: "refs/tags/v1", OID: first}, refs[1], refs[2], refs[3]}},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := &runState{stagingPath: staging, advertisement: &importgit.Advertisement{Refs: test.refs}}
			err := f.service.verifyStagingRefs(context.Background(), run)
			if test.detail == "" {
				noErr(t, err)
			} else {
				require(t, problemCode(err) == CodeVerifyFailed && strings.Contains(err.Error(), test.detail),
					"inventory error = %v, want %q", err, test.detail)
			}
		})
	}
	empty := filepath.Join(f.root, "empty.git")
	f.git("", "init", "--bare", empty)
	run := &runState{stagingPath: empty, advertisement: &importgit.Advertisement{}}
	noErr(t, f.service.verifyStagingRefs(context.Background(), run))
	run.stagingPath = filepath.Join(f.root, "missing.git")
	err := f.service.verifyStagingRefs(context.Background(), run)
	require(t, problemCode(err) == CodeVerifyFailed && strings.Contains(err.Error(), "staged refs could not be read"),
		"Git read failure = %v", err)
}
