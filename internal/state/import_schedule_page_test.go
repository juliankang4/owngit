package state

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestDueImportSchedulePagesKeepOriginalOrderAcrossClaims(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := testImportNow().Add(900 * time.Millisecond)
	want := []string{"early", "alpha", "beta", "epoch", "delta", "gamma"}
	for _, id := range want {
		_, err := store.SetImportSchedule(ctx, id, true, time.Minute, now)
		noErr(t, err)
	}
	for id, started := range map[string]int64{"early": -1, "epoch": 0, "delta": now.Unix() - 60, "gamma": now.Unix() - 60} {
		noErr(t, store.Exec(ctx, `UPDATE import_schedules SET last_started_at=? WHERE repository_id=?`, started, id))
	}
	var after *ImportSchedule
	var seen []string
	for {
		page, err := store.DueImportSchedulesAfter(ctx, now, after, 2)
		noErr(t, err)
		if len(page) == 0 {
			break
		}
		if len(page) > 2 {
			t.Fatal("page exceeded its bound")
		}
		for _, row := range page {
			seen = append(seen, row.RepositoryID)
			if row.RepositoryID != "alpha" {
				claimed, err := store.ClaimDueImportSchedule(ctx, row.RepositoryID, now)
				if err != nil || !claimed {
					t.Fatalf("claim %s: claimed=%v err=%v", row.RepositoryID, claimed, err)
				}
				if claimed, err = store.ClaimDueImportSchedule(ctx, row.RepositoryID, now); err != nil || claimed {
					t.Fatalf("duplicate claim: claimed=%v err=%v", claimed, err)
				}
			}
		}
		after = &page[len(page)-1]
	}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("examined=%v want=%v", seen, want)
	}
	remaining, err := store.DueImportSchedulesAfter(ctx, now, nil, 0)
	if err != nil || len(remaining) != 1 || remaining[0].RepositoryID != "alpha" || remaining[0].LastStartedAt != nil {
		t.Fatalf("skipped schedule was changed: %v err=%v", remaining, err)
	}
	noErr(t, store.db.Close())
	if _, err := store.DueImportSchedulesAfter(ctx, now, nil, 2); err == nil {
		t.Fatal("query failure became an empty page")
	}
}
