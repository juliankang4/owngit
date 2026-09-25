package state

import (
	"context"
	"reflect"
	"testing"
)

func TestNetworkSettingsAreOptionalMetadata(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	before, err := store.Settings(ctx)
	noErr(t, err)
	if network, err := store.NetworkSettings(ctx); err != nil || network != (NetworkSettings{}) {
		t.Fatalf("fresh network settings=%+v err=%v", network, err)
	}
	noErr(t, store.AddTrustedHost(ctx, "Old.Example"))
	noErr(t, store.UpdateNetwork(ctx, NetworkUpdate{
		Settings: NetworkSettings{Listen: "0.0.0.0:7654", BaseURL: "http://gitbox.internal:7654"},
		AddHosts: []string{"gitbox.internal", "100.64.0.7"}, RemoveHosts: []string{"Old.Example"},
	}))
	network, err := store.NetworkSettings(ctx)
	noErr(t, err)
	if network != (NetworkSettings{Listen: "0.0.0.0:7654", BaseURL: "http://gitbox.internal:7654"}) {
		t.Fatalf("saved network settings=%+v", network)
	}
	hosts, err := store.TrustedHosts(ctx)
	noErr(t, err)
	if !reflect.DeepEqual(hosts, []string{"100.64.0.7", "gitbox.internal"}) {
		t.Fatalf("allowed hosts=%v", hosts)
	}
	// Other settings are untouched and still decode.
	after, err := store.Settings(ctx)
	noErr(t, err)
	if after != before {
		t.Fatalf("settings changed: before=%+v after=%+v", before, after)
	}
	// An empty value removes the key, so the default applies again.
	noErr(t, store.UpdateNetwork(ctx, NetworkUpdate{Settings: NetworkSettings{Listen: "0.0.0.0:7654"}}))
	var rows int
	noErr(t, store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM metadata WHERE key=?`, networkBaseURLKey).Scan(&rows))
	if rows != 0 {
		t.Fatalf("empty base URL left %d metadata rows", rows)
	}
	if version, err := store.schemaVersion(ctx); err != nil || version != currentSchemaVersion() {
		t.Fatalf("schema version=%d err=%v", version, err)
	}
}

func TestRunningNetworkRecordRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	if _, found, err := store.RunningNetwork(ctx); err != nil || found {
		t.Fatalf("fresh running record found=%v err=%v", found, err)
	}
	running := RunningNetwork{
		PID: 42, StartedAt: 1700000000, Listen: "127.0.0.1:0", Address: "127.0.0.1:50123",
		ListenSource: "flag", BaseURLSource: "default", Origin: "http://127.0.0.1:50123",
		SavedHosts: []string{}, AcceptedHosts: []string{"127.0.0.1", "::1", "localhost"},
	}
	noErr(t, store.PublishRunningNetwork(ctx, running))
	got, found, err := store.RunningNetwork(ctx)
	noErr(t, err)
	if !found || !reflect.DeepEqual(got, running) {
		t.Fatalf("running record=%+v found=%v", got, found)
	}
	noErr(t, store.ClearRunningNetwork(ctx))
	if _, found, err := store.RunningNetwork(ctx); err != nil || found {
		t.Fatalf("cleared running record found=%v err=%v", found, err)
	}
}
