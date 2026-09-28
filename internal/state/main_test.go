package state

import (
	"fmt"
	"os"
	"testing"

	"owngit/internal/testfixture"
)

// TestMain lets the tests that run this test binary as another process,
// such as concurrent first opens, start those processes without the race
// detector's exit wait.
func TestMain(m *testing.M) {
	if err := testfixture.SkipRaceExitWaitInChildren(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}
