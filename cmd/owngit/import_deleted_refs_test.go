package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestImportRunSourceDeletionCountIsNeutralInTextAndJSON(t *testing.T) {
	for _, local := range []string{"kept", "deleted", "mixed"} {
		t.Run(local, func(t *testing.T) {
			first, second := "a", "a"
			if local == "deleted" {
				first, second = "", ""
			}
			if local == "mixed" {
				second = ""
			}
			result := fmt.Sprintf(`{"ok":true,"run":{"status":"complete","refs_deleted_upstream":2},"status":{"refs":[{"name":"refs/heads/old","state":"deleted_at_source","local_oid":%q},{"name":"refs/tags/old","state":"deleted_at_source","local_oid":%q}]}}`, first, second)
			text, err := captureStdout(func() error { return printImportRun("project", []byte(result), false) })
			noErr(t, err)
			if !strings.Contains(text, "2 refs were deleted at the source.\n") || strings.Contains(text, "kept here") {
				t.Fatalf("misleading deletion result: %s", text)
			}
			output, err := captureStdout(func() error { return printImportRun("project", []byte(result), true) })
			noErr(t, err)
			var response struct {
				Run struct {
					Deleted int `json:"refs_deleted_upstream"`
				} `json:"run"`
			}
			noErr(t, json.Unmarshal([]byte(output), &response))
			if response.Run.Deleted != 2 || !sameJSON(output, result) {
				t.Fatalf("JSON changed the source deletion facts: %s", output)
			}
		})
	}
}
