package server

import (
	"testing"

	"owngit/internal/webui"
)

func TestSetupStorageWarningResultNotice(t *testing.T) {
	notices := noticeFor("setup_completed_storage_warning")
	if len(notices) != 2 || notices[0].Kind != webui.NoticeSuccess || notices[0].Code != webui.MsgSetupCompleted ||
		notices[1].Kind != webui.NoticeWarning || notices[1].Code != webui.MsgSetupStorageShared || notices[1].Detail != "" {
		t.Fatalf("notices=%+v", notices)
	}
}
