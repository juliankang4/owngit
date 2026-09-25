package state

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"owngit/internal/testfixture"
)

// createCrashedWALFixture creates a database whose latest committed
// transaction exists only in its write-ahead log, as after a crash. prepare
// runs against a WAL connection with automatic checkpoints disabled, and the
// open database, WAL and optionally SHM are copied into directory.
func createCrashedWALFixture(t *testing.T, directory string, keepSHM bool, prepare func(t *testing.T, path string, db *sql.DB)) {
	t.Helper()
	work := filepath.Join(t.TempDir(), "work")
	workPath := filepath.Join(work, databaseName)
	db := createSchemaDatabase(t, work)
	defer db.Close()
	for _, statement := range []string{`PRAGMA journal_mode=WAL`, `PRAGMA wal_autocheckpoint=0`} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	prepare(t, workPath, db)
	noErr(t, os.MkdirAll(directory, 0o700))
	names := []string{databaseName, databaseName + walSuffix}
	if keepSHM {
		names = append(names, databaseName+shmSuffix)
	}
	for _, name := range names {
		content, err := os.ReadFile(filepath.Join(work, name))
		noErr(t, err)
		noErr(t, os.WriteFile(filepath.Join(directory, name), content, 0o600))
	}
	if info, err := os.Stat(filepath.Join(directory, databaseName+walSuffix)); err != nil || info.Size() == 0 {
		t.Fatalf("fixture did not retain a committed WAL: %v", err)
	}
}

// commitBaselineThenChangeVersion writes the committed baseline catalog into
// the main file, then commits one metadata row, and the requested schema
// marker when version is not empty, only to the write-ahead log.
func commitBaselineThenChangeVersion(version string) func(*testing.T, string, *sql.DB) {
	return func(t *testing.T, _ string, db *sql.DB) {
		t.Helper()
		applyCommittedBaselineCatalog(t, db)
		for _, statement := range []string{`PRAGMA wal_checkpoint(TRUNCATE)`, `INSERT INTO metadata(key,value) VALUES('wal_only_marker','1')`} {
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
		if version != "" {
			if _, err := db.Exec(`INSERT INTO metadata(key,value) VALUES('schema_version',?)`, version); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func useHooks(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		preflightHooks.temporaryRoot = ""
		preflightHooks.privateWriter = nil
		preflightHooks.at = nil
	})
}

func hookAt(t *testing.T, point string, action func(privateDir string)) {
	t.Helper()
	hookInspection(t, point, func(_ *inspection, privateDir string) { action(privateDir) })
}

func hookInspection(t *testing.T, point string, action func(in *inspection, privateDir string)) {
	t.Helper()
	hookInspectionError(t, point, func(in *inspection, privateDir string) error {
		action(in, privateDir)
		return nil
	})
}

func hookInspectionError(t *testing.T, point string, action func(in *inspection, privateDir string) error) {
	t.Helper()
	useHooks(t)
	preflightHooks.at = func(reached string, in *inspection, privateDir string) error {
		if reached == point {
			return action(in, privateDir)
		}
		return nil
	}
}

func setFixtureModes(t *testing.T, directory string, modes map[string]os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	for name, mode := range modes {
		noErr(t, os.Chmod(filepath.Join(directory, name), mode))
	}
}

func captureProtectionFingerprints(t *testing.T, paths ...string) map[string]string {
	t.Helper()
	fingerprints := make(map[string]string, len(paths))
	for _, path := range paths {
		fingerprint, err := protectionFingerprint(path)
		noErr(t, err)
		fingerprints[path] = fingerprint
	}
	return fingerprints
}

func assertProtectionFingerprints(t *testing.T, want map[string]string) {
	t.Helper()
	for path, fingerprint := range want {
		got, err := protectionFingerprint(path)
		noErr(t, err)
		if got != fingerprint {
			t.Fatalf("protection changed for %s:\nbefore=%s\nafter=%s", filepath.Base(path), fingerprint, got)
		}
	}
}

func openRefused(t *testing.T, directory, fragment string) error {
	t.Helper()
	store, err := Open(context.Background(), directory)
	if store != nil {
		_ = store.Close()
	}
	if err == nil || !strings.Contains(err.Error(), fragment) {
		t.Fatalf("Open error=%v, want %q", err, fragment)
	}
	return err
}

func TestCommittedWALDecidesClassificationAndRefusalPreservesSource(t *testing.T) {
	for _, test := range []struct {
		name     string
		version  string
		fragment string
	}{
		{name: "development schema in WAL", version: "5", fragment: "schema 5"},
		{name: "future schema in WAL", version: "99", fragment: "newer"},
	} {
		for _, keepSHM := range []bool{true, false} {
			name := test.name + " with SHM"
			if !keepSHM {
				name = test.name + " without SHM"
			}
			t.Run(name, func(t *testing.T) {
				directory := filepath.Join(t.TempDir(), "state")
				createCrashedWALFixture(t, directory, keepSHM, commitBaselineThenChangeVersion(test.version))
				setFixtureModes(t, directory, map[string]os.FileMode{".": 0o750, databaseName: 0o640, databaseName + walSuffix: 0o644})
				before := captureSchemaDirectory(t, directory)
				openRefused(t, directory, test.fragment)
				assertSchemaDirectoryUnchanged(t, directory, before)
				if runtime.GOOS != "windows" {
					if info, err := os.Stat(directory); err != nil || info.Mode().Perm() != 0o750 {
						t.Fatalf("refusal changed directory mode to %v err=%v", infoMode(info), err)
					}
				}
			})
		}
	}
}

func TestCommittedWALDataSurvivesAcceptedOpen(t *testing.T) {
	ctx := context.Background()
	for _, keepSHM := range []bool{true, false} {
		t.Run(map[bool]string{true: "with SHM", false: "without SHM"}[keepSHM], func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "state")
			createCrashedWALFixture(t, directory, keepSHM, func(t *testing.T, path string, db *sql.DB) {
				commitBaselineThenChangeVersion("")(t, path, db)
				if _, err := db.Exec(`INSERT INTO repositories(id,name,description,created_at) VALUES('wal','WAL only','',1)`); err != nil {
					t.Fatal(err)
				}
			})
			store, err := Open(ctx, directory)
			if err != nil {
				t.Fatalf("accepted WAL open: %v", err)
			}
			defer store.Close()
			if _, exists, err := store.Repository(ctx, "wal"); err != nil || !exists {
				t.Fatalf("repository committed in WAL exists=%v err=%v", exists, err)
			}
			assertStateStoragePrivate(t, directory)
		})
	}
}

