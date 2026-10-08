package testfixture

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type CheckEnvironment struct {
	Command string
	values  map[string]string
}

func NewCheckEnvironment(t *testing.T) CheckEnvironment {
	t.Helper()
	names := "PATH HOME LANG TZ LC_ALL LC_COLLATE LC_CTYPE LC_MESSAGES LC_MONETARY LC_NUMERIC LC_TIME"
	if runtime.GOOS == "windows" {
		names += " SystemRoot SystemDrive windir ComSpec PATHEXT USERPROFILE APPDATA LOCALAPPDATA ProgramData ProgramFiles ProgramFiles(x86) ProgramW6432 CommonProgramFiles CommonProgramFiles(x86) CommonProgramW6432 HOMEDRIVE HOMEPATH USERNAME NUMBER_OF_PROCESSORS PROCESSOR_ARCHITECTURE PROCESSOR_ARCHITEW6432 OS"
	} else {
		names += " USER LOGNAME SHELL"
	}
	fixture := CheckEnvironment{values: make(map[string]string)}
	for _, name := range strings.Fields(names) {
		value := os.Getenv(name)
		// Windows uses this optional WOW64 value to rewrite the child architecture.
		if name == "PROCESSOR_ARCHITEW6432" && value == "" {
			fixture.values[environmentKey(name)] = ""
			continue
		}
		if value == "" {
			value = "allowlist-value"
		}
		if name == "LANG" || strings.HasPrefix(name, "LC_") {
			value = "C"
		}
		t.Setenv(name, value)
		fixture.values[environmentKey(name)] = value
	}
	for _, name := range strings.Fields("CHECK_LOCAL_SETTING OWNGIT_STATE_DIR TAILSCALE_SOCKET HTTP_PROXY HTTPS_PROXY NO_PROXY ALL_PROXY http_proxy https_proxy no_proxy all_proxy JAVA_HOME GOPROXY GIT_CONFIG_COUNT GITHUB_TOKEN AWS_SECRET_ACCESS_KEY DATABASE_PASSWORD RUNNER_CREDENTIAL BASH_ENV ENV NODE_OPTIONS") {
		value := "parent-only-value"
		if name == "GIT_CONFIG_COUNT" {
			value = "0"
		}
		t.Setenv(name, value)
		fixture.values[environmentKey(name)] = ""
	}
	t.Setenv("CI", "false")
	fixture.values["CI"] = "true"
	fixture.values["TEMPORARY_READY"] = "1"
	fixture.Command = `env; test -d "$TMPDIR" && test "$TMPDIR" = "$TEMP" && test "$TEMP" = "$TMP" && printf temporary > "$TMPDIR/probe" && echo TEMPORARY_READY=1; exit 3`
	if runtime.GOOS == "windows" {
		fixture.Command = `set & (if not exist "%TEMP%\" exit /b 2) & (echo temporary>"%TEMP%\probe" || exit /b 2) & (echo TEMPORARY_READY=1) & exit /b 3`
	}
	return fixture
}

func (fixture CheckEnvironment) Assert(t *testing.T, output, tempRoot string) {
	t.Helper()
	values := make(map[string]string)
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		if name, value, ok := strings.Cut(line, "="); ok {
			values[environmentKey(name)] = value
		}
	}
	for name, want := range fixture.values {
		t.Run(name, func(t *testing.T) {
			if got := values[name]; got != want {
				t.Fatalf("environment %s=%q, want %q", name, got, want)
			}
		})
	}
	t.Run("private temporary directory", func(t *testing.T) {
		temporary := values["TMPDIR"]
		if temporary == "" || temporary != values["TEMP"] || temporary != values["TMP"] || !filepath.IsAbs(temporary) || !strings.HasPrefix(filepath.Base(temporary), "owngit-check-") {
			t.Fatalf("temporary paths: TMPDIR=%q TEMP=%q TMP=%q", temporary, values["TEMP"], values["TMP"])
		}
		if tempRoot != "" && !strings.EqualFold(filepath.Clean(filepath.Dir(temporary)), filepath.Clean(tempRoot)) {
			t.Fatalf("temporary folder %q is not in %q", temporary, tempRoot)
		}
		if _, err := os.Lstat(temporary); !os.IsNotExist(err) {
			t.Fatalf("temporary directory was not removed: %v", err)
		}
	})
	t.Run("failed command note", func(t *testing.T) {
		if strings.Count(output, "[OwnGit did not pass parent environment variables") != 1 || !strings.Contains(output, "CHECK_LOCAL_SETTING") || !strings.Contains(output, "HTTP_PROXY") || !strings.Contains(output, "in the check command itself") {
			t.Fatal("missing environment guidance for the failed command")
		}
		for _, text := range []string{"GITHUB_TOKEN", "AWS_SECRET_ACCESS_KEY", "DATABASE_PASSWORD", "RUNNER_CREDENTIAL", "parent-only-value"} {
			if strings.Contains(output, text) {
				t.Fatalf("environment guidance contains private text %q", text)
			}
		}
	})
}

func environmentKey(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(name)
	}
	return name
}
