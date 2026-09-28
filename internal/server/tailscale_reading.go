package server

import (
	"context"
	"net/netip"
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

// readingInFlight is a reading that runs in the background. done is closed
// when it finished, and reading is then what it read.
type readingInFlight struct {
	done       chan struct{}
	generation uint64
	reading    tailscaleReading
}

// readingCache holds the latest reading and the one in flight.
type readingCache struct {
	mu      sync.Mutex
	reading tailscaleReading
	at      time.Time
	// pending is the reading in flight, or nil.
	pending *readingInFlight
	// generation changes when forget drops the reading, so a reading that
	// was in flight across a change is not kept.
	generation uint64
	// addresses are this computer's Tailscale addresses from the latest
	// reading, none when it could not tell; known is set once a reading
	// finished. refreshed is closed when the reading that addresses
	// started in the background finishes, and is nil when none runs.
	addresses []netip.Addr
	known     bool
	refreshed chan struct{}
}

// tailnetLabelWait bounds how long a page waits for this computer's
// Tailscale addresses before the first reading has finished. Once one has,
// pages use the latest addresses and never wait (addresses).
const tailnetLabelWait = time.Second

// read returns a reading no older than tailscaleReadingTTL. A request never
// waits for Tailscale longer than its own deadline: the reading runs in the
// background, shared with every report that arrives meanwhile, and read
// returns ctx's error when ctx ends first. The reading then still finishes
// within each command's own time limit and serves the next report.
func (sharing *Tailscale) read(ctx context.Context) (tailscaleReading, error) {
	cache := &sharing.readings
	cache.mu.Lock()
	if !cache.at.IsZero() && time.Since(cache.at) < tailscaleReadingTTL {
		reading := cache.reading
		cache.mu.Unlock()
		return reading, nil
	}
	// A reading that started before a change (forget) may show Tailscale as
	// it was, so it is not joined; the new one runs after it, so the
	// commands of one reading still run one at a time.
	flight := cache.pending
	if flight == nil || flight.generation != cache.generation {
		var earlier <-chan struct{}
		if flight != nil {
			earlier = flight.done
		}
		flight = &readingInFlight{done: make(chan struct{}), generation: cache.generation}
		cache.pending = flight
		go sharing.finish(flight, earlier)
	}
	cache.mu.Unlock()
	select {
	case <-flight.done:
		return flight.reading, nil
	case <-ctx.Done():
		return tailscaleReading{}, ctx.Err()
	}
}

// finish runs the reading in flight, after the earlier one when it is not
// nil, and keeps it unless a change dropped the readings meanwhile.
func (sharing *Tailscale) finish(flight *readingInFlight, earlier <-chan struct{}) {
	if earlier != nil {
		<-earlier
	}
	reading := sharing.readNow(context.Background())
	cache := &sharing.readings
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.generation == flight.generation {
		cache.reading, cache.at = reading, time.Now()
	}
	cache.addresses, cache.known = nil, true
	if reading.commandErr == nil && reading.statusErr == nil {
		cache.addresses = reading.status.Addresses
	}
	if cache.pending == flight {
		cache.pending = nil
	}
	flight.reading = reading
	close(flight.done)
}

// addresses returns this computer's Tailscale addresses for the connection
// label of a page, without waiting for the tailscale command, which can
// take up to its time limit when tailscaled does not answer. When the
// latest reading is older than tailscaleReadingTTL, it starts one in the
// background, whose addresses later pages use. Only before the first
// reading has finished does it wait for it, up to tailnetLabelWait.
func (sharing *Tailscale) addresses() []netip.Addr {
	cache := &sharing.readings
	cache.mu.Lock()
	due := cache.at.IsZero() || time.Since(cache.at) >= tailscaleReadingTTL
	if due && cache.refreshed == nil {
		done := make(chan struct{})
		cache.refreshed = done
		go func() {
			_, _ = sharing.read(context.Background())
			cache.mu.Lock()
			cache.refreshed = nil
			cache.mu.Unlock()
			close(done)
		}()
	}
	known, addresses, refreshed := cache.known, cache.addresses, cache.refreshed
	cache.mu.Unlock()
	if known || refreshed == nil {
		return addresses
	}
	timer := time.NewTimer(tailnetLabelWait)
	defer timer.Stop()
	select {
	case <-refreshed:
	case <-timer.C:
		return nil
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.addresses
}

// readNow runs the tailscale commands of one reading.
func (sharing *Tailscale) readNow(ctx context.Context) tailscaleReading {
	var reading tailscaleReading
	reading.command, reading.commandErr = sharing.Find()
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
