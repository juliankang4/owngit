package webui

import (
	"strings"
	"testing"
)

// What OwnGit may claim about the connection
//
// OwnGit sees one thing: whether the request it answered arrived over TLS.
// It does not know whether a VPN, an SSH tunnel, or a reverse proxy protects
// the rest of the path, and a host name is not evidence of one. So the
// connection wording has to stay inside two limits:
//
//   - It must not promise safety. Plain HTTP on an ordinary LAN really can
//     expose passwords, sessions, and repository contents.
//   - It must not declare every plain-HTTP installation wholly exposed. That
//     is false for an owner who already runs a VPN, and being told an obvious
//     falsehood teaches people to ignore the warning.
//
// The owner is told what the application does and does not do, and decides.

func TestKoreanDeveloperTermsUseFamiliarWording(t *testing.T) {
	expected := map[MessageCode]string{
		MsgRepoTabsLabel:          "저장소 메뉴",
		MsgPrereqHTTPMiss:         "Git의 HTTP 서비스를 찾지 못했습니다. HTTP로 클론하거나 푸시할 수 없습니다.",
		MsgSettingsCloneTitle:     "클론 주소",
		MsgSettingsCloneHelp:      "클론할 때 이 주소 뒤에 저장소 이름을 붙이세요.",
		MsgRepoNameRules:          "영문자, 숫자, 점, 하이픈, 밑줄을 사용합니다. 이 이름이 클론 주소가 됩니다.",
		MsgRepoNameInvalid:        "영문자, 숫자, 점, 하이픈, 밑줄만 사용하세요.",
		MsgRepoCloneTitle:         "클론 주소",
		MsgRepoDetached:           "브랜치가 아니라 특정 커밋을 보고 있습니다.",
		MsgCodePathMissing:        "이 커밋에는 그 경로가 없습니다.",
		MsgActivityNoChecks:       "활동은 커밋의 작성 날짜를 작성자가 기록한 시간대 기준으로 집계합니다. 체크를 실행했거나 통과했다는 뜻은 아닙니다.",
		MsgImportKindInitial:      "처음 가져오기",
		MsgImportKindRefresh:      "새로고침",
		MsgImportStatusComplete:   "완료",
		MsgImportStatusFailed:     "실패",
		MsgImportStatusCancelled:  "취소됨",
		MsgImportRefTracked:       "원본과 같음",
		MsgImportRefDiverged:      "달라짐",
		MsgImportCredentialBasic:  "사용자 이름과 비밀번호",
		MsgImportCredentialBearer: "액세스 토큰",
		MsgImportCredentialNone:   "없음",
	}
	for code, want := range expected {
		if got := Text(LangKO, code); got != want {
			t.Errorf("%s Korean text=%q, want %q", code, got, want)
		}
	}
}

func connectionMessages() []MessageCode {
	return []MessageCode{
		MsgSetupInsecureLabel, MsgSetupInsecureHelp, MsgSetupInsecureNeed,
		MsgConnEncrypted, MsgConnPlain, MsgConnPlainDetail,
		MsgConnTailscale, MsgConnNoProof, MsgSettingsAckSubmit, MsgSettingsAckDone,
	}
}

func TestConnectionWordingDoesNotOverclaimExposure(t *testing.T) {
	// Each phrase asserts something about the whole network path, which is
	// exactly what OwnGit cannot observe.
	unconditional := []struct{ phrase, why string }{
		{"can be read on the network", "states the traffic is exposed regardless of other protection"},
		{"is not encrypted", "asserts the whole path is unencrypted"},
		{"is unencrypted", "asserts the whole path is unencrypted"},
		{"anyone can read", "asserts exposure as a certainty"},
		{"your traffic is visible", "asserts exposure as a certainty"},
		{"\uadf8\ub300\ub85c \ubcf4\uc77c \uc218 \uc788\uc2b5\ub2c8\ub2e4", "states the traffic is exposed regardless of other protection"},
		{"\uc554\ud638\ud654\ub418\uc9c0 \uc54a\uc740 \uc5f0\uacb0", "asserts the whole path is unencrypted"},
		{"\uc554\ud638\ud654\ub418\uc9c0 \uc54a\uc558\ub2e4\ub294", "asserts the whole path is unencrypted"},
		{"\ub204\uad6c\ub098 \uc77d\uc744 \uc218 \uc788\uc2b5\ub2c8\ub2e4", "asserts exposure as a certainty"},
	}

	for _, code := range connectionMessages() {
		for _, lang := range Langs() {
			text := Text(lang, code)
			for _, bad := range unconditional {
				if strings.Contains(text, bad.phrase) {
					t.Errorf("%s/%s %s:\n  %q\n  contains %q", lang, code, bad.why, text, bad.phrase)
				}
			}
		}
	}
}

