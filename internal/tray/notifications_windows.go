//go:build windows

package tray

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	toastAppID    = "OwnGit.Desktop"
	toastClassID  = "616690EA-2890-4BA1-87D7-31FB262D37A1"
	toastAppKey   = `Software\Classes\AppUserModelId\` + toastAppID
	toastClassKey = `Software\Classes\CLSID\{` + toastClassID + `}`
)

var (
	toastCLSID                = guid(toastClassID)
	iidUnknown                = guid("00000000-0000-0000-C000-000000000046")
	iidClassFactory           = guid("00000001-0000-0000-C000-000000000046")
	iidNotificationCallback   = guid("53E31837-6600-4A81-9395-75CFFE746F94")
	procCoRegisterClassObject = windows.NewLazySystemDLL("ole32.dll").NewProc("CoRegisterClassObject")
	procCoRevokeClassObject   = windows.NewLazySystemDLL("ole32.dll").NewProc("CoRevokeClassObject")
)

// RegisterNotifications registers the unpackaged executable for this account.
func RegisterNotifications() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	for _, entry := range []struct{ path, name, value string }{
		{toastClassKey + `\LocalServer32`, "", `"` + executable + `" tray open --toast-activation`},
		{toastClassKey + `\LocalServer32`, "ServerExecutable", executable},
		{toastAppKey, "DisplayName", "OwnGit"},
		{toastAppKey, "CustomActivator", "{" + toastClassID + "}"},
	} {
		key, _, err := registry.CreateKey(registry.CURRENT_USER, entry.path, registry.SET_VALUE)
		if err != nil {
			return fmt.Errorf("register Windows notifications: %w", err)
		}
		err = key.SetStringValue(entry.name, entry.value)
		key.Close()
		if err != nil {
			return fmt.Errorf("register Windows notifications: %w", err)
		}
	}
	return nil
}

// RemoveNotifications removes only OwnGit's per-account notification identity
// and COM server. Windows notification preferences remain for a reinstall.
func RemoveNotifications() error {
	var failures []error
	for _, path := range []string{toastClassKey + `\LocalServer32`, toastAppKey, toastClassKey} {
		if err := registry.DeleteKey(registry.CURRENT_USER, path); err != nil && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			failures = append(failures, fmt.Errorf(`HKCU\%s: %w`, path, err))
		}
	}
	if err := errors.Join(failures...); err != nil {
		return fmt.Errorf("unregister Windows notifications: %w", err)
	}
	return nil
}

type toastActivation struct {
	target ToastTarget
	err    error
}

type notificationCOM struct {
	vtable    *[5]uintptr
	refs      atomic.Int32
	factory   bool
	callback  *notificationCOM
	activated chan toastActivation
}

var (
	notificationQuery = windows.NewCallback(func(this, iid, out uintptr) uintptr {
		if out == 0 {
			return 0x80004003
		} // E_POINTER
		*fromAddress[uintptr](out) = 0
		if iid == 0 {
			return 0x80004003
		}
		object := fromAddress[notificationCOM](this)
		id := *fromAddress[windows.GUID](iid)
		if id != iidUnknown && !(object.factory && id == iidClassFactory) && !(!object.factory && id == iidNotificationCallback) {
			return 0x80004002 // E_NOINTERFACE
		}
		object.refs.Add(1)
		*fromAddress[uintptr](out) = this
		return 0
	})
	notificationAddRef  = windows.NewCallback(func(this uintptr) uintptr { return uintptr(fromAddress[notificationCOM](this).refs.Add(1)) })
	notificationRelease = windows.NewCallback(func(this uintptr) uintptr { return uintptr(fromAddress[notificationCOM](this).refs.Add(-1)) })
	notificationCreate  = windows.NewCallback(func(this, outer, iid, out uintptr) uintptr {
		if outer != 0 {
			return 0x80040110
		} // CLASS_E_NOAGGREGATION
		object := fromAddress[notificationCOM](this)
		result, _, _ := syscall.SyscallN(notificationQuery, uintptr(unsafe.Pointer(object.callback)), iid, out)
		return result
	})
	notificationLock     = windows.NewCallback(func(this, lock uintptr) uintptr { return 0 })
	notificationActivate = windows.NewCallback(func(this, appID, arguments, data, count uintptr) uintptr {
		if appID == 0 || arguments == 0 || windows.UTF16PtrToString(fromAddress[uint16](appID)) != toastAppID {
			return 0x80070057 // E_INVALIDARG
		}
		target, err := parseToastTarget(windows.UTF16PtrToString(fromAddress[uint16](arguments)))
		select {
		case fromAddress[notificationCOM](this).activated <- toastActivation{target, err}:
		default:
			return 0x80004005 // E_FAIL
		}
		if err != nil {
			return 0x80070057
		}
		return 0
	})
	factoryVTable  = [5]uintptr{notificationQuery, notificationAddRef, notificationRelease, notificationCreate, notificationLock}
	callbackVTable = [5]uintptr{notificationQuery, notificationAddRef, notificationRelease, notificationActivate}
)

// AwaitToastActivation serves the local COM callback even when the tray has
// exited. Windows starts "tray open --toast-activation" from LocalServer32.
func AwaitToastActivation(ctx context.Context) (ToastTarget, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED); err != nil && !errors.Is(err, syscall.Errno(1)) {
		return ToastTarget{}, err
	}
	callback := &notificationCOM{vtable: &callbackVTable, activated: make(chan toastActivation, 1)}
	callback.refs.Store(1)
	factory := &notificationCOM{vtable: &factoryVTable, factory: true, callback: callback}
	factory.refs.Store(1)
	defer func() {
		windows.CoUninitialize()
		runtime.KeepAlive(factory)
	}()
	var cookie uint32
	result, _, _ := procCoRegisterClassObject.Call(uintptr(unsafe.Pointer(&toastCLSID)), uintptr(unsafe.Pointer(factory)), 4, 1, uintptr(unsafe.Pointer(&cookie)))
	if err := hresult(result); err != nil {
		return ToastTarget{}, err
	}
	defer procCoRevokeClassObject.Call(uintptr(cookie))
	select {
	case activation := <-callback.activated:
		return activation.target, activation.err
	case <-ctx.Done():
		return ToastTarget{}, ctx.Err()
	}
}
