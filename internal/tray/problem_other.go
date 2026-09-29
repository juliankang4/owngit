//go:build !linux

package tray

// IconProblem says why the icon cannot show on this computer, or "" when it
// can. Only Linux depends on programs of the desktop.
func IconProblem() string { return "" }
