//go:build !windows && !linux

package tray

import "errors"

// Run is the icon of the Windows notification area and the Linux panel,
// which exist only there.
func Run(Options) error {
	return errors.New("\"owngit tray icon\" shows the icon in the Windows notification area or a Linux desktop panel and runs only there")
}
