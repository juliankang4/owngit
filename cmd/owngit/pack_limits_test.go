package main

import (
	"context"
	"reflect"
	"testing"

	"owngit/internal/githttp"
	"owngit/internal/hostmem"
)

// A small known memory ceiling limits only the requests that build a pack, and
// the packing memory is shared among them; an unknown ceiling leaves the saved
// limits alone.
func TestPackLimitsFollowTheMemoryCeiling(t *testing.T) {
	saved := func(context.Context) (githttp.Limits, error) {
		return githttp.Limits{PerRepository: 4, ExtraSlots: 1}, nil
	}
	tests := []struct {
		name              string
		ceiling           uint64
		wantSlots, wantAt int
	}{
		{"512 MiB", 512 << 20, 1, 1},
		{"1 GiB", 1 << 30, 2, 2},
		{"8 GiB", 8 << 30, 36, 5},
		{"unknown", 0, 0, 5},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var packers int
			limits, err := packLimits(saved, test.ceiling, func(n int) { packers = n })(context.Background())
			noErr(t, err)
			if limits.PackSlots != test.wantSlots || packers != test.wantAt {
				t.Errorf("pack slots %d, packing shared by %d; want %d and %d", limits.PackSlots, packers, test.wantSlots, test.wantAt)
			}
			if limits.PerRepository != 4 || limits.ExtraSlots != 1 {
				t.Errorf("saved slots became %d + %d, want 4 + 1", limits.PerRepository, limits.ExtraSlots)
			}
		})
	}
}

// With an unknown ceiling, a large saved transfer limit does not shrink the
// packing settings every Git process gets.
func TestPackLimitsKeepPackingSettingsForALooseOwnerSettingWhenTheCeilingIsUnknown(t *testing.T) {
	saved := func(context.Context) (githttp.Limits, error) {
		return githttp.Limits{PerRepository: 32, ExtraSlots: 32}, nil
	}
	var packers int
	limits, err := packLimits(saved, 0, func(n int) { packers = n })(context.Background())
	noErr(t, err)
	if limits.PerRepository != 32 || limits.ExtraSlots != 32 || limits.PackSlots != 0 || packers != 64 {
		t.Fatalf("limits %+v, packers %d; want the saved 32 + 32 kept", limits, packers)
	}
	if got, want := hostmem.PackingConfig(0, 10, packers), hostmem.PackingConfig(0, 10, hostmem.DefaultTransfers); !reflect.DeepEqual(got, want) {
		t.Errorf("packing settings with %d packers = %v, want the default-limit settings %v", packers, got, want)
	}
}