func TestSecondStoreOpensBesideActiveWriter(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "state")
	first, err := Open(ctx, directory)
	noErr(t, err)
	defer first.Close()
	noErr(t, first.AddRepository(ctx, Repository{ID: "one", Name: "one", CreatedAt: time.Unix(1, 0)}))
	for _, name := range []string{databaseName + walSuffix, databaseName + shmSuffix} {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Fatalf("active writer has no %s: %v", name, err)
		}
	}
	second, err := Open(ctx, directory)
	if err != nil {
		t.Fatalf("open beside active writer: %v", err)
	}
	defer second.Close()
	if err := second.AddRepository(ctx, Repository{ID: "two", Name: "two", CreatedAt: time.Unix(2, 0)}); err != nil {
		t.Fatalf("second store write: %v", err)
	}
	if err := first.AddRepository(ctx, Repository{ID: "three", Name: "three", CreatedAt: time.Unix(3, 0)}); err != nil {
		t.Fatalf("first store write after second open: %v", err)
	}
	for name, store := range map[string]*Store{"first": first, "second": second} {
		repositories, err := store.Repositories(ctx)
		if err != nil || len(repositories) != 3 {
			t.Fatalf("%s store sees %d repositories err=%v", name, len(repositories), err)
		}
	}
}

func TestMissingDatabaseWithRecoveryFilesIsRefused(t *testing.T) {
	for _, name := range []string{databaseName + walSuffix, databaseName + shmSuffix} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			noErr(t, os.WriteFile(filepath.Join(directory, name), []byte("orphan"), 0o600))
			before := captureSchemaDirectory(t, directory)
			openRefused(t, directory, "recovery files exist")
			assertSchemaDirectoryUnchanged(t, directory, before)
		})
	}
}

func TestRollbackJournalIsRefusedWithoutBeingRead(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "state")
	createNumberedSchemaDatabase(t, directory, currentSchemaVersion())
	sentinel := filepath.Join(root, "super-journal-sentinel")
	noErr(t, os.WriteFile(sentinel, []byte("outside"), 0o600))
	journal := append([]byte("\xd9\xd5\x05\xf9\x20\xa1\x63\xd7"), []byte(sentinel)...)
	noErr(t, os.WriteFile(filepath.Join(directory, databaseName+journalSuffix), journal, 0o600))
	before := captureSchemaDirectory(t, directory)
	sentinelBefore, err := os.Stat(sentinel)
	noErr(t, err)
	err = openRefused(t, directory, "rollback journal")
	if !errors.Is(err, errRollbackJournal) || errors.Is(err, ErrInspectionUnstable) {
		t.Fatalf("an existing rollback journal must be the permanent refusal, not instability: %v", err)
	}
	assertSchemaDirectoryUnchanged(t, directory, before)
	sentinelAfter, err := os.Stat(sentinel)
	if err != nil || !os.SameFile(sentinelBefore, sentinelAfter) || sentinelAfter.Size() != sentinelBefore.Size() || !sentinelAfter.ModTime().Equal(sentinelBefore.ModTime()) {
		t.Fatalf("super-journal sentinel changed: %v", err)
	}
}

