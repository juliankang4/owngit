package repository

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReadHeadCommandOutcomes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX Git command fixture")
	}
	manager, remote, _ := newTestRepository(t)
	for _, test := range []struct {
		name, target, oid, prefix string
		symbolic, resolve         int
	}{
		{"symbolic", "refs/heads/alias", "resolved", "", 0, 0},
		{"unborn", "refs/heads/unborn", "", "", 0, 1},
		{"detached", "", "resolved", "", 1, 0},
		{"missing", "", "", "", 1, 1},
		{"symbolic failure", "", "", "read HEAD:", 128, 0},
		{"symbolic resolve failure", "refs/heads/alias", "", "resolve HEAD:", 0, 128},
		{"detached resolve failure", "", "", "resolve HEAD:", 1, 128},
	} {
		t.Run(test.name, func(t *testing.T) {
			wrapper := filepath.Join(t.TempDir(), "git-outcome")
			script := fmt.Sprintf("#!/bin/sh\ncase \"$3\" in\n symbolic-ref) test \"$5\" = --no-recurse || exit 129; printf '%%s\\n' %s; exit %d ;;\n rev-parse) printf '%%s\\n' %s; exit %d ;;\n *) exit 129 ;;\nesac\n", shellQuote(test.target), test.symbolic, shellQuote(test.oid), test.resolve)
			noErr(t, os.WriteFile(wrapper, []byte(script), 0o700))
			manager.Git.GitPath = wrapper
			target, oid, err := manager.ReadHead(context.Background(), remote)
			if test.prefix != "" {
				if err == nil || !strings.HasPrefix(err.Error(), test.prefix) || target != "" || oid != "" {
					t.Fatalf("fatal HEAD read = %q %q %v", target, oid, err)
				}
			} else if err != nil || target != test.target || oid != test.oid {
				t.Fatalf("HEAD read = %q %q %v, want %q %q", target, oid, err, test.target, test.oid)
			}
		})
	}
}
