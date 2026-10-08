package testtiminglint

import (
	"testing"
	"time"
)

func TestObservedProgress(t *testing.T) {
	started := time.Now()
	time.Sleep(0)
	t.Log(time.Since(started))
}