// TestRollbackJournalPresenceContract pins the three outcomes of the journal
// predicate: presence of any entry type, absence, and an inspection failure
// whose filesystem cause is preserved rather than reported as presence.
func TestRollbackJournalPresenceContract(t *testing.T) {
	directory := t.TempDir()
	mainPath := filepath.Join(directory, databaseName)
	if present, err := rollbackJournalPresent(mainPath); err != nil || present {
		t.Fatalf("absent journal present=%v err=%v", present, err)
	}
	noErr(t, os.Mkdir(mainPath+journalSuffix, 0o700))
	if present, err := rollbackJournalPresent(mainPath); err != nil || !present {
		t.Fatalf("directory journal entry present=%v err=%v", present, err)
	}
	if runtime.GOOS == "windows" {
		t.Log("inspection-failure case uses POSIX ENOTDIR; not exercised on Windows")
		return
	}
	regular := filepath.Join(directory, "regular")
	noErr(t, os.WriteFile(regular, []byte("file"), 0o600))
	present, err := rollbackJournalPresent(filepath.Join(regular, databaseName))
	if present || err == nil || !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("inspection failure present=%v err=%v, want ENOTDIR preserved", present, err)
	}
	if errors.Is(err, ErrInspectionUnstable) || errors.Is(err, errRollbackJournal) {
		t.Fatalf("inspection failure was reported as presence or instability: %v", err)
	}
}

// TestRevalidationFilesystemFailureKeepsItsCause removes search permission
// from the parent of the state directory after the initial snapshot, so
// revalidation can no longer inspect the state entries by path while the
// state directory itself is unchanged. Open must report the permission
// failure, must not claim that anything appeared or that a journal exists,
// and must leave the state directory mode and entries untouched.
func TestRevalidationFilesystemFailureKeepsItsCause(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory search permission probe")
	}
	if os.Geteuid() == 0 {
		t.Skip("directory permissions do not restrict the superuser")
	}
	for _, test := range []struct {
		name    string
		prepare func(t *testing.T, directory string)
	}{
		{name: "fresh directory", prepare: func(t *testing.T, directory string) {
			noErr(t, os.Mkdir(directory, 0o750))
		}},
		{name: "current database", prepare: func(t *testing.T, directory string) {
			createNumberedSchemaDatabase(t, directory, currentSchemaVersion())
			setFixtureModes(t, directory, map[string]os.FileMode{".": 0o750, databaseName: 0o640})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent := filepath.Join(t.TempDir(), "parent")
			noErr(t, os.Mkdir(parent, 0o700))
			directory := filepath.Join(parent, "state")
			test.prepare(t, directory)
			before := captureSchemaDirectory(t, directory)
			t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
			hookAt(t, pointAccept, func(string) {
				noErr(t, os.Chmod(parent, 0o000))
			})
			store, err := Open(context.Background(), directory)
			if store != nil {
				_ = store.Close()
			}
			if err == nil || !errors.Is(err, os.ErrPermission) {
				t.Fatalf("Open error=%v, want the permission failure preserved", err)
			}
			if errors.Is(err, ErrInspectionUnstable) || errors.Is(err, errRollbackJournal) {
				t.Fatalf("filesystem failure was reported as appearance or presence: %v", err)
			}
			noErr(t, os.Chmod(parent, 0o700))
			if info, err := os.Stat(directory); err != nil || info.Mode().Perm() != 0o750 {
				t.Fatalf("refusal changed directory mode to %v err=%v", infoMode(info), err)
			}
			assertSchemaDirectoryUnchanged(t, directory, before)
		})
	}
}

func TestNonRegularStateEntriesAreRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need privileges on Windows; reparse points are covered by the same Lstat check natively")
	}
	root := t.TempDir()
	target := filepath.Join(root, "target")
	createNumberedSchemaDatabase(t, target, currentSchemaVersion())
	for _, test := range []struct {
		name  string
		build func(t *testing.T, directory string)
	}{
		{name: "database symlink", build: func(t *testing.T, directory string) {
			noErr(t, os.Symlink(filepath.Join(target, databaseName), filepath.Join(directory, databaseName)))
		}},
		{name: "WAL symlink", build: func(t *testing.T, directory string) {
			createNumberedSchemaDatabase(t, directory, currentSchemaVersion())
			noErr(t, os.Symlink(filepath.Join(target, databaseName), filepath.Join(directory, databaseName+walSuffix)))
		}},
		{name: "SHM directory", build: func(t *testing.T, directory string) {
			createNumberedSchemaDatabase(t, directory, currentSchemaVersion())
			noErr(t, os.Mkdir(filepath.Join(directory, databaseName+shmSuffix), 0o700))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "state")
			noErr(t, os.MkdirAll(directory, 0o700))
			test.build(t, directory)
			targetBefore := captureSchemaDirectory(t, target)
			openRefused(t, directory, "must be a regular file")
			assertSchemaDirectoryUnchanged(t, target, targetBefore)
		})
	}
}

func TestInspectionInstabilityIsRetryableAndLeavesSourceUntouched(t *testing.T) {
	walPath := func(directory string) string { return filepath.Join(directory, databaseName+walSuffix) }
	shmPath := func(directory string) string { return filepath.Join(directory, databaseName+shmSuffix) }
	tests := []struct {
		name    string
		keepSHM bool
		point   string
		disturb func(t *testing.T, directory string)
		restore func(t *testing.T, directory string)
	}{
		{name: "WAL append", keepSHM: true, point: pointHashed, disturb: func(t *testing.T, directory string) {
			file, err := os.OpenFile(walPath(directory), os.O_APPEND|os.O_WRONLY, 0)
			noErr(t, err)
			if _, err := file.Write(make([]byte, 24+4096)); err != nil {
				t.Fatal(err)
			}
			noErr(t, file.Close())
		}},
		{name: "concurrent checkpoint", keepSHM: true, point: pointHashed, disturb: func(t *testing.T, directory string) {
			db := openSchemaDatabase(t, filepath.Join(directory, databaseName))
			if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
				t.Fatal(err)
			}
			noErr(t, db.Close())
		}},
		{name: "WAL removed", keepSHM: true, point: pointHashed, disturb: func(t *testing.T, directory string) {
			noErr(t, os.Remove(walPath(directory)))
		}},
		{name: "WAL recreated", keepSHM: true, point: pointHashed, disturb: func(t *testing.T, directory string) {
			content, err := os.ReadFile(walPath(directory))
			noErr(t, err)
			noErr(t, os.Remove(walPath(directory)))
			noErr(t, os.WriteFile(walPath(directory), content, 0o600))
		}},
		{name: "SHM removed", keepSHM: true, point: pointHashed, disturb: func(t *testing.T, directory string) {
			noErr(t, os.Remove(shmPath(directory)))
		}},
		{name: "SHM appears", keepSHM: false, point: pointHashed, disturb: func(t *testing.T, directory string) {
			noErr(t, os.WriteFile(shmPath(directory), make([]byte, 32768), 0o600))
		}},
		// The appeared journal is removed before the retry, so the retry
		// proves that the appearance itself was the only refusal.
		{name: "rollback journal appears before acceptance", keepSHM: true, point: pointAccept, disturb: func(t *testing.T, directory string) {
			noErr(t, os.WriteFile(filepath.Join(directory, databaseName+journalSuffix), []byte("orphan"), 0o600))
		}, restore: func(t *testing.T, directory string) {
			noErr(t, os.Remove(filepath.Join(directory, databaseName+journalSuffix)))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "state")
			createCrashedWALFixture(t, directory, test.keepSHM, commitBaselineThenChangeVersion(""))
			setFixtureModes(t, directory, map[string]os.FileMode{".": 0o750, databaseName: 0o640})
			hookAt(t, test.point, func(string) { test.disturb(t, directory) })
			err := openRefused(t, directory, ErrInspectionUnstable.Error())
			if !errors.Is(err, ErrInspectionUnstable) {
				t.Fatalf("instability is not retryable: %v", err)
			}
			if runtime.GOOS != "windows" {
				if info, err := os.Stat(directory); err != nil || info.Mode().Perm() != 0o750 {
					t.Fatalf("unstable refusal changed directory mode to %v err=%v", infoMode(info), err)
				}
			}
			if test.restore != nil {
				test.restore(t, directory)
			}
			preflightHooks.at = nil
			store, err := Open(context.Background(), directory)
			if err != nil {
				t.Fatalf("retry after instability: %v", err)
			}
			store.Close()
		})
	}

	t.Run("database replaced before acceptance", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "state")
		createCrashedWALFixture(t, directory, true, commitBaselineThenChangeVersion(""))
		setFixtureModes(t, directory, map[string]os.FileMode{".": 0o750, databaseName: 0o640})
		replacement := filepath.Join(t.TempDir(), "replacement")
		createNumberedSchemaDatabase(t, replacement, currentSchemaVersion())
		replacementPath := filepath.Join(replacement, databaseName)
		databasePath := filepath.Join(directory, databaseName)
		replaced := captureSchemaDirectory(t, replacement)[databaseName]
		before := captureSchemaDirectory(t, directory)
		protections := captureProtectionFingerprints(t, directory, databasePath)
		replacementProtection := captureProtectionFingerprints(t, replacementPath)[replacementPath]
		var replacementErr error
		hookInspectionError(t, pointAccept, func(*inspection, string) error {
			replacementErr = os.Rename(replacementPath, databasePath)
			return replacementErr
		})
		store, err := Open(context.Background(), directory)
		if store != nil {
			_ = store.Close()
			t.Fatal("replacement attempt opened the state database")
		}
		if replacementErr != nil {
			if err == nil || !errors.Is(err, replacementErr) {
				t.Fatalf("prevented replacement error=%v, want cause %v", err, replacementErr)
			}
			assertSchemaDirectoryUnchanged(t, directory, before)
			assertProtectionFingerprints(t, protections)
			preflightHooks.at = nil
			if err := os.Rename(replacementPath, databasePath); err != nil {
				t.Fatalf("database replacement remained blocked after inspection release: %v", err)
			}
		} else if !errors.Is(err, ErrInspectionUnstable) {
			t.Fatalf("completed replacement error=%v, want instability", err)
		}
		after := captureSchemaDirectory(t, directory)
		if current, exists := after[databaseName]; !exists || !schemaSnapshotEqual(current, replaced) {
			t.Fatalf("replacement database changed or disappeared: entries=%v", mapKeys(after))
		}
		if fingerprint, err := protectionFingerprint(databasePath); err != nil || fingerprint != replacementProtection {
			t.Fatalf("replacement database protection changed: got=%q want=%q err=%v", fingerprint, replacementProtection, err)
		}
	})
}

