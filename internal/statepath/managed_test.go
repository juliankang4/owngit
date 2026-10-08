package statepath_test

import (
	"strings"
	"testing"

	"owngit/internal/statepath"
)

func TestManagedNames(t *testing.T) {
	for _, path := range []string{
		"owngit.sqlite", "owngit.sqlite-wal", "owngit.sqlite-shm",
		"health-run.json", "tray-access.json", "tray-hidden", "tray-notifications.json", "tray-cursor",
		".offline-operation.lock", ".network-running.lock", ".tailscale-change.lock", ".database-create.lock", "no-upgrade-backup",
		"owner-setup.html", ".owner-setup.lock", ".owner-setup.issue.json", "serve-error.txt",
		"runtime", "runtime/git-home", "runtime/tmp", "runtime/gitconfig.empty",
		"import-credentials", "import-credentials/project.json", "logs", "logs/service.log", "logs/service.log.1",
	} {
		parent, name := "", path
		if cut := strings.LastIndex(path, "/"); cut >= 0 {
			parent, name = path[:cut], path[cut+1:]
		}
		if !statepath.Managed(parent, name) {
			t.Errorf("managed name %q was omitted", path)
		}
	}
	random := []byte{0xfb, 0xff, 0, 1, 2, 3, 4, 5}
	for _, test := range []struct {
		parent string
		name   string
		want   bool
	}{
		{"", statepath.DatabaseTemporary(random), true},
		{"", statepath.ReplacementTemporary(statepath.HealthRun, random), true},
		{"", statepath.ReplacementTemporary(statepath.TrayAccess, random), true},
		{"", statepath.ReplacementTemporary(statepath.TrayNotifications, random), true},
		{"", statepath.ReplacementTemporary(statepath.TrayCursor, random), true},
		{"", strings.Replace(statepath.SetupTemporary, "*", "123456", 1), true},
		{"", strings.Replace(statepath.JournalTemporary, "*", "123456", 1), true},
		{statepath.ImportCredentials, statepath.CredentialTemporary("project", statepath.CredentialWrite, random), true},
		{statepath.ImportCredentials, statepath.CredentialTemporary("project", statepath.CredentialRestore, random), true},
		{"", statepath.DatabaseTemporary(random[:7]), false},
		{"", ".health-run.json-0123456789abcdef", false},
		{"", statepath.ReplacementTemporary(statepath.TrayAccess, random) + "=", false},
		{"", ".owner-setup-not-a-number", false},
		{"", "user-file", false},
		{statepath.Runtime, "user-file", false},
		{statepath.GitHome, statepath.GitConfig, false},
		{statepath.ImportCredentials, ".project.tmp-not-hex", false},
		{statepath.ImportCredentials, "pro\nject.json", false},
		{statepath.Logs, "custom.log", false},
	} {
		t.Run(test.parent+"/"+test.name, func(t *testing.T) {
			if got := statepath.Managed(test.parent, test.name); got != test.want {
				t.Fatalf("managed=%v, want %v", got, test.want)
			}
		})
	}
	for _, test := range []struct {
		path string
		want bool
	}{{statepath.Runtime, true}, {statepath.Logs, true}, {statepath.ImportCredentials, true},
		{statepath.Runtime + "/" + statepath.GitHome, false}, {"repositories", false}, {"check-workspaces", false}} {
		if got := statepath.HasChildren(test.path); got != test.want {
			t.Errorf("children(%q)=%v, want %v", test.path, got, test.want)
		}
	}
}
