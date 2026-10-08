//go:build !windows

package tray

import (
	"context"
	"errors"
)

func RegisterNotifications() error {
	return errors.New("Windows notification registration runs only on Windows")
}
func RemoveNotifications() error { return nil }
func AwaitToastActivation(context.Context) (ToastTarget, error) {
	return ToastTarget{}, errors.New("Windows toast activation runs only on Windows")
}
