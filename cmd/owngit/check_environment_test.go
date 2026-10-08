package main

import (
	"encoding/json"
	"errors"
	"testing"

	"owngit/internal/testfixture"
)

func TestCheckCLIEnvironment(t *testing.T) {
	remoteFlags, taskID, work := startCheckCLIServer(t)
	environment := testfixture.NewCheckEnvironment(t)
	output, err := captureStdout(func() error {
		return checkCommand(append([]string{"run", "--task", taskID, "--workdir", work, "--check", "environment=" + environment.Command}, remoteFlags...))
	})
	var exit *checkExit
	if !errors.As(err, &exit) || exit.code != 1 {
		t.Fatalf("check exit=%v", err)
	}
	var result checkRunOutput
	noErr(t, json.Unmarshal([]byte(output), &result))
	if !result.Uploaded || len(result.Results) != 1 || result.Results[0].Status != "failed" {
		t.Fatalf("check result=%+v", result)
	}
	environment.Assert(t, result.Results[0].OutputExcerpt, "")
}
