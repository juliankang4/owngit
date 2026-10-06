package importsync

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func TestParentPreparedValidationHandlesRefsBeyondArgvBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("publishes 5000 refs in one import")
	}
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
	output, err := command.CombinedOutput()
	require(t, err == nil, "create valid source ref fixture: %v %s", err, output)
	_, err = f.importProject(ImportInput{})
	noErr(t, err, "import of 5001 refs (process argument budget)")
	eq(t, "published refs", len(f.destinationRefs()), count+1)
}
