package markdown

import (
	"fmt"
	"os"
	"testing"
	"time"
)

var spinSink int

// TestMain lets the test binary act as the render child, as the owngit
// binary does. MDTEST_CHILD selects a misbehaving child for the tests of
// the parent's limits.
func TestMain(m *testing.M) {
	if IsChild(os.Args) {
		if deadline, err := time.ParseDuration(os.Getenv("MDTEST_CHILD_DEADLINE")); err == nil {
			childDeadline = deadline
		}
		render, ok := testChildren[os.Getenv("MDTEST_CHILD")]
		if !ok {
			os.Exit(exitBadRequest)
		}
		os.Exit(runChild(os.Stdin, os.Stdout, render))
	}
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	SetHelper(executable)
	os.Exit(m.Run())
}

var testChildren = map[string]func([]byte, Resolver) (string, error){
	"": convert,
	"panic": func([]byte, Resolver) (string, error) {
		panic("render failed")
	},
	"hang": func([]byte, Resolver) (string, error) {
		select {}
	},
	// spin uses the processor until it is stopped.
	"spin": func([]byte, Resolver) (string, error) {
		for n := 0; ; n++ {
			spinSink = n
		}
	},
	// killed dies by a signal it was not sent by the parent.
	"killed": func([]byte, Resolver) (string, error) {
		process, _ := os.FindProcess(os.Getpid())
		_ = process.Kill()
		select {}
	},
	// flood writes past maxOutput without the child's own limit.
	"flood": func([]byte, Resolver) (string, error) {
		chunk := make([]byte, 64<<10)
		for {
			if _, err := os.Stdout.Write(chunk); err != nil {
				os.Exit(exitFailed)
			}
		}
	},
	// hog keeps what it allocates, as a parse tree does.
	"hog": func([]byte, Resolver) (string, error) {
		var kept [][]byte
		for {
			kept = append(kept, make([]byte, 64<<10))
		}
	},
}
