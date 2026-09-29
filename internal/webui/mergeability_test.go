package webui

import "testing"

// A conflict answer names files only when Git listed some.
func TestMergeabilityConflictSentence(t *testing.T) {
	for _, test := range []struct {
		answer MergeabilityAnswer
		want   MessageCode
	}{
		{MergeabilityAnswer{Status: MergeabilityConflict, ConflictPaths: []string{"a.txt"}}, MsgMergeabilityConflict},
		{MergeabilityAnswer{Status: MergeabilityConflict}, MsgMergeabilityConflictUnlisted},
		{MergeabilityAnswer{Status: MergeabilityConflict, Reason: "no_merge_base"}, MsgMergeabilityNoBase},
	} {
		if got := test.answer.Note(); got != test.want {
			t.Errorf("%+v: %s, want %s", test.answer, got, test.want)
		}
	}
}