func TestSidecarFreeInspectionDetectsAppearanceAndPermissionChange(t *testing.T) {
	tests := []struct {
		name    string
		disturb func(t *testing.T, directory string)
	}{
		{name: "WAL appears", disturb: func(t *testing.T, directory string) {
			noErr(t, os.WriteFile(filepath.Join(directory, databaseName+walSuffix), []byte("late"), 0o600))
		}},
		{name: "database modified", disturb: func(t *testing.T, directory string) {
			db := openSchemaDatabase(t, filepath.Join(directory, databaseName))
			if _, err := db.Exec(`INSERT INTO metadata(key,value) VALUES('late','1')`); err != nil {
				t.Fatal(err)
			}
			noErr(t, db.Close())
		}},
	}
	if runtime.GOOS != "windows" {
		tests = append(tests, struct {
			name    string
			disturb func(t *testing.T, directory string)
		}{name: "database mode changed", disturb: func(t *testing.T, directory string) {
			path := filepath.Join(directory, databaseName)
			info, err := os.Stat(path)
			noErr(t, err)
			noErr(t, os.Chmod(path, info.Mode().Perm()^0o040))
		}})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "state")
			createNumberedSchemaDatabase(t, directory, currentSchemaVersion())
			hookAt(t, pointClassified, func(string) { test.disturb(t, directory) })
			err := openRefused(t, directory, ErrInspectionUnstable.Error())
			if !errors.Is(err, ErrInspectionUnstable) {
				t.Fatalf("instability is not retryable: %v", err)
			}
		})
	}
}

