//go:build unix

package main

import "syscall"

// serveErrorOpenFlags opens serveErrorFile without following a link and
// without waiting on a named pipe put in its place.
const serveErrorOpenFlags = syscall.O_NOFOLLOW | syscall.O_NONBLOCK
