//go:build windows

package tray

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"owngit/internal/server"
)

func TestToastDocument(t *testing.T) {
	target := ToastTarget{filepath.Join(t.TempDir(), "state & notes"), "/repositories/notes/commits?ref=main&lang=ko", "ko"}
	for _, test := range []struct {
		name         string
		notification server.TrayNotification
		text         []string
	}{
		{"title only", server.TrayNotification{Title: "1 new commit"}, []string{"OwnGit", "1 new commit"}},
		{"plain XML and Korean", server.TrayNotification{Title: `Update <notes> & "review"`, Subtitle: "main & stable", Body: "새 알림\x00 <done>"}, []string{`Update <notes> & "review"`, "main & stable\n새 알림 <done>"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			data, err := toastDocument(test.notification, target)
			if err != nil {
				t.Fatal(err)
			}
			var document struct {
				Launch  string `xml:"launch,attr"`
				Binding struct {
					Template string   `xml:"template,attr"`
					Text     []string `xml:"text"`
				} `xml:"visual>binding"`
			}
			if err := xml.Unmarshal(data, &document); err != nil {
				t.Fatal(err)
			}
			got, err := parseToastTarget(document.Launch)
			if err != nil || got != target || document.Binding.Template != "ToastGeneric" || !reflect.DeepEqual(document.Binding.Text, test.text) {
				t.Fatalf("document %s, target %v, error %v", data, got, err)
			}
		})
	}
}

func TestNotificationActivation(t *testing.T) {
	target := ToastTarget{filepath.Join(t.TempDir(), "state"), "/repositories/notes", "en"}
	data, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, appID, arguments string
		wantError              bool
	}{
		{"dashboard page", toastAppID, string(data), false},
		{"another app", "Another.App", string(data), true},
		{"invalid payload", toastAppID, "{", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			object := &notificationCOM{vtable: &callbackVTable, activated: make(chan toastActivation, 1)}
			id, arguments := utf16(test.appID), utf16(test.arguments)
			result, _, _ := syscall.SyscallN(notificationActivate, uintptr(unsafe.Pointer(object)), uintptr(unsafe.Pointer(id)), uintptr(unsafe.Pointer(arguments)), 0, 0)
			runtime.KeepAlive(object)
			runtime.KeepAlive(id)
			runtime.KeepAlive(arguments)
			if (hresult(result) != nil) != test.wantError {
				t.Fatalf("HRESULT 0x%08X", result)
			}
			if !test.wantError {
				select {
				case activation := <-object.activated:
					if activation.err != nil || activation.target != target {
						t.Fatalf("activation %v", activation)
					}
				default:
					t.Fatal("the click was not delivered")
				}
			}
		})
	}
}

func TestNotificationRegistration(t *testing.T) {
	for _, path := range []string{toastAppKey, toastClassKey} {
		if key, err := registry.OpenKey(registry.CURRENT_USER, path, registry.QUERY_VALUE); err == nil {
			key.Close()
			t.Skip("notification registration is already in use by this account")
		} else if !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if err := RemoveNotifications(); err != nil {
			t.Error(err)
		}
	})
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		operation string
		foreign   []string
	}{
		{"register", nil},
		{"register again", nil},
		{"remove with a foreign subkey", []string{toastClassKey}},
		{"remove with foreign subkeys", []string{toastAppKey, toastClassKey}},
		{"remove", nil},
		{"remove again", nil},
	} {
		t.Run(test.operation, func(t *testing.T) {
			if strings.HasPrefix(test.operation, "remove") {
				if len(test.foreign) != 0 {
					if err := RegisterNotifications(); err != nil {
						t.Fatal(err)
					}
				}
				for _, path := range test.foreign {
					path += `\ForeignSub`
					key, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.SET_VALUE)
					if err != nil {
						t.Fatal(err)
					}
					key.Close()
					t.Cleanup(func() {
						if err := registry.DeleteKey(registry.CURRENT_USER, path); err != nil {
							t.Error(err)
						}
					})
				}
				err := RemoveNotifications()
				if len(test.foreign) == 0 && err != nil {
					t.Fatal(err)
				}
				for _, path := range test.foreign {
					if !errors.Is(err, windows.ERROR_ACCESS_DENIED) || !strings.Contains(err.Error(), `HKCU\`+path) {
						t.Errorf("leftover key %s was not reported: %v", path, err)
					}
					key, openErr := registry.OpenKey(registry.CURRENT_USER, path+`\ForeignSub`, registry.QUERY_VALUE)
					if openErr != nil {
						t.Fatalf("foreign subkey was not preserved: %v", openErr)
					}
					key.Close()
				}
				for _, path := range []string{toastAppKey, toastClassKey + `\LocalServer32`, toastClassKey} {
					if slices.Contains(test.foreign, path) {
						continue
					}
					key, err := registry.OpenKey(registry.CURRENT_USER, path, registry.QUERY_VALUE)
					if err == nil {
						key.Close()
					}
					if !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
						t.Errorf("registration %s still exists: %v", path, err)
					}
				}
				return
			}
			if err := RegisterNotifications(); err != nil {
				t.Fatal(err)
			}
			for _, entry := range []struct{ path, name, want string }{
				{toastAppKey, "DisplayName", "OwnGit"},
				{toastAppKey, "CustomActivator", "{" + toastClassID + "}"},
				{toastClassKey + `\LocalServer32`, "", `"` + executable + `" tray open --toast-activation`},
			} {
				key, err := registry.OpenKey(registry.CURRENT_USER, entry.path, registry.QUERY_VALUE)
				if err != nil {
					t.Fatal(err)
				}
				got, _, err := key.GetStringValue(entry.name)
				key.Close()
				if err != nil || got != entry.want {
					t.Fatalf("%s: %q, error %v", entry.name, got, err)
				}
			}
		})
	}
}

func TestToastUnavailable(t *testing.T) {
	original := procRoInitialize
	defer func() { procRoInitialize = original }()
	procRoInitialize = windows.NewLazySystemDLL("owngit-unavailable-winrt.dll").NewProc("RoInitialize")
	if delivery, err := newToastDelivery(); err == nil || delivery != nil {
		t.Fatalf("delivery %v, error %v", delivery, err)
	}
}
