//go:build !linux

package main

import "owngit/internal/service"

// Only Linux registers the OwnGit icon in the service install: Windows has
// its own sign-in task and macOS its app.

func (host *serviceHost) installIcon(service.Mode, string, bool) {}

func (host *serviceHost) removeIcon(bool) {}
