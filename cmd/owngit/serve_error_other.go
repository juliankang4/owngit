//go:build !unix

package main

// serveErrorOpenFlags adds nothing where the file system has no named pipes;
// serveErrorSince checks for a regular file before and after opening.
const serveErrorOpenFlags = 0
