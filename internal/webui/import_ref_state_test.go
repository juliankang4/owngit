package webui

import "testing"

// Every ref state the import status reports has a label in both languages.
func TestImportRefStatesHaveLabels(t *testing.T) {
	want := map[string][2]string{
		"tracked":           {"Tracked", "원본과 같음"},
		"diverged":          {"Diverged", "원본과 다름"},
		"absent_locally":    {"Absent locally", "OwnGit에 없음"},
		"earlier_source":    {"Earlier source", "예전 원본 기록"},
		"unknown_local":     {"Local unknown", "OwnGit 쪽 확인 못 함"},
		"deleted_at_source": {"Deleted at source", "원본에서 삭제됨"},
	}
	for state, labels := range want {
		if got := importToken(LangEN, state); got != labels[0] {
			t.Errorf("English label for %s=%q, want %q", state, got, labels[0])
		}
		if got := importToken(LangKO, state); got != labels[1] {
			t.Errorf("Korean label for %s=%q, want %q", state, got, labels[1])
		}
	}
}
