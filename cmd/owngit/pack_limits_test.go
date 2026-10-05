package main

import (
	"context"
	"testing"

	"owngit/internal/githttp"
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
