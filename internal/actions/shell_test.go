package actions

import (
	"slices"
	"testing"
)

func TestShellWords(t *testing.T) {
	for _, test := range []struct {
		text  string
		want  []string
		fails bool
	}{
		{text: "bash", want: []string{"bash"}},
		{text: "sh -e \"{0}\"", want: []string{"sh", "-e", "{0}"}},
		{text: "'./two words/tool' '-x' {0}", want: []string{"./two words/tool", "-x", "{0}"}},
		{text: "  sh\t-e\n{0}  ", want: []string{"sh", "-e", "{0}"}},
		{text: "a\"b\"c {0} ''", want: []string{"abc", "{0}", ""}},
		{text: "sh {0} '$TOKEN'", want: []string{"sh", "{0}", "$TOKEN"}},
		{text: "", fails: true},
		{text: "'' {0}", fails: true},
		{text: "sh -e '{0}", fails: true},
	} {
		t.Run(test.text, func(t *testing.T) {
			words, err := ShellWords(test.text)
			if (err != nil) != test.fails || !slices.Equal(words, test.want) {
				t.Fatalf("words=%v err=%v", words, err)
			}
		})
	}
}
