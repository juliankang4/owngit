package importsync

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func TestParentPreparedValidationHandlesRefsBeyondArgvBudget(t *testing.T) {
	f := newFixture(t)
	oid := f.commit("source", "source bytes\n")
	// Long ref names keep the argument-budget pressure. Windows needs long
	// path support in the fixture's own source repository; the product
	// runner already enables it for OwnGit's Git commands.
	f.git(f.source, "config", "core.longpaths", "true")
	const count = 5000
	var input strings.Builder
	for index := 0; index < count; index++ {
		name := fmt.Sprintf("refs/heads/bulk-%04d-%s", index, strings.Repeat("a", 210))
		fmt.Fprintf(&input, "create %s %s\n", name, oid)
	}
	command := exec.Command(f.gitPath, "update-ref", "--stdin")
	command.Dir, command.Env, command.Stdin = f.source, f.env, strings.NewReader(input.String())
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create valid source ref fixture: %v %s", err, output)
	}
	result, err := f.importProject(ImportInput{})
	if err != nil {
		t.Fatalf("valid %d-ref import exceeded process argument budget during publication: %v (result=%+v)", count+1, err, result)
	}
	if got := len(f.destinationRefs()); got != count+1 {
		t.Fatalf("published refs=%d want=%d", got, count+1)
	}
}