func TestConnectionWordingDoesNotPromiseSafety(t *testing.T) {
	// The opposite failure: implying the connection is protected when OwnGit
	// has no basis for saying so.
	overreassuring := []string{
		"your connection is safe", "securely encrypted", "fully protected",
		"no one can", "\uc548\uc804\ud569\ub2c8\ub2e4", "\uc644\uc804\ud788 \ubcf4\ud638", "\uc544\ubb34\ub3c4 \ubcfc \uc218 \uc5c6",
	}
	for _, code := range connectionMessages() {
		for _, lang := range Langs() {
			text := strings.ToLower(Text(lang, code))
			for _, bad := range overreassuring {
				if strings.Contains(text, strings.ToLower(bad)) {
					t.Errorf("%s/%s promises protection OwnGit cannot verify: %q", lang, code, Text(lang, code))
				}
			}
		}
	}
}

func TestInsecureHelpStatesScopeAndRealRisk(t *testing.T) {
	// The help text carries the whole informed choice, so it must name the
	// actor, the real risk, and the limit of what the application knows.
	for _, want := range []struct {
		lang  Lang
		parts []string
		what  string
	}{
		{LangEN, []string{"OwnGit"}, "names what is and is not doing the encrypting"},
		{LangEN, []string{"plain HTTP"}, "says what OwnGit actually serves"},
		{LangEN, []string{"adds no encryption of its own"}, "scopes the claim to OwnGit"},
		{LangEN, []string{"LAN"}, "describes the situation where the risk is real"},
		{LangEN, []string{"passwords", "repository"}, "names what could be exposed"},
		{LangEN, []string{"cannot check"}, "admits the limit of its knowledge"},
		{LangEN, []string{"VPN"}, "acknowledges protection may already exist"},
		{LangKO, []string{"OwnGit"}, "names what is and is not doing the encrypting"},
		{LangKO, []string{"\uc77c\ubc18 HTTP"}, "says what OwnGit actually serves"},
		{LangKO, []string{"\uc790\uccb4 \uc554\ud638\ud654\ub97c \ub354\ud558\uc9c0 \uc54a"}, "scopes the claim to OwnGit"},
		{LangKO, []string{"LAN"}, "describes the situation where the risk is real"},
		{LangKO, []string{"\ube44\ubc00\ubc88\ud638", "\uc800\uc7a5\uc18c"}, "names what could be exposed"},
		{LangKO, []string{"\ud655\uc778\ud560 \uc218 \uc5c6"}, "admits the limit of its knowledge"},
		{LangKO, []string{"VPN"}, "acknowledges protection may already exist"},
	} {
		text := Text(want.lang, MsgSetupInsecureHelp)
		for _, part := range want.parts {
			if !strings.Contains(text, part) {
				t.Errorf("%s insecure help never %s (missing %q): %q", want.lang, want.what, part, text)
			}
		}
	}

	// The risk must be stated as possible, not certain.
	if !strings.Contains(Text(LangEN, MsgSetupInsecureHelp), "can be read by others") {
		t.Error("the English help does not state the risk as a possibility")
	}
	if !strings.Contains(Text(LangKO, MsgSetupInsecureHelp), "\uc77d\uc744 \uc218 \uc788\uc2b5\ub2c8\ub2e4") {
		t.Error("the Korean help does not state the risk as a possibility")
	}
}

func TestConnectionIndicatorIsScopedToOwnGit(t *testing.T) {
	// "Encrypted" alone would read as a claim about the whole path.
	for _, lang := range Langs() {
		for _, code := range []MessageCode{MsgConnEncrypted, MsgConnPlain} {
			if !strings.Contains(Text(lang, code), "OwnGit") {
				t.Errorf("%s/%s does not say whose encryption it describes: %q", lang, code, Text(lang, code))
			}
		}
	}
}

func TestTailscaleIsRecommendedNotDetected(t *testing.T) {
	// A Tailscale host name can still be served over plain HTTP, so the hint
	// must not imply the application recognised anything.
	for _, lang := range Langs() {
		hint := Text(lang, MsgConnTailscale)
		if !strings.Contains(hint, "Tailscale") {
			t.Fatalf("%s: the hint no longer names the recommendation", lang)
		}
		for _, claim := range []string{"detected", "you are on", "\uac10\uc9c0\ud588", "\uc0ac\uc6a9 \uc911\uc785\ub2c8\ub2e4", "\uc5f0\uacb0\ub418\uc5b4 \uc788\uc2b5\ub2c8\ub2e4"} {
			if strings.Contains(hint, claim) {
				t.Errorf("%s: the hint claims to have detected Tailscale (%q): %q", lang, claim, hint)
			}
		}
	}
	// And the no-proof line must say a host name proves nothing.
	if !strings.Contains(Text(LangEN, MsgConnNoProof), "host name") {
		t.Error("the English notice does not say a host name is not proof")
	}
	if !strings.Contains(Text(LangKO, MsgConnNoProof), "\ud638\uc2a4\ud2b8 \uc774\ub984") {
		t.Error("the Korean notice does not say a host name is not proof")
	}
}

