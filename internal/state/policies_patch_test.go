package state

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestPatchPoliciesKeepsConcurrentNamedFields(t *testing.T) {
	raw, file, maximum, operation := int64(11<<20), int64(3<<20), int64(2<<30), int64(1200)
	start, end, threshold := 22, 6, 30
	enabled, grace := true, int64(30)
	attempts, pause := 2, int64(1200)
	output, queue := int64(2<<20), int64(12)
	for _, test := range []struct {
		name          string
		first, second PolicyFields
		read          func(*Store) (any, error)
		want          any
	}{
		{"browse", PolicyFields{Browse: &BrowseFields{RawBytes: &raw}}, PolicyFields{Browse: &BrowseFields{FileBytes: &file}},
			func(s *Store) (any, error) { return s.BrowseLimits(t.Context()) },
			func() BrowseLimits { v := DefaultBrowseLimits; v.RawBytes, v.FileBytes = raw, file; return v }()},
		{"transfer", PolicyFields{GitTransfer: &GitTransferFields{MaximumBytes: &maximum}}, PolicyFields{GitTransfer: &GitTransferFields{OperationSeconds: &operation}},
			func(s *Store) (any, error) { return s.GitTransferLimits(t.Context()) },
			func() GitTransferLimits {
				v := DefaultGitTransferLimits
				v.MaximumBytes, v.Operation = maximum, time.Duration(operation)*time.Second
				return v
			}()},
		{"maintenance", PolicyFields{Maintenance: &MaintenanceFields{WindowStartHour: &start, WindowEndHour: &end}}, PolicyFields{Maintenance: &MaintenanceFields{PackThreshold: &threshold}},
			func(s *Store) (any, error) { return s.Maintenance(t.Context()) },
			func() Maintenance {
				v := DefaultMaintenance
				v.WindowStart, v.WindowEnd, v.PackThreshold = start, end, threshold
				return v
			}()},
		{"cleanup", PolicyFields{Cleanup: &CleanupFields{Enabled: &enabled}}, PolicyFields{Cleanup: &CleanupFields{GraceDays: &grace}},
			func(s *Store) (any, error) { return s.UnusedObjectCleanup(t.Context()) },
			UnusedObjectCleanup{Enabled: true, Grace: 30 * 24 * time.Hour}},
		{"login", PolicyFields{LoginLimits: &LoginLimitFields{Attempts: &attempts}}, PolicyFields{LoginLimits: &LoginLimitFields{PauseSeconds: &pause}},
			func(s *Store) (any, error) { return s.LoginLimits(t.Context()) },
			func() LoginLimits {
				v := DefaultLoginLimits
				v.Attempts, v.Pause = attempts, time.Duration(pause)*time.Second
				return v
			}()},
		{"ceilings", PolicyFields{CheckCeilings: &CheckCeilingFields{OutputBytes: &output}}, PolicyFields{CheckCeilings: &CheckCeilingFields{QueueLimit: &queue}},
			func(s *Store) (any, error) { return s.CheckCeilings(t.Context()) },
			func() CheckCeilings {
				v := DefaultCheckCeilings
				v.OutputLimitBytes, v.QueueLimit = output, queue
				return v
			}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := openTestStore(t)
			other, err := Open(t.Context(), store.Dir())
			noErr(t, err)
			t.Cleanup(func() { _ = other.Close() })
			start := make(chan struct{})
			finished := make(chan error, 2)
			for _, writer := range []struct {
				store  *Store
				fields PolicyFields
			}{{store, test.first}, {other, test.second}} {
				go func() {
					<-start
					_, err := writer.store.PatchPolicies(t.Context(), PolicyChange{}, writer.fields)
					finished <- err
				}()
			}
			close(start)
			noErr(t, <-finished)
			noErr(t, <-finished)
			got, err := test.read(store)
			noErr(t, err)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("saved=%+v, want=%+v", got, test.want)
			}
		})
	}
}

func TestPatchPoliciesValidatesCurrentValuesAndSavesNothingOnFailure(t *testing.T) {
	store := openTestStore(t)
	end := 4
	_, err := store.PatchPolicies(t.Context(), PolicyChange{}, PolicyFields{Maintenance: &MaintenanceFields{WindowEndHour: &end}})
	noErr(t, err)
	before, err := store.Maintenance(t.Context())
	noErr(t, err)
	branch, raw := "trunk", int64(11<<20)
	saved, err := store.PatchPolicies(t.Context(), PolicyChange{InitialBranch: &branch}, PolicyFields{
		Maintenance: &MaintenanceFields{WindowStartHour: &end}, Browse: &BrowseFields{RawBytes: &raw},
	})
	var invalid *PolicyFieldError
	if !errors.As(err, &invalid) || saved != (PolicyChange{}) || invalid.Error() != "maintenance: the window starts and ends at different whole hours from 0 to 23." {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
	if got, err := store.Maintenance(t.Context()); err != nil || got != before {
		t.Fatalf("maintenance=%+v err=%v", got, err)
	}
	if got, err := store.InitialBranch(t.Context()); err != nil || got != "main" {
		t.Fatalf("branch=%q err=%v", got, err)
	}
	if got, err := store.BrowseLimits(t.Context()); err != nil || got != DefaultBrowseLimits {
		t.Fatalf("browse=%+v err=%v", got, err)
	}
}

func TestPatchPoliciesRollsBackEveryWriteWhenStorageRefusesOne(t *testing.T) {
	store := openTestStore(t)
	noErr(t, store.Exec(t.Context(), `CREATE TRIGGER refuse_policy BEFORE INSERT ON metadata WHEN NEW.key='browse_limits' BEGIN SELECT RAISE(ABORT, 'save refused'); END`))
	branch, raw := "trunk", int64(11<<20)
	saved, err := store.PatchPolicies(t.Context(), PolicyChange{InitialBranch: &branch}, PolicyFields{Browse: &BrowseFields{RawBytes: &raw}})
	if err == nil || saved != (PolicyChange{}) {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
	if got, err := store.InitialBranch(t.Context()); err != nil || got != "main" {
		t.Fatalf("branch=%q err=%v", got, err)
	}
	if got, err := store.BrowseLimits(t.Context()); err != nil || got != DefaultBrowseLimits {
		t.Fatalf("browse=%+v err=%v", got, err)
	}
}
