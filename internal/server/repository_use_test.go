package server

import "testing"

func TestRepositoryNameInPathNamesTheRepositoryOfARequest(t *testing.T) {
	for path, want := range map[string][3]string{
		"/repositories/sample":                   {"/repositories/", "sample", ""},
		"/repositories/sample/":                  {"/repositories/", "sample", "/"},
		"/repositories/sample/tree/main/docs":    {"/repositories/", "sample", "/tree/main/docs"},
		"/api/v1/repositories/sample/pulls":      {"/api/v1/repositories/", "sample", "/pulls"},
		"/api/v1/repositories/sample/import/run": {"/api/v1/repositories/", "sample", "/import/run"},
		"/api/v1/repositories":                   {},
		"/":                                      {},
		"/activity":                              {},
		"/settings":                              {},
		"/assets/app.css":                        {},
	} {
		prefix, name, rest := repositoryNameInPath(path)
		if got := [3]string{prefix, name, rest}; got != want {
			t.Errorf("repositoryNameInPath(%q) = %q, want %q", path, got, want)
		}
	}
}
