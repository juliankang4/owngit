package state

import "errors"

var errMultipleFileNames = errors.New("has another name as well, so it may be another file")

// createAttempts bounds how often OpenOwnFile tries to create a name that
// others keep creating and removing.
const createAttempts = 5
