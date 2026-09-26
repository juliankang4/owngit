//go:build !linux

package service

import "os"

// Probe reads the environment of this process.
func Probe() Environment {
	return Environment{Getenv: os.Getenv, EUID: os.Geteuid()}
}
