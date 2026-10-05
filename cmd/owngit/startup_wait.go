package main

import (
	"sync"
	"time"
)

// slowStepNotice is how long a startup step may run before it is reported.
// Opening a local folder takes microseconds, and a sleeping network disk
// usually answers within a few seconds, so a step still running after 5 s is
// worth telling the owner about without logging on a normal start.
const slowStepNotice = 5 * time.Second

// reportSlowStep runs step and logs one line if it has not finished after
// notice, and one more when it finishes. It does not stop or time out the
// step: a blocked kernel call cannot be interrupted.
func reportSlowStep(logf func(string, ...any), notice time.Duration, waiting string, step func() error) error {
	var mu sync.Mutex
	reported, done := false, false
	timer := time.AfterFunc(notice, func() {
		mu.Lock()
		defer mu.Unlock()
		if !done {
			reported = true
			logf("still waiting for %s", waiting)
		}
	})
	started := time.Now()
	err := step()
	timer.Stop()
	mu.Lock()
	done = true
	if reported {
		logf("continued after waiting %s for %s", time.Since(started).Round(time.Second), waiting)
	}
	mu.Unlock()
	return err
}
