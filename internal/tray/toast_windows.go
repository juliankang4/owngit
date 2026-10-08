//go:build windows

package tray

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"owngit/internal/server"
)

var (
	winrt                      = windows.NewLazySystemDLL("combase.dll")
	procRoInitialize           = winrt.NewProc("RoInitialize")
	procRoUninitialize         = winrt.NewProc("RoUninitialize")
	procRoGetActivationFactory = winrt.NewProc("RoGetActivationFactory")
	procRoActivateInstance     = winrt.NewProc("RoActivateInstance")
	procWindowsCreateString    = winrt.NewProc("WindowsCreateString")
	procWindowsDeleteString    = winrt.NewProc("WindowsDeleteString")

	iidToastManager  = guid("50AC103F-D235-4598-BBEF-98FE4D1A3AD4")
	iidToastFactory  = guid("04124B20-82C6-4229-B109-FD9ED4662B53")
	iidXMLDocumentIO = guid("6CD0E74E-EE65-4489-9EBF-CA43E87BA637")
)

func guid(value string) windows.GUID {
	id, err := windows.GUIDFromString("{" + value + "}")
	if err != nil {
		panic(err)
	}
	return id
}

func hresult(result uintptr) error {
	if int32(result) < 0 {
		return fmt.Errorf("Windows notification API: HRESULT 0x%08X", uint32(result))
	}
	return nil
}

// comObject calls the Windows SDK ABI. WinRT interfaces reserve slots 0 to 5
// for IUnknown and IInspectable; ordinary COM reserves slots 0 to 2.
type comObject struct{ vtable *[16]uintptr }

func (object *comObject) invoke(slot int, args ...uintptr) error {
	result, _, _ := syscall.SyscallN(object.vtable[slot], append([]uintptr{uintptr(unsafe.Pointer(object))}, args...)...)
	runtime.KeepAlive(object)
	return hresult(result)
}

func (object *comObject) release() { _ = object.invoke(2) }

func (object *comObject) query(iid *windows.GUID) (*comObject, error) {
	var next *comObject
	err := object.invoke(0, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&next)))
	return next, err
}

func hstring(value string) (uintptr, error) {
	data, err := windows.UTF16FromString(value)
	if err != nil {
		return 0, err
	}
	var handle uintptr
	result, _, _ := procWindowsCreateString.Call(uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)-1), uintptr(unsafe.Pointer(&handle)))
	runtime.KeepAlive(data)
	return handle, hresult(result)
}

func activationFactory(class string, iid *windows.GUID) (*comObject, error) {
	name, err := hstring(class)
	if err != nil {
		return nil, err
	}
	defer procWindowsDeleteString.Call(name)
	var factory *comObject
	result, _, _ := procRoGetActivationFactory.Call(name, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&factory)))
	return factory, hresult(result)
}

type toastDelivery struct{ notifier, factory *comObject }

// Registry-only unpackaged notification registration is used on Windows 10
// 1809 and later. Older builds, or unavailable WinRT APIs, use balloons.
func newToastDelivery() (*toastDelivery, error) {
	if windows.RtlGetVersion().BuildNumber < 17763 {
		return nil, errors.New("toasts need Windows 10 1809 or later")
	}
	for _, proc := range []*windows.LazyProc{procRoInitialize, procRoUninitialize, procRoGetActivationFactory, procRoActivateInstance, procWindowsCreateString, procWindowsDeleteString} {
		if err := proc.Find(); err != nil {
			return nil, err
		}
	}
	result, _, _ := procRoInitialize.Call(1) // RO_INIT_MULTITHREADED
	if err := hresult(result); err != nil {
		return nil, err
	}
	delivery := &toastDelivery{}
	ready := false
	defer func() {
		if !ready {
			delivery.close()
		}
	}()
	if err := RegisterNotifications(); err != nil {
		return nil, err
	}
	manager, err := activationFactory("Windows.UI.Notifications.ToastNotificationManager", &iidToastManager)
	if err != nil {
		return nil, err
	}
	defer manager.release()
	id, err := hstring(toastAppID)
	if err != nil {
		return nil, err
	}
	defer procWindowsDeleteString.Call(id)
	if err := manager.invoke(7, id, uintptr(unsafe.Pointer(&delivery.notifier))); err != nil { // CreateToastNotifierWithId
		return nil, err
	}
	delivery.factory, err = activationFactory("Windows.UI.Notifications.ToastNotification", &iidToastFactory)
	if err != nil {
		return nil, err
	}
	ready = true
	return delivery, nil
}

func (delivery *toastDelivery) close() {
	if delivery.factory != nil {
		delivery.factory.release()
	}
	if delivery.notifier != nil {
		delivery.notifier.release()
	}
	procRoUninitialize.Call()
}

func toastDocument(notification server.TrayNotification, target ToastTarget) ([]byte, error) {
	launch, err := json.Marshal(target)
	if err != nil {
		return nil, err
	}
	title, body := balloonText(notification)
	type binding struct {
		Template string   `xml:"template,attr"`
		Text     []string `xml:"text"`
	}
	document := struct {
		XMLName xml.Name `xml:"toast"`
		Launch  string   `xml:"launch,attr"`
		Binding binding  `xml:"visual>binding"`
	}{Launch: string(launch), Binding: binding{"ToastGeneric", []string{title, body}}}
	return xml.Marshal(document)
}

func (delivery *toastDelivery) show(notification server.TrayNotification, target ToastTarget) error {
	// Show applies Windows settings and Do not disturb.
	data, err := toastDocument(notification, target)
	if err != nil {
		return err
	}
	name, err := hstring("Windows.Data.Xml.Dom.XmlDocument")
	if err != nil {
		return err
	}
	defer procWindowsDeleteString.Call(name)
	var document *comObject
	result, _, _ := procRoActivateInstance.Call(name, uintptr(unsafe.Pointer(&document)))
	if err := hresult(result); err != nil {
		return err
	}
	defer document.release()
	io, err := document.query(&iidXMLDocumentIO)
	if err != nil {
		return err
	}
	defer io.release()
	text, err := hstring(string(data))
	if err != nil {
		return err
	}
	defer procWindowsDeleteString.Call(text)
	if err := io.invoke(6, text); err != nil {
		return fmt.Errorf("load the toast XML: %w", err)
	} // LoadXml
	var toast *comObject
	if err := delivery.factory.invoke(6, uintptr(unsafe.Pointer(document)), uintptr(unsafe.Pointer(&toast))); err != nil {
		return fmt.Errorf("create the toast: %w", err)
	}
	defer toast.release()
	if err := delivery.notifier.invoke(6, uintptr(unsafe.Pointer(toast))); err != nil { // Show
		return fmt.Errorf("show the toast: %w", err)
	}
	return nil
}
