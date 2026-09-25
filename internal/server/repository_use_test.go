package server

import "testing"

func TestRepositoryIDInPathNamesTheRepositoryOfARequest(t *testing.T) {
	for path, want := range map[string]string{
		"/git/sample.git/info/refs":              "sample",
		"/git/sample.git/git-upload-pack":        "sample",
		"/repositories/sample":                   "sample",
		"/repositories/sample/tree/main/docs":    "sample",
		"/api/v1/repositories/sample/pulls":      "sample",
		"/api/v1/repositories/sample/import/run": "sample",
		"/":                                      "",
		"/activity":                              "",
		"/settings":                              "",
		"/assets/app.css":                        "",
	} {
		if got := repositoryIDInPath(path); got != want {
			t.Errorf("repositoryIDInPath(%q) = %q, want %q", path, got, want)
		}
	}
}
