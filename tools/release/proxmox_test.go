//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// proxmox.sh passes the owner's choices to pct inside comma-separated
// settings, so it checks every value before it reads anything on the host.
// A value that could add a setting is refused with the option's name, and
// valid values go on to the host checks, which fail off Proxmox VE.
func TestProxmoxScriptChecksEveryValueFirst(t *testing.T) {
	script := filepath.Join(repoRoot(t), "packaging", "installer", "proxmox.sh")
	run := func(env string, arguments ...string) (string, error) {
		command := exec.Command("/bin/sh", append([]string{script}, arguments...)...)
		command.Env = os.Environ()
		if env != "" {
			command.Env = append(command.Env, env)
		}
		output, err := command.CombinedOutput()
		return string(output), err
	}

	for _, refused := range []struct {
		arguments []string
		env       string
		want      string
	}{
		{arguments: []string{"--bridge", "vmbr0,firewall=1"}, want: "--bridge takes a bridge name"},
		{arguments: []string{"--repositories", "/tank/git,mp=/etc"}, want: "--repositories takes an absolute path"},
		{arguments: []string{"--repositories", "tank/git"}, want: "--repositories takes an absolute path"},
		{arguments: []string{"--repositories=/tank/../etc"}, want: "--repositories takes an absolute path"},
		{arguments: []string{"--ip", "192.168.1.50/24,gw=192.168.1.9"}, want: "--ip takes an IPv4 address"},
		{arguments: []string{"--gateway", "192.168.1.1"}, want: "--gateway goes with --ip"},
		{arguments: []string{"--template", "local:vztmpl/debian.tar.zst,rootfs=x"}, want: "--template takes a template volume"},
		{arguments: []string{"--hostname", "own_git"}, want: "--hostname takes letters"},
		{arguments: []string{"--id", "99"}, want: "--id takes a number from 100"},
		{arguments: []string{"--memory", "1024M"}, want: "--memory takes a whole number"},
		{arguments: []string{"--version", "latest"}, want: "--version takes a release number"},
		{arguments: []string{"--version", "v"}, want: "--version takes a release number"},
		{arguments: []string{"--storage"}, want: "--storage needs a value"},
		{arguments: []string{"--repositories="}, want: "--repositories needs a value"},
		{arguments: []string{"--version", ""}, want: "--version needs a value"},
		{arguments: []string{"--ip", "192.168.1.50/24"}, want: "--ip needs --gateway"},
		{arguments: []string{"--force"}, want: "unknown option --force"},
		{env: "OWNGIT_RELEASES=http://mirror.example/releases", want: "OWNGIT_RELEASES must be an https:// address"},
	} {
		output, err := run(refused.env, refused.arguments...)
		if err == nil || !strings.Contains(output, "owngit proxmox: "+refused.want) {
			t.Errorf("proxmox.sh %s (%s): %v, want a refusal with %q:\n%s", strings.Join(refused.arguments, " "), refused.env, err, refused.want, output)
		}
	}

	output, err := run("", "--id=250", "--hostname", "git-1", "--storage", "local-zfs", "--disk", "16", "--cores", "4",
		"--memory", "2048", "--bridge", "vmbr1", "--ip", "10.0.0.5/24", "--gateway", "10.0.0.1",
		"--repositories", "/tank/owngit", "--template", "local:vztmpl/debian-13-standard_13.6-1_amd64.tar.zst", "--version", "v1.1.3")
	if err == nil || !(strings.Contains(output, "run this as root on the Proxmox VE host") || strings.Contains(output, "pct is missing")) {
		t.Errorf("valid choices did not reach the host checks: %v\n%s", err, output)
	}

	output, err = run("", "--help")
	if err != nil || !strings.Contains(output, "--repositories FOLDER") {
		t.Errorf("--help: %v\n%s", err, output)
	}
}
