package hostmem

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCeilingReadsTheSmallestLimit(t *testing.T) {
	const meminfo = "MemTotal:        8388608 kB\nMemFree: 1 kB\n"
	tests := []struct {
		name  string
		files map[string]string
		want  uint64
	}{
		{"version 2 limit", map[string]string{"proc/self/cgroup": "0::/a\n", "sys/fs/cgroup/a/memory.max": "536870912\n"}, 512 << 20},
		{"version 2 limit on an ancestor", map[string]string{"proc/self/cgroup": "0::/a/b\n", "sys/fs/cgroup/a/memory.max": "1073741824\n", "sys/fs/cgroup/a/b/memory.max": "max\n"}, 1 << 30},
		{"version 2 max", map[string]string{"proc/self/cgroup": "0::/a\n", "sys/fs/cgroup/a/memory.max": "max\n"}, 8 << 30},
		{"version 1 limit", map[string]string{"proc/self/cgroup": "4:memory:/a\n", "sys/fs/cgroup/memory/a/memory.limit_in_bytes": "268435456"}, 256 << 20},
		{"version 1 unset", map[string]string{"proc/self/cgroup": "4:memory:/a\n", "sys/fs/cgroup/memory/a/memory.limit_in_bytes": "9223372036854771712"}, 8 << 30},
		{"garbage", map[string]string{"proc/self/cgroup": "0::/a\n", "sys/fs/cgroup/a/memory.max": "lots"}, 8 << 30},
		{"no cgroup files", map[string]string{}, 8 << 30},
		{"nothing at all", nil, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if test.files != nil {
				test.files["proc/meminfo"] = meminfo
			}
			for name, content := range test.files {
				path := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := ceilingIn(root); got != test.want {
				t.Errorf("ceiling = %d, want %d", got, test.want)
			}
		})
	}
}

func TestBudgetFollowsTheCeiling(t *testing.T) {
	tests := []struct {
		name     string
		ceiling  uint64
		heap     int64
		slots    int
		packers  int
		settings string
	}{
		{"unknown", 0, 0, 0, 5, "threads=4 window=20372333 cache=81489334 base=81489334"},
		{"512 MiB", 512 << 20, 256 << 20, 1, 1, "threads=1 window=8388608 cache=8388608 base=8388608"},
		{"1 GiB", 1 << 30, 512 << 20, 2, 2, "threads=1 window=16777216 cache=16777216 base=16777216"},
		{"8 GiB", 8 << 30, 4 << 30, 36, 5, "threads=4 window=49133275 cache=196533101 base=196533101"},
		{"64 GiB", 64 << 30, 32 << 30, 307, 5, "threads=4 window=268435456 cache=268435456 base=268435456"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := HeapLimit(test.ceiling, ""); got != test.heap {
				t.Errorf("heap limit = %d, want %d", got, test.heap)
			}
			if got := PackSlots(test.ceiling); got != test.slots {
				t.Errorf("pack slots = %d, want %d", got, test.slots)
			}
			if got := DefaultPackers(test.ceiling); got != test.packers {
				t.Errorf("default packers = %d, want %d", got, test.packers)
			}
			config := PackingConfig(test.ceiling, 4, test.packers)
			got := "threads=" + config[0][1] + " window=" + config[1][1] + " cache=" + config[2][1] + " base=" + config[3][1]
			if got != test.settings {
				t.Errorf("packing = %s, want %s", got, test.settings)
			}
		})
	}
}

func TestOwnerMemoryLimitIsKept(t *testing.T) {
	if got := HeapLimit(512<<20, "1GiB"); got != 0 {
		t.Errorf("heap limit with an owner setting = %d, want none", got)
	}
}
