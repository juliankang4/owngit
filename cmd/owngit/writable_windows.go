package main

// canWrite decides whether a command needs sudo, which Windows does not have:
// an archive update there unpacks the new release beside the old one.
func canWrite(string) bool { return true }
