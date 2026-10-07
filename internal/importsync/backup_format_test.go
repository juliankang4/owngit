package importsync

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"owngit/internal/auth"
	"owngit/internal/recovery"
)

// backupVersion writes a backup of the fixture's state and returns its
// manifest version.
func (f *fixture) backupVersion() int {
	f.t.Helper()
	ctx := context.Background()
	hash, err := auth.HashPassword("synthetic-admin-password")
	noErr(f.t, err)
	noErr(f.t, f.store.CompleteSetup(ctx, f.manager.RepositoryRoot(), "open", "", hash, false))
	output, err := os.MkdirTemp(f.root, "backup-")
	noErr(f.t, err)
	output = filepath.Join(output, "backup")
	_, err = recovery.CreateWithReport(ctx, f.store, f.manager, output)
	noErr(f.t, err)
	contents, err := os.ReadFile(filepath.Join(output, "manifest.json"))
	noErr(f.t, err)
	var manifest recovery.Manifest
	noErr(f.t, json.Unmarshal(contents, &manifest))
	return manifest.Version
}

// The backup format follows the import records a backup holds, not the
// current choices: refs of a removed extra namespace and a followed
// deletion stay recorded after the choices are turned off, and only format
// 11 holds them. Branch and tag history alone stays format 10.
func TestImportHistoryDecidesTheBackupFormat(t *testing.T) {
	ctx := context.Background()
	t.Run("branches and tags", func(t *testing.T) {
		f := newFixture(t)
		f.commit("one", "one\n")
		f.git(f.source, "tag", "v1")
		f.mustImport(ImportInput{})
		f.commit("two", "two\n")
		_, err := f.refresh()
		noErr(t, err)
		version := f.backupVersion()
		require(t, version == 10, "backup version = %d", version)
	})
	t.Run("removed extra namespace", func(t *testing.T) {
		f := newFixture(t)
		oid := f.commit("one", "one\n")
		f.git(f.source, "update-ref", "refs/notes/commits", oid)
		f.mustImport(ImportInput{Options: OptionsChange{ExtraRefPrefixes: prefixesPointer("refs/notes/")}})
		_, err := f.service.ChangeOptions(ctx, "project", OptionsChange{ExtraRefPrefixes: prefixesPointer()})
		noErr(t, err)
		_, err = f.refresh()
		noErr(t, err)
		version := f.backupVersion()
		require(t, version == 11, "backup version = %d", version)
	})
	t.Run("deletion followed, then turned off", func(t *testing.T) {
		f := newFixture(t)
		f.commit("one", "one\n")
		f.git(f.source, "branch", "dev")
		f.mustImport(ImportInput{Options: OptionsChange{FollowUpstreamDeletions: boolPointer(true)}})
		f.git(f.source, "branch", "-D", "dev")
		_, err := f.refresh()
		noErr(t, err)
		_, err = f.service.ChangeOptions(ctx, "project", OptionsChange{FollowUpstreamDeletions: boolPointer(false)})
		noErr(t, err)
		version := f.backupVersion()
		require(t, version == 11, "backup version = %d", version)
	})
}
