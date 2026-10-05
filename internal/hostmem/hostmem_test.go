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

func TestOwnerMemoryLimitIsKept(t *testing.T) {
	if got := HeapLimit(512<<20, "1GiB"); got != 0 {
		t.Errorf("heap limit with an owner setting = %d, want none", got)
	}
}

func TestBudgetFollowsTheCeiling(t *testing.T) {
	tests := []struct {
		name     string
		ceiling  uint64
		heap     int64
		settings string
	}{
		{"unknown", 0, 0, "threads=4 window=44739242 cache=178956970"},
		{"512 MiB", 512 << 20, 320 << 20, "threads=1 window=22369621 cache=22369621"},
		{"1 GiB", 1 << 30, 640 << 20, "threads=1 window=44739242 cache=44739242"},
		{"8 GiB", 8 << 30, 5 << 30, "threads=4 window=89478485 cache=268435456"},
		{"64 GiB", 64 << 30, 40 << 30, "threads=4 window=268435456 cache=268435456"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := HeapLimit(test.ceiling, ""); got != test.heap {
				t.Errorf("heap limit = %d, want %d", got, test.heap)
			}
			config := PackingConfig(test.ceiling, 4)
			got := "threads=" + config[0][1] + " window=" + config[1][1] + " cache=" + config[2][1]
			if got != test.settings {
				t.Errorf("packing = %s, want %s", got, test.settings)
			}
		})
	}
}
