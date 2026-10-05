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
	tests := []struct {
		name                                  string
		ceiling                               uint64
		savedPer, savedExtra                  int
		wantSlots, wantPackers, wantPer, want int
	}{
		{"512 MiB default limits", 512 << 20, 4, 1, 1, 1, 2, 1},
		{"512 MiB loose owner limits", 512 << 20, 32, 32, 1, 1, 2, 1},
		{"1 GiB default limits stay", 1 << 30, 4, 1, 2, 2, 4, 1},
		{"1 GiB loose owner limits", 1 << 30, 32, 32, 2, 2, 5, 1},
		{"8 GiB default limits stay", 8 << 30, 4, 1, 36, 5, 4, 1},
		{"8 GiB loose owner limits", 8 << 30, 32, 32, 36, 33, 32, 1},
		{"unknown keeps the owner's limits", 0, 4, 1, 0, 5, 4, 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			saved := func(context.Context) (githttp.Limits, error) {
				return githttp.Limits{PerRepository: test.savedPer, ExtraSlots: test.savedExtra}, nil
			}
			var packers int
			limits, err := packLimits(saved, test.ceiling, func(n int) { packers = n })(context.Background())
			noErr(t, err)
			if limits.PackSlots != test.wantSlots || packers != test.wantPackers || limits.PerRepository != test.wantPer || limits.ExtraSlots != test.want {
				t.Errorf("pack slots %d, packing shared by %d, transfers %d + %d; want %d, %d, %d + %d",
					limits.PackSlots, packers, limits.PerRepository, limits.ExtraSlots, test.wantSlots, test.wantPackers, test.wantPer, test.want)
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
