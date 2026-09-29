package main

// canWrite is only asked about archive installs on Linux and macOS, where
// replacing the file may need root. Windows installs a new release beside
// the old one instead.
func canWrite(string) bool { return true }
