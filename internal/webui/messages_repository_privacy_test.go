package webui

import (
	"strings"
	"testing"
)

func TestRepositoryPrivacyGuidanceIsActionableInBothLanguages(t *testing.T) {
	for _, lang := range Langs() {
		for _, code := range []MessageCode{MsgDoctorRepositoryRootShared, MsgDoctorRepositoryRootUnchecked, MsgDoctorRepositoryHooksUnsafe} {
			if text := Text(lang, code); strings.HasPrefix(text, "doctor.") {
				t.Errorf("%s/%s is missing from the catalog", code, lang)
			}
		}
		detail := Text(lang, MsgRepoPreparingDetail)
		if !strings.Contains(detail, "owngit doctor") {
			t.Errorf("%s preparing detail does not point to doctor: %q", lang, detail)
		}
		creation := Text(lang, MsgRepoCreateFail)
		if !strings.Contains(creation, "owngit doctor") || lang == LangEN && !strings.Contains(creation, "server log") || lang == LangKO && !strings.Contains(creation, "서버 로그") {
			t.Errorf("%s repository creation failure is not actionable: %q", lang, creation)
		}
	}
	if code := ImportErrorCode("repository_create_failed"); code != MsgRepoCreateFail {
		t.Errorf("private import creation maps to %s", code)
	}
	if text := Text(LangEN, MsgDoctorRepositoryHooksUnsafe); !strings.Contains(text, "Move it out") || !strings.Contains(text, "next retry") {
		t.Errorf("English linked-hook guidance is not actionable: %q", text)
	}
	if text := Text(LangKO, MsgDoctorRepositoryHooksUnsafe); !strings.Contains(text, "밖으로 옮기") || !strings.Contains(text, "다시 만듭니다") {
		t.Errorf("Korean linked-hook guidance is not actionable: %q", text)
	}
}
