package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestBrokenLinksAreReportedAndValidOnesAreNot(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"README.md": "# OwnGit\n\n" +
			"[guide](docs/guide.md#설치-안내) [encoded](docs/guide.md#%EC%84%A4%EC%B9%98-%EC%95%88%EB%82%B4)\n" +
			"[case](docs/guide.md#Setup--Install) [second](docs/guide.md#usage-1) [html id](docs/guide.md#Custom)\n" +
			"[dir](docs) [root](/LICENSE) [site](https://example.test/x.md#nowhere) [self](#owngit)\n" +
			"<p><a href=\"docs/missing.md\">missing</a></p>\n\n" +
			"```\n[in code](docs/nowhere.md)\n```\n\n" +
			"[third](docs/guide.md#usage-2) [outside](../x.md) [heading](docs/guide.md#nowhere)\n",
		"docs/guide.md": "# 설치 안내\n\n## Setup / Install\n\n## Usage\n\n## Usage\n\n<a id=\"custom\"></a>\n\n[back](../README.md#owngit)\n",
		"LICENSE":       "MIT\n",
		"internal/service/unit.go": "package service\n\n" +
			"const doc = \"https://github.com/juliankang4/owngit/blob/main/docs/guide.md#usage\"\n" +
			"const gone = \"docs/guide.md#removed\"\n",
		"internal/service/unit_test.go": "package service\n\nconst example = \"docs/guide.md#test-data\"\n",
	}
	var names []string
	for name, content := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}

	problems, err := newChecker(root, names).check()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`README.md:6: docs/missing.md: no such file or directory in the repository`,
		`README.md:12: docs/guide.md#usage-2: docs/guide.md has no heading or id "usage-2"`,
		`README.md:12: ../x.md: leads outside the repository`,
		`README.md:12: docs/guide.md#nowhere: docs/guide.md has no heading or id "nowhere"`,
		`internal/service/unit.go:4: /docs/guide.md#removed: docs/guide.md has no heading or id "removed"`,
	}
	if !reflect.DeepEqual(problems, want) {
		t.Errorf("problems:\n%q\nwant:\n%q", problems, want)
	}
}