func TestAcknowledgementDoesNotClaimToFixTheConnection(t *testing.T) {
	// Ticking a box changes a preference, not the transport.
	for _, lang := range Langs() {
		done := Text(lang, MsgSettingsAckDone)
		for _, claim := range []string{"now secure", "protected", "\uc548\uc804\ud574\uc84c", "\ubcf4\ud638\ub429\ub2c8\ub2e4", "\uc554\ud638\ud654\ub429\ub2c8\ub2e4"} {
			if strings.Contains(done, claim) {
				t.Errorf("%s: acknowledging appears to change the connection (%q): %q", lang, claim, done)
			}
		}
	}
	if !strings.Contains(Text(LangEN, MsgSettingsAckDone), "Nothing about the connection changed") {
		t.Error("the English confirmation does not say the connection is unchanged")
	}
	if !strings.Contains(Text(LangKO, MsgSettingsAckDone), "\ubc14\ub01c\uc9c0\uc9c0\ub294 \uc54a") {
		t.Error("the Korean confirmation does not say the connection is unchanged")
	}
}

func TestConnectionWordingReachesTheScreens(t *testing.T) {
	// The policy only matters if the corrected text is what a reader sees, so
	// check it on the two screens that present the choice.
	r := newRenderer(t)

	for _, lang := range Langs() {
		chrome := fullChrome(lang)
		chrome.Connection = Connection{Encrypted: false, Host: "owngit.tail-scale.ts.net"}

		setup := render(t, r, SetupPage{
			Chrome: chrome, Stage: SetupWizard,
			SubmitURL: "/setup", Form: SetupForm{StoragePath: "/srv/git"},
		})
		if !strings.Contains(setup, wantText(lang, MsgSetupInsecureHelp)) {
			t.Errorf("%s: the setup wizard does not show the corrected explanation", lang)
		}
		if !strings.Contains(setup, wantText(lang, MsgConnNoProof)) {
			t.Errorf("%s: the setup wizard does not say what OwnGit cannot verify", lang)
		}

		settings := render(t, r, SettingsPage{Chrome: chrome, SubmitURL: "/settings"})
		if !strings.Contains(settings, wantText(lang, MsgConnNoProof)) {
			t.Errorf("%s: settings does not say what OwnGit cannot verify", lang)
		}
		// A Tailscale-looking host must not flip the indicator.
		if !strings.Contains(settings, wantText(lang, MsgConnPlain)) {
			t.Errorf("%s: a Tailscale-style host name changed the reported status", lang)
		}
	}
}

func TestConnectionExplanationComesBeforeItsCaveat(t *testing.T) {
	// A reader should learn what the situation is before being told what the
	// application cannot verify about it, otherwise the caveat qualifies
	// something that has not been said yet.
	r := newRenderer(t)
	chrome := fullChrome(LangEN)
	chrome.Connection = Connection{Encrypted: false, Host: "owngit.example"}

	out := render(t, r, SettingsPage{Chrome: chrome, SubmitURL: "/settings"})
	detail := strings.Index(out, wantText(LangEN, MsgConnPlainDetail))
	caveat := strings.Index(out, wantText(LangEN, MsgConnNoProof))
	if detail < 0 {
		t.Fatal("settings never states the current connection")
	}
	if caveat < 0 || caveat < detail {
		t.Error("the caveat appears before the situation it qualifies")
	}
}

func TestAcknowledgedConnectionStillReportsItsState(t *testing.T) {
	// Acknowledging silences the prompt, not the facts.
	r := newRenderer(t)
	chrome := fullChrome(LangEN)
	chrome.Connection = Connection{Encrypted: false, Host: "owngit.example", InsecureAcknowledged: true}

	out := render(t, r, SettingsPage{Chrome: chrome, SubmitURL: "/settings"})
	if !strings.Contains(out, wantText(LangEN, MsgConnPlain)) {
		t.Error("an acknowledged connection stopped reporting its state")
	}
	if !strings.Contains(out, wantText(LangEN, MsgConnNoProof)) {
		t.Error("an acknowledged connection dropped the limit of what is known")
	}
	if strings.Contains(out, `name="insecure_ack"`) {
		t.Error("the acknowledgement is still being requested after it was given")
	}
}

func TestSetupStorageWordingMatchesTheSupportedStorage(t *testing.T) {
	// Repository storage on a mounted SMB or NFS share is supported with one
	// OwnGit writer at a time; only the database must stay local. The setup
	// hint must neither forbid the share nor call it unverified.
	for _, lang := range Langs() {
		local, remote := Text(lang, MsgSetupStorageLocal), Text(lang, MsgSetupStorageRemote)
		if !strings.Contains(local, "SMB") || !strings.Contains(local, "NFS") {
			t.Errorf("%s: the storage hint does not name the supported shares: %q", lang, local)
		}
		for _, text := range []string{local, remote} {
			for _, stale := range []string{"not runtime-verified", "검증되지 않았"} {
				if strings.Contains(text, stale) {
					t.Errorf("%s: the storage wording still says %q: %q", lang, stale, text)
				}
			}
			if !strings.Contains(text, "OwnGit") {
				t.Errorf("%s: the storage wording omits the one-writer rule: %q", lang, text)
			}
		}
	}
}
