package webui

import (
	"strings"
	"testing"
)

func TestPagingCountersMatchTheContent(t *testing.T) {
	r := newRenderer(t)
	c := PageContinuation{First: 10001, Last: 20000, Total: 1048576, FirstURL: "/first", MoreURL: "/next"}
	for _, lang := range Langs() {
		for _, kind := range []string{"file", "loaded file", "folder", "drawer", "commit lines", "commit files", "pull request lines", "pull request files"} {
			t.Run(string(lang)+"/"+kind, func(t *testing.T) {
				page := codePage(lang, &FileView{Path: "lines.txt", Lines: []string{"x"}, Continuation: c})
				var view Page = page
				lines := kind == "file" || strings.HasSuffix(kind, "lines")
				switch kind {
				case "loaded file":
					page.Code.File.Truncated = true
					page.Code.File.Continuation.Incomplete = true
				case "folder", "drawer":
					page.Code.File.Continuation = PageContinuation{}
					page.Code.Continuation = c
					if kind == "folder" {
						page.Code.File = nil
					}
					view = page
				case "commit lines", "commit files":
					page = repoPage(fullChrome(lang), RepoTabCommits)
					if lines {
						page.Commits.Detail.Continuation = c
					} else {
						page.Commits.Detail.FilePages = c
					}
					view = page
				case "pull request lines", "pull request files":
					pr := pullRequestPage(fullChrome(lang), prFixtureFailing)
					if lines {
						pr.ChangesLines = c
					} else {
						pr.ChangesPages = c
					}
					view = pr
				}
				out := render(t, r, view)
				groups, _ := pageLinks(out)
				wantEN, wantKO := "Showing 10,000 of 1,048,576 (10,001 to 20,000).", "전체 1,048,576개 중 10,001번째부터 20,000번째까지 10,000개를 표시합니다."
				if lines {
					wantEN, wantKO = "Showing 10,000 of 1,048,576 lines (10,001 to 20,000).", "전체 1,048,576줄 중 10,001번째부터 20,000번째까지 10,000줄을 표시합니다."
				} else if kind == "loaded file" {
					wantEN, wantKO = "Showing 10,000 of 1,048,576 loaded lines", "읽어온 1,048,576줄 중"
					if !strings.Contains(out, `data-en="1,048,576 loaded lines" data-ko="읽어온 1,048,576줄"`) {
						t.Error("loaded-line fact is not grouped in both languages")
					}
				}
				if len(groups) == 0 {
					t.Fatal("paging marker is missing")
				}
				for _, group := range groups {
					if !strings.Contains(group, wantEN) || !strings.Contains(group, wantKO) || strings.Contains(group, "%!") {
						t.Errorf("wrong paging counter: %s", group)
					}
				}
			})
		}
	}
}

func TestInterfaceTermsAndCommandLiterals(t *testing.T) {
	for _, row := range []struct {
		code MessageCode
		ko   string
	}{
		{MsgOlder, "더 오래된 기록"}, {MsgNewer, "더 최근 기록"},
		{MsgTSReplaceButton, "기존 서비스를 OwnGit으로 교체"},
		{MsgPRNewSource, "가져올 브랜치"}, {MsgPRSourceLabel, "가져올 브랜치"},
		{MsgCodingTasksTitle, "최근 체크 에이전트 작업"}, {MsgCCJobsTitle, "최근 자동 체크 작업"},
		{MsgFolderHost, "처음 설정 화면"}, {MsgImportExtraRefs, "ref 이름공간"},
		{MsgShareLabelInvalid, "문자에 따라 달라집니다"},
		{MsgLoginLimitsAttemptsHlp, "접속 주소의 로그인 시도를 일시 차단"},
		{MsgSetupRaceLost, "다른 곳에서 설정을 마쳤습니다."},
	} {
		for _, lang := range Langs() {
			out := string(bi(lang, row.code))
			if !strings.Contains(out, `data-en="`) || !strings.Contains(out, row.ko) || !Has(row.code) {
				t.Errorf("%s/%s: missing bilingual term %q", lang, row.code, row.ko)
			}
		}
	}
	for _, code := range []MessageCode{"tailscale.problem.port_taken", MsgTSStale, MsgTSTakenSteps, MsgHostRefusedHint} {
		if !Has(code) {
			t.Fatalf("missing command guidance %s", code)
		}
		for _, literal := range []string{"--https-port PORT", "--hostname=NAME", "--https=PORT", "<public address>", "<proxy address>"} {
			if strings.Contains(Text(LangEN, code), literal) != strings.Contains(Text(LangKO, code), literal) {
				t.Errorf("%s: command literal %q differs between languages", code, literal)
			}
		}
	}
}
