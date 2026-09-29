//go:build !windows

package tray

import "errors"

// Run is the icon of the Windows notification area, which exists only on
// Windows.
func Run(Options) error {
	return errors.New("\"owngit tray icon\" shows the icon in the Windows notification area and runs only on Windows")
}
