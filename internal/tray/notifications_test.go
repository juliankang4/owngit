package tray

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestToastTarget(t *testing.T) {
	valid := ToastTarget{StateDir: filepath.Join(t.TempDir(), "state"), Page: "/repositories/notes/commits?ref=main", Lang: "ko"}
	for _, test := range []struct {
		name      string
		change    func(*ToastTarget)
		raw       string
		wantError bool
	}{
		{"dashboard page", func(*ToastTarget) {}, "", false},
		{"relative state", func(target *ToastTarget) { target.StateDir = "state" }, "", true},
		{"NUL in state", func(target *ToastTarget) { target.StateDir += "\x00" }, "", true},
		{"another host", func(target *ToastTarget) { target.Page = "//example.invalid/" }, "", true},
		{"absolute URL", func(target *ToastTarget) { target.Page = "https://example.invalid/" }, "", true},
		{"backslash", func(target *ToastTarget) { target.Page = "/a\\b" }, "", true},
		{"empty page", func(target *ToastTarget) { target.Page = "" }, "", true},
		{"unknown language", func(target *ToastTarget) { target.Lang = "fr" }, "", true},
		{"malformed JSON", func(*ToastTarget) {}, "{", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := valid
			test.change(&target)
			data, err := json.Marshal(target)
			if err != nil {
				t.Fatal(err)
			}
			if test.raw != "" {
				data = []byte(test.raw)
			}
			got, err := parseToastTarget(string(data))
			if (err != nil) != test.wantError || !test.wantError && got != target {
				t.Fatalf("target %v, error %v", got, err)
			}
		})
	}
}