// TestFreshDirectoryAcceptanceDetectsAppearanceAndReplacement covers a
// directory that had no database when inspected. Anything that appears or
// replaces the directory before acceptance must leave its mode unchanged.
func TestFreshDirectoryAcceptanceDetectsAppearanceAndReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode probe; Windows ACL has native coverage")
	}
	prepared := filepath.Join(t.TempDir(), "prepared")
	createNumberedSchemaDatabase(t, prepared, 5)
	schemaFive, err := os.ReadFile(filepath.Join(prepared, databaseName))
	noErr(t, err)
	tests := []struct {
		name     string
		fragment string
		disturb  func(t *testing.T, directory string)
	}{
		{name: "schema 5 database appears", fragment: ErrInspectionUnstable.Error(), disturb: func(t *testing.T, directory string) {
			noErr(t, os.WriteFile(filepath.Join(directory, databaseName), schemaFive, 0o640))
		}},
		{name: "WAL appears", fragment: ErrInspectionUnstable.Error(), disturb: func(t *testing.T, directory string) {
			noErr(t, os.WriteFile(filepath.Join(directory, databaseName+walSuffix), []byte("orphan"), 0o600))
		}},
		{name: "rollback journal appears", fragment: ErrInspectionUnstable.Error(), disturb: func(t *testing.T, directory string) {
			noErr(t, os.WriteFile(filepath.Join(directory, databaseName+journalSuffix), []byte("orphan"), 0o600))
		}},
		{name: "directory replaced", fragment: ErrInspectionUnstable.Error(), disturb: func(t *testing.T, directory string) {
			replacement := filepath.Join(filepath.Dir(directory), "replacement")
			noErr(t, os.Mkdir(replacement, 0o750))
			noErr(t, os.Rename(directory, directory+".moved"))
			noErr(t, os.Rename(replacement, directory))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "state")
			noErr(t, os.Mkdir(directory, 0o750))
			noErr(t, os.Chmod(directory, 0o750))
			reached := false
			hookAt(t, pointAccept, func(string) {
				reached = true
				test.disturb(t, directory)
			})
			before := captureSchemaDirectory(t, directory)
			if len(before) != 0 {
				t.Fatalf("fresh fixture is not empty: %v", mapKeys(before))
			}
			err := openRefused(t, directory, test.fragment)
			if !errors.Is(err, ErrInspectionUnstable) {
				t.Fatalf("appearance before acceptance is not retryable: %v", err)
			}
			if !reached {
				t.Fatal("acceptance point was not reached")
			}
			for _, path := range []string{directory, directory + ".moved"} {
				info, err := os.Stat(path)
				if os.IsNotExist(err) {
					continue
				}
				if err != nil || info.Mode().Perm() != 0o750 {
					t.Fatalf("refusal changed %s mode to %v err=%v", filepath.Base(path), infoMode(info), err)
				}
			}
			if appeared, exists := captureSchemaDirectory(t, directory)[databaseName]; exists && !bytes.Equal(appeared.data, schemaFive) {
				t.Fatal("refusal wrote to the database that appeared")
			}
			if _, err := os.Stat(filepath.Join(directory, databaseName+shmSuffix)); !os.IsNotExist(err) {
				t.Fatalf("refusal created a shared-memory index: %v", err)
			}
		})
	}
}

func TestBaselineReplacedBeforeAcceptanceIsNotMigrated(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	directory := filepath.Join(root, "state")
	if err := testfixture.CreateCommittedBaselineState(ctx, directory, testfixture.BaselineStateOptions{
		RepositoryRoot: filepath.Join(root, "repositories"), AdminPasswordHash: "synthetic-admin-hash",
	}); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(root, "replacement")
	createNumberedSchemaDatabase(t, replacement, 5)
	databasePath := filepath.Join(directory, databaseName)
	replacementPath := filepath.Join(replacement, databaseName)
	before := captureSchemaDirectory(t, directory)
	replaced := captureSchemaDirectory(t, replacement)
	protections := captureProtectionFingerprints(t, directory, databasePath)
	replacementProtection := captureProtectionFingerprints(t, replacementPath)[replacementPath]
	var replacementErr error
	hookInspectionError(t, pointAccept, func(*inspection, string) error {
		replacementErr = os.Rename(replacementPath, databasePath)
		return replacementErr
	})
	store, err := Open(ctx, directory)
	if store != nil {
		_ = store.Close()
		t.Fatal("baseline replacement opened the state database")
	}
	if replacementErr != nil {
		if err == nil || !errors.Is(err, replacementErr) {
			t.Fatalf("prevented replacement error=%v, want cause %v", err, replacementErr)
		}
		assertSchemaDirectoryUnchanged(t, directory, before)
		assertProtectionFingerprints(t, protections)
		preflightHooks.at = nil
		if err := os.Rename(replacementPath, databasePath); err != nil {
			t.Fatalf("baseline replacement remained blocked after inspection release: %v", err)
		}
	} else if !errors.Is(err, ErrInspectionUnstable) {
		t.Fatalf("completed replacement error=%v, want instability", err)
	}
	after := captureSchemaDirectory(t, directory)
	if len(after) != 1 || !schemaSnapshotEqual(after[databaseName], replaced[databaseName]) {
		t.Fatalf("replaced database was written: entries=%v", mapKeys(after))
	}
	if fingerprint, err := protectionFingerprint(databasePath); err != nil || fingerprint != replacementProtection {
		t.Fatalf("replacement database protection changed: got=%q want=%q err=%v", fingerprint, replacementProtection, err)
	}
	preflightHooks.at = nil
	openRefused(t, directory, "schema 5")
}

