package gitexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunnerIgnoresInheritedGitConfiguration(t *testing.T) {
	root := t.TempDir()
	maliciousHome := filepath.Join(root, "user-home")
	if err := os.Mkdir(maliciousHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(maliciousHome, ".gitconfig"), []byte("[core]\n\thooksPath = /tmp/untrusted-hooks\n[credential]\n\thelper = untrusted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", maliciousHome)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "alias.injected")
	t.Setenv("GIT_CONFIG_VALUE_0", "status")
	runner, err := New("", filepath.Join(root, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	if runner.GlobalConfigPath == "/dev/null" || runner.GlobalConfigPath == "NUL" {
		t.Fatalf("global config path is not portable: %s", runner.GlobalConfigPath)
	}
	for _, key := range []string{"core.hooksPath", "credential.helper", "alias.injected"} {
		result, err := runner.Run(context.Background(), "", nil, "config", "--global", "--get", key)
		if err == nil || strings.TrimSpace(string(result.Stdout)) != "" {
			t.Fatalf("inherited key %s was visible: stdout=%q err=%v", key, result.Stdout, err)
		}
	}
	content, err := os.ReadFile(filepath.Join(maliciousHome, ".gitconfig"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "untrusted-hooks") {
		t.Fatal("runner changed the user's global Git config")
	}
}
