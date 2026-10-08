package testfixture

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"

	"owngit/internal/actions"
)

func ActionsPlan(t *testing.T, key string, index int, steps ...actions.Step) (json.RawMessage, string) {
	t.Helper()
	if len(steps) == 0 {
		steps = []actions.Step{{Name: "test", Run: "echo synthetic"}}
	}
	encoded, err := json.Marshal(actions.JobPlan{JobKey: key, MatrixIndex: index, Steps: steps, SecretNames: []string{"SYNTHETIC_TOKEN"}})
	if err != nil {
		t.Fatal(err)
	}
	return encoded, fmt.Sprintf("%x", sha256.Sum256(encoded))
}