func schemaSnapshotEqual(left, right schemaFileSnapshot) bool {
	return left.mode == right.mode && string(left.data) == string(right.data)
}

type failingWriter struct {
	limit   int
	written int
}

func (w *failingWriter) Write(data []byte) (int, error) {
	if w.written+len(data) > w.limit {
		return 0, errors.New("no space left on device")
	}
	w.written += len(data)
	return len(data), nil
}

func TestPrivateInspectionFailuresLeaveSourceUntouched(t *testing.T) {
	tests := []struct {
		name     string
		fragment string
		arrange  func(t *testing.T, cancel context.CancelFunc)
	}{
		{name: "private directory creation", fragment: "create private inspection directory", arrange: func(t *testing.T, _ context.CancelFunc) {
			useHooks(t)
			preflightHooks.temporaryRoot = filepath.Join(t.TempDir(), "missing")
		}},
		{name: "private copy space", fragment: "write private copy", arrange: func(t *testing.T, _ context.CancelFunc) {
			useHooks(t)
			preflightHooks.privateWriter = func(*os.File) io.Writer { return &failingWriter{limit: 100} }
		}},
		{name: "cancellation", fragment: context.Canceled.Error(), arrange: func(t *testing.T, cancel context.CancelFunc) {
			hookAt(t, pointCapture, func(string) { cancel() })
		}},
		{name: "private query", fragment: "inspect state database", arrange: func(t *testing.T, _ context.CancelFunc) {
			hookAt(t, pointClassify, func(privateDir string) {
				noErr(t, os.Truncate(filepath.Join(privateDir, databaseName), 100))
			})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "state")
			createCrashedWALFixture(t, directory, true, commitBaselineThenChangeVersion(""))
			setFixtureModes(t, directory, map[string]os.FileMode{".": 0o750, databaseName: 0o640})
			temporaryRoot := t.TempDir()
			preflightHooks.temporaryRoot = temporaryRoot
			t.Cleanup(func() { preflightHooks.temporaryRoot = "" })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			test.arrange(t, cancel)
			before := captureSchemaDirectory(t, directory)
			store, err := Open(ctx, directory)
			if store != nil {
				_ = store.Close()
			}
			if err == nil || !strings.Contains(err.Error(), test.fragment) {
				t.Fatalf("Open error=%v, want %q", err, test.fragment)
			}
			assertSchemaDirectoryUnchanged(t, directory, before)
			if entries, err := os.ReadDir(temporaryRoot); err != nil || len(entries) != 0 {
				t.Fatalf("private inspection leftovers=%d err=%v", len(entries), err)
			}
		})
	}
}

func TestPrivateInspectionCleanupFailureBlocksOpen(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	createCrashedWALFixture(t, directory, true, commitBaselineThenChangeVersion(""))
	setFixtureModes(t, directory, map[string]os.FileMode{".": 0o750, databaseName: 0o640})
	temporaryRoot := t.TempDir()
	preflightHooks.temporaryRoot = temporaryRoot
	t.Cleanup(func() { preflightHooks.temporaryRoot = "" })
	var leftover string
	hookAt(t, pointClassified, func(privateDir string) {
		leftover = privateDir
		blockPrivateRemoval(t, privateDir)
	})
	before := captureSchemaDirectory(t, directory)
	openRefused(t, directory, "remove private inspection copy")
	assertSchemaDirectoryUnchanged(t, directory, before)
	if leftover == "" {
		t.Fatal("private copy was never created")
	}
	if _, err := os.Stat(leftover); err != nil {
		t.Fatalf("blocked private copy disappeared: %v", err)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(leftover); err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("private leftover mode=%v err=%v", infoMode(info), err)
		}
	}
}

// TestPrivateCleanupFailureIsJoinedWithCompatibilityError requires the
// unsupported-schema refusal and the private cleanup failure to be reported
// together, so neither hides the other.
func TestPrivateCleanupFailureIsJoinedWithCompatibilityError(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	createCrashedWALFixture(t, directory, true, commitBaselineThenChangeVersion("5"))
	setFixtureModes(t, directory, map[string]os.FileMode{".": 0o750, databaseName: 0o640})
	preflightHooks.temporaryRoot = t.TempDir()
	t.Cleanup(func() { preflightHooks.temporaryRoot = "" })
	hookAt(t, pointClassified, func(privateDir string) { blockPrivateRemoval(t, privateDir) })
	before := captureSchemaDirectory(t, directory)
	err := openRefused(t, directory, "schema 5")
	if !strings.Contains(err.Error(), "remove private inspection copy") {
		t.Fatalf("cleanup failure was hidden by the compatibility error: %v", err)
	}
	assertSchemaDirectoryUnchanged(t, directory, before)
}

