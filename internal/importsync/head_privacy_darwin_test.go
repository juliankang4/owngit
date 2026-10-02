//go:build darwin

package importsync

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func addInheritedRepositoryWriteACL(t *testing.T, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "chmod", "+a", "everyone allow write,append,add_file,add_subdirectory,delete,delete_child,file_inherit,directory_inherit", path).CombinedOutput()
	if err != nil {
		t.Fatalf("set synthetic inheritable write ACL: %v %s", err, output)
	}
}

func repositoryACLListing(t *testing.T, path string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "ls", "-lde", path).CombinedOutput()
	noErr(t, err)
	return string(output)
}

func TestMacOSInheritedWriteACLDoesNotBlockNormalImport(t *testing.T) {
	f := newFixture(t)
	addInheritedRepositoryWriteACL(t, f.manager.RepositoryRoot())
	f.commit("initial", "initial\n")
	f.mustImport(ImportInput{})
	if listing := repositoryACLListing(t, f.destinationPath()); strings.Contains(listing, "inherited") {
		t.Fatalf("private import inherited the parent's write ACL:\n%s", listing)
	}
	assertNormalHEADPublication(t, f)
}

func TestMacOSRecoveryPreservesLockWithInheritableWriteACL(t *testing.T) {
	f := killedPublicationFixture(t, "head-locked")
	root := f.manager.RepositoryRoot()
	addInheritedRepositoryWriteACL(t, root)
	before := repositoryACLListing(t, root)
	assertRecoveryPrivacyRefusal(t, f)
	if after := repositoryACLListing(t, root); after != before {
		t.Fatal("recovery changed the repository storage ACL")
	}
}
