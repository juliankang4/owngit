// Command fakeowngit stands in for owngit in the installer tests. It
// records each run with its arguments in the file OWNGIT_FAKE_LOG names and
// does nothing else, so no test installs a real service. It exits with 3
// when its arguments equal OWNGIT_FAKE_FAIL.
package main

import (
	"fmt"
	"os"
	"strings"
)

// version is set with -ldflags "-X main.version=...".
var version = "0.0.0"

func main() {
	arguments := strings.Join(os.Args[1:], " ")
	if path := os.Getenv("OWNGIT_FAKE_LOG"); path != "" {
		file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Fprintf(file, "%s %s\n", version, arguments)
		file.Close()
	}
	fmt.Printf("fake owngit %s: %s\n", version, arguments)
	if fail := os.Getenv("OWNGIT_FAKE_FAIL"); fail != "" && fail == arguments {
		os.Exit(3)
	}
}