// TestSourceReleaseFailureBlocksWritableOpen closes an owned source handle
// underneath the inspection so its release fails, and requires Open to refuse
// the accepted database instead of proceeding read-write.
func TestSourceReleaseFailureBlocksWritableOpen(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(t *testing.T, directory string)
		handle  func(in *inspection) *os.File
	}{
		{name: "directory handle on fresh path", prepare: func(*testing.T, string) {}, handle: func(in *inspection) *os.File { return in.dir.handle }},
		{name: "database handle on immutable path", prepare: func(t *testing.T, directory string) {
			createNumberedSchemaDatabase(t, directory, currentSchemaVersion())
		}, handle: func(in *inspection) *os.File { return in.main.handle }},
		{name: "WAL handle on private-copy path", prepare: func(t *testing.T, directory string) {
			createCrashedWALFixture(t, directory, true, commitBaselineThenChangeVersion(""))
		}, handle: func(in *inspection) *os.File { return in.wal.handle }},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "state")
			noErr(t, os.MkdirAll(directory, 0o700))
			test.prepare(t, directory)
			before := captureSchemaDirectory(t, directory)
			paths := []string{directory}
			for _, name := range mapKeys(before) {
				paths = append(paths, filepath.Join(directory, name))
			}
			protections := captureProtectionFingerprints(t, paths...)
			hookInspectionError(t, pointAccept, func(in *inspection, _ string) error {
				return test.handle(in).Close()
			})
			store, err := Open(context.Background(), directory)
			if store != nil {
				_ = store.Close()
				t.Fatal("source release failure opened the state database")
			}
			if err == nil || !strings.Contains(err.Error(), "inspect held ") || !strings.Contains(err.Error(), "release ") {
				t.Fatalf("source release error=%v", err)
			}
			assertSchemaDirectoryUnchanged(t, directory, before)
			assertProtectionFingerprints(t, protections)
			preflightHooks.at = nil
			store, err = Open(context.Background(), directory)
			if err != nil {
				t.Fatalf("reopen after release failure: %v", err)
			}
			store.Close()
		})
	}
}

// TestPrivateCopyCloseFailureIsReportedWithCopyError closes the private copy
// underneath the writer so both the write failure and the close failure
// appear in the refusal.
func TestPrivateCopyCloseFailureIsReportedWithCopyError(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	createCrashedWALFixture(t, directory, true, commitBaselineThenChangeVersion(""))
	useHooks(t)
	preflightHooks.temporaryRoot = t.TempDir()
	preflightHooks.privateWriter = func(file *os.File) io.Writer {
		noErr(t, file.Close())
		return file
	}
	before := captureSchemaDirectory(t, directory)
	err := openRefused(t, directory, "write private copy of "+databaseName)
	if !strings.Contains(err.Error(), "close private copy of "+databaseName) {
		t.Fatalf("close failure was hidden by the write failure: %v", err)
	}
	assertSchemaDirectoryUnchanged(t, directory, before)
	if entries, err := os.ReadDir(preflightHooks.temporaryRoot); err != nil || len(entries) != 0 {
		t.Fatalf("private inspection leftovers=%d err=%v", len(entries), err)
	}
}

// blockPrivateRemoval makes privateDir non-removable until test cleanup. On
// Unix a child directory loses its write bit while holding an entry; on
// Windows an open handle without delete sharing keeps a file in place.
func blockPrivateRemoval(t *testing.T, privateDir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		file, err := os.Open(filepath.Join(privateDir, databaseName))
		noErr(t, err)
		t.Cleanup(func() { _ = file.Close() })
		return
	}
	if os.Geteuid() == 0 {
		t.Skip("directory write bits do not block the superuser")
	}
	blocker := filepath.Join(privateDir, "blocker")
	noErr(t, os.Mkdir(blocker, 0o700))
	noErr(t, os.WriteFile(filepath.Join(blocker, "held"), []byte("held"), 0o600))
	noErr(t, os.Chmod(blocker, 0o500))
	t.Cleanup(func() { _ = os.Chmod(blocker, 0o700) })
}
