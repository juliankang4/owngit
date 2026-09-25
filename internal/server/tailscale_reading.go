package server

import (
	"context"
	"sync"
	"time"

	"owngit/internal/tailscale"
)

// tailscaleReadingTTL is how long one reading of Tailscale's state serves
// further reports. The Settings page reports on every view, for any viewer
// with general access, so reports share one reading in flight and reuse it
// for this long: however many requests arrive, at most one status and one
// Serve configuration command run at a time, and none again within the TTL.
const tailscaleReadingTTL = 3 * time.Second

// tailscaleReading is what a report reads from the tailscale command.
type tailscaleReading struct {
	command    tailscale.Command
	commandErr error
	status     tailscale.Status
	statusErr  error
	config     tailscale.ServeConfig
	configErr  error
}

// readingCache holds the latest reading and the one in flight.
type readingCache struct {
	mu      sync.Mutex
	reading tailscaleReading
	at      time.Time
	// pending is closed when the reading in flight finishes.
	pending chan struct{}
	// generation changes when forget drops the reading, so a reading that
	// was in flight across a change is not kept.
	generation uint64
}

// read returns a reading no older than tailscaleReadingTTL, sharing the one
// in flight. The shared reading does not stop when ctx does, since other
// reports wait for it; each command keeps its own time limit.
func (sharing *Tailscale) read(ctx context.Context) (tailscaleReading, error) {
	cache := &sharing.readings
	cache.mu.Lock()
	for {
		if !cache.at.IsZero() && time.Since(cache.at) < tailscaleReadingTTL {
			reading := cache.reading
			cache.mu.Unlock()
			return reading, nil
		}
		if cache.pending == nil {
			break
		}
		wait := cache.pending
		cache.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return tailscaleReading{}, ctx.Err()
		}
		cache.mu.Lock()
	}
	done, generation := make(chan struct{}), cache.generation
	cache.pending = done
	cache.mu.Unlock()

	reading := sharing.readNow(context.WithoutCancel(ctx))

	cache.mu.Lock()
	if cache.generation == generation {
		cache.reading, cache.at = reading, time.Now()
	}
	cache.pending = nil
	close(done)
	cache.mu.Unlock()
	return reading, nil
}

// readNow runs the tailscale commands of one reading.
func (sharing *Tailscale) readNow(ctx context.Context) tailscaleReading {
	var reading tailscaleReading
	reading.command, reading.commandErr = sharing.findCommand()
	if reading.commandErr != nil {
		return reading
	}
	reading.status, reading.statusErr = reading.command.Status(ctx)
	reading.config, reading.configErr = reading.command.ServeConfig(ctx)
	return reading
}

// forget drops the kept reading after a change, so the next report reads
// Tailscale again.
func (sharing *Tailscale) forget() {
	cache := &sharing.readings
	cache.mu.Lock()
	cache.at = time.Time{}
	cache.generation++
	cache.mu.Unlock()
}
