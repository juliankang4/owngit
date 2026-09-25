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

func TestTrustedProxiesAreAddedAndRemovedInOneUpdate(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	if proxies, err := store.TrustedProxies(ctx); err != nil || len(proxies) != 0 {
		t.Fatalf("fresh trusted proxies=%v err=%v", proxies, err)
	}
	noErr(t, store.UpdateNetwork(ctx, NetworkUpdate{AddProxies: []string{"192.0.2.10", "10.1.0.0/16", "192.0.2.10"}}))
	noErr(t, store.UpdateNetwork(ctx, NetworkUpdate{AddProxies: []string{"fd00::10"}, RemoveProxies: []string{"192.0.2.10", "198.51.100.1"}}))
	proxies, err := store.TrustedProxies(ctx)
	noErr(t, err)
	if !reflect.DeepEqual(proxies, []string{"10.1.0.0/16", "fd00::10"}) {
		t.Fatalf("trusted proxies=%v", proxies)
	}
	// An update without proxy changes keeps them.
	noErr(t, store.UpdateNetwork(ctx, NetworkUpdate{Settings: NetworkSettings{Listen: "127.0.0.1:7654"}}))
	noErr(t, store.UpdateNetwork(ctx, NetworkUpdate{RemoveProxies: []string{"10.1.0.0/16"}}))
	if proxies, err := store.TrustedProxies(ctx); err != nil || !reflect.DeepEqual(proxies, []string{"fd00::10"}) {
		t.Fatalf("trusted proxies=%v err=%v", proxies, err)
	}
	noErr(t, store.UpdateNetwork(ctx, NetworkUpdate{RemoveProxies: []string{"fd00::10"}}))
	var rows int
	noErr(t, store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM metadata WHERE key=?`, networkTrustedProxiesKey).Scan(&rows))
	if rows != 0 {
		t.Fatalf("an empty list left %d metadata rows", rows)
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
		TrustedProxies: []string{"192.0.2.10"}, TrustedProxiesSource: "saved",
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

// The Tailscale sharing record is optional metadata that changes in the same
// transaction as the network settings it describes.
func TestTailscaleSharingRecordChangesWithTheNetworkSettings(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	if _, found, err := store.TailscaleServe(ctx); err != nil || found {
		t.Fatalf("fresh record found=%v err=%v", found, err)
	}
	record := TailscaleServe{Name: "box.tail0000.ts.net", HTTPSPort: 443, Target: "http://127.0.0.1:7654", Created: true}
	noErr(t, store.SaveTailscaleServe(ctx, record))
	record.Confirmed, record.BaseURL, record.AddedProxy = true, "https://box.tail0000.ts.net", "127.0.0.1"
	noErr(t, store.UpdateNetwork(ctx, NetworkUpdate{
		Settings: NetworkSettings{BaseURL: record.BaseURL}, AddProxies: []string{"127.0.0.1"}, Tailscale: &record,
	}))
	if saved, found, err := store.TailscaleServe(ctx); err != nil || !found || !reflect.DeepEqual(saved, record) {
		t.Fatalf("saved=%+v found=%v err=%v", saved, found, err)
	}
	noErr(t, store.UpdateNetwork(ctx, NetworkUpdate{RemoveProxies: []string{"127.0.0.1"}, ClearTailscale: true}))
	if _, found, err := store.TailscaleServe(ctx); err != nil || found {
		t.Fatalf("cleared record found=%v err=%v", found, err)
	}
	if proxies, err := store.TrustedProxies(ctx); err != nil || len(proxies) != 0 {
		t.Fatalf("proxies=%v err=%v", proxies, err)
	}
}
