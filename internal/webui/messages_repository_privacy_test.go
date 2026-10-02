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
	}
	if text := Text(LangEN, MsgDoctorRepositoryHooksUnsafe); !strings.Contains(text, "Move it out") || !strings.Contains(text, "next retry") {
		t.Errorf("English linked-hook guidance is not actionable: %q", text)
	}
	if text := Text(LangKO, MsgDoctorRepositoryHooksUnsafe); !strings.Contains(text, "밖으로 옮기") || !strings.Contains(text, "다시 만듭니다") {
		t.Errorf("Korean linked-hook guidance is not actionable: %q", text)
	}
}
