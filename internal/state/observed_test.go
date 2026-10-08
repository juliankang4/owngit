package state

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestObservedStatePreservesItsSource(t *testing.T) {
	ctx := context.Background()
	oldVersion := 0
	for _, step := range schemaSteps {
		if step.released && step.version < currentSchemaVersion() {
			oldVersion = step.version
			break
		}
	}
	for _, test := range []struct {
		name        string
		version     int
		active      bool
		temporary   string
		linked      bool
		systemTemp  bool
		rootMode    os.FileMode
		replacement bool
		prepare     func(*testing.T, string)
		refusal     string
		server      string
		planned     []string
	}{
		{name: "missing locks", server: ServerNotRunning},
		{name: "system temporary root", systemTemp: true, server: ServerNotRunning},
		{name: "sticky temporary root", rootMode: os.ModeSticky | 0o777, server: ServerNotRunning},
		{name: "writable temporary root", rootMode: 0o777, refusal: "set " + temporaryEnvironment + " to a writable temporary directory outside the state directory"},
		{name: "replacement at cleanup", replacement: true, server: ServerNotRunning},
		{name: "temporary root in state", temporary: ".", server: ServerNotRunning},
		{name: "temporary root below state", temporary: "runtime/tmp", server: ServerNotRunning},
		{name: "linked temporary root below state", temporary: "runtime/tmp", linked: true, server: ServerNotRunning},
		{name: "committed WAL", active: true, server: ServerStarting},
		{name: "offline holder", server: ServerUnknown, prepare: func(t *testing.T, dir string) {
			release, err := AcquireOfflineLock(dir)
			noErr(t, err)
			t.Cleanup(release)
		}},
		{name: "extra read access", planned: []string{".", databaseName}, server: ServerNotRunning, prepare: func(t *testing.T, dir string) {
			if runtime.GOOS == "windows" {
				t.Skip("Unix modes; Windows grants have their own table")
			}
			noErr(t, os.Chmod(dir, 0o755))
			noErr(t, os.Chmod(filepath.Join(dir, databaseName), 0o644))
		}},
		{name: "owner read-only", planned: []string{".", databaseName}, server: ServerNotRunning, prepare: func(t *testing.T, dir string) {
			if runtime.GOOS == "windows" {
				t.Skip("Unix modes; Windows grants have their own table")
			}
			noErr(t, os.Chmod(dir, 0o500))
			noErr(t, os.Chmod(filepath.Join(dir, databaseName), 0o400))
			t.Cleanup(func() { noErr(t, os.Chmod(dir, 0o700)) })
		}},
		{name: "database writer", refusal: "another account can change", prepare: func(t *testing.T, dir string) {
			if runtime.GOOS == "windows" {
				t.Skip("Unix modes; Windows grants have their own table")
			}
			noErr(t, os.Chmod(filepath.Join(dir, databaseName), 0o666))
		}},
		{name: "database with a second name", refusal: "replace", prepare: func(t *testing.T, dir string) {
			noErr(t, os.Link(filepath.Join(dir, databaseName), filepath.Join(t.TempDir(), "outside")))
		}},
		{name: "incomplete restore", refusal: "incomplete offline restore", prepare: func(t *testing.T, dir string) {
			file, err := CreatePrivateFile(filepath.Join(dir, IncompleteRestoreMarkerName))
			noErr(t, err)
			noErr(t, file.Close())
		}},
		{name: "older schema", version: oldVersion, refusal: ErrOlderSchema.Error()},
		{name: "unsupported schema", version: 5, refusal: "schema 5"},
		{name: "failed snapshot", refusal: "set " + temporaryEnvironment + " to a writable temporary directory outside the state directory", prepare: func(t *testing.T, dir string) {
			useHooks(t)
			preflightHooks.temporaryRoot = filepath.Join(dir, databaseName)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "state")
			if test.version != 0 {
				if test.version == 5 {
					createNumberedSchemaDatabase(t, dir, test.version)
				} else {
					createMigratedSchemaDatabase(t, dir, test.version)
				}
				noErr(t, ProtectPrivatePath(dir, true))
				noErr(t, ProtectPrivatePath(filepath.Join(dir, databaseName), false))
			} else {
				writer, err := Open(ctx, dir)
				noErr(t, err)
				noErr(t, writer.Exec(ctx, "INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", networkListenKey, "127.0.0.1:19867"))
				if test.active {
					release, err := writer.ClaimRunningNetwork(ctx)
					noErr(t, err)
					t.Cleanup(func() { release(); noErr(t, writer.Close()) })
				} else {
					noErr(t, writer.Close())
				}
			}
			useHooks(t)
			staging := t.TempDir()
			preflightHooks.temporaryRoot = staging
			if test.rootMode != 0 {
				if runtime.GOOS == "windows" {
					t.Skip("Unix modes; Windows grants have their own table")
				}
				noErr(t, os.Chmod(staging, test.rootMode))
			}
			if test.systemTemp {
				root, err := systemTemporaryRoot()
				noErr(t, err)
				preflightHooks.temporaryRoot = root
			}
			rootBefore, err := LstatIdentity(staging)
			noErr(t, err)
			if test.prepare != nil {
				test.prepare(t, dir)
			}
			if test.temporary != "" {
				root := filepath.Join(dir, filepath.FromSlash(test.temporary))
				held, err := CreateDirectory(root)
				noErr(t, err)
				noErr(t, held.Close())
				if test.linked {
					link := filepath.Join(staging, "linked-root")
					if err := os.Symlink(root, link); err != nil && runtime.GOOS == "windows" {
						t.Skipf("directory links unavailable: %v", err)
					} else {
						noErr(t, err)
					}
					root = link
				}
				preflightHooks.temporaryRoot = ""
				for _, variable := range []string{"TMPDIR", "TMP", "TEMP"} {
					t.Setenv(variable, root)
				}
			}
			before := captureObservedSource(t, dir)
			var paths []string
			for path := range before {
				paths = append(paths, path)
			}
			permissions := captureProtectionFingerprints(t, paths...)
			var snapshot string
			var inspected *inspection
			hookInspection(t, pointCapture, func(in *inspection, privateDir string) {
				snapshot, inspected = privateDir, in
			})
			observed, err := OpenObserved(ctx, dir)
			if observed != nil {
				defer observed.Close()
			}
			if test.refusal != "" {
				if observed != nil || err == nil || !strings.Contains(err.Error(), test.refusal) {
					t.Fatalf("open=%v, error=%v, want %q", observed, err, test.refusal)
				}
				var private *NotPrivateError
				if test.rootMode == 0o777 && errors.As(err, &private) {
					t.Fatal("temporary-folder refusal was reported as unsafe state")
				}
			} else {
				noErr(t, err)
				settings, err := observed.NetworkSettings(ctx)
				noErr(t, err)
				if settings.Listen != "127.0.0.1:19867" {
					t.Fatalf("snapshot lost committed settings: %q", settings.Listen)
				}
				status, err := observed.ObserveRunningNetwork(ctx)
				noErr(t, err)
				if status.Server != test.server {
					t.Fatalf("server=%q, want %q", status.Server, test.server)
				}
				var planned []string
				for _, change := range observed.StateProtectionChanges() {
					planned = append(planned, change.Path)
				}
				slices.Sort(planned)
				if !slices.Equal(planned, test.planned) {
					t.Fatalf("planned=%v, want %v", planned, test.planned)
				}
				if err := observed.Exec(ctx, "DELETE FROM metadata"); err == nil {
					t.Fatal("observational queries allowed a write")
				}
				var sentinel os.FileInfo
				if test.replacement {
					noErr(t, observed.db.Close())
					original := filepath.Join(t.TempDir(), "original")
					if runtime.GOOS == "windows" {
						if err := os.Rename(snapshot, original); err == nil {
							t.Fatal("held snapshot allowed replacement")
						}
						noErr(t, inspected.privateDir.handle.Close())
						inspected.privateDir.handle = nil
					}
					noErr(t, os.Rename(snapshot, original))
					replacement := filepath.Join(t.TempDir(), "replacement")
					noErr(t, MkdirPrivate(replacement))
					noErr(t, os.WriteFile(filepath.Join(replacement, "sentinel"), []byte("keep"), 0o600))
					noErr(t, os.Rename(replacement, snapshot))
					sentinel, err = LstatIdentity(filepath.Join(snapshot, "sentinel"))
					noErr(t, err)
				}
				closeErr := observed.Close()
				if test.replacement {
					if closeErr == nil || !strings.Contains(closeErr.Error(), "left it in place") {
						t.Fatalf("cleanup error=%v, want replacement left in place", closeErr)
					}
					path := filepath.Join(snapshot, "sentinel")
					info, err := LstatIdentity(path)
					noErr(t, err)
					data, err := os.ReadFile(path)
					noErr(t, err)
					if !os.SameFile(sentinel, info) || string(data) != "keep" {
						t.Fatal("cleanup changed the replacement sentinel")
					}
				} else {
					noErr(t, closeErr)
				}
				if repeated := observed.Close(); repeated != closeErr {
					t.Fatalf("repeated close error=%v, want %v", repeated, closeErr)
				}
			}
			after := captureObservedSource(t, dir)
			if len(after) != len(before) {
				t.Fatalf("inspection changed source entries: before=%v after=%v", before, after)
			}
			for path, want := range before {
				got, ok := after[path]
				if !ok || !os.SameFile(want.info, got.info) || want.info.Mode() != got.info.Mode() || want.info.Size() != got.info.Size() || !want.info.ModTime().Equal(got.info.ModTime()) || !bytes.Equal(want.data, got.data) {
					t.Fatalf("inspection changed source metadata or bytes: %s", path)
				}
			}
			assertProtectionFingerprints(t, permissions)
			if test.rootMode == 0o777 {
				rootAfter, err := LstatIdentity(staging)
				noErr(t, err)
				if snapshot != "" || !os.SameFile(rootBefore, rootAfter) || !rootBefore.ModTime().Equal(rootAfter.ModTime()) {
					t.Fatal("unsafe temporary root changed before refusal")
				}
			}
			if snapshot != "" && !test.replacement {
				if _, err := os.Stat(snapshot); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("private snapshot remains after close or refusal: %s (%v)", snapshot, err)
				}
			}
			copies, err := os.ReadDir(staging)
			noErr(t, err)
			if test.linked || test.replacement {
				copies = slices.DeleteFunc(copies, func(entry os.DirEntry) bool {
					return test.linked && entry.Name() == "linked-root" || test.replacement && entry.Name() == filepath.Base(snapshot)
				})
			}
			if len(copies) != 0 {
				t.Fatalf("private snapshots remain after close or refusal: %v", copies)
			}
		})
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := OpenObserved(ctx, missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing state error=%v", err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("observational open created a state directory: %v", err)
	}
}

type observedSourceEntry struct {
	info os.FileInfo
	data []byte
}

func captureObservedSource(t *testing.T, dir string) map[string]observedSourceEntry {
	t.Helper()
	entries := make(map[string]observedSourceEntry)
	noErr(t, filepath.WalkDir(dir, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := LstatIdentity(path)
		if err != nil {
			return err
		}
		var data []byte
		if info.Mode().IsRegular() && info.Size() != 0 {
			data, err = os.ReadFile(path)
		}
		entries[path] = observedSourceEntry{info, data}
		return err
	}))
	return entries
}
