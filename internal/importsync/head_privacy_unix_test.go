//go:build !windows

package importsync

import (
	"os"
	"testing"
)

func makeRepositoryStorageSharedForTest(t *testing.T, path string) {
	t.Helper()
	noErr(t, os.Chmod(path, 0777))
}

func TestPermissiveRepositoryRootDoesNotBlockNormalImport(t *testing.T) {
	f := newFixture(t)
	noErr(t, os.Chmod(f.manager.RepositoryRoot(), 0777))
	f.commit("initial", "initial\n")
	f.mustImport(ImportInput{})
	assertNormalHEADPublication(t, f)
}

func TestRecoveryPreservesLockWhenRepositoryPermissionsAreLoosened(t *testing.T) {
	for _, target := range []string{"storage-root", "repository"} {
		t.Run(target, func(t *testing.T) {
			f := killedPublicationFixture(t, "head-locked")
			root := f.manager.RepositoryRoot()
			if target == "repository" {
				root = f.destinationPath()
			}
			noErr(t, os.Chmod(root, 0777))
			assertRecoveryPrivacyRefusal(t, f)
		})
	}
}
