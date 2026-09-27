package main

import "errors"

// requireProtectedPath is never reached: the macOS agent is not set up on
// Windows.
var requireProtectedPath = func(string) error { return errors.New("not available on Windows") }
