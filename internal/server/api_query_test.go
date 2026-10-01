package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"owngit/internal/pullrequest"
)

// The API refuses query text it cannot parse. URL.Query drops an invalid
// pair together with its parse error, so without this check a malformed ref,
// year or commit pair is read as an omitted one and answered for a different
// target.
func TestAPIQueryTextRejectsMalformed(t *testing.T) {
	fixture := newAPIFixture(t, false)
	created, err := fixture.app.PullRequests.Create(context.Background(), pullrequest.CreateInput{
		Repository: "project", Title: "Feature", SourceBranch: "feature", TargetBranch: "main",
	})
	noErr(t, err)
	server := serve(t, fixture.app.Handler())
	pullRequest := server.URL + "/api/v1/repositories/project/pull-requests/" + itoa(created.Number)

	for _, test := range []struct {
		name   string
		target string
		admin  bool
	}{
		{"activity year escape", server.URL + activityAPIPath + "?year=%ZZ", false},
		{"activity semicolon", server.URL + activityAPIPath + "?year=1;ignored=x", false},
		{"activity malformed unknown key", server.URL + activityAPIPath + "?unexpected=%ZZ", false},
		{"archive ref escape", server.URL + "/api/v1/repositories/project/archive?ref=%ZZ&format=zip", false},
		{"archive semicolon", server.URL + "/api/v1/repositories/project/archive?ref=1;ignored=x&format=zip", false},
		{"archive malformed unknown key", server.URL + "/api/v1/repositories/project/archive?format=zip&unexpected=%ZZ", false},
		{"import history limit escape", server.URL + "/api/v1/repositories/project/import/history?limit=%ZZ", true},
		{"import history semicolon", server.URL + "/api/v1/repositories/project/import/history?limit=1;ignored=x", true},
		{"import history malformed unknown key", server.URL + "/api/v1/repositories/project/import/history?limit=1&unexpected=%ZZ", true},
		{"diff pair escape", pullRequest + "/diff?source_oid=%ZZ&target_oid=%ZZ", false},
		{"diff semicolon", pullRequest + "/diff?source_oid=1;ignored=x&target_oid=" + fixture.targetOID, false},
		{"diff malformed unknown key", pullRequest + "/diff?unexpected=%ZZ", false},
		{"mergeability pair escape", pullRequest + "/mergeability?source_oid=%ZZ&target_oid=%ZZ", false},
		{"mergeability semicolon", pullRequest + "/mergeability?source_oid=1;ignored=x&target_oid=" + fixture.targetOID, false},
		{"mergeability malformed unknown key", pullRequest + "/mergeability?unexpected=%ZZ", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var response *http.Response
			if test.admin {
				response = importAPIRequest(t, http.MethodGet, test.target, nil, "admin-password")
			} else {
				response = apiRequest(t, http.MethodGet, test.target, nil, "", "")
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			noErr(t, err)
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("GET %s status=%d, want 400; body=%.120q", test.target, response.StatusCode, body)
			}
			var envelope pullrequest.ErrorEnvelope
			noErrf(t, json.Unmarshal(body, &envelope), "decode API error")
			if envelope.Error.Code != "invalid_request" {
				t.Fatalf("GET %s code=%q, want invalid_request; body=%.120q", test.target, envelope.Error.Code, body)
			}
		})
	}
}

// Query text that parses keeps every answer it had before the rejection
// path was added.
func TestAPIQueryTextAnswersValid(t *testing.T) {
	fixture := newAPIFixture(t, false)
	created, err := fixture.app.PullRequests.Create(context.Background(), pullrequest.CreateInput{
		Repository: "project", Title: "Feature", SourceBranch: "feature", TargetBranch: "main",
	})
	noErr(t, err)
	server := serve(t, fixture.app.Handler())
	pullRequest := server.URL + "/api/v1/repositories/project/pull-requests/" + itoa(created.Number)
	pair := url.Values{"source_oid": {fixture.sourceOID}, "target_oid": {fixture.targetOID}}.Encode()

	t.Run("activity", func(t *testing.T) {
		target := server.URL + activityAPIPath + "?year=" + strconv.Itoa(time.Now().Year())
		var activity activityResponse
		if status := decodeAPI(t, apiRequest(t, http.MethodGet, target, nil, "", ""), &activity); status != http.StatusOK || !activity.OK {
			t.Fatalf("GET %s status=%d ok=%v", target, status, activity.OK)
		}
	})

	t.Run("archive", func(t *testing.T) {
		target := server.URL + "/api/v1/repositories/project/archive?ref=main&format=zip"
		response, body := getArchive(t, target)
		if response.StatusCode != http.StatusOK || archiveNames(t, "zip", body) != "project-main/,project-main/file.txt" {
			t.Fatalf("GET %s status=%d", target, response.StatusCode)
		}
	})

	t.Run("import history", func(t *testing.T) {
		target := server.URL + "/api/v1/repositories/project/import/history?limit=1"
		var history struct {
			OK bool `json:"ok"`
		}
		if status := decodeAPI(t, importAPIRequest(t, http.MethodGet, target, nil, "admin-password"), &history); status != http.StatusOK || !history.OK {
			t.Fatalf("GET %s status=%d ok=%v", target, status, history.OK)
		}
	})

	t.Run("diff", func(t *testing.T) {
		target := pullRequest + "/diff?" + pair
		answer := decodeDiff(t, apiRequest(t, http.MethodGet, target, nil, "", ""))
		if answer.Source.OID != fixture.sourceOID || answer.Target.OID != fixture.targetOID {
			t.Fatalf("GET %s pair=%s..%s", target, answer.Source.OID, answer.Target.OID)
		}
	})

	t.Run("mergeability", func(t *testing.T) {
		target := pullRequest + "/mergeability?" + pair
		answer := getMergeability(t, target, http.StatusOK)
		if answer.Status != pullrequest.MergeabilityClean || answer.Source.OID != fixture.sourceOID || answer.Target.OID != fixture.targetOID {
			t.Fatalf("GET %s answer=%+v", target, answer)
		}
	})
}
